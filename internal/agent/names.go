package agent

import "fmt"

const (
	// WorkflowName is the stable Temporal registration for the reusable agent runtime.
	WorkflowName = "AgentWorkflow"
	// PrepareActivityName is the prompt and initial-conversation activity.
	PrepareActivityName = "agent.prepare"
	// ModelTurnActivityName is one direct provider turn.
	ModelTurnActivityName = "agent.model-turn"
	// ToolActivityName is one sandbox-affine typed tool execution.
	ToolActivityName = "agent.tool"
	// FinalizeActivityName decodes terminal structured text into a stage result.
	FinalizeActivityName = "agent.finalize"
	// LifecycleActivityName records one content-free terminal child outcome.
	LifecycleActivityName = "agent.lifecycle"
)

// WorkflowID returns one stage turn's deterministic child workflow ID.
func WorkflowID(runID, stage string, turn int) string {
	return fmt.Sprintf("agent/%s/%s/%d", runID, stage, turn)
}
