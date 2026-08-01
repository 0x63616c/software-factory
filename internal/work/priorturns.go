package work

import "encoding/json"

// PriorTurns is exactly what one stage's prompt is ever rendered from: the
// plan, the latest implement turn, and the latest review turn — each the
// zero StageOutput if that stage has not produced one yet this run.
//
// It exists so the activity input a stage attempt travels in cannot carry
// more than this, structurally. The workflow loop (internal/workflows) keeps
// the full, ordered history of every turn in its own local state — it needs
// that for progress detection, comparing finding ids and failing checks
// across turns — but nothing in internal/prompts reads further back than
// each stage's own latest turn (buildStageInput's own doc comment says so).
// Before this type existed, the whole history travelled in the activity
// input as a map keyed by stage, which Temporal records into workflow
// history on every single stage invocation: turn 15's input carried all 14
// preceding outputs, turn 14 carried 13, and so on — O(N^2) total bytes
// recorded across a run, for data no reader ever used past its own tail.
// A purpose-built struct with three fields cannot regress back into that: a
// caller wanting to pass "everything" has no field to put it in.
type PriorTurns struct {
	// Plan is the plan stage's output. Every implement turn reads it; review
	// does not read it directly (see "The template change slice i owns" in
	// the pipeline-rewrite spec — review reads the implementation, not the
	// plan, though it is shown the ticket text like every stage is).
	Plan StageOutput

	// LatestImplement is the most recently completed implement turn's
	// output, the zero value before implement has run at all this run. Both
	// implement (as its own previous turn, since implement's session is
	// resumed but a workflow replay reads this prompt fresh) and review (as
	// the turn it is reviewing) read this same value.
	LatestImplement StageOutput

	// LatestReview is the most recently completed review turn's output, the
	// zero value before review has run at all this run. Both implement
	// (seeing the review that reopened its window) and review (its own
	// previous turn, to keep a finding's id stable across a fresh thread with
	// no memory of raising it) read this same value.
	LatestReview StageOutput

	// ReviewLedger is what EVERY earlier review turn raised and cleared, in
	// turn order, oldest first — empty before review has run this run. Only
	// the review prompt reads it.
	//
	// It is the one exception to this type's "latest turn only" shape, and it
	// is bounded rather than open-ended: review turns are hard-capped at
	// MaxReviewTurns and each entry is a handful of one-line strings, so the
	// ledger's whole size is a constant, not a function of run length. That
	// is precisely what the implement turns have no analogue of — implement
	// runs up to its immutable review budget times carrying a
	// full report each, which is the O(N^2) history this type was built to
	// stop. A ledger of implement reports would reintroduce it; a ledger of
	// bounded number of finding lists cannot.
	//
	// LatestReview does not subsume it. LatestReview is one turn's document;
	// the ledger is the run's whole review memory, and without it a turn's
	// instruction to reuse a finding id "character for character" holds only
	// against its immediate predecessor. A defect raised on turn 1, fixed,
	// and regressed by turn 3's implement would be minted a fresh id, which
	// reads to the workflow's progress detection as new work rather than a
	// loop.
	ReviewLedger []ReviewTurnRecord
}

// ReviewTurnRecord is one past review turn, compacted to what a later turn
// needs to not repeat it: which turn it was, what it raised, and what it
// checked and would keep. The document itself is deliberately absent — the
// ledger is memory, not a transcript.
type ReviewTurnRecord struct {
	// Turn is the 1-indexed review turn this record is of.
	Turn int

	// Findings is every finding that turn raised, blocking and advisory
	// alike. Advisory ones are kept because an advisory finding a later turn
	// re-raises as blocking is the same defect and must reuse its id.
	Findings []Finding

	// Verified is that turn's "checked and would keep" list. See
	// ReviewOutput.Verified for the run this field was built out of.
	Verified []string
}

// priorTurnsWire is PriorTurns' own JSON shape: each field a pointer, absent
// rather than present-but-zero when that stage has not run yet this run.
//
// This exists because StageOutput.MarshalJSON deliberately refuses to encode
// its own zero value — a guard against a stage that forgot to call
// NewStageOutput on its SUCCESS path (stageoutput.go's own doc comment) —
// and a PriorTurns field being the zero StageOutput is not that: it is the
// ordinary, expected state of "this stage has not produced a turn yet",
// exactly what an absent key in the old map-based Prior meant. A plain
// struct-field encoding would hit that guard on every run's first turns, so
// each field here is a pointer instead: nil serialises as a JSON absence
// (omitempty), which UnmarshalJSON reads back as the zero StageOutput
// PriorTurns already treats as "hasn't run yet" everywhere else.
// ReviewLedger needs none of that: it is an ordinary slice with no
// zero-value guard of its own, so it encodes and decodes as itself, absent
// when empty.
type priorTurnsWire struct {
	Plan            *StageOutput       `json:"plan,omitempty"`
	LatestImplement *StageOutput       `json:"latestImplement,omitempty"`
	LatestReview    *StageOutput       `json:"latestReview,omitempty"`
	ReviewLedger    []ReviewTurnRecord `json:"reviewLedger,omitempty"`
}

// MarshalJSON encodes only the turns that have actually happened, so a run's
// early turns — most of Prior still zero — never trip StageOutput's own
// zero-value guard.
func (p PriorTurns) MarshalJSON() ([]byte, error) {
	var wire priorTurnsWire
	if p.Plan.Value() != nil {
		wire.Plan = &p.Plan
	}
	if p.LatestImplement.Value() != nil {
		wire.LatestImplement = &p.LatestImplement
	}
	if p.LatestReview.Value() != nil {
		wire.LatestReview = &p.LatestReview
	}
	wire.ReviewLedger = p.ReviewLedger
	return json.Marshal(wire)
}

// UnmarshalJSON decodes priorTurnsWire back, an absent field becoming the
// zero StageOutput — "this stage has not produced a turn yet," the same
// meaning it already carries everywhere else in this type.
func (p *PriorTurns) UnmarshalJSON(data []byte) error {
	var wire priorTurnsWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Plan != nil {
		p.Plan = *wire.Plan
	}
	if wire.LatestImplement != nil {
		p.LatestImplement = *wire.LatestImplement
	}
	if wire.LatestReview != nil {
		p.LatestReview = *wire.LatestReview
	}
	p.ReviewLedger = wire.ReviewLedger
	return nil
}
