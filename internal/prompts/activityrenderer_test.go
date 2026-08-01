package prompts

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// TestActivityRendererMatchesTheUnderlyingRenderer proves the adapter forwards
// rather than diverges: what it renders is what Renderer.Render would have
// rendered for the same Input, and the schema is that stage's own.
func TestActivityRendererMatchesTheUnderlyingRenderer(t *testing.T) {
	t.Parallel()

	renderer := newTestRenderer(t)
	adapter := NewActivityRenderer(renderer)

	stage := work.StagePlan
	detail := ticket()
	prior := everyDocument()

	prompt, schema, err := adapter.Render(work.StageKey{Ticket: detail.Number, RunID: "r", Stage: stage, Turn: 1}, detail, prior, work.AgentPromptContext{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(prompt, detail.Title) {
		t.Errorf("Render prompt does not contain the ticket title %q", detail.Title)
	}
	want, err := templates.ReadFile("templates/plan.schema.json")
	if err != nil {
		t.Fatalf("reading templates/plan.schema.json: %v", err)
	}
	if string(schema) != string(want) {
		t.Errorf("Render schema does not match plan's own schema file")
	}
}

// TestActivityRendererSchemaMatchesEachStagesOwnFile is the falsifiable
// per-stage wiring check: every stage's rendered schema is byte-identical to
// that stage's own embedded file, and implement's is not byte-identical to
// plan's. A stage-blind lookup (e.g. always returning plan's schema) would
// still pass TestActivityRendererMatchesTheUnderlyingRenderer above, because
// that test only exercises one stage — this one would catch it.
func TestActivityRendererSchemaMatchesEachStagesOwnFile(t *testing.T) {
	t.Parallel()

	adapter := NewActivityRenderer(newTestRenderer(t))
	detail := ticket()
	prior := everyDocument()

	schemas := map[work.Stage][]byte{}
	for _, stage := range work.Pipeline() {
		file, err := stageSchema(stage)
		if err != nil {
			t.Fatalf("stageSchema(%s): %v", stage, err)
		}
		want, err := templates.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}

		_, schema, err := adapter.Render(work.StageKey{Ticket: detail.Number, RunID: "r", Stage: stage, Turn: 1}, detail, prior, work.AgentPromptContext{})
		if err != nil {
			t.Fatalf("Render(%s): %v", stage, err)
		}
		if string(schema) != string(want) {
			t.Errorf("Render(%s) schema does not match %s", stage, file)
		}
		schemas[stage] = schema
	}

	if string(schemas[work.StageImplement]) == string(schemas[work.StagePlan]) {
		t.Error("implement's schema is byte-identical to plan's; the per-stage lookup is not actually per-stage")
	}
}

// TestActivityRendererDecodeForwardsToThePackageFunction proves Decode is
// not reimplemented, only exposed as a method.
func TestActivityRendererDecodeForwardsToThePackageFunction(t *testing.T) {
	t.Parallel()

	adapter := NewActivityRenderer(newTestRenderer(t))
	result := []byte(`{"document":"the handoff"}`)

	got, err := adapter.Decode(work.StagePlan, result)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want, err := Decode(work.StagePlan, result)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Errorf("adapter.Decode = %#v, want %#v", got, want)
	}
}

// TestActivityRendererFailsLikeTheRendererItWraps proves an error from
// Renderer.Render is not swallowed or replaced.
func TestActivityRendererFailsLikeTheRendererItWraps(t *testing.T) {
	t.Parallel()

	adapter := NewActivityRenderer(newTestRenderer(t))
	_, _, err := adapter.Render(work.StageKey{Stage: work.StagePlan, Turn: 1}, work.TicketDetail{}, work.PriorTurns{}, work.AgentPromptContext{})
	if err == nil {
		t.Fatal("Render with an empty ticket detail: want an error, got nil")
	}
}

// TestActivityRendererRendersTheKeysOwnTurn is why Render takes a StageKey
// rather than a bare Stage. Every other test here renders turn 1, so an
// adapter that dropped Turn — or hardcoded it — would be green while every
// review prompt in production said "turn 0".
func TestActivityRendererRendersTheKeysOwnTurn(t *testing.T) {
	t.Parallel()

	adapter := NewActivityRenderer(newTestRenderer(t))
	detail := ticket()

	prompt, _, err := adapter.Render(
		work.StageKey{Ticket: detail.Number, RunID: "r", Stage: work.StageReview, Turn: 7},
		detail, everyDocument(), work.AgentPromptContext{},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "review turn 7"
	if !strings.Contains(prompt, want) {
		t.Errorf("the rendered review prompt does not say %q: the key's turn did not reach it", want)
	}
	if strings.Contains(prompt, "turn 0") {
		t.Error("the rendered review prompt says turn 0: the key's turn was dropped")
	}
}

func TestActivityRendererCarriesAuthoritativeAgentPromptContext(t *testing.T) {
	t.Parallel()
	adapter := NewActivityRenderer(newTestRenderer(t))
	detail := ticket()
	prior := everyDocument()

	reviewer, _, err := adapter.Render(
		work.StageKey{Ticket: detail.Number, RunID: "r", Stage: work.StageReview, Turn: 1},
		detail, prior, work.AgentPromptContext{CandidateHeadSHA: "H1"},
	)
	if err != nil {
		t.Fatalf("Render(review): %v", err)
	}
	if !strings.Contains(reviewer, "reviewing exactly candidate commit H1") {
		t.Fatalf("review prompt does not name its exact H1 candidate:\n%s", reviewer)
	}

	implementer, _, err := adapter.Render(
		work.StageKey{Ticket: detail.Number, RunID: "r", Stage: work.StageImplement, Turn: 2},
		detail, prior, work.AgentPromptContext{CandidateHeadSHA: "H1", CIFailures: []work.CheckFailure{{Name: "test", Fingerprint: "abc", Evidence: "expected true to be false"}}},
	)
	if err != nil {
		t.Fatalf("Render(implement): %v", err)
	}
	for _, want := range []string{"CI failures for the exact checked candidate H1", "check=test", "fingerprint=abc", "expected true to be false"} {
		if !strings.Contains(implementer, want) {
			t.Errorf("implement prompt does not contain %q", want)
		}
	}

	blockingReview, _, err := adapter.Render(
		work.StageKey{Ticket: detail.Number, RunID: "r", Stage: work.StageImplement, Turn: 2},
		detail, prior, work.AgentPromptContext{CandidateHeadSHA: "H1", ReviewFindings: []work.Finding{{ID: "finding_1", Blocking: true, Summary: "repair the boundary"}}},
	)
	if err != nil {
		t.Fatalf("Render(blocking-review implement): %v", err)
	}
	for _, want := range []string{"Blocking review feedback for candidate H1", "id=finding_1", "repair the boundary"} {
		if !strings.Contains(blockingReview, want) {
			t.Errorf("blocking review prompt does not contain %q", want)
		}
	}
}
