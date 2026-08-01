package config

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func completeRunWorkerEnv() map[string]string {
	return map[string]string{
		work.RunWorkerIDEnv:                "run-worker-019fb900-0000-7000-8000-000000000001-g1",
		work.RunWorkerRunIDEnv:             "019fb900-0000-7000-8000-000000000001",
		work.RunWorkerGenerationEnv:        "1",
		work.RunWorkerTaskQueueEnv:         "software-factory-run-worker-019fb900-0000-7000-8000-000000000001-g1",
		work.RunWorkerTemporalHostPortEnv:  "temporal-frontend.temporal:7233",
		work.RunWorkerTemporalNamespaceEnv: "software-factory",
		work.RunWorkerBlobsURLEnv:          "http://software-factory-blobs:8080",
		work.RunWorkerMetricsAddrEnv:       ":9090",
		work.RunWorkerCheckpointAPIURLEnv:  "http://software-factory-api:8080",
		work.RunWorkerGitHubRepositoryEnv:  "0x63616c/world-wide-webb",
		work.RunWorkerBranchEnv:            "software-factory/factory-ticket-29/019fb900-0000-7000-8000-000000000001",
	}
}

func setRunWorkerEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range append(runWorkerEnvNames(), "LOG_LEVEL") {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

func TestLoadRunWorkerRequiresTheBranchBoundToItsRun(t *testing.T) {
	for _, test := range []struct {
		name   string
		branch string
	}{
		{name: "missing", branch: ""},
		{name: "another run", branch: "software-factory/factory-ticket-29/019fb900-0000-7000-8000-000000000002"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := completeRunWorkerEnv()
			env[work.RunWorkerBranchEnv] = test.branch
			setRunWorkerEnv(t, env)
			if _, err := LoadRunWorker(); err == nil || !strings.Contains(err.Error(), work.RunWorkerBranchEnv) {
				t.Fatalf("LoadRunWorker with %s=%q = %v", work.RunWorkerBranchEnv, test.branch, err)
			}
		})
	}
}

func TestLoadRunWorkerReadsItsTargetOnlyEnvironment(t *testing.T) {
	setRunWorkerEnv(t, completeRunWorkerEnv())

	got, err := LoadRunWorker()
	if err != nil {
		t.Fatalf("LoadRunWorker: %v", err)
	}
	if got.Identity.RunID != "019fb900-0000-7000-8000-000000000001" || got.Identity.Generation != 1 {
		t.Errorf("identity = %+v", got.Identity)
	}
	wantID, err := work.ParseRunWorkerID("run-worker-019fb900-0000-7000-8000-000000000001-g1", got.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != wantID {
		t.Errorf("ID = %q", got.ID)
	}
	wantQueue, err := work.RunWorkerTaskQueue(got.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskQueue != wantQueue {
		t.Errorf("TaskQueue = %q, want the queue derived from %+v", got.TaskQueue, got.Identity)
	}
	if got.Branch != completeRunWorkerEnv()[work.RunWorkerBranchEnv] || got.TemporalHostPort == "" || got.TemporalNamespace == "" || got.BlobsURL == "" || got.MetricsAddr != ":9090" || got.CheckpointAPIURL == "" || got.GitHubOwner != "0x63616c" || got.GitHubRepo != "world-wide-webb" {
		t.Errorf("incomplete config: %+v", got)
	}
	if got.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", got.LogLevel)
	}
}

func TestLoadRunWorkerNamesEveryMissingVariable(t *testing.T) {
	for _, missing := range runWorkerEnvNames() {
		t.Run(missing, func(t *testing.T) {
			env := completeRunWorkerEnv()
			delete(env, missing)
			setRunWorkerEnv(t, env)
			_, err := LoadRunWorker()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("LoadRunWorker without %s = %v", missing, err)
			}
		})
	}
}

func TestLoadRunWorkerRejectsQueueIdentityDrift(t *testing.T) {
	env := completeRunWorkerEnv()
	other, err := work.NewRunWorkerIdentity(env[work.RunWorkerRunIDEnv], 2)
	if err != nil {
		t.Fatal(err)
	}
	env[work.RunWorkerTaskQueueEnv], err = work.RunWorkerTaskQueue(other)
	if err != nil {
		t.Fatal(err)
	}
	setRunWorkerEnv(t, env)

	_, err = LoadRunWorker()
	if err == nil || !strings.Contains(err.Error(), work.RunWorkerTaskQueueEnv) {
		t.Fatalf("LoadRunWorker accepted a queue for another generation: %v", err)
	}
}

func TestLoadRunWorkerRejectsNonHTTPServiceURLs(t *testing.T) {
	for _, name := range []string{work.RunWorkerBlobsURLEnv, work.RunWorkerCheckpointAPIURLEnv} {
		t.Run(name, func(t *testing.T) {
			env := completeRunWorkerEnv()
			env[name] = "http://software-factory-api:8080?token=leaked"
			setRunWorkerEnv(t, env)

			_, err := LoadRunWorker()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("LoadRunWorker with invalid %s = %v", name, err)
			}
		})
	}
}
