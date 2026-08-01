package prompts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/0x63616c/software-factory/internal/work"
)

// documentEnvelope is codex's wire shape for the one stage that answers in a
// single prose field with nothing else beside it: plan.
//
// It carries codex's own field spellings — this is the schema-facing type,
// distinct from work.DocumentOutput, which is a Go-to-Go encoding across a
// worker redeploy and has no reason to share tags with what codex was told
// to answer. See "Two encodings, deliberately separate" in this step's spec.
type documentEnvelope struct {
	// Raw rather than *string, so an absent field and a present-but-null one
	// are told apart. They are different failures — a stage that answered in
	// some other shape, and a stage that answered in this one and put nothing
	// in it — and an error naming the wrong one sends whoever is debugging
	// the run after a schema mismatch that is not there.
	Document json.RawMessage `json:"document"`
}

// implementEnvelope is codex's wire shape for the implement stage: its
// report, whether it finished, and the pull request title/body for the
// branch as it now stands.
type implementEnvelope struct {
	Report        json.RawMessage `json:"report"`
	Blocked       *bool           `json:"blocked"`
	BlockedReason *string         `json:"blocked_reason"`
	Title         *string         `json:"title"`
	Body          *string         `json:"body"`
}

// findingEnvelope is codex's wire shape for one review finding.
type findingEnvelope struct {
	ID       string `json:"id"`
	Blocking bool   `json:"blocking"`
	Summary  string `json:"summary"`
}

// reviewEnvelope is codex's wire shape for the review stage: its document,
// every finding it raised, and what it checked and would keep.
//
// Verified carries no control flow: a turn that answers with an empty array
// has told a later turn nothing, which is the same position every turn was
// in before the field existed. The schema still lists it in "required"
// alongside document and findings — every property must be, because codex's
// structured-output mode rejects a schema where "required" omits a declared
// property (#576 lost three consecutive runs to exactly that: the API
// returned invalid_json_schema before spending a single token). "Optional" is
// expressed by the model being free to answer with an empty array, never by
// leaving the field out of "required".
type reviewEnvelope struct {
	Document json.RawMessage   `json:"document"`
	Findings []findingEnvelope `json:"findings"`
	Verified []string          `json:"verified"`
}

// decodeDocumentEnvelope reads a stage's result envelope and returns the one
// prose field it holds.
//
// It is strict, and every rejection below is a stage that did not do its
// job: an unreadable result means the stage failed, and the run is worth
// failing visibly rather than carrying an empty or half-guessed document
// into the next prompt. Unknown fields are rejected too — the schema says
// additionalProperties false, and accepting more here would let the two
// disagree quietly.
func decodeDocumentEnvelope(result []byte) (string, error) {
	var envelope documentEnvelope

	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", fmt.Errorf("reading the stage's result envelope: %w", err)
	}
	// A second value after the envelope means the file holds more than the
	// final message — a stage that appended, or a retry that wrote twice.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("the stage's result holds more than one envelope; only its final message is its output")
	}
	if envelope.Document == nil {
		return "", fmt.Errorf("the stage's result has no document field: it returned something other than the envelope it was given")
	}
	if string(envelope.Document) == "null" {
		return "", fmt.Errorf("the stage's result sets document to null: it answered in the envelope and put nothing in it")
	}

	var document string
	if err := json.Unmarshal(envelope.Document, &document); err != nil {
		return "", fmt.Errorf("reading the stage's document out of its result envelope: %w", err)
	}
	if strings.TrimSpace(document) == "" {
		return "", fmt.Errorf("the stage returned an empty document: it produced no handoff at all")
	}
	return document, nil
}

// decodeImplementEnvelope reads the implement stage's result envelope: its
// report, whether it finished, and the pull request title/body for the
// branch as it now stands.
//
// Strict in the same ways decodeDocumentEnvelope is, plus one more: blocked
// and blocked_reason travel together. A blocked run with no reason told
// nobody what it needed, and a blocked_reason on a run that says it finished
// is a stage contradicting itself. title and body are checked for presence
// only, not for content: both are legitimately empty on a blocked turn that
// pushed nothing worth describing.
func decodeImplementEnvelope(result []byte) (report string, blocked bool, blockedReason, title, body string, err error) {
	var envelope implementEnvelope

	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", false, "", "", "", fmt.Errorf("reading the stage's result envelope: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", false, "", "", "", fmt.Errorf("the stage's result holds more than one envelope; only its final message is its output")
	}
	if envelope.Report == nil {
		return "", false, "", "", "", fmt.Errorf("the stage's result has no report field: it returned something other than the envelope it was given")
	}
	if string(envelope.Report) == "null" {
		return "", false, "", "", "", fmt.Errorf("the stage's result sets report to null: it answered in the envelope and put nothing in it")
	}
	if err := json.Unmarshal(envelope.Report, &report); err != nil {
		return "", false, "", "", "", fmt.Errorf("reading the stage's report out of its result envelope: %w", err)
	}
	if strings.TrimSpace(report) == "" {
		return "", false, "", "", "", fmt.Errorf("the stage returned an empty report: it produced no handoff at all")
	}
	if envelope.Blocked == nil {
		return "", false, "", "", "", fmt.Errorf("the stage's result has no blocked field: it returned something other than the envelope it was given")
	}
	if envelope.BlockedReason == nil {
		return "", false, "", "", "", fmt.Errorf("the stage's result has no blocked_reason field: it returned something other than the envelope it was given")
	}
	if envelope.Title == nil {
		return "", false, "", "", "", fmt.Errorf("the stage's result has no title field: it returned something other than the envelope it was given")
	}
	if envelope.Body == nil {
		return "", false, "", "", "", fmt.Errorf("the stage's result has no body field: it returned something other than the envelope it was given")
	}
	switch {
	case *envelope.Blocked && strings.TrimSpace(*envelope.BlockedReason) == "":
		return "", false, "", "", "", fmt.Errorf("the stage says it is blocked but gives no blocked_reason")
	case !*envelope.Blocked && strings.TrimSpace(*envelope.BlockedReason) != "":
		return "", false, "", "", "", fmt.Errorf("the stage gives a blocked_reason but says blocked is false")
	}

	return report, *envelope.Blocked, *envelope.BlockedReason, *envelope.Title, *envelope.Body, nil
}

// decodeReviewEnvelope reads the review stage's result envelope: its
// document, plus every finding it raised.
//
// Strict in the same ways decodeDocumentEnvelope is. An empty findings array
// is not an error — a clean pass is a legitimate review outcome — but every
// finding present must carry a non-empty id: sameness across turns is exact
// string equality on it (see work.ReviewOutput), and an empty id would make
// every such finding compare equal to every other.
func decodeReviewEnvelope(result []byte) (document string, findings []work.Finding, verified []string, err error) {
	var envelope reviewEnvelope

	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", nil, nil, fmt.Errorf("reading the stage's result envelope: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", nil, nil, fmt.Errorf("the stage's result holds more than one envelope; only its final message is its output")
	}
	if envelope.Document == nil {
		return "", nil, nil, fmt.Errorf("the stage's result has no document field: it returned something other than the envelope it was given")
	}
	if string(envelope.Document) == "null" {
		return "", nil, nil, fmt.Errorf("the stage's result sets document to null: it answered in the envelope and put nothing in it")
	}
	if err := json.Unmarshal(envelope.Document, &document); err != nil {
		return "", nil, nil, fmt.Errorf("reading the stage's document out of its result envelope: %w", err)
	}
	if strings.TrimSpace(document) == "" {
		return "", nil, nil, fmt.Errorf("the stage returned an empty document: it produced no handoff at all")
	}

	for i, f := range envelope.Findings {
		if strings.TrimSpace(f.ID) == "" {
			return "", nil, nil, fmt.Errorf("finding %d has no id: sameness across turns is exact string equality on it, and an empty id cannot be compared", i)
		}
		findings = append(findings, work.Finding{ID: f.ID, Blocking: f.Blocking, Summary: f.Summary})
	}
	return document, findings, envelope.Verified, nil
}

// Decode reads a stage's result envelope — codex's answer to
// templates/<stage>.schema.json — into the domain's StageOutput.
//
// Exhaustive, no default: a fourth stage needs a case here before it
// compiles, matching stageTemplate and work.decodeStageOutputValue.
//
// This must only ever be called from activity code, today from the
// AgentWorkflow finalize activity. It calls work.NewStageOutput, which panics on a
// stage/shape mismatch; that panic is only safe on the activity side of the
// workflow/activity boundary (see NewStageOutput's doc comment). Calling
// Decode from internal/workflows/** would risk a workflow-task panic that
// Temporal retries forever instead of failing the run — depguard already
// forbids that package from importing this one at all, so this is enforced
// mechanically, not only by this comment.
func Decode(stage work.Stage, result []byte) (work.StageOutput, error) {
	switch stage {
	case work.StagePlan:
		document, err := decodeDocumentEnvelope(result)
		if err != nil {
			return work.StageOutput{}, err
		}
		return work.NewStageOutput(stage, work.DocumentOutput{Document: document}), nil
	case work.StageImplement:
		report, blocked, blockedReason, title, body, err := decodeImplementEnvelope(result)
		if err != nil {
			return work.StageOutput{}, err
		}
		return work.NewStageOutput(stage, work.ImplementOutput{
			Report: report, Blocked: blocked, BlockedReason: blockedReason, Title: title, Body: body,
		}), nil
	case work.StageReview:
		document, findings, verified, err := decodeReviewEnvelope(result)
		if err != nil {
			return work.StageOutput{}, err
		}
		return work.NewStageOutput(stage, work.ReviewOutput{
			Document: document, Findings: findings, Verified: verified,
		}), nil
	}
	return work.StageOutput{}, fmt.Errorf("no decoder for stage %q", stage)
}
