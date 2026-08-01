package work

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentPromptContextRejectsOversizedFeedbackDeterministically(t *testing.T) {
	t.Parallel()
	context := AgentPromptContext{CIFailures: []CheckFailure{{
		Name: "test", Evidence: strings.Repeat("x", MaxAgentPromptFeedbackFieldBytes+1),
	}}}
	if err := context.Validate(); !errors.Is(err, ErrPermanent) {
		t.Fatalf("Validate() error = %v, want permanent oversized-feedback rejection", err)
	}
}
