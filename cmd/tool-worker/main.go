// Command tool-worker hosts the credential-free typed-tool activities for one
// target Run Worker generation. Model calls and credentials remain in the Run
// Worker container; this sidecar only reads or mutates the shared checkout.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.temporal.io/sdk/activity"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttools"
	"github.com/0x63616c/software-factory/internal/blobs"
	temporalapi "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/work"
)

const workerStopTimeout = 90 * time.Second

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			Error("the Tool Worker stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadToolWorker()
	if err != nil {
		return fmt.Errorf("reading the Tool Worker's configuration: %w", err)
	}
	logger := newLogger(cfg.LogLevel)
	clk := clock.System{}
	blobStore, err := blobs.NewHTTPStore(cfg.BlobsURL, nil)
	if err != nil {
		return fmt.Errorf("opening HTTP blob store: %w", err)
	}
	temporal, err := temporalapi.Dial(temporalapi.Options{
		HostPort: cfg.TemporalHostPort, Namespace: cfg.TemporalNamespace,
		Logger: tlog.NewStructuredLogger(logger),
	}, blobStore, nil)
	if err != nil {
		return fmt.Errorf("dialling Temporal at %s in namespace %s: %w", cfg.TemporalHostPort, cfg.TemporalNamespace, err)
	}
	defer temporal.Close()

	w := worker.New(temporal, cfg.TaskQueue, worker.Options{
		WorkerStopTimeout:                  workerStopTimeout,
		EnableSessionWorker:                true,
		MaxConcurrentSessionExecutionSize:  1,
		MaxConcurrentActivityExecutionSize: 1,
	})
	toolsets, err := agenttools.NewToolsets(work.RepoDir, "tool-worker/"+cfg.TaskQueue, blobStore)
	if err != nil {
		return fmt.Errorf("building the Tool Worker agent toolsets: %w", err)
	}
	toolActivities, err := agentactivities.NewToolActivities(blobStore, clk, toolsets...)
	if err != nil {
		return fmt.Errorf("building the Tool Worker activity: %w", err)
	}
	register(w, toolActivities, logger)
	logger.Info("Tool Worker starting", slog.String("task_queue", cfg.TaskQueue), slog.String("temporal_namespace", cfg.TemporalNamespace))
	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("running the Tool Worker on task queue %s: %w", cfg.TaskQueue, err)
	}
	logger.Info("Tool Worker drained")
	return nil
}

// register exposes only the generic typed-tool boundary. CreateSession still
// binds the caller to this one-worker queue through the session-worker options
// above; this process never registers workflows or model activities.
func register(w worker.Worker, tools *agentactivities.ToolActivities, logger *slog.Logger) {
	w.RegisterActivityWithOptions(tools.Tool, activity.RegisterOptions{Name: agent.ToolActivityName})
	logger.Info("registrations", slog.Int("workflows", 0), slog.Int("activities", 1))
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
