package activities

import "github.com/0x63616c/software-factory/internal/work"

// StageAttempt is the bounded semantic input passed to AgentWorkflow.
type StageAttempt struct {
	Key            work.StageKey
	Model          work.Model
	Detail         work.TicketDetail
	Prior          work.PriorTurns
	PromptContext  work.AgentPromptContext
	MaxReviewSteps int
}
