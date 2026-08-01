package temporal

import (
	"context"
	"fmt"

	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

const preActivationWorkflowQuery = `ExecutionStatus = 'Running' AND (WorkflowType = 'FactoryDispatcher' OR WorkflowType = 'FactoryWorkTicket' OR WorkflowType = 'AgentWorkflow')`

// ActivationReadiness provides the one read-only legacy-history check needed
// before the target worker begins polling.
type ActivationReadiness struct {
	client    client.Client
	namespace string
}

// NewActivationReadiness constructs the read-only legacy-history check.
func NewActivationReadiness(client client.Client, namespace string) *ActivationReadiness {
	return &ActivationReadiness{client: client, namespace: namespace}
}

// RunningPreActivationExecutions counts every still-open pre-activation type.
func (readiness *ActivationReadiness) RunningPreActivationExecutions(ctx context.Context) (int, error) {
	if readiness == nil || readiness.client == nil || readiness.namespace == "" {
		return 0, fmt.Errorf("activation readiness requires a Temporal client and namespace")
	}
	count := 0
	var next []byte
	for {
		page, err := readiness.client.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: readiness.namespace, Query: preActivationWorkflowQuery, NextPageToken: next,
		})
		if err != nil {
			return 0, fmt.Errorf("listing pre-activation workflow executions: %w", err)
		}
		count += len(page.Executions)
		next = page.NextPageToken
		if len(next) == 0 {
			return count, nil
		}
	}
}
