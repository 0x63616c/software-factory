// Command api serves the Software Factory's typed HTTP API.
package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2/humacli"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	tlog "go.temporal.io/sdk/log"

	factoryapi "github.com/0x63616c/software-factory/internal/api"
	"github.com/0x63616c/software-factory/internal/api/auth"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/checkpoint"
	temporalapi "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/database"
	"github.com/0x63616c/software-factory/internal/httpserver"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/webhook"
	"github.com/jackc/pgx/v5/pgxpool"
)

const buildVersion = "development"

func main() {
	cli := newCLI(os.Stdout, os.Stderr)
	cli.Run()
}

// newCLI puts the spec dump on Huma's Cobra root. The command constructs only
// the contract, making generation independent of database and network boot.
func newCLI(stdout, stderr io.Writer) humacli.CLI {
	cli := humacli.New(func(hooks humacli.Hooks, _ *struct{}) {
		hooks.OnStart(func() {
			if err := run(); err != nil {
				slog.New(slog.NewJSONHandler(stderr, nil)).Error("the API stopped", slog.String("error", err.Error()))
			}
		})
	})
	cli.Root().AddCommand(&cobra.Command{
		Use:   "openapi",
		Short: "Print the OpenAPI spec",
		RunE: func(_ *cobra.Command, _ []string) error {
			return writeOpenAPI(stdout)
		},
	})
	return cli
}

func writeOpenAPI(writer io.Writer) error {
	spec, err := factoryapi.New(buildVersion, nil).OpenAPIYAML()
	if err != nil {
		return errors.Wrap(err, "generate OpenAPI 3.1 document")
	}
	if _, err := writer.Write(spec); err != nil {
		return errors.Wrap(err, "write OpenAPI 3.1 document")
	}
	return nil
}

func run() error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return errors.Wrap(err, "reading API configuration")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	authentication, err := auth.New(auth.Options{
		AccessIssuer:    cfg.AccessIssuer,
		AccessAudience:  cfg.AccessAudience,
		AccessCertsURL:  cfg.AccessCertsURL,
		WorkerBearer:    cfg.WorkerBearer,
		RunWorkerBearer: cfg.RunWorkerBearer,
	})
	if err != nil {
		return errors.Wrap(err, "starting API authentication")
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return errors.Wrap(err, "opening PostgreSQL connection")
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return errors.Wrap(err, "pinging PostgreSQL before API startup")
	}
	if err := database.ApplyMigrations(ctx, db); err != nil {
		return errors.Wrap(err, "applying PostgreSQL migrations before API startup")
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.Wrap(err, "opening PostgreSQL pool for ticket API")
	}
	defer pool.Close()
	blobStore, err := blobs.NewHTTPStore(cfg.BlobsURL, nil)
	if err != nil {
		return errors.Wrap(err, "opening HTTP blob store")
	}
	temporal, err := temporalapi.Dial(temporalapi.Options{
		HostPort:  cfg.TemporalHostPort,
		Namespace: cfg.TemporalNamespace,
		Logger:    tlog.NewStructuredLogger(logger),
	}, blobStore, nil)
	if err != nil {
		return errors.Wrapf(err, "dialling Temporal at %s in namespace %s", cfg.TemporalHostPort, cfg.TemporalNamespace)
	}
	defer temporal.Close()

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	_ = telemetry.NewMetrics(registry)
	metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return errors.Wrapf(err, "listening for metrics on %s (METRICS_ADDR)", cfg.MetricsAddr)
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	metricsServer := httpserver.ServeWithServer(
		metricsListener,
		&http.Server{
			Handler:           metricsMux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger, "API metrics",
	)
	defer func() {
		if err := metricsServer.Shutdown(context.Background(), 5*time.Second); err != nil {
			logger.Warn("the metrics server did not stop cleanly", slog.String("error", err.Error()))
		}
	}()

	ticketStore := store.New(pool)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	// The webhook consumer (#557) is deliberately NOT behind authentication.Wrap:
	// its caller is the relay (#535), not a human or an agent, and it
	// authenticates each delivery itself, by HMAC, exactly as the relay does.
	// The checkpoint route is the other exception: its exact-attempt capability
	// is authenticated by the Store. Legacy API paths stay behind Cloudflare
	// Access or the in-cluster bearer.
	mountGitHubWebhook(mux, cfg.WebhookSecret, ticketStore, logger, registry)
	factory := factoryapi.NewWithRunWorkerStores(buildVersion, temporalapi.NewCommands(temporal), ticketStore, ticketStore, ticketStore)
	mountFactoryAPI(mux, authentication, factory)

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return errors.Wrapf(err, "listening for API requests on %s (API_ADDR)", cfg.ListenAddr)
	}
	logger.Info("API starting", slog.String("address", cfg.ListenAddr))
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := httpserver.RunWithShutdownError(shutdown, listener, mux, logger, "API"); err != nil {
		return errors.Wrap(err, "serving API server")
	}
	return nil
}

func mountGitHubWebhook(mux *http.ServeMux, secret []byte, deliveries store.WebhookDeliveryAcknowledger, logger *slog.Logger, registry prometheus.Registerer) {
	mux.Handle("/v1/hooks/github", webhook.NewTargetHandler(secret, deliveries, logger, registry))
}

type routeAuthenticator interface {
	Wrap(http.Handler) http.Handler
}

func mountFactoryAPI(mux *http.ServeMux, authentication routeAuthenticator, factory *factoryapi.Service) {
	// The Store authenticates this exact-attempt capability. Requiring the
	// legacy broad worker bearer as well would give the Run Worker authority the
	// narrow checkpoint route exists to avoid.
	mux.Handle(checkpoint.PutServeMuxPattern, factory.Handler())
	mux.Handle(checkpoint.GetServeMuxPattern, factory.Handler())
	mux.Handle(checkpoint.RepositoryPutServeMuxPattern, factory.Handler())
	mux.Handle(checkpoint.RepositoryGetServeMuxPattern, factory.Handler())
	mux.Handle(checkpoint.RepositoryEffectPatchServeMuxPattern, factory.Handler())
	mux.Handle("/", authentication.Wrap(factory.Handler()))
}
