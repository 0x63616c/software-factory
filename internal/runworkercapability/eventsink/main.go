// Command eventsink is the independent append-only boundary used by the v0
// Codex capability probe. It is test harness code, not a production service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const maxEventBytes = 1 << 20

type eventSink struct {
	mu    sync.Mutex
	file  *os.File
	count int
}

func newEventSink(path string) (*eventSink, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open event store: %w", err)
	}
	return &eventSink{file: file}, nil
}

func (s *eventSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/events":
		s.appendEvent(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/count":
		if err := s.writeCount(w); err != nil {
			slog.ErrorContext(r.Context(), "write event count response", "error", err)
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *eventSink) appendEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEventBytes+1))
	if err != nil {
		http.Error(w, "read event", http.StatusBadRequest)
		return
	}
	if len(body) == 0 || len(body) > maxEventBytes || !json.Valid(body) {
		http.Error(w, "event must be one JSON value", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.file.Write(append(body, '\n')); err != nil {
		http.Error(w, "store event", http.StatusInternalServerError)
		return
	}
	if err := s.file.Sync(); err != nil {
		http.Error(w, "sync event", http.StatusInternalServerError)
		return
	}
	s.count++
	w.WriteHeader(http.StatusNoContent)
}

func (s *eventSink) writeCount(w http.ResponseWriter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := fmt.Fprintln(w, strconv.Itoa(s.count)); err != nil {
		return fmt.Errorf("write count response: %w", err)
	}
	return nil
}

func (s *eventSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("close event store: %w", err)
	}
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(); err != nil {
		logger.Error("event sink failed", "error", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	eventsPath := flag.String("events", "", "append-only JSONL output path")
	readyPath := flag.String("ready", "", "path that receives the listening address")
	flag.Parse()
	if *eventsPath == "" || *readyPath == "" {
		return errors.New("-events and -ready are required")
	}

	sink, err := newEventSink(*eventsPath)
	if err != nil {
		return fmt.Errorf("create event sink: %w", err)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler:           sink,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := os.WriteFile(*readyPath, []byte(listener.Addr().String()), 0o600); err != nil {
		return fmt.Errorf("write ready address: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveError := make(chan error, 1)
	go func() {
		serveError <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down server: %w", err)
		}
		if err := <-serveError; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	}
}
