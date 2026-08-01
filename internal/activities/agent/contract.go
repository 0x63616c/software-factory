package agentactivities

import (
	"time"

	"github.com/0x63616c/software-factory/internal/activities"
	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
)

// AgentMetrics is the provider-neutral metric surface used by both
// production activity composition roots.
type AgentMetrics interface {
	AgentModelTurn(work.Model, telemetry.AgentOutcome, work.Usage, bool, int64, time.Duration)
	AgentToolCall(string, telemetry.AgentOutcome, int64, time.Duration)
	AgentActivityRetry(string)
	AgentChildFinished(telemetry.AgentOutcome)
}

type noopAgentMetrics struct{}

func (noopAgentMetrics) AgentModelTurn(work.Model, telemetry.AgentOutcome, work.Usage, bool, int64, time.Duration) {
}
func (noopAgentMetrics) AgentToolCall(string, telemetry.AgentOutcome, int64, time.Duration) {}
func (noopAgentMetrics) AgentActivityRetry(string)                                          {}
func (noopAgentMetrics) AgentChildFinished(telemetry.AgentOutcome)                          {}

// PromptRenderer is the existing product-owned stage prompt and result format.
type PromptRenderer interface {
	Render(key work.StageKey, detail work.TicketDetail, prior work.PriorTurns, promptContext work.AgentPromptContext) (prompt string, schema []byte, err error)
	Decode(stage work.Stage, result []byte) (work.StageOutput, error)
}

// PrepareInput contains the bounded parent-owned stage attempt.
type PrepareInput struct {
	Attempt activities.StageAttempt
	// Identity is the durable semantic execution identity. Empty retains the
	// stage-key-derived identity required by pre-target-run histories.
	Identity string
	CacheKey string
	Seed     *agent.ConversationSeed
}

// PrepareOutput starts the reference-only workflow state.
type PrepareOutput struct {
	ConversationRef agent.ConversationRef
	TranscriptRef   agent.TranscriptRef
	ResponseFormat  agent.ResponseFormatRef
	PromptCacheKey  string
}

// FinalizeInput identifies terminal structured text and its expected stage shape.
type FinalizeInput struct {
	Stage         work.Stage
	TextRef       agent.TextRef
	TranscriptRef agent.TranscriptRef
}

// FinalizeOutput contains the decoded closed stage result.
type FinalizeOutput struct {
	Result        *work.StageOutput
	TranscriptRef agent.TranscriptRef
}

// LifecycleInput contains only terminal classification metadata.
type LifecycleInput struct {
	Outcome telemetry.AgentOutcome
	// Budget decodes lifecycle commands from pre-unbounded workflow histories.
	// RecordLifecycle deliberately ignores it.
	Budget string
}
