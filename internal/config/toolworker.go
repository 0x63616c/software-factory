package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/0x63616c/software-factory/internal/work"
)

// ToolWorker is the complete credential-free configuration for the typed-tools
// sidecar in one target Run Worker pod.
type ToolWorker struct {
	TemporalHostPort  string
	TemporalNamespace string
	TaskQueue         string
	BlobsURL          string
	LogLevel          slog.Level
}

func toolWorkerEnvNames() []string {
	return []string{
		work.ToolWorkerTemporalHostPortEnv,
		work.ToolWorkerTemporalNamespaceEnv,
		work.ToolWorkerTaskQueueEnv,
		work.ToolWorkerBlobsURLEnv,
	}
}

// Validate reports whether this configuration can start a Tool Worker.
func (w ToolWorker) Validate() error {
	required := map[string]string{
		work.ToolWorkerTemporalHostPortEnv:  w.TemporalHostPort,
		work.ToolWorkerTemporalNamespaceEnv: w.TemporalNamespace,
		work.ToolWorkerTaskQueueEnv:         w.TaskQueue,
		work.ToolWorkerBlobsURLEnv:          w.BlobsURL,
	}
	for _, name := range toolWorkerEnvNames() {
		if strings.TrimSpace(required[name]) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

// LoadToolWorker reads the target typed-tools sidecar configuration from its
// environment. All connection and queue fields are required; LOG_LEVEL keeps
// the service-wide safe default.
func LoadToolWorker() (ToolWorker, error) {
	cfg := ToolWorker{
		TemporalHostPort:  os.Getenv(work.ToolWorkerTemporalHostPortEnv),
		TemporalNamespace: os.Getenv(work.ToolWorkerTemporalNamespaceEnv),
		TaskQueue:         os.Getenv(work.ToolWorkerTaskQueueEnv),
		BlobsURL:          os.Getenv(work.ToolWorkerBlobsURLEnv),
	}
	if err := cfg.Validate(); err != nil {
		return ToolWorker{}, describeToolWorkerRequirement(err)
	}
	level, err := logLevel()
	if err != nil {
		return ToolWorker{}, err
	}
	cfg.LogLevel = level
	return cfg, nil
}

func describeToolWorkerRequirement(err error) error {
	purposes := map[string]string{
		work.ToolWorkerTemporalHostPortEnv:  "the Temporal frontend to dial, host:port",
		work.ToolWorkerTemporalNamespaceEnv: "the Temporal namespace this Run Worker lives in",
		work.ToolWorkerTaskQueueEnv:         "this generation's credential-free typed-tools task queue",
		work.ToolWorkerBlobsURLEnv:          "the in-cluster payload blob API used to decode Temporal payloads",
	}
	for name, purpose := range purposes {
		if strings.Contains(err.Error(), name) {
			return fmt.Errorf("%w: %s", err, purpose)
		}
	}
	return err
}
