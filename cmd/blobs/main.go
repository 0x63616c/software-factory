// Command blobs serves opaque payload blobs from the mounted storage volume.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/config"
)

const maxBlobSize int64 = 64 << 20

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("the blob service stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadBlobs()
	if err != nil {
		return fmt.Errorf("reading blob service configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	store, err := blobs.NewFileStore(cfg.Root)
	if err != nil {
		return fmt.Errorf("opening blob store: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listening for blob requests on %s (LISTEN_ADDR): %w", cfg.ListenAddr, err)
	}
	logger.Info("blob service starting", slog.String("address", cfg.ListenAddr), slog.String("root", cfg.Root))

	server := &http.Server{
		Handler:           newHandler(store, cfg.Root, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serving blob requests: %w", err)
		}
	case <-shutdown.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutting down blob server: %w", err)
		}
	}
	return nil
}

func newHandler(store blobs.Store, root string, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			handleHealthz(writer, root, logger)
			return
		}
		if !strings.HasPrefix(request.URL.Path, "/blobs/") {
			http.NotFound(writer, request)
			return
		}

		key, err := blobs.ParseKey(strings.TrimPrefix(request.URL.Path, "/blobs/"))
		if err != nil {
			logger.Warn("invalid blob key", slog.String("path", request.URL.Path), slog.String("error", err.Error()))
			http.Error(writer, "invalid blob key", http.StatusBadRequest)
			return
		}

		switch request.Method {
		case http.MethodGet:
			handleGet(writer, request, store, key, logger)
		case http.MethodPut:
			handlePut(writer, request, store, key, logger)
		default:
			writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func handleHealthz(writer http.ResponseWriter, root string, logger *slog.Logger) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("storage root is not a directory")
		}
		logger.Error("blob storage root is unavailable", slog.String("root", root), slog.String("error", err.Error()))
		http.Error(writer, "storage root unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.WriteHeader(http.StatusOK)
}

func handleGet(writer http.ResponseWriter, request *http.Request, store blobs.Store, key blobs.Key, logger *slog.Logger) {
	value, err := store.Get(request.Context(), key)
	if errors.Is(err, blobs.ErrNotFound) {
		http.NotFound(writer, request)
		return
	}
	if err != nil {
		logger.Error("get blob", slog.String("key", key.String()), slog.String("error", err.Error()))
		http.Error(writer, "get blob", http.StatusInternalServerError)
		return
	}
	if _, err := writer.Write(value); err != nil {
		logger.Warn("write blob response", slog.String("key", key.String()), slog.String("error", err.Error()))
	}
}

func handlePut(writer http.ResponseWriter, request *http.Request, store blobs.Store, key blobs.Key, logger *slog.Logger) {
	value, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxBlobSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(writer, "blob exceeds maximum size", http.StatusRequestEntityTooLarge)
			return
		}
		logger.Warn("read blob request", slog.String("key", key.String()), slog.String("error", err.Error()))
		http.Error(writer, "read blob request", http.StatusBadRequest)
		return
	}
	if err := store.Put(request.Context(), key, value); err != nil {
		logger.Error("put blob", slog.String("key", key.String()), slog.String("error", err.Error()))
		http.Error(writer, "put blob", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
