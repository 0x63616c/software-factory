// Command codec serves Temporal's remote payload codec protocol.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/payloads"
	"github.com/0x63616c/software-factory/internal/telemetry"
)

const (
	controlCenterTemporalNamespace   = "control-center"
	softwareFactoryTemporalNamespace = "software-factory"
)

var allowedTemporalNamespaces = map[string]struct{}{
	controlCenterTemporalNamespace:   {},
	softwareFactoryTemporalNamespace: {},
}

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("the codec service stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadCodec()
	if err != nil {
		return fmt.Errorf("reading codec service configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	store, err := blobs.NewHTTPStore(cfg.BlobsURL, nil)
	if err != nil {
		return fmt.Errorf("opening HTTP blob store: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listening for codec requests on %s (LISTEN_ADDR): %w", cfg.ListenAddr, err)
	}
	logger.Info("codec service starting", slog.String("address", cfg.ListenAddr), slog.String("blobs_url", cfg.BlobsURL))

	server := &http.Server{
		Handler:           newHandler(store, cfg.CORSOrigins, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serving codec requests: %w", err)
		}
	case <-shutdown.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutting down codec server: %w", err)
		}
	}
	return nil
}

func newHandler(store blobs.Store, origins []string, logger *slog.Logger) http.Handler {
	codec := payloads.Handler(store, telemetry.NewMetrics(prometheus.NewRegistry()))
	mux := http.NewServeMux()
	// The UI supplies the selected namespace in X-Namespace. The endpoint stays
	// namespace-agnostic, while the allowlist prevents this shared codec from
	// decoding a future namespace by accident.
	mux.Handle("/encode", allowedNamespaceCodec(codec))
	mux.Handle("/decode", allowedNamespaceCodec(codec))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	return codecRequestLogger(logger, cors(origins, mux))
}

// codecRequestLogger records request outcomes without ever inspecting a payload.
// A codec may decode a Ticket body or workflow input, so contents must never
// enter the log pipeline.
func codecRequestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := (clock.System{}).Now()
		recorded := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorded, request)

		attributes := []slog.Attr{
			slog.String("method", request.Method),
			slog.String("path", request.URL.Path),
			slog.String("namespace", request.Header.Get("X-Namespace")),
			slog.Int("status", recorded.status),
			slog.Duration("duration", (clock.System{}).Now().Sub(startedAt)),
		}
		if recorded.status >= http.StatusBadRequest {
			logger.LogAttrs(request.Context(), slog.LevelWarn, "codec request completed", attributes...)
			return
		}
		logger.LogAttrs(request.Context(), slog.LevelInfo, "codec request completed", attributes...)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.status = status
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(body []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func allowedNamespaceCodec(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, allowed := allowedTemporalNamespaces[request.Header.Get("X-Namespace")]; !allowed {
			http.Error(writer, "namespace is not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func cors(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(writer, request)
			return
		}
		if !slices.Contains(origins, origin) {
			http.Error(writer, "origin is not allowed", http.StatusForbidden)
			return
		}

		writer.Header().Set("Access-Control-Allow-Origin", origin)
		writer.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Namespace")
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
		writer.Header().Set("Vary", "Origin")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
