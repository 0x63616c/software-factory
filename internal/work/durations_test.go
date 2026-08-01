package work

import (
	"testing"
	"time"
)

// The ladder these constants form is the reason they live together. Each test
// below is a relationship, not a value: changing one duration is meant to fail
// here so whoever tunes it re-derives the rest rather than discovering the
// consequence in production at 3am.

func TestAHeartbeatTimeoutCanFireWithinAStage(t *testing.T) {
	t.Parallel()

	// A heartbeat timeout at or above the stage length can never fire, which
	// is a 60-minute black box wearing a liveness check.
	if StageHeartbeatTimeout >= MaxStageDuration {
		t.Errorf("StageHeartbeatTimeout (%s) is not shorter than MaxStageDuration (%s): a stage could never be declared dead",
			StageHeartbeatTimeout, MaxStageDuration)
	}
}

func TestStageHeartbeatTimeoutAllowsFiveMinutesForFirstEvent(t *testing.T) {
	t.Parallel()

	if want := 5 * time.Minute; StageHeartbeatTimeout != want {
		t.Errorf("StageHeartbeatTimeout = %s, want %s", StageHeartbeatTimeout, want)
	}
}

func TestARunCanContainItsStages(t *testing.T) {
	t.Parallel()

	// Not len(Pipeline()) (3, under the pipeline rewrite): a run's real worst
	// case is the implement/review loop's derived ceiling, MaxStageInvocations
	// (19) stage invocations, not one pass over the three stages Pipeline()
	// names. See RunPolicy.Validate's identical check.
	stages := MaxStageInvocations * MaxStageDuration
	if MaxRunDuration <= stages {
		t.Errorf("MaxRunDuration (%s) does not exceed the %d stage invocations it can contain (%s): a run would time out while a stage was still legitimately working",
			MaxRunDuration, MaxStageInvocations, stages)
	}
}

func TestKubernetesNeverKillsAPodTemporalStillBelievesIn(t *testing.T) {
	t.Parallel()

	// ADR-0011: activeDeadlineSeconds sits above the workflow run timeout. The
	// other order gives a stage whose Run Worker vanished under it, reported as a
	// exec failure with no cause.
	if RunWorkerDeadline <= MaxRunDuration {
		t.Errorf("RunWorkerDeadline (%s) does not exceed MaxRunDuration (%s): Kubernetes would delete a Run Worker a live run still expects",
			RunWorkerDeadline, MaxRunDuration)
	}
}

func TestRunWorkerDeadlineSecondsIsThatDeadlineInSeconds(t *testing.T) {
	t.Parallel()

	// It is handed to a Kubernetes field measured in seconds. A units mistake
	// here is a pod that dies in minutes or outlives the cluster.
	if want := int64(RunWorkerDeadline / time.Second); RunWorkerDeadlineSeconds != want {
		t.Errorf("RunWorkerDeadlineSeconds = %d, want %d", RunWorkerDeadlineSeconds, want)
	}
	if RunWorkerDeadlineSeconds <= 0 {
		t.Errorf("RunWorkerDeadlineSeconds = %d; Kubernetes reads a non-positive activeDeadlineSeconds as no deadline at all", RunWorkerDeadlineSeconds)
	}
}

func TestEveryDurationIsPositive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		d    time.Duration
	}{
		{name: "MaxStageDuration", d: MaxStageDuration},
		{name: "StageHeartbeatTimeout", d: StageHeartbeatTimeout},
		{name: "MaxRunDuration", d: MaxRunDuration},
		{name: "RunWorkerDeadline", d: RunWorkerDeadline},
		{name: "SessionExecutionTimeout", d: SessionExecutionTimeout},
		{name: "SessionCreationTimeout", d: SessionCreationTimeout},
	}

	for _, tc := range cases {
		if tc.d <= 0 {
			t.Errorf("%s is %s; every constructor here rejects a non-positive duration", tc.name, tc.d)
		}
	}
}

// TestASessionCanOutlastItsRun is D4's inequality: a Session that expires
// before the run it serves fails every stage after it, while one that
// outlives the run is merely pointless. The same direction as
// TestKubernetesNeverKillsAPodTemporalStillBelievesIn, one rung up the ladder.
func TestASessionCanOutlastItsRun(t *testing.T) {
	t.Parallel()

	if SessionExecutionTimeout < MaxRunDuration {
		t.Errorf("SessionExecutionTimeout (%s) is shorter than MaxRunDuration (%s): the session would expire while a run it serves was still legitimately working",
			SessionExecutionTimeout, MaxRunDuration)
	}
}

// TestSessionCreationTimeoutMatchesWhatWaitSandboxReadyAlreadyBounds pins D4's
// derivation rather than its number: with no warm pool (D1), CreateSession's
// wait for a pod to claim its session is the same wait WaitSandboxReady
// already performs under controlOptions() today, so this must equal
// ControlTimeout*ControlAttempts and not a second literal that could quietly
// stop agreeing with it.
func TestSessionCreationTimeoutMatchesWhatWaitSandboxReadyAlreadyBounds(t *testing.T) {
	t.Parallel()

	policy := DefaultRunPolicy()
	want := policy.ControlTimeout * time.Duration(policy.ControlAttempts)
	if SessionCreationTimeout != want {
		t.Errorf("SessionCreationTimeout = %s, want %s (DefaultRunPolicy's ControlTimeout*ControlAttempts, the same bound WaitSandboxReady is already held to)",
			SessionCreationTimeout, want)
	}
}
