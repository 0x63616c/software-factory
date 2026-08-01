package work_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// The literal strings below are asserted rather than derived. These paths and
// this ID are a wire format: the workflow ID is the claim on a Ticket, and the
// Run Worker paths are the contract with the Run Worker image. Recomputing them the
// way the code does would assert nothing, so a change that alters them has to
// change this test too, deliberately.

func TestTargetDispatcherWorkflowIDIsStable(t *testing.T) {
	t.Parallel()

	if got, want := work.TargetDispatcherWorkflowID, "software-factory-target-dispatcher"; got != want {
		t.Errorf("TargetDispatcherWorkflowID = %q, want %q; changing it orphans the active dispatcher", got, want)
	}
}

func TestPipelineOrdersTheStages(t *testing.T) {
	t.Parallel()

	want := []work.Stage{work.StagePlan, work.StageImplement, work.StageReview}
	got := work.Pipeline()
	if len(got) != len(want) {
		t.Fatalf("Pipeline() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Pipeline() = %v, want %v", got, want)
		}
	}
}

func TestPipelineCannotBeReorderedByACaller(t *testing.T) {
	t.Parallel()

	work.Pipeline()[0] = work.StageReview
	if got := work.Pipeline()[0]; got != work.StagePlan {
		t.Errorf("Pipeline()[0] = %v after a caller wrote to a previous result, want %v", got, work.StagePlan)
	}
}

func TestUsageAddsAcrossStages(t *testing.T) {
	t.Parallel()

	total := work.Usage{InputTokens: 10, CachedInputTokens: 2, OutputTokens: 3, ReasoningTokens: 4}.
		Add(work.Usage{InputTokens: 1, CachedInputTokens: 1, OutputTokens: 1, ReasoningTokens: 1})

	want := work.Usage{InputTokens: 11, CachedInputTokens: 3, OutputTokens: 4, ReasoningTokens: 5}
	if total != want {
		t.Errorf("Add = %+v, want %+v", total, want)
	}
}

const secret = "sk-not-a-real-token"

func TestCredentialRevealsOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	if got := work.NewCredential(secret).Reveal(); got != secret {
		t.Errorf("Reveal() = %q, want the wrapped value", got)
	}
}

func TestCredentialStaysOutOfFormattedOutput(t *testing.T) {
	t.Parallel()

	c := work.NewCredential(secret)

	// %v on a struct containing one is the realistic leak: nobody formats the
	// credential deliberately.
	wrapper := struct{ Token work.Credential }{Token: c}
	rendered := []string{
		c.String(),
		fmt.Sprintf("%v", c.LogValue()),
		fmt.Sprintf("%v", wrapper),
		fmt.Sprintf("%+v", wrapper),
	}
	for _, r := range rendered {
		if strings.Contains(r, secret) {
			t.Errorf("rendered credential contains the secret: %q", r)
		}
	}
}

// A LogValue method that slog never calls is worse than no method at all: the
// file reads as though the credential is handled, so an audit ticks it off.
// Asserting the INTERFACE is what catches that — a test that calls LogValue()
// directly passes just as happily when nothing else ever does.
//
// The load-bearing assertion now lives at package scope in work.go, so the
// regression fails `go build` rather than only `go test`. It is restated here
// because a bare `var _` with nothing beside it reads as dead code to someone
// tidying up, and deleting it silently removes the guard.
func TestCredentialSatisfiesTheInterfaceSlogActuallyUses(t *testing.T) {
	t.Parallel()

	var _ slog.LogValuer = work.Credential{}
}

// The assertion above is compile-time, so this one proves slog reaches the
// method at run time. Resolve is how slog itself unwraps an attribute: it
// invokes LogValue on a LogValuer and leaves anything else as KindAny. A
// Credential that stays KindAny is one slog is treating as an opaque struct.
//
// This is the test that fails on the bug. The handler test below does not —
// see its comment.
func TestSlogResolvesACredentialThroughLogValue(t *testing.T) {
	t.Parallel()

	resolved := slog.AnyValue(work.NewCredential(secret)).Resolve()

	if resolved.Kind() == slog.KindAny {
		t.Errorf("slog left the credential unresolved (kind %s); LogValue is never called, so redaction rests entirely on fmt falling through to String()", resolved.Kind())
	}
	if strings.Contains(resolved.String(), secret) {
		t.Errorf("the resolved value contains the secret: %q", resolved.String())
	}
}

// The property that actually matters, through a real handler rather than a
// method call — and named for what it is NOT, because the name is what a reader
// auditing "is the credential covered?" sees first, and the name wins at 2am.
//
// NOTE this test PASSES on the bug. With LogValue() returning any, slog falls
// through: to fmt, which finds String(), under a text handler; to
// encoding/json, which MarshalJSON refuses, under a JSON one. Either way no
// secret appears, so grepping handler output cannot tell fixed code from
// broken. It is kept because it guards the outcome rather than the mechanism —
// it is what fails if String() ever stops redacting — but it is emphatically
// NOT the regression test for this issue. Only the two above are.
func TestSlogNeverWritesACredentialsValueThoughThisIsNotTheGuard(t *testing.T) {
	t.Parallel()

	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := slog.New(newHandler(&buf))

			c := work.NewCredential(secret)
			log.Info("using a credential", "token", c)
			// Nested in a struct too: nobody logs the credential deliberately,
			// they log the thing that holds one.
			log.Info("using a credential", "wrapper", struct{ Token work.Credential }{Token: c})

			if strings.Contains(buf.String(), secret) {
				t.Errorf("slog wrote the secret: %q", buf.String())
			}
		})
	}
}

func TestCredentialRefusesToBeSerialised(t *testing.T) {
	t.Parallel()

	// Marshalling is how a credential would reach workflow history or a
	// Kubernetes object and outlive the run that fetched it. Failing loudly
	// beats silently writing "[redacted]" where a token was expected.
	if _, err := json.Marshal(work.NewCredential(secret)); err == nil {
		t.Error("json.Marshal accepted a Credential; a token could be persisted to workflow history")
	}
}

func TestCredentialFileRevealsItsBytesOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	doc := []byte(`{"tokens":{"access_token":"` + secret + `"}}`)

	if got := work.NewCredentialFile(doc).Reveal(); string(got) != string(doc) {
		t.Errorf("Reveal() = %q, want the wrapped document", got)
	}
}

func TestCredentialFileStaysOutOfFormattedOutput(t *testing.T) {
	t.Parallel()

	f := work.NewCredentialFile([]byte(`{"tokens":{"access_token":"` + secret + `"}}`))

	wrapper := struct{ File work.CredentialFile }{File: f}
	rendered := []string{
		f.String(),
		fmt.Sprintf("%v", f.LogValue()),
		fmt.Sprintf("%v", wrapper),
		fmt.Sprintf("%+v", wrapper),
	}
	for _, r := range rendered {
		if strings.Contains(r, secret) {
			t.Errorf("rendered credential file contains the secret: %q", r)
		}
	}
}

// A document is exactly the shape somebody logs whole, so its redaction has to
// be a property of the type rather than an accident of how a handler happens to
// render an unrecognised value. Asserting the interface is what makes it one.
//
// The load-bearing assertion now lives at package scope in work.go, so the
// regression fails `go build` rather than only `go test`. It is restated here
// for the reason the Credential one is: a bare `var _` with nothing beside it
// reads as dead code, and deleting it silently removes the guard.
//
// What follows it is the run-time half — that a real handler writes nothing of
// the document.
func TestCredentialFileRedactsItselfThroughTheInterfaceSlogActuallyUses(t *testing.T) {
	t.Parallel()

	var _ slog.LogValuer = work.CredentialFile{}

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("writing the Run Worker credential",
		"file", work.NewCredentialFile([]byte(`{"tokens":{"access_token":"`+secret+`"}}`)))

	if strings.Contains(buf.String(), secret) {
		t.Errorf("slog wrote the credential document: %q", buf.String())
	}
}

func TestCredentialFileRefusesToBeSerialised(t *testing.T) {
	t.Parallel()

	// A document is the shape somebody would return from an activity, and
	// Temporal would persist it to workflow history for the whole retention.
	if _, err := json.Marshal(work.NewCredentialFile([]byte(`{}`))); err == nil {
		t.Error("json.Marshal accepted a CredentialFile; a credential document could be persisted to workflow history")
	}
}

// Reveal hands out the backing array, so a caller that mutates what it is given
// would edit the file every later caller receives.
func TestCredentialFileCannotBeMutatedThroughWhatItHandsOut(t *testing.T) {
	t.Parallel()

	f := work.NewCredentialFile([]byte(`{"a":1}`))

	revealed := f.Reveal()
	revealed[0] = 'X'

	if got := string(f.Reveal()); got != `{"a":1}` {
		t.Errorf("mutating a revealed document changed the CredentialFile: %q", got)
	}
}

func TestPermanentSurvivesWrapping(t *testing.T) {
	t.Parallel()

	// The marker is only useful if it reaches the activity boundary through the
	// layers of context every error picks up on the way up.
	wrapped := fmt.Errorf("creating the Run Worker for ticket #312: %w", work.ErrPermanent)
	if !errors.Is(wrapped, work.ErrPermanent) {
		t.Error("a wrapped ErrPermanent no longer reports as permanent; the retry decision would silently flip to retryable")
	}
}

func TestPermanentIsDistinctFromOtherSentinels(t *testing.T) {
	t.Parallel()

	if errors.Is(work.ErrFileNotFound, work.ErrPermanent) {
		t.Error("a missing Run Worker file reports as permanent; absence is a signal a stage keys off, not a reason to stop retrying")
	}
}

func TestAnObservedVersionAppliesItselfToTheWrite(t *testing.T) {
	t.Parallel()

	got, err := work.ObservedVersion("41208").Precondition()
	if err != nil {
		t.Fatalf("Precondition() on an observed version: %v", err)
	}
	if got != "41208" {
		t.Errorf("Precondition() = %q, want the observed token", got)
	}
}

func TestAnEmptyTokenNeverBecomesAPrecondition(t *testing.T) {
	t.Parallel()

	// An empty resourceVersion is an unconditional overwrite to the Kubernetes
	// apiserver, so a store that read "" and passed it on would silently write
	// blind. It has to arrive as a refusal instead.
	if _, err := work.ObservedVersion("").Precondition(); !errors.Is(err, work.ErrNoPrecondition) {
		t.Errorf("Precondition() on an empty token = %v, want a refusal; a lease would be silently disarmed", err)
	}
}

func TestAForgottenVersionCannotYieldAUsablePrecondition(t *testing.T) {
	t.Parallel()

	// The whole point of the type: the natural implementation assigns what it
	// gets straight onto the write, so a dropped or unset version must not be
	// able to hand back the empty string that means "overwrite blindly".
	var forgotten work.SecretVersion
	if _, err := forgotten.Precondition(); !errors.Is(err, work.ErrNoPrecondition) {
		t.Errorf("Precondition() on the zero version = %v, want a refusal", err)
	}
}

func TestAForgottenVersionIsDistinguishableFromContention(t *testing.T) {
	t.Parallel()

	// A store refusing a caller's mistake and a store reporting someone else's
	// write are opposite instructions: one is a bug to fix, the other is a
	// conflict to handle.
	_, err := work.SecretVersion{}.Precondition()
	if errors.Is(err, work.ErrVersionConflict) {
		t.Error("a missing precondition reports as a version conflict; a caller would retry its own bug")
	}
}

func TestAnUnconditionalWriteMustBeAskedForByName(t *testing.T) {
	t.Parallel()

	got, err := work.Unconditional().Precondition()
	if err != nil {
		t.Fatalf("Precondition() on a deliberate blind write: %v", err)
	}
	if got != "" {
		t.Errorf("Precondition() = %q, want the empty precondition that constrains nothing", got)
	}
}
