package temporal

import (
	"context"
	"fmt"

	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
	workflowservice "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// DispatcherPolicyPublication identifies one idempotent Update-With-Start
// operation. RequestID is deliberately distinct from the content fingerprint.
type DispatcherPolicyPublication struct {
	RequestID string
	Input     workflows.DispatcherInput
}

// DispatcherPublisher publishes target dispatcher policy before a worker may
// start its main queue. It intentionally owns the SDK Update-With-Start shape.
type DispatcherPublisher struct{ client client.Client }

// NewDispatcherPublisher constructs a DispatcherPublisher over one live client.
func NewDispatcherPublisher(client client.Client) *DispatcherPublisher {
	return &DispatcherPublisher{client: client}
}

// PublishDispatcherPolicy starts the singleton if absent, otherwise updates its
// current execution, then waits for the synchronous handler outcome.
func (p *DispatcherPublisher) PublishDispatcherPolicy(ctx context.Context, publication DispatcherPolicyPublication) (workflows.DispatcherPublication, error) {
	if publication.RequestID == "" {
		return "", fmt.Errorf("dispatcher policy update ID is required")
	}
	fingerprint, err := publication.Input.Policy.Fingerprint()
	if err != nil {
		return "", fmt.Errorf("fingerprinting dispatcher policy: %w", err)
	}
	if p == nil || p.client == nil {
		return "", fmt.Errorf("dispatcher policy publisher has no Temporal client")
	}
	start := p.client.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID:                       work.TargetDispatcherWorkflowID,
		TaskQueue:                work.TargetDispatcherTaskQueue,
		WorkflowIDConflictPolicy: workflowservice.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, workflows.Dispatcher, publication.Input)
	handle, err := p.client.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: start,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID: publication.RequestID, UpdateName: workflows.UpdateDispatcherPolicy,
			Args:         []interface{}{workflows.DispatcherPolicyUpdate{Fingerprint: fingerprint, Policy: publication.Input.Policy}},
			WaitForStage: client.WorkflowUpdateStageCompleted,
		},
	})
	if err != nil {
		return "", fmt.Errorf("starting or updating target dispatcher: %w", err)
	}
	var outcome workflows.DispatcherPublication
	if err := handle.Get(ctx, &outcome); err != nil {
		return "", fmt.Errorf("waiting for target dispatcher policy update: %w", err)
	}
	return outcome, nil
}
