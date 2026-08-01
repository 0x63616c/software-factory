package main

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/prompts"
	"github.com/0x63616c/software-factory/internal/work"
)

func sampleTicket() work.TicketDetail {
	return work.TicketDetail{
		Ticket: work.Ticket{
			Number: 340,
			Title:  "main.go and worker wiring",
			Body:   "Wire concrete clients into activities, register workflows and activities, run the worker.",
		},
	}
}

func TestNewPromptRendererDrawsOnRealEntropy(t *testing.T) {
	t.Parallel()

	renderer, err := newPromptRenderer()
	if err != nil {
		t.Fatalf("newPromptRenderer: %v", err)
	}

	in := prompts.Input{Stage: work.StagePlan, Ticket: sampleTicket()}
	first, err := renderer.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := renderer.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The composition root's one job here is to hand over a real source of
	// randomness. A fixed or zeroed reader would still construct, still render,
	// and still pass every test in internal/prompts — and would give every run
	// in production the same, guessable fence.
	if first == second {
		t.Fatal("two renders produced identical prompts; the renderer was built on a predictable entropy source")
	}
}

func TestNewPromptRendererRendersEveryStageOfThePipeline(t *testing.T) {
	t.Parallel()

	renderer, err := newPromptRenderer()
	if err != nil {
		t.Fatalf("newPromptRenderer: %v", err)
	}

	// Every prior stage's output, so any stage can be rendered. What this
	// asserts is that the wiring here reaches the merged prompt set at all —
	// the prompts' own contents are internal/prompts' business.
	prior := work.PriorTurns{
		Plan:            work.NewStageOutput(work.StagePlan, work.DocumentOutput{Document: "the plan"}),
		LatestImplement: work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "the implementation report"}),
		LatestReview:    work.NewStageOutput(work.StageReview, work.ReviewOutput{Document: "the review"}),
	}

	for _, stage := range work.Pipeline() {
		rendered, err := renderer.Render(prompts.Input{Stage: stage, Ticket: sampleTicket(), Prior: prior})
		if err != nil {
			t.Fatalf("Render(%s): %v", stage, err)
		}
		if !strings.Contains(rendered, "## Stage: "+string(stage)) {
			t.Errorf("the %s prompt does not carry its own stage instructions", stage)
		}
	}
}
