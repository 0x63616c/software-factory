package prompts

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/0x63616c/software-factory/internal/work"
)

// stageInput is what one stage's own fields contribute to its prompt, beyond
// the ticket and fence nonce every stage gets already. One type per stage:
// the contract is what the type declares, not an entry in a lookup table.
type stageInput interface {
	// templateValues returns this stage's own handoff DOCUMENTS, keyed by
	// template placeholder. Every entry must be interpolated inside a
	// document fence in that stage's template: checkFence counts the fences
	// in the rendered prompt against len(templateValues), so a value added
	// here and left unfenced is a refused render rather than untrusted prose
	// reaching a model bare. Anything that is not a prior stage's document
	// belongs in scalarValues.
	templateValues() (map[string]string, error)

	// scalarValues returns this stage's own non-document placeholders —
	// numbers and other values this system computed itself, which are not
	// fenced because there is nothing untrusted about them. Nil is the normal
	// answer; only review has any today.
	//
	// The split exists so that the fence count stays exactly the document
	// count. Before it, every stage value was assumed to be a document, and
	// adding a plain number to a template failed the render with a fence-count
	// error that described a security hole that was not there.
	scalarValues() map[string]string
}

// missingPrior is the error every stageInput returns when the earlier
// stage's document it reads is not there — the run cannot skip a stage.
func missingPrior(reader, produced work.Stage) error {
	return fmt.Errorf("the %s stage reads the %s stage's document, and there is none: the run cannot skip a stage", reader, produced)
}

type planInput struct{}

func (planInput) templateValues() (map[string]string, error) {
	return map[string]string{}, nil
}

func (planInput) scalarValues() map[string]string { return nil }

// implementInput is what one implement turn's prompt is rendered from: the
// plan every turn reads, plus two documents that only exist from the second
// turn a run reaches onward. Both declare their own absence on turn one
// rather than being omitted, so implement.md can carry one fixed set of
// placeholders regardless of which turn is rendering — see
// previousImplementReportProse and mostRecentReviewFindingsProse.
type implementInput struct {
	// Plan is the plan stage's output. Every turn reads it.
	Plan work.StageOutput

	// PreviousReport is this run's own previous implement turn's report — the
	// zero value on the first implement turn of the whole run. It exists
	// because implement's codex conversation is resumed turn to turn (see the
	// pipeline-rewrite spec's "Codex sessions"), but a workflow replay reads
	// this prompt fresh, so anything the previous turn said that later turns'
	// prompts depend on has to be handed forward as a document like any other,
	// not assumed to still be "in the model's head".
	PreviousReport work.StageOutput

	// MostRecentReview is the most recent review turn's output, if review has
	// run at all yet. A CI-window's first implement turn after a review that
	// raised blocking findings is the turn this matters for; every other turn
	// simply sees the same findings again, which is redundant but not wrong.
	MostRecentReview work.StageOutput

	// Feedback is authoritative CI, review, or merge feedback that caused this
	// implement Step. It is not PriorTurns because it did not come from an
	// earlier agent document.
	Feedback work.AgentPromptContext
}

func (in implementInput) templateValues() (map[string]string, error) {
	if strings.TrimSpace(in.Plan.Prose()) == "" {
		return nil, missingPrior(work.StageImplement, work.StagePlan)
	}
	return map[string]string{
		"plan":                      in.Plan.Prose(),
		"previous_implement_report": previousImplementReportProse(in.PreviousReport),
		"review_findings":           findingsProse(in.MostRecentReview),
		"implementation_feedback":   feedbackProse(in.Feedback),
	}, nil
}

func (implementInput) scalarValues() map[string]string { return nil }

// previousImplementReportProse declares absence rather than leaving a blank
// section: a turn that does not know it is the first has no way to tell "no
// previous report" apart from "the previous report was lost".
func previousImplementReportProse(previous work.StageOutput) string {
	if strings.TrimSpace(previous.Prose()) == "" {
		return "(This is the first implement turn of this run: there is no previous report to continue from.)"
	}
	return previous.Prose()
}

// reviewInput is what one review turn's prompt is rendered from: the most
// recent implement turn's report, plus the previous review turn's findings —
// present from review's second turn onward, and declaring its own absence
// before then, for the reason implementInput's PreviousReport does.
type reviewInput struct {
	// Implementation is the most recent implement turn's output. Every review
	// turn reads it — review runs only once that turn's CI is green.
	Implementation work.StageOutput

	// PreviousReview is this run's own previous review turn's output, the zero
	// value on review's first turn. Every review turn is a fresh thread with no
	// memory of the last, so a finding id can only be kept stable across turns
	// by showing a turn what the last one raised — see the pipeline-rewrite
	// spec's "What a finding id is, and how sameness is determined."
	PreviousReview work.StageOutput

	// Ledger is every earlier review turn this run, oldest first. Where
	// PreviousReview answers "what did the last turn say", this answers "what
	// has this run's review already covered" — see work.PriorTurns.ReviewLedger
	// for why review, alone among the stages, gets a whole-run memory.
	Ledger []work.ReviewTurnRecord

	// Turn is which review turn this is, 1-indexed.
	Turn int

	CandidateHeadSHA string
}

func (in reviewInput) templateValues() (map[string]string, error) {
	if strings.TrimSpace(in.Implementation.Prose()) == "" {
		return nil, missingPrior(work.StageReview, work.StageImplement)
	}
	return map[string]string{
		"implementation_report":    in.Implementation.Prose(),
		"previous_review_findings": findingsProse(in.PreviousReview),
		"review_ledger":            ledgerProse(in.Ledger),
	}, nil
}

// scalarValues renders review's turn and candidate identity. Both are this
// system's own values, never documents, so neither is fenced.
func (in reviewInput) scalarValues() map[string]string {
	return map[string]string{
		"review_turn":        strconv.Itoa(in.Turn),
		"candidate_head_sha": in.CandidateHeadSHA,
	}
}

// feedbackProse renders only structured, bounded feedback delivered by the
// workflow. It remains a fenced document because CI and GitHub diagnostics are
// untrusted text even though their envelope is authoritative.
func feedbackProse(context work.AgentPromptContext) string {
	var b strings.Builder
	if len(context.CIFailures) != 0 {
		fmt.Fprintf(&b, "CI failures for the exact checked candidate %s:\n\n", context.CandidateHeadSHA)
		for _, failure := range context.CIFailures {
			fmt.Fprintf(&b, "- check=%s fingerprint=%s: %s\n", failure.Name, failure.Fingerprint, failure.Evidence)
		}
	}
	if len(context.ReviewFindings) != 0 {
		if b.Len() != 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Blocking review feedback for candidate %s:\n\n", context.CandidateHeadSHA)
		for _, finding := range context.ReviewFindings {
			kind := "advisory"
			if finding.Blocking {
				kind = "blocking"
			}
			fmt.Fprintf(&b, "- id=%s (%s): %s\n", finding.ID, kind, finding.Summary)
		}
	}
	if context.Merge != nil {
		if b.Len() != 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Merge feedback for candidate %s: outcome=%s reviewed_head=%s current_head=%s current_base=%s\n\n%s\n", context.CandidateHeadSHA, context.Merge.Outcome, context.Merge.ReviewedHeadSHA, context.Merge.CurrentHeadSHA, context.Merge.CurrentBaseSHA, context.Merge.Diagnostic)
	}
	if b.Len() == 0 {
		return "(No external CI, review, or merge feedback reopened this implementation step.)"
	}
	return b.String()
}

// ledgerProse renders every earlier review turn's findings and verified list,
// oldest first, or declares that there is none.
//
// It repeats the previous turn's findings, which the prompt also shows on
// their own under a heading of their own. That redundancy is deliberate: the
// two sections answer different questions — "what must you keep the id of"
// versus "what has this run's review already covered" — and a ledger with its
// most recent entry silently missing would be the more confusing shape to
// read. The Run deadline bounds how long this ledger can grow.
func ledgerProse(ledger []work.ReviewTurnRecord) string {
	if len(ledger) == 0 {
		return "(No earlier review turns this run: this is review's first turn.)"
	}

	var b strings.Builder
	for _, record := range ledger {
		fmt.Fprintf(&b, "#### Review turn %d\n\nRaised:\n\n", record.Turn)
		if len(record.Findings) == 0 {
			b.WriteString("- (nothing)\n")
		}
		for _, f := range record.Findings {
			kind := "advisory"
			if f.Blocking {
				kind = "blocking"
			}
			fmt.Fprintf(&b, "- id=%s (%s): %s\n", f.ID, kind, f.Summary)
		}
		b.WriteString("\nChecked and would keep:\n\n")
		if len(record.Verified) == 0 {
			b.WriteString("- (that turn named nothing)\n")
		}
		for _, v := range record.Verified {
			fmt.Fprintf(&b, "- %s\n", v)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// findingsProse renders a review turn's findings as prose for a later
// prompt to read, or declares that there is none to show. It reads out through
// the stageOutputValue interface's own concrete type rather than a bare
// string, because a finding's id and blocking bit — the fields sameness is
// judged on — live in work.ReviewOutput, not in the prose document.
//
// A zero-value StageOutput (no review has run yet) and a real review that
// raised nothing both take the "none to show" branch: the two happen to read
// identically to a later prompt, which is correct, since either way there is
// nothing to reuse an id against.
func findingsProse(out work.StageOutput) string {
	review, ok := out.Value().(work.ReviewOutput)
	if !ok || len(review.Findings) == 0 {
		return "(No findings to show: either review has not run yet this run, or its last turn raised none.)"
	}

	var b strings.Builder
	for _, f := range review.Findings {
		kind := "advisory"
		if f.Blocking {
			kind = "blocking"
		}
		fmt.Fprintf(&b, "- id=%s (%s): %s\n", f.ID, kind, f.Summary)
	}
	return b.String()
}

// buildStageInput selects and builds one stage's typed input from
// work.PriorTurns — already narrowed to each stage's own latest turn by the
// time it reaches here (see PriorTurns' own doc comment for why nothing
// wider ever crosses the activity boundary).
//
// turn is that stage's own 1-indexed turn number; only review reads it.
//
// Exhaustive, no default — matches stageTemplate.
func buildStageInput(stage work.Stage, turn int, prior work.PriorTurns, promptContext work.AgentPromptContext) (stageInput, error) {
	switch stage {
	case work.StagePlan:
		return planInput{}, nil
	case work.StageImplement:
		return implementInput{
			Plan:             prior.Plan,
			PreviousReport:   prior.LatestImplement,
			MostRecentReview: prior.LatestReview,
			Feedback:         promptContext,
		}, nil
	case work.StageReview:
		return reviewInput{
			Implementation:   prior.LatestImplement,
			PreviousReview:   prior.LatestReview,
			Ledger:           prior.ReviewLedger,
			Turn:             turn,
			CandidateHeadSHA: promptContext.CandidateHeadSHA,
		}, nil
	}
	return nil, fmt.Errorf("no input shape for stage %q", stage)
}
