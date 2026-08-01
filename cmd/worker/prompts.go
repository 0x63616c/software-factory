package main

import (
	"crypto/rand"
	"fmt"

	"github.com/0x63616c/software-factory/internal/prompts"
)

// newPromptRenderer builds the renderer every stage's prompt is assembled by.
//
// This is the only place in the service that names a source of randomness. The
// renderer takes one because its fence nonce is drawn from it: the tags that
// wrap an issue's own words are unforgeable only while the nonce cannot be
// guessed, and a test that could not choose the nonce could not prove it gets
// stripped out of attacker-controlled text. crypto/rand is what makes that
// true in production, and .golangci.yml denies crypto/rand everywhere except
// here, so this file is the whole of the entropy surface.
//
// It also belongs to cmd/ for a second reason: internal/prompts is on the
// workflows-are-deterministic deny list. A workflow that rendered a prompt
// would mint a fresh nonce on every replay and corrupt its own history. The
// renderer is constructed here and used from an activity, and there is no
// third shape.
//
// codexauth.go's holderID is the only other reader of crypto/rand in this
// service, for the same "construct entropy only at the composition root"
// reason; between the two, cmd/ is the whole of the entropy surface.
func newPromptRenderer() (*prompts.Renderer, error) {
	renderer, err := prompts.New(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("building the prompt renderer: %w", err)
	}
	return renderer, nil
}
