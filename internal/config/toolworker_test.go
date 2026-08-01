package config

import (
	"log/slog"
	"strings"
	"testing"
)

func completeToolWorkerEnv() map[string]string {
	return map[string]string{
		"TEMPORAL_HOST_PORT":     "temporal-frontend.temporal:7233",
		"TEMPORAL_NAMESPACE":     "software-factory",
		"TOOL_WORKER_TASK_QUEUE": "software-factory-run-worker-b6f1e2b2-1c1e-4b1a-9c1a-1234567890ab-g1-tools",
		"BLOBS_URL":              "http://blobs:8080",
	}
}

func TestTheRequiredToolWorkerEnvironmentIsExactlyWhatTheTestsSupply(t *testing.T) {
	required := make(map[string]bool, len(toolWorkerEnvNames()))
	for _, name := range toolWorkerEnvNames() {
		required[name] = true
	}
	supplied := make(map[string]bool, len(completeToolWorkerEnv()))
	for name := range completeToolWorkerEnv() {
		supplied[name] = true
	}
	for name := range supplied {
		if !required[name] {
			t.Errorf("%s is supplied but is not required", name)
		}
	}
	for name := range required {
		if !supplied[name] {
			t.Errorf("%s is required but not supplied", name)
		}
	}
}

func setToolWorkerEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range append(toolWorkerEnvNames(), "LOG_LEVEL") {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

func TestLoadToolWorkerReadsTheWholeEnvironment(t *testing.T) {
	setToolWorkerEnv(t, completeToolWorkerEnv())
	got, err := LoadToolWorker()
	if err != nil {
		t.Fatalf("LoadToolWorker: %v", err)
	}
	switch {
	case got.TemporalHostPort != "temporal-frontend.temporal:7233":
		t.Errorf("TemporalHostPort = %q", got.TemporalHostPort)
	case got.TemporalNamespace != "software-factory":
		t.Errorf("TemporalNamespace = %q", got.TemporalNamespace)
	case got.TaskQueue != "software-factory-run-worker-b6f1e2b2-1c1e-4b1a-9c1a-1234567890ab-g1-tools":
		t.Errorf("TaskQueue = %q", got.TaskQueue)
	case got.BlobsURL != "http://blobs:8080":
		t.Errorf("BlobsURL = %q", got.BlobsURL)
	case got.LogLevel != slog.LevelInfo:
		t.Errorf("LogLevel = %v, want %v", got.LogLevel, slog.LevelInfo)
	}
}

func TestLoadToolWorkerNamesTheVariableThatIsMissing(t *testing.T) {
	for _, missing := range toolWorkerEnvNames() {
		t.Run(missing, func(t *testing.T) {
			env := completeToolWorkerEnv()
			delete(env, missing)
			setToolWorkerEnv(t, env)
			_, err := LoadToolWorker()
			if err == nil {
				t.Fatalf("LoadToolWorker succeeded without %s", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q does not name %s", err, missing)
			}
		})
	}
}

func TestLoadToolWorkerTakesTheLogLevelItIsGiven(t *testing.T) {
	env := completeToolWorkerEnv()
	env["LOG_LEVEL"] = "debug"
	setToolWorkerEnv(t, env)
	got, err := LoadToolWorker()
	if err != nil {
		t.Fatalf("LoadToolWorker: %v", err)
	}
	if got.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", got.LogLevel, slog.LevelDebug)
	}
}

func TestLoadToolWorkerRefusesALogLevelItCannotRead(t *testing.T) {
	env := completeToolWorkerEnv()
	env["LOG_LEVEL"] = "chatty"
	setToolWorkerEnv(t, env)
	_, err := LoadToolWorker()
	if err == nil {
		t.Fatal("LoadToolWorker accepted LOG_LEVEL=chatty")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error %q does not name LOG_LEVEL", err)
	}
}

func TestToolWorkerValidateRejectsAHandBuiltHole(t *testing.T) {
	t.Parallel()
	var empty ToolWorker
	if err := empty.Validate(); err == nil {
		t.Fatal("an empty ToolWorker validated")
	}
}
