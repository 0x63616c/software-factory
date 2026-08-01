package prompts

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestInterpolateFillsPlaceholdersInPlace(t *testing.T) {
	t.Parallel()

	got, err := interpolate("issue #{{n}}: {{title}}\n{{n}} again", map[string]string{"n": "329", "title": "prompts"})
	if err != nil {
		t.Fatalf("interpolate: %v", err)
	}
	if want := "issue #329: prompts\n329 again"; got != want {
		t.Errorf("interpolate = %q, want %q", got, want)
	}
}

func TestInterpolateIsStrictInBothDirections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		template string
		values   map[string]string
	}{
		{
			// The failure this prevents is the literal text `{{ticket_body}}`
			// reaching a model as if it were the issue.
			name:     "the template asks for something the stage has no value for",
			template: "read {{ticket_body}}",
			values:   map[string]string{},
		},
		{
			// And this one is the same edit from the other side: a variable
			// renamed in the markdown, its value now silently dropped.
			name:     "the stage has a value the template never asks for",
			template: "read the issue",
			values:   map[string]string{"ticket_body": "b"},
		},
		{
			name:     "a placeholder that is never closed",
			template: "read {{ticket_body",
			values:   map[string]string{"ticket_body": "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := interpolate(tc.template, tc.values); err == nil {
				t.Fatal("interpolate accepted a template and values that do not match")
			}
		})
	}
}

func TestInterpolateNeverRescansWhatItSubstituted(t *testing.T) {
	t.Parallel()

	// The value here is issue text. If substitution were recursive, an issue
	// body could name a variable and choose what goes into its own prompt.
	got, err := interpolate("{{body}}", map[string]string{"body": "{{fence_nonce}}"})
	if err != nil {
		t.Fatalf("interpolate: %v", err)
	}
	if want := "{{fence_nonce}}"; got != want {
		t.Errorf("interpolate = %q, want %q", got, want)
	}
}

func TestEveryStageHasAPromptAndTheyAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]work.Stage{}
	for _, stage := range work.Pipeline() {
		file, err := stageTemplate(stage)
		if err != nil {
			t.Fatalf("stageTemplate(%s): %v", stage, err)
		}
		if other, ok := seen[file]; ok {
			t.Errorf("stages %s and %s share the prompt %s", stage, other, file)
		}
		seen[file] = stage

		body, err := templates.ReadFile(file)
		if err != nil {
			t.Fatalf("the prompt for %s is not embedded: %v", stage, err)
		}
		if want := "## Stage: " + string(stage); !strings.Contains(string(body), want) {
			t.Errorf("%s does not open with %q", file, want)
		}
	}
}

func TestImplementPromptRequiresTheCanonicalPullRequestDescription(t *testing.T) {
	t.Parallel()

	body, err := templates.ReadFile("templates/implement.md")
	if err != nil {
		t.Fatalf("reading implement prompt: %v", err)
	}

	for _, requirement := range []string{
		"bun run check",
		"actual output in the implementation report",
		".github/pull_request_template.md",
		"complete every applicable section",
		"Fixes #{{ticket_number}}",
		"delete the Screenshot section when there is no UI change",
		"Never manufacture command output or visual evidence",
	} {
		if !strings.Contains(string(body), requirement) {
			t.Errorf("implement prompt does not require %q", requirement)
		}
	}
}

func TestImplementPromptCommitsButLeavesPublicationToTheWorkflow(t *testing.T) {
	t.Parallel()

	body, err := templates.ReadFile("templates/implement.md")
	if err != nil {
		t.Fatalf("reading implement prompt: %v", err)
	}
	prompt := string(body)
	for _, requirement := range []string{
		"Commit the finished change before you return",
		"do not push it",
		"workflow publishes",
		"fresh repository credential",
	} {
		if !strings.Contains(prompt, requirement) {
			t.Errorf("implement prompt does not require %q", requirement)
		}
	}
	if strings.Contains(prompt, "git push") {
		t.Fatal("implement prompt still instructs the credential-free model tool container to run git push")
	}
}

func TestReviewPromptRequiresDocumentationDriftFindings(t *testing.T) {
	t.Parallel()

	body, err := templates.ReadFile("templates/review.md")
	if err != nil {
		t.Fatalf("reading review prompt: %v", err)
	}

	for _, requirement := range []string{
		"AGENTS.md",
		"CODEBASE_OVERVIEW.md",
		"docs/**",
		".claude/skills/**",
		"normal stable `id`, `blocking`, and `summary` fields",
		"operational documentation stale,",
		"or no longer appropriate",
	} {
		if !strings.Contains(string(body), requirement) {
			t.Errorf("review prompt does not require %q", requirement)
		}
	}
}

func TestBaseFencesTheIssueTextWithTheRunsNonce(t *testing.T) {
	t.Parallel()

	base, err := templates.ReadFile(baseTemplate)
	if err != nil {
		t.Fatalf("reading %s: %v", baseTemplate, err)
	}

	// Two tags, one nonce placeholder each. A base that opened the fence and
	// forgot to close it would leave every issue's text un-fenced.
	if got := strings.Count(string(base), "{{fence_nonce}}"); got != 2 {
		t.Errorf("%s carries %d fence nonce placeholders, want 2", baseTemplate, got)
	}
	// The stage's own instructions come after the fence closes, so untrusted
	// text is never the last thing the model reads.
	if strings.Index(string(base), "</"+fenceTag) > strings.Index(string(base), "Your instructions for this stage follow") {
		t.Error("the fence closes after the base hands over to the stage prompt")
	}
}

func TestEveryStageHasASchemaAndTheyAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]work.Stage{}
	for _, stage := range work.Pipeline() {
		file, err := stageSchema(stage)
		if err != nil {
			t.Fatalf("stageSchema(%s): %v", stage, err)
		}
		if other, ok := seen[file]; ok {
			t.Errorf("stages %s and %s share the schema %s", stage, other, file)
		}
		seen[file] = stage

		if _, err := templates.ReadFile(file); err != nil {
			t.Fatalf("the schema for %s is not embedded: %v", stage, err)
		}
	}
}

// TestBuildStageInputProducesTheDeclaredVariableNames is the regression guard
// for the typed input seam: it proves buildStageInput's per-stage structs
// emit exactly the placeholder names each stage's own template expects
// (plan, previous_implement_report, review_findings for implement;
// implementation_report, previous_review_findings for review), given a full
// two-turn history so every "declare absence" fallback has real content to
// stand in for instead.
func TestBuildStageInputProducesTheDeclaredVariableNames(t *testing.T) {
	t.Parallel()

	prior := work.PriorTurns{
		Plan:            work.NewStageOutput(work.StagePlan, work.DocumentOutput{Document: "the plan"}),
		LatestImplement: work.NewStageOutput(work.StageImplement, work.ImplementOutput{Report: "turn one's report"}),
		LatestReview: work.NewStageOutput(work.StageReview, work.ReviewOutput{
			Document: "the review", Findings: []work.Finding{{ID: "f1", Blocking: true, Summary: "s"}},
		}),
	}

	cases := []struct {
		stage work.Stage
		want  []string
	}{
		{stage: work.StagePlan, want: nil},
		{stage: work.StageImplement, want: []string{"plan", "previous_implement_report", "review_findings", "implementation_feedback"}},
		{stage: work.StageReview, want: []string{"implementation_report", "previous_review_findings", "review_ledger"}},
	}

	for _, tc := range cases {
		t.Run(string(tc.stage), func(t *testing.T) {
			t.Parallel()

			in, err := buildStageInput(tc.stage, 1, prior, work.AgentPromptContext{})
			if err != nil {
				t.Fatalf("buildStageInput(%s): %v", tc.stage, err)
			}
			values, err := in.templateValues()
			if err != nil {
				t.Fatalf("templateValues(%s): %v", tc.stage, err)
			}
			if len(values) != len(tc.want) {
				t.Fatalf("%s produced %v, want variables %v", tc.stage, values, tc.want)
			}
			for _, name := range tc.want {
				if _, ok := values[name]; !ok {
					t.Errorf("%s did not produce {{%s}}", tc.stage, name)
				}
			}
		})
	}
}

func TestNoTwoPlaceholdersCanJoinIntoANonce(t *testing.T) {
	t.Parallel()

	// The invariant per-value stripping rests on, and it lives in the markdown
	// rather than in the code.
	//
	// strip runs on each value separately, so it cannot see a nonce split
	// across two of them — a title ending `...a7a7` and a body opening
	// `a7a7...` are each clean, and each survives untouched. What stops them
	// reassembling is the template between them: every pair of placeholders is
	// separated by prose, a newline or a tag, none of which is a hex digit, so
	// no contiguous hex run can span the join.
	//
	// checkFence is the backstop and it does hold: with two placeholders made
	// adjacent, a nonce or a whole forged tag split across a title and a body
	// reassembles and the render is *refused* — verified by probe, both halves.
	// So breaking this is not a silent forgery. It is every ticket carrying
	// that text becoming unrenderable, which an attacker chooses freely, and
	// the loss of the property that makes per-value stripping sound at all.
	// Both are worth one markdown edit away from nothing noticing. Hence this.
	files := []string{baseTemplate}
	for _, stage := range work.Pipeline() {
		file, err := stageTemplate(stage)
		if err != nil {
			t.Fatalf("stageTemplate(%s): %v", stage, err)
		}
		files = append(files, file)
	}

	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			body, err := templates.ReadFile(file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			for _, gap := range gapsBetweenPlaceholders(string(body)) {
				if strings.Trim(gap, "0123456789abcdefABCDEF") == "" {
					t.Errorf("two placeholders in %s are separated by %q, which is nothing but hex: a nonce split across the two values would reassemble between them and checkFence would refuse every such ticket", file, gap)
				}
			}
		})
	}
}

// gapsBetweenPlaceholders returns the template text lying between each
// consecutive pair of `{{name}}` placeholders.
func gapsBetweenPlaceholders(template string) []string {
	var gaps []string

	rest := template
	first := true
	for {
		before, after, found := strings.Cut(rest, "{{")
		if !found {
			return gaps
		}
		if !first {
			gaps = append(gaps, before)
		}
		_, remainder, closed := strings.Cut(after, "}}")
		if !closed {
			return gaps
		}
		first = false
		rest = remainder
	}
}
