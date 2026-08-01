package work

import (
	"strings"
	"testing"
)

// TestTaskQueueIsPinnedToItsPublishedName holds the constant against a literal.
//
// The name is published outside this module — the deploy's Deployment and the
// first-run runbook both name the queue an operator has to look at in
// Temporal's UI — so renaming it is a change to those too. A rename that
// compiles and passes everything else produces a worker polling a queue nobody
// schedules onto: no error at either end, no failed test, and a system that
// looks exactly like one with nothing to do.
//
// This test is what makes that rename deliberate. If it fails, the fix is not
// to update the literal on its own.
func TestTaskQueueIsPinnedToItsPublishedName(t *testing.T) {
	t.Parallel()

	if TaskQueue != "software-factory" {
		t.Errorf("TaskQueue = %q, want %q: the deploy and the first-run runbook name this queue too, so a rename has to travel with them",
			TaskQueue, "software-factory")
	}
}

func TestTaskQueueIsUsableAsATaskQueueName(t *testing.T) {
	t.Parallel()

	// Temporal takes any non-empty string, so the checks that matter are the
	// ones a human pays for: a queue with a space or a newline in it is one
	// nobody can type into a CLI or a UI filter correctly, and an empty one
	// is a worker that polls a queue named "".
	switch {
	case TaskQueue == "":
		t.Error("TaskQueue is empty")
	case strings.TrimSpace(TaskQueue) != TaskQueue:
		t.Errorf("TaskQueue %q is padded with whitespace", TaskQueue)
	case strings.ContainsAny(TaskQueue, " \t\n\r"):
		t.Errorf("TaskQueue %q contains whitespace", TaskQueue)
	}
}
