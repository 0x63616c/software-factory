//go:build manual

package runworkercapability

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	enums "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/temporalproto"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/0x63616c/software-factory/internal/activities"
	temporalclient "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

// TestExportTargetDispatcherHistory captures the activated Dispatcher while it
// makes its core admission decision on a real Temporal dev server.
func TestExportTargetDispatcherHistory(t *testing.T) {
	server, err := testsuite.StartDevServer(context.Background(), testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{Version: "v1.8.1"},
		LogLevel:       "error",
	})
	if err != nil {
		t.Fatalf("starting Temporal dev server: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := server.Stop(); stopErr != nil {
			t.Errorf("stopping Temporal dev server: %v", stopErr)
		}
	})

	const queue = "target-dispatcher-history-export"
	var attempts atomic.Int32
	w := worker.New(server.Client(), queue, worker.Options{
		Identity:               "target-dispatcher-history-exporter",
		DisableEagerActivities: true,
	})
	w.RegisterWorkflow(workflows.Dispatcher)
	w.RegisterWorkflowWithOptions(targetDispatcherFixtureChild, workflow.RegisterOptions{Name: "WorkOnTicket"})
	w.RegisterActivityWithOptions(
		func(context.Context) ([]store.Ticket, error) {
			if attempts.Add(1) == 1 {
				return nil, temporal.NewApplicationErrorWithOptions(
					"no dispatchable factory tickets", activities.ErrTypeNoDispatchableTickets,
					temporal.ApplicationErrorOptions{NextRetryDelay: 10 * time.Second},
				)
			}
			return []store.Ticket{{ID: 17, Title: "replay fixture admission", State: store.TicketOpen}}, nil
		},
		activity.RegisterOptions{Name: "AwaitDispatchableTickets"},
	)
	if err := w.Start(); err != nil {
		t.Fatalf("starting target dispatcher worker: %v", err)
	}
	t.Cleanup(w.Stop)

	run, err := server.Client().ExecuteWorkflow(context.Background(), temporalclient.StartWorkflowOptions{
		ID:        work.TargetDispatcherWorkflowID + "-history",
		TaskQueue: queue,
	}, workflows.Dispatcher, workflows.DispatcherInput{
		Policy:   work.DefaultDispatcherPolicy(),
		CloneURL: "https://github.com/example/repository.git",
		Model:    work.Model{Name: "gpt-5", Effort: "high"},
	})
	if err != nil {
		t.Fatalf("starting target dispatcher: %v", err)
	}

	time.Sleep(12 * time.Second)
	if err := server.Client().TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "fixture export"); err != nil {
		t.Fatalf("terminating target dispatcher: %v", err)
	}

	history := readTargetTemporalHistory(t, server, run.GetID(), run.GetRunID())
	assertRepresentativeTargetDispatcherHistory(t, history)
	encoded, err := (temporalproto.CustomJSONMarshalOptions{Indent: "  "}).Marshal(history)
	if err != nil {
		t.Fatalf("encoding target dispatcher history: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetDispatcherHistoryFixture), 0o755); err != nil {
		t.Fatalf("creating fixture directory: %v", err)
	}
	if err := os.WriteFile(targetDispatcherHistoryFixture, append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("writing %s: %v", targetDispatcherHistoryFixture, err)
	}
}

func targetDispatcherFixtureChild(ctx workflow.Context, _ workflows.WorkOnTicketInput) error {
	workflow.GetLogger(ctx).Info("target dispatcher replay fixture child started")
	return workflow.Await(ctx, func() bool { return false })
}

func readTargetTemporalHistory(t *testing.T, server *testsuite.DevServer, workflowID, runID string) *historypb.History {
	t.Helper()
	iterator := server.Client().GetWorkflowHistory(
		context.Background(), workflowID, runID, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT,
	)
	history := &historypb.History{}
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatalf("reading target dispatcher history: %v", err)
		}
		history.Events = append(history.Events, event)
	}
	if len(history.Events) == 0 {
		t.Fatal("target dispatcher history is empty")
	}
	return history
}
