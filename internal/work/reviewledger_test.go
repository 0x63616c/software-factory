package work

import (
	"encoding/json"
	"testing"
)

// TestPriorTurnsCarriesTheReviewLedgerAcrossTheActivityBoundary is the whole
// point of the ledger: it travels in an activity input, which Temporal
// serializes, so a ledger that did not survive a round trip would be empty by
// the time any prompt read it.
func TestPriorTurnsCarriesTheReviewLedgerAcrossTheActivityBoundary(t *testing.T) {
	t.Parallel()

	prior := PriorTurns{
		ReviewLedger: []ReviewTurnRecord{
			{
				Turn:     1,
				Findings: []Finding{{ID: "docs/stale-topology", Blocking: true, Summary: "a stale comment"}},
				Verified: []string{"packages/platform/src/index.ts: hooks host comment is accurate"},
			},
			{Turn: 2},
		},
	}

	data, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PriorTurns
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.ReviewLedger) != 2 {
		t.Fatalf("ledger has %d entries, want 2", len(got.ReviewLedger))
	}
	first := got.ReviewLedger[0]
	if first.Turn != 1 {
		t.Errorf("first entry is turn %d, want 1: the ledger is ordered oldest first", first.Turn)
	}
	if len(first.Findings) != 1 || first.Findings[0].ID != "docs/stale-topology" || !first.Findings[0].Blocking {
		t.Errorf("first entry's findings did not survive the round trip: %+v", first.Findings)
	}
	if len(first.Verified) != 1 {
		t.Fatalf("first entry's verified list did not survive the round trip: %+v", first.Verified)
	}
	if got.ReviewLedger[1].Turn != 2 || len(got.ReviewLedger[1].Findings) != 0 {
		t.Errorf("second entry = %+v, want turn 2 with nothing raised", got.ReviewLedger[1])
	}
}

// TestPriorTurnsWithNoReviewLedgerStillMarshalsCleanly guards the same
// zero-value hazard the rest of this type is built around: the ledger is
// absent for every plan turn and every implement turn before review has run.
func TestPriorTurnsWithNoReviewLedgerStillMarshalsCleanly(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(PriorTurns{})
	if err != nil {
		t.Fatalf("Marshal(zero PriorTurns): %v", err)
	}
	var got PriorTurns
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ReviewLedger != nil {
		t.Errorf("ReviewLedger = %+v, want nil", got.ReviewLedger)
	}
}

// TestReviewOutputCarriesItsVerifiedList holds the field a later review turn
// reads to see what an earlier one already cleared. Nothing in the workflow
// branches on it, so a silent loss here would show up only as a review turn
// re-litigating a file — which is the failure it was added for.
func TestReviewOutputCarriesItsVerifiedList(t *testing.T) {
	t.Parallel()

	out := NewStageOutput(StageReview, ReviewOutput{
		Document: "the review",
		Verified: []string{"internal/relay/relay.go: HMAC is verified before forwarding"},
	})

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got StageOutput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	review, ok := got.Value().(ReviewOutput)
	if !ok {
		t.Fatalf("Value() is %T, want ReviewOutput", got.Value())
	}
	if len(review.Verified) != 1 {
		t.Fatalf("Verified = %+v, want one entry", review.Verified)
	}
}
