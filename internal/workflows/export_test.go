package workflows

import (
	"github.com/0x63616c/software-factory/internal/work"
	"go.temporal.io/sdk/workflow"
)

func AgentToolActivityOptionsForTest() workflow.ActivityOptions { return agentToolActivityOptions() }

func AgentModelTurnActivityOptionsForTest(policy work.AgentActivityPolicy) workflow.ActivityOptions {
	return agentModelTurnActivityOptions(policy)
}
