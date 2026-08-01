package temporal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

type fakeEncodedPolicy struct {
	status workflows.DispatcherPolicyStatus
	err    error
}

func (value fakeEncodedPolicy) HasValue() bool { return value.err == nil }

func (value fakeEncodedPolicy) Get(destination interface{}) error {
	if value.err != nil {
		return value.err
	}
	status, ok := destination.(*workflows.DispatcherPolicyStatus)
	if !ok {
		return errors.New("unexpected encoded policy destination")
	}
	*status = value.status
	return nil
}

type fakeUpdateHandle struct {
	value interface{}
	err   error
}

func (handle fakeUpdateHandle) WorkflowID() string { return work.TargetDispatcherWorkflowID }
func (handle fakeUpdateHandle) RunID() string      { return "run-id" }
func (handle fakeUpdateHandle) UpdateID() string   { return "update-id" }

func (handle fakeUpdateHandle) Get(_ context.Context, destination interface{}) error {
	if handle.err != nil {
		return handle.err
	}
	target := reflect.ValueOf(destination)
	value := reflect.ValueOf(handle.value)
	if target.Kind() != reflect.Pointer || value.Type() != target.Elem().Type() {
		return errors.New("unexpected update outcome destination")
	}
	target.Elem().Set(value)
	return nil
}

type fakeCommandClient struct {
	status        workflows.DispatcherPolicyStatus
	queryWorkflow string
	queryName     string
	update        client.UpdateWorkflowOptions
	canceledID    string
	queryErr      error
	updateErr     error
	waitErr       error
	cancelErr     error
	updateValue   interface{}
}

func (fake *fakeCommandClient) QueryWorkflow(_ context.Context, workflowID, _ string, query string, _ ...interface{}) (converter.EncodedValue, error) {
	fake.queryWorkflow, fake.queryName = workflowID, query
	if fake.queryErr != nil {
		return nil, fake.queryErr
	}
	return fakeEncodedPolicy{status: fake.status}, nil
}

func (fake *fakeCommandClient) UpdateWorkflow(_ context.Context, options client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	fake.update = options
	if fake.updateErr != nil {
		return nil, fake.updateErr
	}
	return fakeUpdateHandle{value: fake.updateValue, err: fake.waitErr}, nil
}

func (fake *fakeCommandClient) CancelWorkflow(_ context.Context, workflowID, _ string) error {
	fake.canceledID = workflowID
	return fake.cancelErr
}

func TestCommandsApplyTargetDispatcherControls(t *testing.T) {
	t.Parallel()

	paused, resumed, maxInFlight := true, false, 4
	for _, test := range []struct {
		name   string
		update work.ConfigUpdate
		want   func(work.DispatcherPolicy) bool
	}{
		{"pause", work.ConfigUpdate{Paused: &paused}, func(policy work.DispatcherPolicy) bool { return policy.Paused }},
		{"resume", work.ConfigUpdate{Paused: &resumed}, func(policy work.DispatcherPolicy) bool { return !policy.Paused }},
		{"maximum in flight", work.ConfigUpdate{MaxInFlight: &maxInFlight}, func(policy work.DispatcherPolicy) bool { return policy.MaxInFlight == maxInFlight }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeCommandClient{
				status:      workflows.DispatcherPolicyStatus{Policy: work.DefaultDispatcherPolicy()},
				updateValue: workflows.DispatcherPublicationApplied,
			}
			if err := (&Commands{client: fake}).UpdateConfig(t.Context(), test.update); err != nil {
				t.Fatalf("UpdateConfig() error = %v", err)
			}
			if fake.queryWorkflow != work.TargetDispatcherWorkflowID || fake.queryName != workflows.QueryDispatcherPolicy {
				t.Fatalf("query = (%q, %q), want target Dispatcher policy", fake.queryWorkflow, fake.queryName)
			}
			if fake.update.WorkflowID != work.TargetDispatcherWorkflowID || fake.update.UpdateName != workflows.UpdateDispatcherPolicy ||
				fake.update.WaitForStage != client.WorkflowUpdateStageCompleted || !strings.HasPrefix(fake.update.UpdateID, "api-") {
				t.Fatalf("update options = %#v", fake.update)
			}
			publication, ok := fake.update.Args[0].(workflows.DispatcherPolicyUpdate)
			if !ok || !test.want(publication.Policy) {
				t.Fatalf("publication = %#v", fake.update.Args)
			}
			fingerprint, err := publication.Policy.Fingerprint()
			if err != nil || publication.Fingerprint != fingerprint {
				t.Fatalf("publication fingerprint = %q, want %q (error %v)", publication.Fingerprint, fingerprint, err)
			}
		})
	}
}

func TestCommandsRequestImmediateWorkForTheTargetTicket(t *testing.T) {
	t.Parallel()

	fake := &fakeCommandClient{updateValue: workflows.DispatcherWorkNowAcknowledged}
	if err := (&Commands{client: fake}).WorkNow(t.Context(), 42); err != nil {
		t.Fatalf("WorkNow() error = %v", err)
	}
	if fake.update.WorkflowID != work.TargetDispatcherWorkflowID || fake.update.UpdateName != workflows.UpdateDispatcherWorkNow ||
		fake.update.WaitForStage != client.WorkflowUpdateStageCompleted || !strings.HasPrefix(fake.update.UpdateID, "api-work-now-") {
		t.Fatalf("update options = %#v", fake.update)
	}
	request, ok := fake.update.Args[0].(workflows.DispatcherWorkNowRequest)
	if !ok || request.TicketID != 42 {
		t.Fatalf("work-now request = %#v, want Ticket 42", fake.update.Args)
	}
}

func TestCommandsTreatDrainingWorkNowAsClosed(t *testing.T) {
	t.Parallel()

	fake := &fakeCommandClient{updateValue: workflows.DispatcherWorkNowDraining}
	if err := (&Commands{client: fake}).WorkNow(t.Context(), 42); !errors.Is(err, work.ErrWorkflowClosed) {
		t.Fatalf("WorkNow() error = %v, want workflow closed", err)
	}
}

func TestCommandsCancelTargetTicket(t *testing.T) {
	t.Parallel()

	fake := &fakeCommandClient{}
	if err := (&Commands{client: fake}).CancelTicket(t.Context(), 42); err != nil {
		t.Fatalf("CancelTicket() error = %v", err)
	}
	if fake.canceledID != work.TicketWorkflowID(42) {
		t.Fatalf("canceled workflow ID = %q, want %q", fake.canceledID, work.TicketWorkflowID(42))
	}
}

func TestCommandsPreserveTemporalFailureKinds(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"not found", serviceerror.NewNotFound("missing"), work.ErrWorkflowNotFound},
		{"closed", serviceerror.NewFailedPrecondition("closed"), work.ErrWorkflowClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := &Commands{client: &fakeCommandClient{cancelErr: test.err}}
			if err := commands.CancelTicket(t.Context(), 42); !errors.Is(err, test.want) {
				t.Fatalf("CancelTicket() error = %v, want %v", err, test.want)
			}
		})
	}
}
