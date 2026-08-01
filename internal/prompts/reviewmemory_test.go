package prompts

import (
	"strconv"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// reviewPrior is a review turn's input with a ledger of earlier turns behind
// it — what every test in this file renders against.
func reviewPrior(ledger ...work.ReviewTurnRecord) work.PriorTurns {
	prior := everyDocument()
	prior.ReviewLedger = ledger
	return prior
}

func TestReviewPromptNamesItsTurn(t *testing.T) {
	t.Parallel()

	rendered, err := newTestRenderer(t).Render(Input{
		Stage: work.StageReview, Turn: 7, Ticket: ticket(), Prior: everyDocument(),
	})
	if err != nil {
		t.Fatalf("Render(review turn 7): %v", err)
	}
	if !strings.Contains(rendered, "review turn 7") {
		t.Errorf("the review prompt does not name its turn:\n%s", rendered)
	}
}

// TestReviewPromptTellsTheTurnToSweepTheWholeClass holds the instruction that
// would have closed #535 on its first review turn instead of its third. It is
// prose, so the assertion is deliberately loose — it fails when the section is
// deleted, not when it is reworded.
func TestReviewPromptTellsTheTurnToSweepTheWholeClass(t *testing.T) {
	t.Parallel()

	rendered, err := newTestRenderer(t).Render(Input{
		Stage: work.StageReview, Turn: 1, Ticket: ticket(), Prior: everyDocument(),
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"search the",
		"one",
		"finding listing every location",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the review prompt no longer tells a turn to sweep a defect class: missing %q", want)
		}
	}
}

// TestReviewPromptCarriesEveryEarlierTurnNotOnlyTheLast is the ledger's
// reason to exist. On #535 review turn 1 cleared a file in its "verified and
// keep" list and turns 2 and 3 each raised a blocking finding against it;
// turn 3 was shown only turn 2's findings, so it could not see either the
// original verdict or that the file had already been round-tripped once.
func TestReviewPromptCarriesEveryEarlierTurnNotOnlyTheLast(t *testing.T) {
	t.Parallel()

	prior := reviewPrior(
		work.ReviewTurnRecord{
			Turn:     1,
			Findings: []work.Finding{{ID: "docs/first-turn-finding", Blocking: true, Summary: "raised on turn one"}},
			Verified: []string{"platform/index.ts: the hooks comment is accurate"},
		},
		work.ReviewTurnRecord{
			Turn:     2,
			Findings: []work.Finding{{ID: "relay/second-turn-finding", Summary: "raised on turn two"}},
		},
	)

	rendered, err := newTestRenderer(t).Render(Input{
		Stage: work.StageReview, Turn: 3, Ticket: ticket(), Prior: prior,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		"docs/first-turn-finding",
		"relay/second-turn-finding",
		"platform/index.ts: the hooks comment is accurate",
		"Review turn 1",
		"Review turn 2",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the review prompt does not carry %q from the ledger", want)
		}
	}
}

// TestReviewPromptDeclaresAnEmptyLedgerRatherThanLeavingAGap follows the rule
// every other handoff in this package is held to: an empty region reads to a
// model as something it failed to receive.
func TestReviewPromptDeclaresAnEmptyLedgerRatherThanLeavingAGap(t *testing.T) {
	t.Parallel()

	rendered, err := newTestRenderer(t).Render(Input{
		Stage: work.StageReview, Turn: 1, Ticket: ticket(), Prior: everyDocument(),
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(rendered, "this is review's first turn") {
		t.Error("the review prompt does not declare that there are no earlier review turns")
	}
}

// TestLedgerProseNamesATurnThatVerifiedNothing keeps the two absences apart:
// a turn that raised nothing and a turn that named nothing it would keep are
// different facts about that turn, and rendering either as a blank line would
// read as neither.
func TestLedgerProseNamesATurnThatVerifiedNothing(t *testing.T) {
	t.Parallel()

	got := ledgerProse([]work.ReviewTurnRecord{{Turn: 1}})
	if !strings.Contains(got, "(nothing)") {
		t.Errorf("ledgerProse does not say the turn raised nothing:\n%s", got)
	}
	if !strings.Contains(got, "that turn named nothing") {
		t.Errorf("ledgerProse does not say the turn verified nothing:\n%s", got)
	}
}

func TestReviewTurnIsNotFenced(t *testing.T) {
	t.Parallel()

	in := reviewInput{
		Implementation: stageOutputOf(work.StageImplement, "the report"),
		Turn:           2,
	}
	documents, err := in.templateValues()
	if err != nil {
		t.Fatalf("templateValues: %v", err)
	}
	if _, ok := documents["review_turn"]; ok {
		t.Error("review_turn is counted as a fenced document; it belongs in scalarValues")
	}
	if got := in.scalarValues()["review_turn"]; got != strconv.Itoa(2) {
		t.Errorf("review_turn = %q, want 2", got)
	}
}
