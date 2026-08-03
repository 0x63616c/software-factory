package config

import (
	"log/slog"
	"strings"
	"testing"
)

// completeEnv is a worker environment with nothing missing. A case removes or
// replaces the one variable it is about.
func completeEnv() map[string]string {
	return map[string]string{
		"SOFTWARE_FACTORY_DATABASE_URL":     "postgres://factory:password@postgres/factory",
		"TEMPORAL_HOST_PORT":                "temporal-frontend.temporal:7233",
		"TEMPORAL_NAMESPACE":                "software-factory",
		"RUN_WORKER_NAMESPACE":              "software-factory",
		"RUN_WORKER_IMAGE":                  "ghcr.io/0x63616c/www-software-factory-run-worker@sha256:def",
		"CHECKPOINT_API_URL":                "http://api.software-factory.svc.cluster.local:8080",
		"METRICS_ADDR":                      ":9090",
		"POD_NAME":                          "software-factory-worker-7d9f8c-abcde",
		"DEPLOY_ID":                         "1785790005-2",
		"BLOBS_URL":                         "http://blobs:8080",
		"CODEX_RESPONSES_ENDPOINT":          "https://chatgpt.com/backend-api/codex/responses",
		"CODEX_AUTH_SECRET_NAME":            "codex-auth",
		"RUN_WORKER_IMAGE_PULL_SECRET_NAME": "ghcr-pull",
	}
}

// TestTheRequiredEnvironmentIsExactlyWhatTheTestsSupply pins the required set
// against literals, because every other test here is generated from it.
//
// TestLoadWorkerNamesTheVariableThatIsMissing iterates workerEnvNames(), which
// is the thing under test: drop a name from it and the requirement and the case
// that would have caught it disappear together, compiling and green. The map
// above is hand-written, so this comparison is the one assertion in the file
// that is not the code checking itself.
//
// What it stops: RUN_WORKER_IMAGE tidied out of the required list, after which
// the worker starts with an empty image. Or POD_NAME, where the
// credential lease holder becomes "" and no lease can be attributed at 3am,
// which is the only reason that variable is required at all.
func TestTheRequiredEnvironmentIsExactlyWhatTheTestsSupply(t *testing.T) {
	required := make(map[string]bool, len(workerEnvNames()))
	for _, name := range workerEnvNames() {
		required[name] = true
	}
	supplied := make(map[string]bool, len(completeEnv()))
	for name := range completeEnv() {
		supplied[name] = true
	}

	for name := range supplied {
		if !required[name] {
			t.Errorf("%s is supplied by completeEnv but is not required; either require it or stop supplying it", name)
		}
	}
	for name := range required {
		if !supplied[name] {
			t.Errorf("%s is required but completeEnv does not supply it, so no missing-variable case covers it", name)
		}
	}
}

// setEnv installs an environment for one test, and clears everything else this
// loader reads so a variable left over from the shell cannot make a test pass.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()

	for _, name := range append(workerEnvNames(), "LOG_LEVEL", envRunWorkerCPURequest, envRunWorkerMemoryLimit) {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

func TestLoadWorkerReadsTheWholeEnvironment(t *testing.T) {
	setEnv(t, completeEnv())

	got, err := LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}

	switch {
	case got.DatabaseURL != "postgres://factory:password@postgres/factory":
		t.Errorf("DatabaseURL = %q", got.DatabaseURL)
	case got.TemporalHostPort != "temporal-frontend.temporal:7233":
		t.Errorf("TemporalHostPort = %q", got.TemporalHostPort)
	case got.TemporalNamespace != "software-factory":
		t.Errorf("TemporalNamespace = %q", got.TemporalNamespace)
	case got.RunWorkerNamespace != "software-factory":
		t.Errorf("RunWorkerNamespace = %q", got.RunWorkerNamespace)
	case got.RunWorkerImage != "ghcr.io/0x63616c/www-software-factory-run-worker@sha256:def":
		t.Errorf("RunWorkerImage = %q", got.RunWorkerImage)
	case got.CheckpointAPIURL != "http://api.software-factory.svc.cluster.local:8080":
		t.Errorf("CheckpointAPIURL = %q", got.CheckpointAPIURL)
	case got.MetricsAddr != ":9090":
		t.Errorf("MetricsAddr = %q", got.MetricsAddr)
	case got.PodName != "software-factory-worker-7d9f8c-abcde":
		t.Errorf("PodName = %q", got.PodName)
	case got.DeployID != "1785790005-2":
		t.Errorf("DeployID = %q", got.DeployID)
	case got.BlobsURL != "http://blobs:8080":
		t.Errorf("BlobsURL = %q", got.BlobsURL)
	case got.CodexResponsesEndpoint != "https://chatgpt.com/backend-api/codex/responses":
		t.Errorf("CodexResponsesEndpoint = %q", got.CodexResponsesEndpoint)
	case got.CodexAuthSecretName != "codex-auth":
		t.Errorf("CodexAuthSecretName = %q", got.CodexAuthSecretName)
	case got.RunWorkerImagePullSecretName != "ghcr-pull":
		t.Errorf("RunWorkerImagePullSecretName = %q", got.RunWorkerImagePullSecretName)
	case got.LogLevel != slog.LevelInfo:
		t.Errorf("LogLevel = %v, want the default %v", got.LogLevel, slog.LevelInfo)
	case got.RunWorkerCPURequest != defaultRunWorkerCPURequest:
		t.Errorf("RunWorkerCPURequest = %q, want the default %q", got.RunWorkerCPURequest, defaultRunWorkerCPURequest)
	case got.RunWorkerMemoryLimit != defaultRunWorkerMemoryLimit:
		t.Errorf("RunWorkerMemoryLimit = %q, want the default %q", got.RunWorkerMemoryLimit, defaultRunWorkerMemoryLimit)
	}
}

// TestLoadWorkerTakesTheRunWorkerResourcesItIsGiven proves the optional CPU
// request and memory limit are read like every other input when set, and only
// fall back to their defaults when absent — the same contract LogLevel has.
func TestLoadWorkerTakesTheRunWorkerResourcesItIsGiven(t *testing.T) {
	env := completeEnv()
	env[envRunWorkerCPURequest] = "4"
	env[envRunWorkerMemoryLimit] = "16Gi"
	setEnv(t, env)

	got, err := LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}
	if got.RunWorkerCPURequest != "4" {
		t.Errorf("RunWorkerCPURequest = %q, want %q", got.RunWorkerCPURequest, "4")
	}
	if got.RunWorkerMemoryLimit != "16Gi" {
		t.Errorf("RunWorkerMemoryLimit = %q, want %q", got.RunWorkerMemoryLimit, "16Gi")
	}
}

func TestLoadWorkerNamesTheVariableThatIsMissing(t *testing.T) {
	for _, missing := range workerEnvNames() {
		t.Run(missing, func(t *testing.T) {
			env := completeEnv()
			delete(env, missing)
			setEnv(t, env)

			_, err := LoadWorker()
			if err == nil {
				t.Fatalf("LoadWorker succeeded without %s", missing)
			}
			// A config failure at 3am is read by someone who does not know
			// this file. The variable's own name is the whole fix.
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q does not name %s", err, missing)
			}
		})
	}
}

func TestLoadWorkerTakesTheLogLevelItIsGiven(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  slog.Level
	}{
		{name: "unset is info", value: "", want: slog.LevelInfo},
		{name: "debug for a bad night", value: "debug", want: slog.LevelDebug},
		{name: "case does not matter", value: "WARN", want: slog.LevelWarn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := completeEnv()
			if tc.value != "" {
				env["LOG_LEVEL"] = tc.value
			}
			setEnv(t, env)

			got, err := LoadWorker()
			if err != nil {
				t.Fatalf("LoadWorker: %v", err)
			}
			if got.LogLevel != tc.want {
				t.Errorf("LogLevel = %v, want %v", got.LogLevel, tc.want)
			}
		})
	}
}

func TestLoadWorkerRefusesALogLevelItCannotRead(t *testing.T) {
	env := completeEnv()
	env["LOG_LEVEL"] = "chatty"
	setEnv(t, env)

	// Falling back to info would start a worker that quietly logs at a level
	// nobody asked for, which is discovered while debugging something else.
	_, err := LoadWorker()
	if err == nil {
		t.Fatal("LoadWorker accepted LOG_LEVEL=chatty")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error %q does not name LOG_LEVEL", err)
	}
}

func TestWorkerValidateRejectsAHandBuiltHole(t *testing.T) {
	t.Parallel()

	// A Worker can also be built by a test or by another composition root, so
	// the check cannot live only inside the loader.
	var empty Worker
	if err := empty.Validate(); err == nil {
		t.Fatal("an empty Worker validated")
	}
}
