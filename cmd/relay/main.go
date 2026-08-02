// Command relay verifies GitHub webhooks once then independently forwards them
// to configured consumers.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/httpserver"
	"github.com/0x63616c/software-factory/internal/relay"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("webhook relay stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadRelay()
	if err != nil {
		return errors.Wrap(err, "reading relay configuration")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	registry := prometheus.NewRegistry()
	handler := relay.NewHandler(
		cfg.WebhookSecret,
		cfg.Targets,
		relay.NewHTTPPoster(http.DefaultClient),
		clock.System{},
		logger,
		registry,
	)

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listening for relay requests on %s: %w", cfg.ListenAddr, err)
	}
	metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return errors.Wrapf(err, "listening for relay metrics on %s", cfg.MetricsAddr)
	}
	metricsServer := httpserver.ServeWithServer(
		metricsListener,
		&http.Server{
			Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/metrics" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(writer, request)
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger,
		"relay metrics",
	)
	defer func() {
		if err := metricsServer.Shutdown(context.Background(), 5*time.Second); err != nil {
			logger.Error("the relay metrics server did not stop cleanly", "error", err)
		}
	}()

	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := httpserver.RunWithShutdownError(shutdown, listener, handler, logger, "relay"); err != nil {
		return errors.Wrap(err, "serving relay requests")
	}
	return nil
}
