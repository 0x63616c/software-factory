package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Worker is everything the worker process needs to start: where Temporal is,
// where Run Workers go, what to run in them, and how loudly to say what it is
// doing.
//
// It deliberately holds no tunable behaviour — no concurrency cap, no model
// choice, no dry-run switch. Those are the dispatcher's configuration, which
// travels as a Temporal signal so it can change without a deploy, and a second
// copy of them here would be a second answer to "what is it running under".
type Worker struct {
	// DatabaseURL is the factory Postgres connection the dispatcher writes its
	// per-tick state to (#551). Same variable name as the API's
	// (DatabaseURLEnv, database.go) — one Postgres, one spelling, whichever
	// process is reading it.
	DatabaseURL string

	// TemporalHostPort is the frontend to dial, host:port.
	TemporalHostPort string

	// TemporalNamespace is the namespace this service's workflows live in.
	TemporalNamespace string

	// RunWorkerNamespace is the Kubernetes namespace per-Run pods are created
	// in. The worker's Role is scoped to it.
	RunWorkerNamespace string

	// RunWorkerImage is the separately pinned target execution image.
	RunWorkerImage string

	// CheckpointAPIURL is projected into target Run Workers as a non-secret
	// service address for their capability-authenticated evidence writes.
	CheckpointAPIURL string

	// MetricsAddr is what the metrics and health server listens on.
	MetricsAddr string

	// PodName is this pod's own name, from the downward API. It is not
	// decoration: it identifies the holder of the credential refresh lease, and
	// a lease nobody can attribute cannot be investigated at 3am.
	PodName string

	// BlobsURL is the in-cluster blob API. It is copied into Run Worker pods so
	// their future payload codec clients use the same durable service.
	BlobsURL string

	// CodexResponsesEndpoint is the direct subscription-backed model endpoint.
	// Only the main worker receives it; Run Worker pods never call the provider.
	CodexResponsesEndpoint string

	// CodexAuthSecretName is the Kubernetes Secret holding the codex
	// credential.
	//
	// It is read rather than hardcoded because the deploy pins the worker's
	// Role to this exact name with `resourceNames` (infra/src/software-factory.ts).
	// A second spelling in Go would be a grant that covers nothing, and it
	// fails as Forbidden rather than as a bad credential — which sends whoever
	// debugs it to the credential layer instead of to RBAC.
	CodexAuthSecretName string

	// RunWorkerImagePullSecretName is the Kubernetes Secret every Run Worker pod
	// authenticates its image pull with. The image is private on
	// GHCR, like the worker's own — but unlike the worker's Deployment, a pod
	// podspec.go builds has no Pulumi-managed spec to set imagePullSecrets on
	// by hand, so this name has to arrive as config and be threaded onto each
	// pod explicitly (k8s.WithImagePullSecret).
	//
	// It is read rather than assumed because there is no cluster-side
	// fallback: an empty value here is a pod with no imagePullSecrets at all,
	// which reads as a healthy Create followed by an ErrImagePull rather than
	// as a startup failure (#404).
	RunWorkerImagePullSecretName string

	// LogLevel is the level everything below this process logs at.
	LogLevel slog.Level

	// RunWorkerCPURequest and RunWorkerMemoryLimit are the per-Run worker
	// pod's CPU request and memory limit, as Kubernetes quantity strings
	// ("2", "8Gi"). There is deliberately no CPU limit: CPU is compressible
	// and #87 banned limiting it repo-wide, so only a request is configured
	// here (see podspec.go). Memory is incompressible, so it keeps both a
	// request and a limit.
	//
	// Optional, like LogLevel: nothing deploys them today (#340 landed the
	// worker's own composition ahead of a resourced deploy for the sandbox
	// pods it creates), and a default that lets a first deploy create a
	// working pod is worth more here than a crashloop over a number nobody
	// has decided is wrong yet. Once infra sets these explicitly the default
	// stops mattering; until then they are real resource settings, not
	// placeholders that skip enforcement.
	RunWorkerCPURequest  string
	RunWorkerMemoryLimit string
}

// Defaults for the two optional Run Worker resource settings. See their fields'
// doc comment on Worker for why they default rather than fail.
//
// defaultRunWorkerMemoryLimit is set from a measurement, not a guess: #493
// measured `bun run typecheck` peaking at 6.92Gi inside the sandbox image,
// against the previous 4Gi limit — which is why #479's runs died mid-implement.
// 8Gi is 1.16x that peak, a deliberate near-term unblock rather than a
// comfortable margin; #492 (bounding tsc's fan-out) is the real fix for the
// peak itself.
const (
	defaultRunWorkerCPURequest  = "2"
	defaultRunWorkerMemoryLimit = "8Gi"
)

// Environment variables LoadWorker reads. They are constants because the errors
// quote them, and an error naming an input that does not exist is worse than no
// error at all.
const (
	// envDatabaseURL is the same spelling as config.DatabaseURLEnv
	// (database.go) and config's own envAPIDatabaseURL (api.go): one Postgres,
	// one variable name, whichever process reads it. A literal here rather
	// than a reference to DatabaseURLEnv, because
	// scripts/test-worker-env-parity.sh greps workerEnvNames() for a string
	// literal assigned to each identifier it names, by design (D1, #340) — a
	// bare reference would silently drop out of that check's required set
	// instead of failing loudly.
	envDatabaseURL              = "SOFTWARE_FACTORY_DATABASE_URL"
	envTemporalHostPort         = "TEMPORAL_HOST_PORT"
	envTemporalNamespace        = "TEMPORAL_NAMESPACE"
	envRunWorkerNamespace       = "RUN_WORKER_NAMESPACE"
	envRunWorkerImage           = "RUN_WORKER_IMAGE"
	envCheckpointAPIURL         = "CHECKPOINT_API_URL"
	envMetricsAddr              = "METRICS_ADDR"
	envPodName                  = "POD_NAME"
	envBlobsURL                 = "BLOBS_URL"
	envCodexResponsesEndpoint   = "CODEX_RESPONSES_ENDPOINT"
	envCodexAuthSecret          = "CODEX_AUTH_SECRET_NAME"
	envRunWorkerImagePullSecret = "RUN_WORKER_IMAGE_PULL_SECRET_NAME"
	envLogLevel                 = "LOG_LEVEL"

	envRunWorkerCPURequest  = "RUN_WORKER_CPU_REQUEST"
	envRunWorkerMemoryLimit = "RUN_WORKER_MEMORY_LIMIT"
)

// workerEnvNames are the variables that must be set. LOG_LEVEL is absent
// deliberately: it is the one input with a safe default.
func workerEnvNames() []string {
	return []string{
		envDatabaseURL,
		envTemporalHostPort,
		envTemporalNamespace,
		envRunWorkerNamespace,
		envRunWorkerImage,
		envCheckpointAPIURL,
		envMetricsAddr,
		envPodName,
		envBlobsURL,
		envCodexResponsesEndpoint,
		envCodexAuthSecret,
		envRunWorkerImagePullSecret,
	}
}

// Validate reports whether this config can start a worker.
//
// It exists beside LoadWorker because a Worker can also be built by hand, and a
// constructor handed a half-filled struct must fail at construction rather than
// at the first poll.
func (w Worker) Validate() error {
	required := map[string]string{
		envDatabaseURL:              w.DatabaseURL,
		envTemporalHostPort:         w.TemporalHostPort,
		envTemporalNamespace:        w.TemporalNamespace,
		envRunWorkerNamespace:       w.RunWorkerNamespace,
		envRunWorkerImage:           w.RunWorkerImage,
		envCheckpointAPIURL:         w.CheckpointAPIURL,
		envMetricsAddr:              w.MetricsAddr,
		envPodName:                  w.PodName,
		envBlobsURL:                 w.BlobsURL,
		envCodexResponsesEndpoint:   w.CodexResponsesEndpoint,
		envCodexAuthSecret:          w.CodexAuthSecretName,
		envRunWorkerImagePullSecret: w.RunWorkerImagePullSecretName,
	}
	for _, name := range workerEnvNames() {
		if strings.TrimSpace(required[name]) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

// LoadWorker reads the worker's configuration from its environment.
//
// Everything except the log level is required, and nothing is defaulted to a
// plausible-looking value: a worker that starts against the wrong Temporal
// namespace or creates Run Workers in the wrong Kubernetes one looks healthy and
// does nothing, which is the failure that costs a morning.
func LoadWorker() (Worker, error) {
	cfg := Worker{
		DatabaseURL:        os.Getenv(envDatabaseURL),
		TemporalHostPort:   os.Getenv(envTemporalHostPort),
		TemporalNamespace:  os.Getenv(envTemporalNamespace),
		RunWorkerNamespace: os.Getenv(envRunWorkerNamespace),
		RunWorkerImage:     os.Getenv(envRunWorkerImage),
		CheckpointAPIURL:   os.Getenv(envCheckpointAPIURL),
		MetricsAddr:        os.Getenv(envMetricsAddr),
		PodName:            os.Getenv(envPodName),

		BlobsURL:               os.Getenv(envBlobsURL),
		CodexResponsesEndpoint: os.Getenv(envCodexResponsesEndpoint),
		CodexAuthSecretName:    os.Getenv(envCodexAuthSecret),

		RunWorkerImagePullSecretName: os.Getenv(envRunWorkerImagePullSecret),
	}
	if err := cfg.Validate(); err != nil {
		return Worker{}, describeWorkerRequirement(err)
	}

	level, err := logLevel()
	if err != nil {
		return Worker{}, err
	}
	cfg.LogLevel = level

	cfg.RunWorkerCPURequest = orDefault(envRunWorkerCPURequest, defaultRunWorkerCPURequest)
	cfg.RunWorkerMemoryLimit = orDefault(envRunWorkerMemoryLimit, defaultRunWorkerMemoryLimit)
	return cfg, nil
}

// orDefault reads an optional environment variable, or returns fallback if it
// is unset or blank.
func orDefault(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// describeWorkerRequirement adds to a missing-variable error what the variable
// is for, so the person reading it in a crashloop does not have to find this
// file to fix their Deployment.
func describeWorkerRequirement(err error) error {
	purposes := map[string]string{
		envDatabaseURL:              "the factory Postgres connection the dispatcher writes its per-tick state to",
		envTemporalHostPort:         "the Temporal frontend to dial, host:port",
		envTemporalNamespace:        "the Temporal namespace this service's workflows live in",
		envRunWorkerNamespace:       "the Kubernetes namespace per-Run workers are created in",
		envRunWorkerImage:           "the target Run Worker image, pinned by digest",
		envCheckpointAPIURL:         "the in-cluster API used for target Attempt checkpoints",
		envMetricsAddr:              "the address the metrics and health server listens on",
		envPodName:                  "this pod's own name, from the downward API; it identifies the credential lease holder",
		envBlobsURL:                 "the in-cluster payload blob API copied into Run Worker pods",
		envCodexResponsesEndpoint:   "the direct subscription-backed Codex Responses endpoint",
		envCodexAuthSecret:          "the Kubernetes Secret holding the codex credential; the worker's Role is pinned to this exact name",
		envRunWorkerImagePullSecret: "the Kubernetes Secret every Run Worker authenticates its image pull with; without it a worker ErrImagePulls against GHCR",
	}
	for name, purpose := range purposes {
		if strings.Contains(err.Error(), name) {
			return fmt.Errorf("%w: %s", err, purpose)
		}
	}
	return err
}

// logLevel reads the one optional input. Unset is info; unreadable is an error
// rather than a silent fallback, because a worker logging at a level nobody
// asked for is discovered halfway through debugging something else.
func logLevel() (slog.Level, error) {
	raw := strings.TrimSpace(os.Getenv(envLogLevel))
	if raw == "" {
		return slog.LevelInfo, nil
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%s=%q is not a log level (debug, info, warn, error): %w", envLogLevel, raw, err)
	}
	return level, nil
}
