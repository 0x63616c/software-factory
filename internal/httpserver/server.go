package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server wraps an http.Server with lifecycle helpers used by command entrypoints.
type Server struct {
	listener   net.Listener
	httpServer *http.Server
	logger     *slog.Logger
	label      string
	errors     chan error
}

const defaultReadHeaderTimeout = 5 * time.Second
const defaultShutdownTimeout = 5 * time.Second

// Serve constructs and starts an HTTP server in the background.
func Serve(listener net.Listener, handler http.Handler, logger *slog.Logger, label string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return ServeWithServer(listener, &http.Server{Handler: handler}, logger, label)
}

// ServeWithServer starts a pre-configured server in the background, defaulting
// ReadHeaderTimeout only when not explicitly set.
func ServeWithServer(listener net.Listener, server *http.Server, logger *slog.Logger, label string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if server.ReadHeaderTimeout == 0 {
		server.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	serverErrors := make(chan error, 1)
	s := &Server{listener: listener, httpServer: server, logger: logger, label: label, errors: serverErrors}
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", slog.String("server", label), slog.String("error", err.Error()))
		}
		serverErrors <- err
	}()
	return s
}

// Shutdown gracefully stops the server and returns a normalized wrapped error if one
// occurs.
func (server *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := server.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutting down %s server: %w", server.label, err)
	}
	err := <-server.errors
	if err == nil {
		return nil
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serving %s server: %w", server.label, err)
}

// RunWithShutdownError starts a server and blocks until it stops or context is done.
func RunWithShutdownError(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger, label string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	server := Serve(listener, handler, logger, label)
	select {
	case err := <-server.errors:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving %s server: %w", label, err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

func (server *Server) Errors() <-chan error {
	return server.errors
}
