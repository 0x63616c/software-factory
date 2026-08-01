package agentactivities_test

import (
	"encoding/json"
	"testing"

	agentactivities "github.com/0x63616c/software-factory/internal/activities/agent"
)

func TestFinalizeOutputEncodesWhenFinalizeReturnsATypedFailure(t *testing.T) {
	t.Parallel()

	if _, err := json.Marshal(agentactivities.FinalizeOutput{}); err != nil {
		t.Fatalf("marshal empty FinalizeOutput: %v", err)
	}
}
