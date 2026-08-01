// Package prompts renders one pipeline stage's prompt: what the stage is told
// to do, the issue it is told to do it for, and the documents earlier stages
// handed forward.
//
// It is the whole of what this system says to a model. The stage runner takes a
// rendered string and never assembles one, so issue text — which anyone who can
// file or comment on an issue chooses — reaches a prompt through exactly one
// function, wrapped in exactly one fence. That is why the templates are
// markdown files here rather than strings spread across the workflows that use
// them: changing what a plan should contain is an edit to prose, reviewed as
// prose, with no struct, schema or golden file to regenerate.
//
// The envelope each stage answers in lives here too, for the same reason: one
// JSON Schema and one Go decoder per stage (output.go), so the writer of a
// stage's schema and the reader of its result cannot drift apart.
package prompts

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/0x63616c/software-factory/internal/work"
)

// Renderer renders stage prompts.
//
// It holds the entropy source and nothing else: no ticket, no run, no state
// between calls. One renderer serves every run the worker has in flight, and
// two concurrent Renders cannot see each other's nonce.
type Renderer struct {
	entropy io.Reader
}

// New builds a renderer on a source of randomness.
//
// The source is injected rather than reached for, because it is an external
// edge like any other: a test that could not choose the nonce could not plant
// it in an issue body and prove it gets stripped. cmd/ passes crypto/rand.
func New(entropy io.Reader) (*Renderer, error) {
	if entropy == nil {
		return nil, fmt.Errorf("a prompt renderer needs an entropy source: the fence nonce is drawn from it")
	}
	return &Renderer{entropy: entropy}, nil
}

// Input is everything one stage's prompt interpolates.
type Input struct {
	// Stage is the stage being rendered for.
	Stage work.Stage

	// Turn is which of that stage's own turns this is, 1-indexed — the same
	// number work.StageKey.Turn carries, which is where callers get it.
	//
	// Only review renders it today.
	// Zero is not special-cased, because every real call site has a turn; a
	// zero would render as "turn 0", which reads as obviously wrong rather
	// than as plausible.
	Turn int

	// Ticket is the issue and its thread, as the issue's authors wrote them.
	// Every field of it is attacker-controlled text and is rendered inside the
	// fence.
	Ticket work.TicketDetail

	// Prior is exactly the plan, latest implement turn and latest review
	// turn — see work.PriorTurns' own doc comment for why this is a
	// purpose-built struct rather than the run's whole turn history. A stage
	// that has not run yet reads as the zero StageOutput on the field for it.
	Prior work.PriorTurns

	// PromptContext is authoritative workflow/GitHub feedback for this agent
	// invocation. It must not be smuggled into PriorTurns, which is strictly
	// agent-produced history.
	PromptContext work.AgentPromptContext
}

// Render assembles the stage's whole prompt.
//
// The result is one string, written into the Run Worker as a file. It is never an
// argument to anything: the argv-only guarantee in AGENTS.md exists because
// this string contains text an issue author chose.
//
// It fails rather than degrade. A prompt with a placeholder still in it, a
// missing input document, or a nonce that reached the model outside the fence
// are each a prompt that would produce confidently wrong work, and a stage that
// does not start is cheaper than a stage that starts wrong.
func (r *Renderer) Render(in Input) (string, error) {
	template, err := in.template()
	if err != nil {
		return "", err
	}
	values, documents, err := in.staticValues()
	if err != nil {
		return "", err
	}

	nonce, err := mintNonce(r.entropy)
	if err != nil {
		return "", fmt.Errorf("rendering the %s prompt for ticket #%d: %w", in.Stage, in.Ticket.Number, err)
	}
	values["fence_nonce"] = nonce
	// Each value is stripped on its own, which is sound only because no two
	// placeholders in a template are separated by nothing but hex digits: a
	// nonce split across a title and a body is clean in each half and cannot
	// rejoin across the prose between them. checkFence below catches it either
	// way — a reassembled nonce is a refused render, not a forged fence — but
	// refusing every ticket that carries such a string is a denial of service
	// an attacker picks for free. The property lives in the markdown rather
	// than in this loop, so TestNoTwoPlaceholdersCanJoinIntoANonce holds the
	// templates to it.
	for name, value := range values {
		if name == "fence_nonce" {
			continue
		}
		values[name] = strip(value, nonce)
	}

	rendered, err := interpolate(template, values)
	if err != nil {
		return "", fmt.Errorf("rendering the %s prompt for ticket #%d: %w", in.Stage, in.Ticket.Number, err)
	}
	if err := checkFence(rendered, nonce, documents); err != nil {
		return "", fmt.Errorf("rendering the %s prompt for ticket #%d: %w", in.Stage, in.Ticket.Number, err)
	}
	return rendered, nil
}

// template is the base instructions followed by this stage's own.
//
// The order is the point. The issue fence closes at the end of the base, so
// the stage's real task is stated after the issue's text rather than before it.
//
// A stage that reads an earlier stage's document does have untrusted text last
// — a handoff has to come after the instructions that say what to do with it —
// which is why those documents are fenced too, and why the base says what a
// fenced region is worth before any of them appear.
func (in Input) template() (string, error) {
	stageFile, err := stageTemplate(in.Stage)
	if err != nil {
		return "", err
	}
	base, err := templates.ReadFile(baseTemplate)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", baseTemplate, err)
	}
	stage, err := templates.ReadFile(stageFile)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", stageFile, err)
	}
	return string(base) + "\n" + string(stage), nil
}

// staticValues is every interpolated value except the nonce, with the input
// validated on the way: a value that cannot be rendered honestly is an error
// here rather than a gap in the prompt. The second return is how many of
// those values are prior-stage documents — checkFence's count of the
// document fences the render must open and close, one per document.
func (in Input) staticValues() (map[string]string, int, error) {
	if in.Ticket.Number <= 0 {
		return nil, 0, fmt.Errorf("ticket number %d is not an issue number: the prompt names the issue it is for", in.Ticket.Number)
	}
	if strings.TrimSpace(in.Ticket.Title) == "" {
		return nil, 0, fmt.Errorf("ticket #%d has no title: every GitHub issue has one, so this detail was not fetched", in.Ticket.Number)
	}

	values := map[string]string{
		"ticket_number": strconv.Itoa(in.Ticket.Number),
		"ticket_title":  in.Ticket.Title,
		"ticket_body":   body(in.Ticket),
	}

	stageInput, err := buildStageInput(in.Stage, in.Turn, in.Prior, in.PromptContext)
	if err != nil {
		return nil, 0, err
	}
	stageValues, err := stageInput.templateValues()
	if err != nil {
		return nil, 0, err
	}
	for name, value := range stageValues {
		values[name] = value
	}
	// Counted separately below: only stageValues are fenced documents.
	for name, value := range stageInput.scalarValues() {
		values[name] = value
	}
	return values, len(stageValues), nil
}

// body is the issue's description, or a statement that it has none.
//
// An empty region in a prompt reads to a model as something it failed to
// receive. Saying the issue was filed without a description is the difference
// between a planner treating that as a fact about the ticket and a planner
// treating it as its own missing context.
func body(detail work.TicketDetail) string {
	if strings.TrimSpace(detail.Body) == "" {
		return "(This issue was filed with no description.)"
	}
	text, cut := truncate(detail.Body, maxUntrustedBytes)
	if !cut {
		return text
	}
	return text + truncationNotice(len(detail.Body), len(text))
}

// maxUntrustedBytes is the most issue text one prompt carries, applied once to
// the description and once to the whole comment thread.
//
// GitHub takes an issue body of 65536 characters and the seam carries up to 40
// comments, so "the issue as its authors wrote it" is megabytes in the worst
// case. Unbounded, that is an unbounded token spend on every stage of the run,
// and a prompt whose own instructions fall out of the context window behind
// text the issue's author chose the length of. The number is a bound on the
// pathological case rather than a budget an ordinary ticket is spent against:
// twenty thousand bytes is several times the longest issue in this repository.
const maxUntrustedBytes = 20_000

// truncate cuts text to at most limit bytes, on a rune boundary, and reports
// whether it cut anything.
//
// The boundary matters: half a rune is invalid UTF-8 in the prompt, and in a
// body that is mostly non-ASCII it lands at exactly the point a reader is
// looking.
func truncate(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// truncationNotice says what was cut, in the prompt, where the model reads it.
//
// Cutting text out silently is the failure the trimmed-thread notice already
// exists to avoid: a stage that does not know it was handed part of the issue
// plans as though it had all of it, and a human reading the plan cannot tell
// which happened.
func truncationNotice(was, kept int) string {
	return fmt.Sprintf("\n\n(This text was truncated here: it is %d bytes and this prompt carries the first %d.)", was, kept)
}
