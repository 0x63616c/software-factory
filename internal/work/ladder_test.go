package work_test

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestTheDeadlineLadderHoldsEndToEnd(t *testing.T) {
	t.Parallel()

	policy := work.DefaultRunPolicy()

	// stage timeout x5 < run timeout < pod deadline. Each rung exists so the
	// layer above never kills something the layer below still believes in.
	if policy.RunBudget() >= policy.RunTimeout {
		t.Fatalf("stages may use %s but the run is allowed only %s", policy.RunBudget(), policy.RunTimeout)
	}
	if policy.RunTimeout >= work.SandboxDeadline {
		t.Fatalf("the run may take %s but Kubernetes kills the pod at %s — the pod would die under a live run",
			policy.RunTimeout, work.SandboxDeadline)
	}
	if policy.StageHeartbeatTimeout >= policy.StageTimeout {
		t.Fatalf("a heartbeat timeout of %s cannot fire inside a stage timeout of %s",
			policy.StageHeartbeatTimeout, policy.StageTimeout)
	}
}
