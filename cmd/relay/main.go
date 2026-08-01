// Command relay verifies GitHub webhooks once then independently forwards them
// to configured consumers.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
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
		return fmt.Errorf("reading relay configuration: %w", err)
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
		return fmt.Errorf("listening for relay metrics on %s: %w", cfg.MetricsAddr, err)
	}
	go serveMetrics(metricsListener, registry, logger)

	if serveErr := http.Serve(listener, handler); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serving relay requests: %w", serveErr)
	}
	return nil
}

func serveMetrics(listener net.Listener, registry *prometheus.Registry, logger *slog.Logger) {
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	if err := http.Serve(listener, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/metrics" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		handler.ServeHTTP(writer, request)
	})); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("metrics server stopped", slog.String("error", err.Error()))
	}
}
