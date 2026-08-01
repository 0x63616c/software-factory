package runworkercapability

import (
	"os"
	"testing"

	enums "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/temporalproto"
	"go.temporal.io/sdk/worker"

	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

const targetDispatcherHistoryFixture = "../workflows/testdata/target-dispatcher-admission.json"

// TestTargetDispatcherHistoryReplays protects the activated Dispatcher's
// wait-to-admission command sequence with a history exported from Temporal.
func TestTargetDispatcherHistoryReplays(t *testing.T) {
	history := readTargetDispatcherHistoryFixture(t)
	assertRepresentativeTargetDispatcherHistory(t, history)

	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(workflows.Dispatcher)
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatalf("replaying %s through the Dispatcher registration: %v", targetDispatcherHistoryFixture, err)
	}
}

func readTargetDispatcherHistoryFixture(t *testing.T) *historypb.History {
	t.Helper()
	data, err := os.ReadFile(targetDispatcherHistoryFixture)
	if err != nil {
		t.Fatalf("reading %s: %v", targetDispatcherHistoryFixture, err)
	}
	history := &historypb.History{}
	if err := (temporalproto.CustomJSONUnmarshalOptions{}).Unmarshal(data, history); err != nil {
		t.Fatalf("decoding %s: %v", targetDispatcherHistoryFixture, err)
	}
	return history
}

func assertRepresentativeTargetDispatcherHistory(t *testing.T, history *historypb.History) {
	t.Helper()
	activityCompleted := false
	activityRetried := false
	childStarted := false
	childUsesMainRunQueue := false
	childRequestsCancellation := false
	for _, event := range history.Events {
		if event.GetEventType() == enums.EVENT_TYPE_ACTIVITY_TASK_COMPLETED {
			activityCompleted = true
		}
		if event.GetEventType() == enums.EVENT_TYPE_ACTIVITY_TASK_STARTED && event.GetActivityTaskStartedEventAttributes().GetAttempt() > 1 {
			activityRetried = true
		}
		if event.GetEventType() == enums.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_INITIATED {
			childStarted = true
			attributes := event.GetStartChildWorkflowExecutionInitiatedEventAttributes()
			childUsesMainRunQueue = attributes.GetTaskQueue().GetName() == work.TaskQueue
			childRequestsCancellation = attributes.GetParentClosePolicy() == enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL
		}
	}
	if !activityCompleted || !activityRetried || !childStarted || !childUsesMainRunQueue || !childRequestsCancellation {
		t.Fatalf(
			"target dispatcher history must contain a retried wait, completed dispatch, and child admission on the main run queue that requests cancellation; activity_completed=%t activity_retried=%t child_started=%t child_uses_main_run_queue=%t child_requests_cancellation=%t",
			activityCompleted, activityRetried, childStarted, childUsesMainRunQueue, childRequestsCancellation,
		)
	}
}
