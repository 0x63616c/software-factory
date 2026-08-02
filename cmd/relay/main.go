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

	relayServer := httpserver.Serve(listener, handler, logger, "relay")
	metricsServer := httpserver.Serve(metricsListener, metricsHandler(registry), logger, "relay metrics")

	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		select {
		case err := <-relayServer.Errors():
			return fmt.Errorf("serving relay requests: %w", err)
		case err := <-metricsServer.Errors():
			return fmt.Errorf("serving relay metrics: %w", err)
		case <-shutdown.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := relayServer.Shutdown(ctx); err != nil {
				return fmt.Errorf("shutting down relay requests: %w", err)
			}
			if err := metricsServer.Shutdown(ctx); err != nil {
				return fmt.Errorf("shutting down relay metrics: %w", err)
			}
			return nil
		}
	}
}

func metricsHandler(registry *prometheus.Registry) http.Handler {
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/metrics" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		handler.ServeHTTP(writer, request)
	})
}
