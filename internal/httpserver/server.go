package httpserver

import (
	"context"
	stdErrors "errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/cockroachdb/errors"
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultShutdownTimeout   = 5 * time.Second
)

// Server owns one in-process HTTP server started by ServeWithServer.
//
// Keep this type small and opinionated: one listener, one mux, one logger, and
// one way to expose readiness, shutdown, and error reporting.
type Server struct {
	listener net.Listener
	server   *http.Server
	logger   *slog.Logger
	label    string
	done     chan error
}

// ServeWithServer starts an already-configured server in the background.
//
// The returned handle is intentionally narrow. Call Run for the signal-driven
// path, or Shutdown from defers when a parent component exits for another reason.
func ServeWithServer(listener net.Listener, server *http.Server, logger *slog.Logger, label string) *Server {
	if server == nil {
		server = &http.Server{}
	}
	if server.ReadHeaderTimeout == 0 {
		server.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		listener: listener,
		server:   server,
		logger:   logger,
		label:    label,
		done:     make(chan error, 1),
	}
	go func() {
		logger.Info("HTTP server starting", slog.String("address", listener.Addr().String()), slog.String("server", label))
		if err := server.Serve(listener); err != nil && !stdErrors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped", slog.String("server", label), slog.String("error", err.Error()))
			s.done <- errors.Wrap(err, "HTTP server serve loop failed")
			return
		}
		s.done <- nil
	}()
	return s
}

// Run starts one server and blocks until its context is cancelled.
//
// A context cancel triggers graceful shutdown using the provided timeout. On any
// unclean serve error, Run returns immediately with that error.
func (s *Server) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	select {
	case err := <-s.done:
		return err
	case <-ctx.Done():
		shutdownErr := s.Shutdown(ctx, shutdownTimeout)
		runErr := <-s.done
		if runErr != nil {
			return runErr
		}
		if shutdownErr != nil {
			return shutdownErr
		}
		return nil
	}
}

// Shutdown gracefully closes an active server and waits up to timeout.
func (s *Server) Shutdown(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return errors.Wrapf(err, "shutting down %q HTTP server", s.label)
	}
	return nil
}

// RunWithShutdownError starts a server and blocks using a context lifecycle.
func RunWithShutdownError(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger, label string) error {
	return RunWithShutdownErrorTimeout(ctx, listener, handler, logger, label, defaultShutdownTimeout)
}

// RunWithShutdownErrorTimeout allows overriding the shutdown grace period.
func RunWithShutdownErrorTimeout(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger, label string, shutdownTimeout time.Duration) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
	}
	runner := ServeWithServer(listener, server, logger, label)
	return runner.Run(ctx, shutdownTimeout)
}
