package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/0x63616c/software-factory/internal/work"
)

// RunWorker is the non-secret startup contract for cmd/run-worker. The pod
// receives renewable credentials only through projected files.
type RunWorker struct {
	ID                work.RunWorkerID
	Identity          work.RunWorkerIdentity
	Branch            string
	TaskQueue         string
	TemporalHostPort  string
	TemporalNamespace string
	BlobsURL          string
	MetricsAddr       string
	CheckpointAPIURL  string
	GitHubOwner       string
	GitHubRepo        string
	LogLevel          slog.Level
}

func runWorkerEnvNames() []string {
	return []string{
		work.RunWorkerIDEnv,
		work.RunWorkerRunIDEnv,
		work.RunWorkerGenerationEnv,
		work.RunWorkerBranchEnv,
		work.RunWorkerTaskQueueEnv,
		work.RunWorkerTemporalHostPortEnv,
		work.RunWorkerTemporalNamespaceEnv,
		work.RunWorkerBlobsURLEnv,
		work.RunWorkerMetricsAddrEnv,
		work.RunWorkerCheckpointAPIURLEnv,
		work.RunWorkerGitHubRepositoryEnv,
	}
}

// Validate reports whether this process can poll exactly its own private queue.
func (w RunWorker) Validate() error {
	required := map[string]string{
		work.RunWorkerIDEnv:                string(w.ID),
		work.RunWorkerRunIDEnv:             w.Identity.RunID,
		work.RunWorkerBranchEnv:            w.Branch,
		work.RunWorkerTaskQueueEnv:         w.TaskQueue,
		work.RunWorkerTemporalHostPortEnv:  w.TemporalHostPort,
		work.RunWorkerTemporalNamespaceEnv: w.TemporalNamespace,
		work.RunWorkerBlobsURLEnv:          w.BlobsURL,
		work.RunWorkerMetricsAddrEnv:       w.MetricsAddr,
		work.RunWorkerCheckpointAPIURLEnv:  w.CheckpointAPIURL,
		work.RunWorkerGitHubRepositoryEnv:  w.GitHubOwner + "/" + w.GitHubRepo,
	}
	for _, name := range runWorkerEnvNames() {
		if name == work.RunWorkerGenerationEnv {
			continue
		}
		if strings.TrimSpace(required[name]) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if err := w.Identity.Validate(); err != nil {
		return fmt.Errorf("validating Run Worker identity: %w", err)
	}
	wantID, err := work.RunWorkerName(w.Identity)
	if err != nil {
		return fmt.Errorf("deriving Run Worker ID: %w", err)
	}
	if w.ID != wantID {
		return fmt.Errorf("%s=%q does not match Run %q generation %d (want %q)", work.RunWorkerIDEnv, w.ID, w.Identity.RunID, w.Identity.Generation, wantID)
	}
	if !work.FactoryTicketBranchBelongsToRun(w.Branch, w.Identity.RunID) {
		return fmt.Errorf("%s=%q is not bound to Run %q", work.RunWorkerBranchEnv, w.Branch, w.Identity.RunID)
	}
	wantQueue, err := work.RunWorkerTaskQueue(w.Identity)
	if err != nil {
		return fmt.Errorf("deriving Run Worker task queue: %w", err)
	}
	if w.TaskQueue != wantQueue {
		return fmt.Errorf("%s=%q does not match Run %q generation %d (want %q)", work.RunWorkerTaskQueueEnv, w.TaskQueue, w.Identity.RunID, w.Identity.Generation, wantQueue)
	}
	for name, raw := range map[string]string{work.RunWorkerBlobsURLEnv: w.BlobsURL, work.RunWorkerCheckpointAPIURLEnv: w.CheckpointAPIURL} {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s=%q must be an absolute HTTP URL", name, raw)
		}
	}
	if strings.ContainsAny(w.GitHubOwner+w.GitHubRepo, " /\\\t\r\n") || w.GitHubOwner == "" || w.GitHubRepo == "" {
		return fmt.Errorf("%s must be an owner/repository pair", work.RunWorkerGitHubRepositoryEnv)
	}
	return nil
}

// LoadRunWorker reads cmd/run-worker's non-secret environment.
func LoadRunWorker() (RunWorker, error) {
	runID := strings.TrimSpace(os.Getenv(work.RunWorkerRunIDEnv))
	if runID == "" {
		return RunWorker{}, fmt.Errorf("%s is required", work.RunWorkerRunIDEnv)
	}
	generationRaw := strings.TrimSpace(os.Getenv(work.RunWorkerGenerationEnv))
	if generationRaw == "" {
		return RunWorker{}, fmt.Errorf("%s is required", work.RunWorkerGenerationEnv)
	}
	generation, err := strconv.Atoi(generationRaw)
	if err != nil {
		return RunWorker{}, fmt.Errorf("%s=%q is not a generation number: %w", work.RunWorkerGenerationEnv, generationRaw, err)
	}
	identity, err := work.NewRunWorkerIdentity(runID, generation)
	if err != nil {
		return RunWorker{}, fmt.Errorf("reading Run Worker identity: %w", err)
	}
	id, err := work.ParseRunWorkerID(os.Getenv(work.RunWorkerIDEnv), identity)
	if err != nil {
		return RunWorker{}, fmt.Errorf("reading %s: %w", work.RunWorkerIDEnv, err)
	}
	repository := strings.Split(strings.TrimSpace(os.Getenv(work.RunWorkerGitHubRepositoryEnv)), "/")
	if len(repository) != 2 {
		return RunWorker{}, fmt.Errorf("%s must be an owner/repository pair", work.RunWorkerGitHubRepositoryEnv)
	}
	cfg := RunWorker{
		ID:                id,
		Identity:          identity,
		Branch:            os.Getenv(work.RunWorkerBranchEnv),
		TaskQueue:         os.Getenv(work.RunWorkerTaskQueueEnv),
		TemporalHostPort:  os.Getenv(work.RunWorkerTemporalHostPortEnv),
		TemporalNamespace: os.Getenv(work.RunWorkerTemporalNamespaceEnv),
		BlobsURL:          os.Getenv(work.RunWorkerBlobsURLEnv),
		MetricsAddr:       os.Getenv(work.RunWorkerMetricsAddrEnv),
		CheckpointAPIURL:  os.Getenv(work.RunWorkerCheckpointAPIURLEnv),
		GitHubOwner:       repository[0],
		GitHubRepo:        repository[1],
	}
	if err := cfg.Validate(); err != nil {
		return RunWorker{}, fmt.Errorf("validating Run Worker configuration: %w", err)
	}
	cfg.LogLevel, err = logLevel()
	if err != nil {
		return RunWorker{}, fmt.Errorf("reading Run Worker log level: %w", err)
	}
	return cfg, nil
}
