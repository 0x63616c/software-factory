package work

import (
	"fmt"
	"strings"
)

const (
	// MaxAgentPromptFeedbackItems bounds each repeated feedback list before it
	// reaches workflow history or a model prompt.
	MaxAgentPromptFeedbackItems = 10
	// MaxAgentPromptFeedbackFieldBytes bounds one diagnostic/evidence field.
	MaxAgentPromptFeedbackFieldBytes = 2_048
	// MaxAgentPromptFeedbackBytes bounds the complete structured handoff.
	MaxAgentPromptFeedbackBytes = 8_192
)

// AgentPromptContext is the bounded, structured handoff from workflow
// decisions to one agent prompt. It is intentionally separate from
// PriorTurns: PriorTurns is agent-produced context, while this carries
// authoritative GitHub feedback and the exact candidate identity.
type AgentPromptContext struct {
	// CandidateHeadSHA is the GitHub head this prompt is authorized to inspect.
	// Review receives it only after CI authorized that exact immutable commit.
	CandidateHeadSHA string

	// CIFailures is the bounded evidence GitHub returned for the exact checked
	// candidate. It is present only on an implement retry caused by red CI.
	CIFailures []CheckFailure

	// ReviewFindings is the structured result that reopened an implement step.
	// It is separate from PriorTurns so the agent knows it is current workflow
	// feedback rather than inferred conversation state.
	ReviewFindings []Finding

	// Merge is authoritative merge feedback that reopened implementation.
	// It is nil unless GitHub rejected the reviewed candidate semantically.
	Merge *MergeFeedback
}

// MergeFeedback is the bounded GitHub context an implementer needs to repair
// a textual conflict or refresh a stale base without trusting model prose.
type MergeFeedback struct {
	Outcome         PullRequestMergeOutcome
	ReviewedHeadSHA string
	CurrentHeadSHA  string
	CurrentBaseSHA  string
	Diagnostic      string
}

// Validate rejects oversized handoffs deterministically before they are sent
// to the agent activity. Rejection, rather than silent truncation, preserves
// the exact GitHub evidence that caused a semantic retry.
func (c AgentPromptContext) Validate() error {
	if len(c.CIFailures) > MaxAgentPromptFeedbackItems || len(c.ReviewFindings) > MaxAgentPromptFeedbackItems {
		return fmt.Errorf("agent prompt feedback has too many items: %w", ErrPermanent)
	}
	values := []string{c.CandidateHeadSHA}
	for _, failure := range c.CIFailures {
		values = append(values, failure.Name, failure.Fingerprint, failure.Evidence)
	}
	for _, finding := range c.ReviewFindings {
		values = append(values, finding.ID, finding.Summary)
	}
	if c.Merge != nil {
		values = append(values, string(c.Merge.Outcome), c.Merge.ReviewedHeadSHA, c.Merge.CurrentHeadSHA, c.Merge.CurrentBaseSHA, c.Merge.Diagnostic)
	}
	total := 0
	for _, value := range values {
		if len(value) > MaxAgentPromptFeedbackFieldBytes {
			return fmt.Errorf("agent prompt feedback field exceeds %d bytes: %w", MaxAgentPromptFeedbackFieldBytes, ErrPermanent)
		}
		total += len(value)
	}
	if total > MaxAgentPromptFeedbackBytes {
		return fmt.Errorf("agent prompt feedback exceeds %d bytes: %w", MaxAgentPromptFeedbackBytes, ErrPermanent)
	}
	if strings.TrimSpace(c.CandidateHeadSHA) == "" && c.Merge != nil && strings.TrimSpace(c.Merge.ReviewedHeadSHA) == "" {
		return fmt.Errorf("merge feedback requires a reviewed head SHA: %w", ErrPermanent)
	}
	return nil
}
