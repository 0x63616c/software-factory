package temporal

import (
	"context"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/0x63616c/software-factory/internal/work"
)

type fakeScheduleClient struct {
	client.ScheduleClient
	createErr error
	created   client.ScheduleOptions
	handle    *fakeScheduleHandle
}

func (f *fakeScheduleClient) Create(_ context.Context, options client.ScheduleOptions) (client.ScheduleHandle, error) {
	f.created = options
	return f.handle, f.createErr
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, id string) client.ScheduleHandle {
	f.handle.id = id
	return f.handle
}

type fakeScheduleHandle struct {
	client.ScheduleHandle
	id      string
	updated *client.Schedule
}

func (f *fakeScheduleHandle) Update(_ context.Context, options client.ScheduleUpdateOptions) error {
	update, err := options.DoUpdate(client.ScheduleUpdateInput{Description: client.ScheduleDescription{
		Schedule: client.Schedule{State: &client.ScheduleState{Paused: false}},
	}})
	if err != nil {
		return err
	}
	f.updated = update.Schedule
	return nil
}

func TestEnsureMaintainFactoryScheduleCreatesTheStableTargetSchedule(t *testing.T) {
	t.Parallel()
	fake := &fakeScheduleClient{handle: &fakeScheduleHandle{}}
	if err := EnsureMaintainFactorySchedule(context.Background(), fake); err != nil {
		t.Fatalf("EnsureMaintainFactorySchedule: %v", err)
	}
	assertMaintainFactorySchedule(t, fake.created.ID, fake.created.Spec, fake.created.Action, fake.created.Overlap)
}

func TestEnsureMaintainFactoryScheduleReplacesAnExistingDefinition(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		createErr error
	}{
		{name: "SDK sentinel", createErr: temporal.ErrScheduleAlreadyRunning},
		{name: "service error", createErr: serviceerror.NewAlreadyExists("exists")},
	} {
		t.Run(test.name, func(t *testing.T) {
			handle := &fakeScheduleHandle{}
			fake := &fakeScheduleClient{handle: handle, createErr: test.createErr}
			if err := EnsureMaintainFactorySchedule(context.Background(), fake); err != nil {
				t.Fatalf("EnsureMaintainFactorySchedule: %v", err)
			}
			if handle.id != work.MaintainFactoryScheduleID || handle.updated == nil {
				t.Fatalf("updated handle = %q, %+v", handle.id, handle.updated)
			}
			assertMaintainFactorySchedule(t, handle.id, *handle.updated.Spec, handle.updated.Action, handle.updated.Policy.Overlap)
		})
	}
}

func assertMaintainFactorySchedule(t *testing.T, id string, spec client.ScheduleSpec, rawAction client.ScheduleAction, overlap enumspb.ScheduleOverlapPolicy) {
	t.Helper()
	if id != work.MaintainFactoryScheduleID {
		t.Errorf("schedule ID = %q, want %q", id, work.MaintainFactoryScheduleID)
	}
	if len(spec.Intervals) != 1 || spec.Intervals[0].Every != 5*time.Minute {
		t.Errorf("intervals = %+v, want one five-minute interval", spec.Intervals)
	}
	action, ok := rawAction.(*client.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("action = %T, want *client.ScheduleWorkflowAction", rawAction)
	}
	if action.ID != work.MaintainFactoryWorkflowID || action.Workflow != "MaintainFactory" || action.TaskQueue != work.TaskQueue {
		t.Errorf("action = %+v, want stable MaintainFactory on the main queue", action)
	}
	if overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP {
		t.Errorf("overlap = %s, want SKIP", overlap)
	}
}
