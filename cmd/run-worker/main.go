// Command run-worker is the target per-Run Temporal Session worker. It polls
// exactly one generation-specific queue and hosts typed agent tools and
// repository-affine activities locally. Provisioning, Secret rotation, model
// calls, recording, and cleanup remain main-worker capabilities.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/blobs"
	checkpointclient "github.com/0x63616c/software-factory/internal/clients/checkpoint"
	"github.com/0x63616c/software-factory/internal/clients/github"
	"github.com/0x63616c/software-factory/internal/clients/local"
	temporalapi "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/httpserver"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
)

const (
	runWorkerStopTimeout = 90 * time.Second
	shutdownGrace        = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("the Run Worker stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadRunWorker()
	if err != nil {
		return fmt.Errorf("reading Run Worker configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metrics := telemetry.NewMetrics(registry)
	blobStore, err := blobs.NewHTTPStore(cfg.BlobsURL, nil)
	if err != nil {
		return fmt.Errorf("opening HTTP blob store: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("listening for Run Worker metrics on %s: %w", cfg.MetricsAddr, err)
	}
	metricsServer := httpserver.Serve(listener, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), logger, "run-worker metrics")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := metricsServer.Shutdown(ctx); err != nil {
			logger.Warn("the Run Worker metrics server did not stop cleanly", "error", err)
		}
	}()

	temporal, err := temporalapi.Dial(temporalapi.Options{
		HostPort: cfg.TemporalHostPort, Namespace: cfg.TemporalNamespace,
		Logger: tlog.NewStructuredLogger(logger),
	}, blobStore, metrics)
	if err != nil {
		return fmt.Errorf("dialling Temporal: %w", err)
	}
	defer temporal.Close()

	targetActs, err := newActivities(cfg, logger)
	if err != nil {
		return fmt.Errorf("building Run Worker activities: %w", err)
	}
	w := worker.New(temporal, cfg.TaskQueue, worker.Options{
		WorkerStopTimeout:                  runWorkerStopTimeout,
		EnableSessionWorker:                true,
		MaxConcurrentSessionExecutionSize:  1,
		MaxConcurrentActivityExecutionSize: 1,
	})
	register(w, targetActs)
	logger.Info("Run Worker starting", "run_worker", cfg.ID, "run_id", cfg.Identity.RunID,
		"generation", cfg.Identity.Generation, "task_queue", cfg.TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("running Run Worker %s: %w", cfg.ID, err)
	}
	return nil
}

type activityRegistrar interface {
	RegisterActivity(any)
}

func register(w activityRegistrar, targetActs *activities.RunWorkerActivities) {
	w.RegisterActivity(targetActs)
}

func newActivities(cfg config.RunWorker, logger *slog.Logger) (*activities.RunWorkerActivities, error) {
	repositoryCheckpointFactory, err := checkpointclient.NewRepositoryFactory(cfg.CheckpointAPIURL, work.RunWorkerRepositoryCapabilityFile, http.DefaultClient, os.ReadFile)
	if err != nil {
		return nil, fmt.Errorf("building repository checkpoint client factory: %w", err)
	}
	repository, err := local.NewRepository(work.RepoDir, local.OSGitRunner{})
	if err != nil {
		return nil, fmt.Errorf("building local target repository: %w", err)
	}
	githubClient, err := github.NewProjected(cfg.GitHubOwner, cfg.GitHubRepo, work.RunWorkerGitHubTokenFile, os.ReadFile, logger)
	if err != nil {
		return nil, fmt.Errorf("building projected GitHub client: %w", err)
	}
	target, err := activities.NewRunWorkerActivities(activities.RunWorkerDeps{
		Clock: clock.System{}, Repository: repository, GitHub: githubClient, Identity: cfg.Identity, Branch: cfg.Branch,
		RepositoryCheckpoints: func(identity work.RunWorkerIdentity) (activities.RepositoryCheckpoint, error) {
			return repositoryCheckpointFactory.Open(identity)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building target Run Worker activities: %w", err)
	}
	return target, nil
}
