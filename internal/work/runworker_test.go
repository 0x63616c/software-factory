package work

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRunWorkerTaskQueueIsDeterministicPerRunGeneration(t *testing.T) {
	t.Parallel()

	identity, err := NewRunWorkerIdentity("019fb900-0000-7000-8000-000000000001", 1)
	if err != nil {
		t.Fatalf("NewRunWorkerIdentity: %v", err)
	}
	first, err := RunWorkerTaskQueue(identity)
	if err != nil {
		t.Fatalf("RunWorkerTaskQueue: %v", err)
	}
	if got, err := RunWorkerTaskQueue(identity); err != nil || got != first {
		t.Fatalf("same Run and generation produced %q then %q", first, got)
	}
	replacement, _ := NewRunWorkerIdentity(identity.RunID, 2)
	if got, _ := RunWorkerTaskQueue(replacement); got == first {
		t.Fatalf("replacement generation reused queue %q", got)
	}
	other, _ := NewRunWorkerIdentity("019fb900-0000-7000-8000-000000000002", 1)
	if got, _ := RunWorkerTaskQueue(other); got == first {
		t.Fatalf("another Run reused queue %q", got)
	}
	if !strings.HasPrefix(first, "software-factory-run-worker-") {
		t.Errorf("queue %q has no published Run Worker prefix", first)
	}
}

func TestRunWorkerIdentityValidatesRunAndGeneration(t *testing.T) {
	t.Parallel()

	valid, err := NewRunWorkerIdentity("019fb900-0000-7000-8000-000000000001", 1)
	if err != nil {
		t.Fatalf("NewRunWorkerIdentity: %v", err)
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	for _, invalid := range []RunWorkerIdentity{
		{},
		{RunID: valid.RunID},
		{RunID: "Not/A Kubernetes Name", Generation: 1},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("identity %+v was accepted", invalid)
		}
	}
	for _, invalid := range []struct {
		runID      string
		generation int
	}{
		{runID: "not-a-uuid", generation: 1},
		{runID: "019FB900-0000-7000-8000-000000000001", generation: 1},
		{runID: valid.RunID, generation: 0},
	} {
		if _, err := NewRunWorkerIdentity(invalid.runID, invalid.generation); err == nil {
			t.Errorf("NewRunWorkerIdentity(%q, %d) succeeded", invalid.runID, invalid.generation)
		}
	}
}

func TestRunWorkerNamesParseOnlyForTheirValidatedIdentity(t *testing.T) {
	t.Parallel()

	identity, err := NewRunWorkerIdentity("019fb900-0000-7000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := RunWorkerName(identity)
	if err != nil {
		t.Fatalf("RunWorkerName: %v", err)
	}
	if _, err := ParseRunWorkerID(string(id), identity); err != nil {
		t.Fatalf("ParseRunWorkerID: %v", err)
	}
	if _, err := ParseRunWorkerID("some-other-pod", identity); err == nil {
		t.Fatal("ParseRunWorkerID accepted an arbitrary pod name")
	}
	if _, err := RunWorkerName(RunWorkerIdentity{}); err == nil {
		t.Fatal("RunWorkerName accepted an invalid identity")
	}
	if _, err := RunWorkerTaskQueue(RunWorkerIdentity{}); err == nil {
		t.Fatal("RunWorkerTaskQueue accepted an invalid identity")
	}
}

func TestNewRunWorkerSpecSnapshotsValidatedInputs(t *testing.T) {
	t.Parallel()

	identity, err := NewRunWorkerIdentity("019fb900-0000-7000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"KEY": "value"}
	spec, err := NewRunWorkerSpec(RunWorkerSpec{
		TicketNumber: 42, Identity: identity, Image: "image@sha256:digest",
		CPURequest: "2", MemoryLimit: "8Gi", DeadlineSeconds: 60, Env: env,
	})
	if err != nil {
		t.Fatalf("NewRunWorkerSpec: %v", err)
	}
	env["KEY"] = "mutated"
	if spec.Env["KEY"] != "value" {
		t.Fatalf("spec environment changed after construction: %+v", spec.Env)
	}
	for _, invalid := range []RunWorkerSpec{
		{},
		{Identity: identity, TicketNumber: 1, DeadlineSeconds: 1, CPURequest: "2", MemoryLimit: "8Gi", Env: map[string]string{}},
	} {
		if _, err := NewRunWorkerSpec(invalid); err == nil {
			t.Fatalf("NewRunWorkerSpec accepted %+v", invalid)
		}
	}
}

func TestCredentialRevisionContainsOnlySafeMetadata(t *testing.T) {
	t.Parallel()

	got := RunWorkerCredentialRevision{
		Revision:  "17",
		ExpiresAt: time.Date(2026, 7, 31, 12, 30, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal revision: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{"token", "credential", "secret", "ghs_"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("safe metadata JSON %q contains forbidden %q", text, forbidden)
		}
	}
}

func TestRunWorkerPublishedEnvironmentNamesAreTargetSpecific(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"RunWorkerIDEnv":         "RUN_WORKER_ID",
		"RunWorkerRunIDEnv":      "RUN_ID",
		"RunWorkerGenerationEnv": "RUN_WORKER_GENERATION",
		"RunWorkerTaskQueueEnv":  "RUN_WORKER_TASK_QUEUE",
	}
	got := map[string]string{
		"RunWorkerIDEnv":         RunWorkerIDEnv,
		"RunWorkerRunIDEnv":      RunWorkerRunIDEnv,
		"RunWorkerGenerationEnv": RunWorkerGenerationEnv,
		"RunWorkerTaskQueueEnv":  RunWorkerTaskQueueEnv,
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}
