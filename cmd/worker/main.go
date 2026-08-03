// Command worker runs the software factory: a Temporal worker that reads
// ready Tickets from the factory's own Postgres and takes them from idea to
// open pull request.
//
// This file is the composition root, and it is the only place in the service
// where a concrete client meets an interface that consumes it. Construction is
// manual and explicit — no container, no reflection, no init() — so what the
// process is made of is what is written here, top to bottom, and a test
// elsewhere can hand any of it a fake.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.temporal.io/sdk/activity"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/0x63616c/software-factory/internal/activities"
	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttools"
	"github.com/0x63616c/software-factory/internal/blobs"
	checkpointclient "github.com/0x63616c/software-factory/internal/clients/checkpoint"
	"github.com/0x63616c/software-factory/internal/clients/codexresponses"
	"github.com/0x63616c/software-factory/internal/clients/github"
	"github.com/0x63616c/software-factory/internal/clients/k8s"
	"github.com/0x63616c/software-factory/internal/clients/runs"
	temporalapi "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/prompts"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

// shutdownGrace bounds how long the metrics server is given to finish in-flight
// scrapes once the worker has drained. It is short because nothing important
// happens over HTTP here: the work is on the task queue.
const shutdownGrace = 5 * time.Second

// workerStopTimeout is how long a drain waits for in-flight activities after a
// SIGTERM. It must be set explicitly: worker.Options{} leaves it at the SDK's
// zero value, and a zero timeout is not "wait forever" — awaitWaitGroup starts
// an already-fired timer, so the drain returns immediately, logs "graceful stop
// timed out" and cancels every activity context. That is the opposite of a
// drain, and it is what this file used to do while claiming otherwise.
//
// It is deliberately far shorter than a stage. Stages run on the Run Worker
// that hosts their Session, not on this main worker, so draining this
// process does not cancel them. This window is for the short control activities
// here — a GitHub comment, a transcript write, a credential rotation — finishing
// instead of being torn in half.
//
// The relationship that matters is with the pod's grace period, not with the
// stage timeout: terminationGracePeriodSeconds must exceed this plus
// shutdownGrace, or the kubelet SIGKILLs the drain it is waiting for. F1 sets
// 120s (infra/src/software-factory.ts, TERMINATION_GRACE_SECONDS), which leaves
// 25s of headroom. TestTheDrainFitsInsideThePodsGracePeriod is what stops
// either number moving alone.
const workerStopTimeout = 90 * time.Second

func main() {
	if err := run(); err != nil {
		// The process may have failed before it had a configured logger, so
		// this one is built on the spot. It is the only logger in the service
		// that is not the injected one, and it exists for exactly this line.
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			Error("the worker stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run builds the process and blocks until it drains.
//
// It returns an error rather than exiting, so every failure leaves through one
// path and main stays a place where nothing can hide.
func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return fmt.Errorf("reading the worker's configuration: %w", err)
	}

	logger := newLogger(cfg.LogLevel)
	bridgeKlog(logger)

	// Pinged before anything else starts, matching cmd/api: SoftwareStyle
	// tenet 7 (fail fast, fail helpful). An unreachable database would
	// otherwise start a dispatcher that looks healthy and fails its
	// RecordDispatcherState activity every tick forever, silently, instead of
	// crash-looping loudly at boot with the reason in its logs.
	//
	// One pool, one *store.Store, for both dispatchers: the legacy one's
	// per-tick dispatcher_state row (#551) and ADR-0012's Ticket-driven
	// pipeline (Tickets, Runs, Steps, Attempts, transcripts) below both read
	// and write the same factory Postgres. cmd/api already applies this
	// service's migrations at its own boot and is deployed ahead of this
	// worker (#554), so the worker only needs to dial and ping — it does not
	// re-run ApplyMigrations.
	dbPool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("constructing the factory database pool: %w", err)
	}
	defer dbPool.Close()
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPing()
	if err := dbPool.Ping(pingCtx); err != nil {
		return fmt.Errorf("pinging the factory database before worker startup: %w", err)
	}
	factoryStore := store.New(dbPool)

	// The one metrics registry in this process. Prometheus panics on a
	// duplicate registration, deliberately, so a second registry or a second
	// construction of the metrics that record into it is a crash in
	// production; that is why both live here and are passed down.
	registry, metrics := newObservability()
	blobStore, err := blobs.NewHTTPStore(cfg.BlobsURL, nil)
	if err != nil {
		return fmt.Errorf("opening HTTP blob store: %w", err)
	}

	// Bound before the worker starts, so a port already in use is a startup
	// failure with a clear message rather than a worker that runs unmonitored.
	listener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("listening for metrics on %s (METRICS_ADDR): %w", cfg.MetricsAddr, err)
	}
	var activationReady atomic.Bool
	server := &http.Server{
		Handler:           observability(registry, activationReady.Load),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("the metrics server stopped", slog.String("error", err.Error()))
		}
	}()
	defer stopServer(server, logger)

	temporal, err := temporalapi.Dial(temporalapi.Options{
		HostPort:  cfg.TemporalHostPort,
		Namespace: cfg.TemporalNamespace,
		Logger:    tlog.NewStructuredLogger(logger),
	}, blobStore, metrics)
	if err != nil {
		return fmt.Errorf("dialling Temporal at %s in namespace %s: %w", cfg.TemporalHostPort, cfg.TemporalNamespace, err)
	}
	defer temporal.Close()

	controlWorker := worker.New(temporal, work.TargetDispatcherTaskQueue, worker.Options{
		WorkerStopTimeout: workerStopTimeout,
	})
	mainWorker := worker.New(temporal, work.TaskQueue, worker.Options{
		WorkerStopTimeout: workerStopTimeout,
	})

	renderer, err := newPromptRenderer()
	if err != nil {
		return fmt.Errorf("building the prompt renderer: %w", err)
	}

	clk := clock.System{}
	tokenSource, err := newCodexAuthSource(cfg, clk, logger)
	if err != nil {
		return fmt.Errorf("building the codex credential source: %w", err)
	}
	ghCfg, err := config.LoadGitHub()
	if err != nil {
		return fmt.Errorf("reading the GitHub App's configuration: %w", err)
	}
	ghClient, err := github.New(ghCfg, clk, logger)
	if err != nil {
		return fmt.Errorf("building the GitHub client: %w", err)
	}
	runWorkerControl, err := newTargetRunWorkerControlActivities(cfg, ghCfg, ghClient, factoryStore, logger)
	if err != nil {
		return fmt.Errorf("building the target Run Worker activity set: %w", err)
	}

	// ADR-0012's second, Ticket-driven activity sets, over the same
	// factoryStore: they read and write Tickets, Runs, Steps, Attempts and
	// transcripts, never a GitHub issue or comment.
	ticketActs, err := activities.NewTicketActivities(factoryStore)
	if err != nil {
		return fmt.Errorf("building the ticket activity set: %w", err)
	}
	targetRecordingActs, err := activities.NewTargetRecordingActivities(factoryStore)
	if err != nil {
		return fmt.Errorf("building the target recording activity set: %w", err)
	}
	targetRecoveryActs, err := activities.NewTargetRecoveryActivities(factoryStore)
	if err != nil {
		return fmt.Errorf("building the target recovery activity set: %w", err)
	}
	maintenanceActs, err := activities.NewTargetMaintenanceActivities(factoryStore)
	if err != nil {
		return fmt.Errorf("building the target maintenance activity set: %w", err)
	}
	targetExecutionActs, err := activities.NewTargetExecutionActivities(runs.New(temporal))
	if err != nil {
		return fmt.Errorf("building the target execution activity set: %w", err)
	}
	targetEvidenceActs, err := activities.NewTargetAgentEvidenceActivities(factoryStore, blobStore)
	if err != nil {
		return fmt.Errorf("building the target agent evidence activity set: %w", err)
	}
	toolsets, err := agenttools.NewToolsets(work.RepoDir, "model/catalog", blobStore)
	if err != nil {
		return fmt.Errorf("building the model-visible agent toolsets: %w", err)
	}
	credentialSource, err := codexresponses.NewManagedCredentialSource(tokenSource)
	if err != nil {
		return fmt.Errorf("adapting the durable codex credential source: %w", err)
	}
	turner, err := codexresponses.New(
		&http.Client{Timeout: 110 * time.Second}, cfg.CodexResponsesEndpoint, credentialSource, logger,
	)
	if err != nil {
		return fmt.Errorf("building the direct Codex Responses client: %w", err)
	}
	modelActs, err := agentactivities.NewObservedActivities(turner, blobStore, clk, metrics, logger, toolsets...)
	if err != nil {
		return fmt.Errorf("building the agent model activity set: %w", err)
	}
	promptActs, err := agentactivities.NewPromptActivities(prompts.NewActivityRenderer(renderer), blobStore)
	if err != nil {
		return fmt.Errorf("building the agent prompt activity set: %w", err)
	}

	registerControl(controlWorker, ticketActs)
	register(
		mainWorker, runWorkerControl, targetRecordingActs, targetRecoveryActs, maintenanceActs, targetExecutionActs,
		targetEvidenceActs, modelActs, promptActs, logger,
	)

	activationCtx := context.Background()
	readiness := temporalapi.NewActivationReadiness(temporal, cfg.TemporalNamespace)
	request := deployedDispatcherPolicyPublicationRequest(cfg.DeployID)
	publisher := targetDispatcherPolicyPublisher{
		publisher: temporalapi.NewDispatcherPublisher(temporal),
		input: workflows.DispatcherInput{
			CloneURL: cloneURL(ghCfg),
			Model:    work.DefaultFactoryConfig().DefaultModel,
		},
	}
	stopWorkers, err := activateTargetWorkers(
		activationCtx, controlWorker, mainWorker,
		func(ctx context.Context) error { return ensureActivationReady(ctx, readiness, factoryStore) },
		publisher, request,
		func(ctx context.Context) error { return ensureMaintainFactorySchedule(ctx, temporal) },
	)
	if err != nil {
		return err
	}
	defer stopWorkers()
	activationReady.Store(true)

	logger.Info("worker starting",
		// Two different concepts that happen to share the string
		// "software-factory": the queue work is scheduled on, and the Temporal
		// namespace it lives in. They are logged as separate fields, and a
		// runbook command line that transposes --task-queue and --namespace
		// would be invisible on the wire — worth reading twice.
		slog.String("task_queue", work.TaskQueue),
		slog.String("temporal_namespace", cfg.TemporalNamespace),
		slog.String("run_worker_namespace", cfg.RunWorkerNamespace),
		slog.String("pod", cfg.PodName),
		slog.String("dispatcher_workflow_id", work.TargetDispatcherWorkflowID),
		slog.String("dispatcher_control_queue", work.TargetDispatcherTaskQueue),
		slog.String("maintenance_schedule_id", work.MaintainFactoryScheduleID),
	)

	<-worker.InterruptCh()
	stopWorkers()
	logger.Info("worker drained")
	return nil
}

// newObservability builds the process's one metrics registry and the one set of
// stage metrics recording into it.
//
// Both are singletons and both are here for the same reason: Prometheus panics
// on a duplicate registration, deliberately. A second registry or a second
// construction of the metrics is a crash in production — and the quieter half
// of that is worse. A track that built its own Metrics against its own registry
// would not panic at all; its counters would increment into a registry nothing
// serves, and /metrics would stay empty while every call site looked correct.
//
// The stage metrics are built even though nothing records into them yet,
// because "construct it where it can only happen once" is the property, and a
// gap here is what the next track fills with its own copy.
func newObservability() (*prometheus.Registry, *telemetry.Metrics) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return registry, telemetry.NewMetrics(registry)
}

// register puts this worker's workflows and activities on its task queue.
//
// One function, one call site: a registration list that grew in two places
// would be a queue serving a set of workflows nobody can enumerate. The
// WorkTicket and Dispatcher workflows, and every activity method, land here
// and nowhere else.
//
// acts arrives fully built rather than being assembled here: newActivities is
// where every concrete client meets the interface it satisfies, and this
// function's only job is telling the worker about the result — mixing the two
// would make "what got registered" and "what got constructed" one lookup
// instead of two.
func register(
	w worker.Worker,
	runWorkerControl *activities.RunWorkerControlActivities,
	targetRecordingActs *activities.TargetRecordingActivities,
	targetRecoveryActs *activities.TargetRecoveryActivities,
	maintenanceActs *activities.TargetMaintenanceActivities,
	targetExecutionActs *activities.TargetExecutionActivities,
	targetEvidenceActs *activities.TargetAgentEvidenceActivities,
	modelActs *agentactivities.Activities,
	promptActs *agentactivities.PromptActivities,
	logger *slog.Logger,
) {
	w.RegisterWorkflow(workflows.WorkOnTicket)
	w.RegisterWorkflow(workflows.MaintainFactory)
	w.RegisterWorkflowWithOptions(workflows.AgentWorkflow, workflow.RegisterOptions{Name: agent.WorkflowName})
	w.RegisterActivity(runWorkerControl)
	w.RegisterActivity(targetRecordingActs)
	w.RegisterActivity(targetRecoveryActs)
	w.RegisterActivity(maintenanceActs)
	w.RegisterActivity(targetExecutionActs)
	w.RegisterActivityWithOptions(targetEvidenceActs.Finalize, activity.RegisterOptions{Name: activities.TargetAgentEvidenceFinalizeActivityName})
	w.RegisterActivityWithOptions(promptActs.Prepare, activity.RegisterOptions{Name: agent.PrepareActivityName})
	w.RegisterActivityWithOptions(modelActs.ModelTurn, activity.RegisterOptions{Name: agent.ModelTurnActivityName})
	w.RegisterActivityWithOptions(modelActs.RecordLifecycle, activity.RegisterOptions{Name: agent.LifecycleActivityName})
	w.RegisterActivityWithOptions(promptActs.DecodeFinalOutput, activity.RegisterOptions{Name: agent.FinalizeActivityName})
	logger.Info("registrations",
		slog.Int("workflows", 3),
		slog.Int("max_in_flight", work.DefaultDispatcherPolicy().MaxInFlight),
	)
}

func registerControl(w worker.Worker, ticketActs *activities.TicketActivities) {
	w.RegisterWorkflow(workflows.Dispatcher)
	w.RegisterActivity(ticketActs.AwaitDispatchableTickets)
}

// newTargetRunWorkerControlActivities builds only the Kubernetes authority the
// activated Run Worker model needs. It deliberately never constructs the
// retired sandbox pods/exec and remote repository-transfer client.
func newTargetRunWorkerControlActivities(
	cfg config.Worker,
	ghCfg config.GitHub,
	ghClient *github.Client,
	checkpointBinder interface {
		activities.CheckpointCapabilityBinder
		activities.RepositoryCapabilityBinder
	},
	logger *slog.Logger,
) (*activities.RunWorkerControlActivities, error) {
	runWorkers, err := k8s.NewRunWorkersInCluster(cfg.RunWorkerNamespace, logger, cfg.RunWorkerImagePullSecretName)
	if err != nil {
		return nil, fmt.Errorf("building the Kubernetes Run Worker client: %w", err)
	}
	capabilities, err := checkpointclient.NewCapabilityMinter(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("building the checkpoint capability minter: %w", err)
	}
	control, err := activities.NewRunWorkerControlActivities(activities.RunWorkerControlDeps{
		Workers: runWorkers, GitHub: ghClient, Capabilities: capabilities,
		Binder: checkpointBinder, RepositoryBinder: checkpointBinder,
		Template: activities.RunWorkerTemplate{
			Image: cfg.RunWorkerImage, CPURequest: cfg.RunWorkerCPURequest, MemoryLimit: cfg.RunWorkerMemoryLimit,
			DeadlineSeconds: work.RunWorkerDeadlineSeconds,
			Env: map[string]string{
				work.GhConfigDirEnv:               work.GhConfigDir,
				work.RunWorkerTemporalHostPortEnv: cfg.TemporalHostPort, work.RunWorkerTemporalNamespaceEnv: cfg.TemporalNamespace,
				work.RunWorkerBlobsURLEnv: cfg.BlobsURL, work.RunWorkerCheckpointAPIURLEnv: cfg.CheckpointAPIURL,
				work.RunWorkerGitHubRepositoryEnv: ghCfg.Owner + "/" + ghCfg.Repo,
				work.RunWorkerMetricsAddrEnv:      ":9090",
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building Run Worker control activities: %w", err)
	}
	return control, nil
}

// cloneURL is the HTTPS clone URL for the one repository this service works
// tickets against, built from the same GITHUB_OWNER/GITHUB_REPO config.LoadGitHub
// already reads and every worker already has set — CloneRepo's credential and
// this URL both describe the App's own installation, so there is no new
// required environment variable here, only a second use of two that already
// are.
func cloneURL(cfg config.GitHub) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", cfg.Owner, cfg.Repo)
}

// stopServer gives in-flight scrapes a moment to finish. Its failure is logged
// rather than returned: the worker has already drained by the time this runs,
// and a metrics server that would not close is not a reason to report the run
// as failed.
func stopServer(server *http.Server, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Warn("the metrics server did not shut down cleanly", slog.String("error", err.Error()))
	}
}
