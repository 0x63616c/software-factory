package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// TestTheWorkerPollsTheQueueTheWorkflowsScheduleOnto is the demonstration
// work.TaskQueue was extracted for and did not yet have.
//
// The constant landed with zero consumers, so nothing showed the worker
// registering on it rather than on a literal — and this composition root is
// exactly where a second spelling would appear. It has to be caught here
// because it cannot be caught later: a worker polling a queue nobody schedules
// onto raises no error, crashes nothing and fails no test. It looks precisely
// like a system with no work to do.
//
// Asserted against the source rather than by running a worker, because the
// alternative is a live Temporal. Two limits follow from that, and both are
// real:
//
//   - It proves the call site names the constant, not that the SDK received it.
//   - It can only see registrations spelled as a literal `worker.New(` call in
//     this file. One built through a helper, or moved elsewhere, is invisible —
//     which is why a missing match is a Fatal rather than a pass.
//
// EVERY match is checked, not the first. FindSubmatch would stop at the
// earliest `worker.New`, so a second registration appended below it — a worker
// on an experimental queue, say, which the constant's own doc comment
// contemplates — would sit there on a literal with this test green. Confirmed
// by mutation, in both orders: inserted first it was caught, appended after it
// was invisible.
//
// The count is asserted for the same reason. "All matches are correct" is
// vacuously true of no matches, and a refactor that moves registration out of
// this file would otherwise turn this test into one that asserts nothing while
// still passing.
func TestTheWorkerPollsTheActivatedControlAndMainQueues(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}

	// worker.New's second argument is the queue polled. Anything but the
	// shared constant there is a second spelling of a name that must have one.
	registration := regexp.MustCompile(`worker\.New\([^,]+,\s*([^,]+),`)
	found := registration.FindAllSubmatch(source, -1)

	// Activation has one control worker for the singleton Dispatcher and one
	// main worker for ticket workflows and activities. The control worker must
	// be able to acknowledge policy before the main queue begins polling.
	const wantRegistrations = 2
	if len(found) != wantRegistrations {
		t.Fatalf("main.go makes %d worker.New calls, want %d; this test can only vouch for the registrations it can see",
			len(found), wantRegistrations)
	}

	wantQueues := map[string]bool{
		"work.TargetDispatcherTaskQueue": false,
		"work.TaskQueue":                 false,
	}
	for _, match := range found {
		got := string(match[1])
		if _, ok := wantQueues[got]; !ok {
			t.Errorf("the worker registers on unexpected queue %s", got)
			continue
		}
		wantQueues[got] = true
	}
	for queue, found := range wantQueues {
		if !found {
			t.Errorf("the worker does not poll activated queue %s", queue)
		}
	}
}

// TestTheQueueAndTheNamespaceAreNotTheSameField guards a readability trap
// rather than a defect: the task queue and the Temporal namespace are both the
// string "software-factory", and they appear side by side on runbook command
// lines where a transposed --task-queue/--namespace flag would be invisible.
//
// If they ever diverge this test is noise and should be deleted. While they
// agree, it is a place for the next person to find out that they do.
func TestTheQueueAndTheNamespaceAreNotTheSameField(t *testing.T) {
	t.Parallel()

	if work.TaskQueue != "software-factory" {
		t.Errorf("work.TaskQueue = %q; if the queue name has moved, check every runbook that also passes a --namespace", work.TaskQueue)
	}
}
