// Package work holds the domain vocabulary of autonomous ticket work: the
// ticket, the stages it passes through, the sandbox a stage runs in, and what a
// stage produces. Every seam in this service is expressed in these types, so no
// third-party worldview — Kubernetes, GitHub or codex — crosses a module edge.
package work

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrFileNotFound reports that a path does not exist inside a sandbox.
//
// It is not merely an error code: absence is the signal a stage keys off to
// decide whether it has already run, so implementations must distinguish "no
// such file" from "could not tell" and never collapse the two. Compare with
// errors.Is.
var ErrFileNotFound = errors.New("file not found in sandbox")

// ErrPermanent marks an error that a retry cannot fix, so a caller stops paying
// for attempts that were never going to work.
//
// It is one bit, and it is deliberately the only one. Retry semantics belong to
// Temporal's taxonomy, not a domain one — but a client cannot import the
// Temporal SDK without breaking the seal that keeps an SDK's worldview out of
// the rest of this service. So the single bit Temporal needs travels as domain
// vocabulary and is translated exactly once, in internal/activities, into a
// non-retryable ApplicationError. Anything unmarked is retryable, which is
// Temporal's default.
//
// That translation site is the reason not to grow this into a rival scheme. An
// error-kind enum, a Retryable() method or a second marker would be the
// parallel taxonomy this exists to avoid, and would have to be reconciled with
// Temporal's at the same boundary. Wrap with %w; compare with errors.Is.
var ErrPermanent = errors.New("permanent failure")

// ErrSecretNotFound reports that a stored secret does not exist.
//
// Absence is a signal, never a failure to read: the credential secret is seeded
// by a human out of band, so "it is not there" means somebody has a job to do,
// while "I could not tell" means try again shortly. An implementation that
// collapsed the two would turn a transient apiserver blip into a demand for a
// browser login. Compare with errors.Is.
var ErrSecretNotFound = errors.New("secret not found")

// ErrVersionConflict reports that a stored object changed between a read and
// the write derived from it, and that the write was therefore not applied.
//
// It says only that, deliberately. Whether a conflict is contention worth
// retrying or an invariant already violated depends entirely on what the caller
// had done by the time it fired — a lease loser retries, a rotation that has
// already spent its single-use refresh token cannot.
var ErrVersionConflict = errors.New("stored object changed since it was read")

// ErrNoPrecondition reports that a write named no precondition at all: the
// version handed to it was never set, or was dropped on the way.
//
// It is separate from ErrVersionConflict because the two are opposite
// instructions. A conflict is news about another writer and may be worth
// retrying; this is the caller's own bug, and retrying it changes nothing.
// Compare with errors.Is.
var ErrNoPrecondition = errors.New("write names no precondition")

// SecretVersion is the state a read of a stored object observed, and the
// precondition a write derived from that read applies to it.
//
// It is a struct rather than a string because the obvious spelling is unsafe.
// Kubernetes treats an empty resourceVersion on an update as an unconditional
// overwrite that never conflicts, so with a bare string a dropped return value
// or an unset field disarms a compare-and-swap silently, leaving code that
// reads exactly like a lease and enforces nothing. Here the empty string is
// reachable only through Unconditional, and the zero value has no way to
// produce one at all — see Precondition.
type SecretVersion struct {
	token         string
	unconditional bool
}

// ObservedVersion is the precondition "unchanged since this token was read".
// Implementations mint one from whatever their store calls a version; an empty
// token yields the zero value, because a store that cannot say what it read
// cannot constrain a write.
func ObservedVersion(token string) SecretVersion {
	return SecretVersion{token: token}
}

// Unconditional is the precondition "none": the write overwrites whatever is
// there. It exists so that overwriting blind is a thing a caller asks for
// rather than a thing a caller forgets.
func Unconditional() SecretVersion {
	return SecretVersion{unconditional: true}
}

// Precondition returns the store's own version string for a write to apply,
// and ErrNoPrecondition if this version names none.
//
// It is the only way out of the type, and it returns an error so that the
// refusal is mechanical rather than remembered. The natural implementation
// assigns whatever it is given straight onto the write it is about to make; if
// the zero value could answer that question at all it would answer "", which
// Kubernetes reads as an unconditional overwrite, and the compare-and-swap
// would be gone with nothing to see at the call site. Ignoring the error here
// fails errcheck, so the mistake stops at lint rather than at a spent refresh
// token.
//
// An empty string is therefore a deliberate blind write and nothing else: only
// Unconditional can produce one.
func (v SecretVersion) Precondition() (resourceVersion string, err error) {
	if v.token == "" && !v.unconditional {
		return "", ErrNoPrecondition
	}
	return v.token, nil
}

// Stage is one step of the pipeline.
type Stage string

// The stages of a run, in pipeline order.
//
// StageRevise and StagePropose existed here once and are gone, as constants
// and as words: revise's job folded into implement (a plan an implementer
// finds wrong is a plan it deviates from, in its own report, not a fourth
// stage's document), and propose's job — opening the pull request — is now
// workflow code acting on GitHub's own API rather than a model told to run
// `gh pr create`. See the pipeline-rewrite spec's "Locked decisions" for why.
const (
	// StagePlan turns a ticket into an implementation plan.
	StagePlan Stage = "plan"
	// StageImplement writes the code, pushes the branch, and may run more than
	// once: a turn that leaves CI red is followed by another implement turn in
	// the same window, resumed from its own prior codex conversation.
	StageImplement Stage = "implement"
	// StageReview adversarially critiques implement's work once CI is green,
	// from a fresh thread every turn. It may also run more than once: a turn
	// that raises a blocking finding is followed by a fresh implement window.
	StageReview Stage = "review"
)

// Pipeline is the order stages run in, and the single source of truth for that
// order. It returns a fresh slice per call so no caller can reorder another's.
//
// This is the order a run's stages are first reached in, not a fixed-length
// schedule: implement and review each loop, under the turn budgets and
// progress-detection rules internal/workflows' loop enforces. See "The turn
// schedule" in the pipeline-rewrite spec.
func Pipeline() []Stage {
	return []Stage{StagePlan, StageImplement, StageReview}
}

// Ticket is a GitHub issue eligible for machine work.
//
// Title and Body are attacker-controllable: anyone who can file an issue
// chooses them. They reach a model as prompt content and a sandbox as file
// content. They must never reach a shell, a command argument, a Kubernetes
// object or a filesystem path — which is why the types that touch those things
// take a ticket number rather than this struct.
type Ticket struct {
	// Number is the Ticket's own id and the identity of the whole run.
	Number int
	Title  string
	Body   string
}

// TicketDetail is a Ticket as one run's stages read it.
//
// It is a separate type from Ticket rather than more fields on it because the
// two are read at different prices and by different callers: listing eligible
// Tickets is a poll, and a run reads one Ticket once and carries it through
// every stage so all three plan against the same ask.
type TicketDetail struct {
	Ticket
}

// Model names the model and reasoning effort a stage runs at. Per-stage
// overrides exist so the adversarial reviewer can be given different blind
// spots from the planner without touching workflow code.
type Model struct {
	Name   string `json:"name"`
	Effort string `json:"effort"`
}

// Validate reports whether this model can be invoked.
//
// It checks that both halves are present and nothing else. Effort in
// particular is deliberately not checked against a list of known values:
// codex's own ReasoningEffort carries a Custom(String) arm for "a
// model-defined effort value that this client does not know yet" (verified
// against rust-v0.145.0), so an allowlist here would reject efforts codex
// accepts, and would go stale the first time a model gains one.
func (m Model) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("model name is required")
	}
	if m.Effort == "" {
		return fmt.Errorf("reasoning effort is required for model %q", m.Name)
	}
	return nil
}

// Usage is the token accounting for one stage, as reported by the model's own
// completion event. Tokens are the only cost this service spends, and they come
// out of the same subscription window as its owner's interactive sessions.
//
// Two of these four are nested inside the other two, which is the fact anyone
// summing them needs and nothing about the numbers themselves reveals. Both are
// carried as the provider reports them (verified against codex rust-v0.145.0)
// rather than pre-subtracted here, so this stays a faithful record of what was
// said and the arithmetic happens once, where it is used.
type Usage struct {
	// InputTokens is the whole input, INCLUDING CachedInputTokens.
	InputTokens int64

	// CachedInputTokens is the part of InputTokens served from the provider's
	// prompt cache, and priced differently from the rest of it.
	CachedInputTokens int64

	// OutputTokens is the whole output, INCLUDING ReasoningTokens.
	OutputTokens int64

	// ReasoningTokens is the part of OutputTokens spent on reasoning. It bills
	// at the output rate and is already counted there, so it is reported beside
	// the output rather than added to it.
	ReasoningTokens int64
}

// Add returns the sum of two usages, so a run can total its stages.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + other.InputTokens,
		CachedInputTokens: u.CachedInputTokens + other.CachedInputTokens,
		OutputTokens:      u.OutputTokens + other.OutputTokens,
		ReasoningTokens:   u.ReasoningTokens + other.ReasoningTokens,
	}
}

// Credential is a short-lived secret — a GitHub App installation token, or a
// model access token. It is deliberately not a string: the type is what stops
// the value reaching a log line or a durable store.
//
// It must never be returned from an activity. Temporal persists activity
// results to workflow history, so a token that crosses that boundary is written
// to the database and stays there for the namespace's whole retention. Fetch
// credentials inside the activity that uses them.
type Credential struct {
	value string
}

// NewCredential wraps a secret value.
func NewCredential(value string) Credential {
	return Credential{value: value}
}

// GitHubCredential is the installation token written into a sandbox, together
// with the login GitHub attributes its use to.
//
// The login travels with the token rather than being fetched beside it because
// it is a property OF the token — who this credential acts as — and because the
// gh CLI refuses to use a token whose account it cannot name. gh resolves that
// name by calling /user, which an installation token cannot answer, so it has
// to be told: with no login in its hosts.yml, every gh invocation fails during
// config migration with "couldn't get user name", before it runs the command it
// was given. Verified against gh 2.96.0, not inferred.
//
// It carries the same never-return-from-an-activity rule Credential does.
type GitHubCredential struct {
	// Token is the short-lived, repository-scoped installation token.
	Token Credential

	// Login is the App's bot identity — its slug with a "[bot]" suffix.
	Login string

	// AccountID is the stable GitHub account ID for Login.
	AccountID int64

	// ExpiresAt is safe rotation metadata; the token remains opaque.
	ExpiresAt time.Time
}

// String redacts the whole struct, so a stray %v cannot leak the token it
// wraps.
//
// Whole, not field by field: Credential redacts itself, so %v on this struct
// already printed "{[redacted] <login>}" rather than the secret. That is one
// formatting change away from being wrong, and the login is not worth the
// exception — nothing that logs this needs it, and TicketDetail's filtering
// already resolves the bot login by itself where it is genuinely needed.
func (c GitHubCredential) String() string {
	return "[redacted]"
}

// GitHubCredential must satisfy slog.LogValuer for the same reason Credential
// must, and it is enforced the same way — at package scope, so a drifted
// signature fails `go build`.
var _ slog.LogValuer = GitHubCredential{}

// LogValue redacts the credential in structured logs.
func (c GitHubCredential) LogValue() slog.Value {
	return slog.StringValue("[redacted]")
}

// Reveal returns the underlying secret. Call it only at the point the value is
// written to its destination.
func (c Credential) Reveal() string {
	return c.value
}

// String redacts the credential, so a stray %v cannot leak it.
func (c Credential) String() string {
	return "[redacted]"
}

// Credential must satisfy slog.LogValuer, and this is where that is enforced:
// at package scope, so a signature that drifts off the interface fails
// `go build` rather than only `go test`. LogValue below says why the
// distinction is not academic.
var _ slog.LogValuer = Credential{}

// LogValue redacts the credential in structured logs.
//
// It returns slog.Value, NOT any. slog only calls this method on a value that
// satisfies slog.LogValuer, and that interface requires exactly this signature
// — returning any means slog never calls it at all, and redaction falls back
// on whatever the handler does with an opaque struct. Nothing leaked while this
// method had the wrong signature, but what saved it differed by handler, and
// neither fallback was this type's doing (both measured):
//
//   - slog.TextHandler hands the value to fmt, which finds String().
//   - slog.JSONHandler hands it to encoding/json, which never reaches String()
//     — MarshalJSON below refuses, and the attribute renders as an !ERROR
//     string. That is the path this service takes: its logs are JSON for Loki.
//
// So the protection was an accident of two different lookup orders rather than
// a property of this type. The assertion above is what makes it a property.
// See also TestSlogResolvesACredentialThroughLogValue.
func (c Credential) LogValue() slog.Value {
	return slog.StringValue(c.String())
}

// MarshalJSON always fails, and that is the point. Redacting instead would let
// a credential be serialised into workflow history or a Kubernetes object as
// the literal text "[redacted]" — a confusing runtime failure far from its
// cause. Failing here names the mistake at the moment it is made.
func (c Credential) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("refusing to serialise a Credential: fetch it inside the activity that uses it")
}

// CredentialFile is a complete credential document destined for a sandbox's
// filesystem — the whole of a codex auth.json, not one field of it.
//
// Like Credential it is deliberately not a []byte: the type is what stops the
// value reaching a log line or a durable store. It is a distinct type rather
// than a Credential holding JSON so that it cannot be passed where a bare token
// is wanted, and because Reveal returning []byte is what a file write consumes
// — Credential.Reveal returning a string invites string manipulation of a
// credential document.
//
// The file must be written with mode 0600. That is the writer's to enforce,
// and because the destination is a pod rather than a local disk it is a
// property of the transfer's write path, not of a umask.
//
// It must never be returned from an activity, for the same reason a Credential
// must not, only more so: a document is exactly the shape somebody returns from
// an activity, and Temporal would persist it to workflow history for the
// namespace's whole retention.
type CredentialFile struct {
	content []byte
}

// NewCredentialFile wraps a credential document.
func NewCredentialFile(content []byte) CredentialFile {
	return CredentialFile{content: bytes.Clone(content)}
}

// Reveal returns the document's bytes. Call it only at the point they are
// written to their destination.
//
// It returns a copy, so a caller that mutates what it is handed cannot edit the
// document every later caller receives.
func (f CredentialFile) Reveal() []byte {
	return bytes.Clone(f.content)
}

// String redacts the document, so a stray %v cannot leak it.
func (f CredentialFile) String() string {
	return "[redacted credential file]"
}

// CredentialFile must satisfy slog.LogValuer, enforced here at package scope
// for the same reason Credential's is: a signature that drifts off the
// interface fails `go build` rather than only `go test`. It matters at least as
// much here — this is the type whose own doc argues that incidental protection
// is not good enough for a whole document.
var _ slog.LogValuer = CredentialFile{}

// LogValue redacts the document in structured logs.
//
// It returns slog.Value, NOT any, so that CredentialFile actually satisfies
// slog.LogValuer and slog genuinely calls it. Credential.LogValue has the same
// signature and the same guard — see the assertion above it.
//
// Were this signature to drift, what happened next would depend on the handler,
// and neither outcome is this type's doing: a TextHandler falls through to fmt,
// which finds String(); a JSONHandler reaches encoding/json, which never calls
// String() — MarshalJSON below refuses and the attribute renders as an !ERROR
// string. So on this service's real path, whose logs are JSON for Loki, the
// symptom of a regression is a corrupted field rather than a clean
// "[redacted]". Protected, but by an accident of lookup order, and visibly
// broken. The assertion above is what makes redaction a property of this type.
func (f CredentialFile) LogValue() slog.Value {
	return slog.StringValue(f.String())
}

// MarshalJSON always fails, for the reason Credential's does.
func (f CredentialFile) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("refusing to serialise a CredentialFile: build it inside the activity that writes it")
}
