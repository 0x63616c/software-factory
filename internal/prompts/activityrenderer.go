package prompts

import (
	"fmt"

	"github.com/0x63616c/software-factory/internal/work"
)

// ActivityRenderer adapts *Renderer to the agent prompt activity's
// consumer-side interface without importing that activity package.
//
// It exists as its own type rather than extra methods on *Renderer because
// The agent prompt activity's Render takes the stage, ticket and prior
// documents positionally and returns a schema alongside the prompt, while
// Renderer.Render takes one Input and returns the prompt alone — two
// signatures for the same operation, chosen for two different readers. This
// type is the seam between them, not a new one.
type ActivityRenderer struct {
	renderer *Renderer
}

// NewActivityRenderer wraps a Renderer for the agent prompt activities.
func NewActivityRenderer(renderer *Renderer) *ActivityRenderer {
	return &ActivityRenderer{renderer: renderer}
}

// Render renders a stage's prompt and returns it with that stage's own
// output schema, looked up by stageSchema.
func (a *ActivityRenderer) Render(
	key work.StageKey, detail work.TicketDetail, prior work.PriorTurns, promptContext work.AgentPromptContext,
) (prompt string, schema []byte, err error) {
	prompt, err = a.renderer.Render(Input{Stage: key.Stage, Turn: key.Turn, Ticket: detail, Prior: prior, PromptContext: promptContext})
	if err != nil {
		return "", nil, err
	}
	file, err := stageSchema(key.Stage)
	if err != nil {
		return "", nil, err
	}
	schema, err = templates.ReadFile(file)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return prompt, schema, nil
}

// Decode unwraps a stage's result envelope. It forwards to the package-level
// Decode, which is the whole implementation; this method exists only so the
// adapter satisfies the agent activity's prompt interface.
func (a *ActivityRenderer) Decode(stage work.Stage, result []byte) (work.StageOutput, error) {
	return Decode(stage, result)
}
