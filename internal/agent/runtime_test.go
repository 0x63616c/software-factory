package agent

import (
	"errors"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestToolTargetResolvesRunWorkerQueue(t *testing.T) {
	t.Parallel()

	identity := work.RunWorkerIdentity{RunID: "019fb900-0000-7000-8000-000000000001", Generation: 2}
	want, err := work.RunWorkerToolTaskQueue(identity)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (ToolTarget{Kind: ToolTargetRunWorker, RunWorkerIdentity: identity}).TaskQueue(identity.RunID)
	if err != nil || got != want {
		t.Fatalf("Run Worker target queue = %q, %v, want %q", got, err, want)
	}
}

func TestToolTargetRejectsInvalidOrMismatchedRunWorkerIdentity(t *testing.T) {
	t.Parallel()

	tests := []ToolTarget{
		{},
		{Kind: ToolTargetRunWorker},
		{Kind: ToolTargetRunWorker, RunWorkerIdentity: work.RunWorkerIdentity{RunID: "019fb900-0000-7000-8000-000000000002", Generation: 1}},
		{Kind: "unknown"},
	}
	for _, target := range tests {
		if _, err := target.TaskQueue("019fb900-0000-7000-8000-000000000001"); !errors.Is(err, work.ErrInvalidRun) {
			t.Fatalf("target %#v error = %v, want ErrInvalidRun", target, err)
		}
	}
}
