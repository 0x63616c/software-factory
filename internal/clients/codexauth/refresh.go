package codexauth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/0x63616c/software-factory/internal/work"
)

// RefreshOutcome classifies what happened to a refresh token that was handed to
// the provider.
//
// It exists because "the HTTP call failed" and "the token was not spent" are
// different statements, and for a single-use rotating credential the difference
// decides between a retry and a dead credential.
//
// The zero value is RefreshUnknown, deliberately. Every `return Refreshed{}, 0,
// err` — a partially written request, a fake, a value decoded from anything —
// then says "this token may already be spent", which is the answer that costs a
// wasted halt. The answer that licenses presenting a token a second time has to
// be given on purpose.
type RefreshOutcome int

const (
	// RefreshUnknown means the request reached the wire and no usable response
	// came back. The token may already be spent, so it must never be presented
	// again automatically.
	RefreshUnknown RefreshOutcome = iota
	// RefreshNotSent means no part of the request reached the wire: the token
	// was definitely not presented and may safely be presented again.
	RefreshNotSent
	// RefreshRejected means the provider refused the token: expired, revoked,
	// or otherwise finished.
	RefreshRejected
	// RefreshReused means the provider refused the token because it had
	// already been presented. That is not our credential going stale — it is
	// INV-1 violated by something outside this process, and it has a different
	// recovery: find the other holder before re-seeding, or the replacement
	// gets eaten too.
	RefreshReused
	// RefreshRotated means a new pair was issued.
	RefreshRotated
)

// String names the outcome for logs and metric labels.
func (o RefreshOutcome) String() string {
	switch o {
	case RefreshUnknown:
		return "unknown"
	case RefreshNotSent:
		return "not_sent"
	case RefreshRejected:
		return "rejected"
	case RefreshReused:
		return "reused"
	case RefreshRotated:
		return "rotated"
	default:
		return fmt.Sprintf("RefreshOutcome(%d)", int(o))
	}
}

// Refreshed is one token-endpoint response.
//
// Its fields are Credentials, and it redacts itself on top of that, because a
// struct of three token strings is one %+v away from writing both halves of the
// credential to the cluster's log pipeline — where they would outlive the
// tokens themselves.
type Refreshed struct {
	AccessToken  work.Credential
	RefreshToken work.Credential
	IDToken      work.Credential
}

// String redacts the whole struct, so a wrapped error carrying one cannot leak.
func (r Refreshed) String() string { return "[redacted codex token pair]" }

// Refreshed must satisfy slog.LogValuer — see the assertion on credentialFile
// in authfile.go for why this is enforced at package scope rather than in a
// test.
var _ slog.LogValuer = Refreshed{}

// LogValue redacts the whole struct in structured logs.
func (r Refreshed) LogValue() slog.Value { return slog.StringValue(r.String()) }

// TokenRefresher exchanges a refresh token for a new pair.
//
// It is a seam because the exchange is the one network call in this package and
// no unit test may make it, and because calling it is destructive: the token
// handed in is spent whether or not the response is received. The outcome is
// returned separately from the error precisely so a caller can tell a token
// that was never presented from one whose fate is unknown — an implementation
// that decides that by matching error strings has not implemented this.
type TokenRefresher interface {
	Refresh(ctx context.Context, refreshToken work.Credential) (Refreshed, RefreshOutcome, error)
}

// DeathReason says why a credential is unusable until a human intervenes.
//
// It is an enum rather than a string because the set is closed and the metric,
// the log and the sentinel list must agree on it; a free-form label would let
// the dashboard and the code drift apart with nothing to notice.
type DeathReason string

const (
	// DeathUnseeded means no usable credential is stored.
	DeathUnseeded DeathReason = "unseeded"
	// DeathRejected means the provider refused the refresh token.
	DeathRejected DeathReason = "rejected"
	// DeathOutcomeUnknown means a presentation's result was lost.
	DeathOutcomeUnknown DeathReason = "outcome_unknown"
	// DeathSingleWriterViolated means a foreign writer rotated the credential.
	DeathSingleWriterViolated DeathReason = "single_writer_violated"
	// DeathCredentialLost means a rotation could not be stored.
	DeathCredentialLost DeathReason = "credential_lost"
	// DeathNoAccessToken means a refresh rotated the credential but returned
	// no access token to use. The chain is intact and stored; there is simply
	// nothing to hand out.
	DeathNoAccessToken DeathReason = "no_access_token"
)

// Metrics records credential outcomes.
//
// The credential has no automatic recovery path, so "it is dead" must be an
// alertable number rather than something a human infers from N identical
// workflow failures at 3am.
//
// Takeover is its own method rather than a fifth RefreshOutcome because a
// takeover is not something the provider told us; folding it into that enum
// would drag a value with no meaning at the token endpoint into the switch that
// classifies the endpoint's answer.
type Metrics interface {
	// RefreshOutcome records what the provider did with a presented token.
	RefreshOutcome(outcome RefreshOutcome)
	// Takeover records that a holder died mid-refresh and its lease was taken
	// over. Recurring values mean pod restarts, not a credential problem.
	Takeover()
	// CredentialDead records that the credential needs a human.
	CredentialDead(reason DeathReason)
}

// noMetrics is the default recorder: the seam ships before the telemetry
// package does, so call sites are written once and correctly rather than added
// later to code that already works without them.
type noMetrics struct{}

func (noMetrics) RefreshOutcome(RefreshOutcome) {}
func (noMetrics) Takeover()                     {}
func (noMetrics) CredentialDead(DeathReason)    {}
