// Package store is the factory's own Postgres store: sqlc-generated queries
// against the schema ADR-0012 defines, behind a narrow, consumer-side door.
//
// internal/store/storedb holds every pgx and sqlc-generated type; nothing
// outside this package imports it (enforced by .golangci.yml's
// store-generated-rows-are-sealed rule). Store's exported methods take and
// return this package's own domain types, reusing internal/work's StageKey,
// Stage, Model, Usage, Outcome and FailureKind rather than redefining them, so
// a caller never sees a database row and this package never invents a second
// spelling of a type that already exists.
//
// A row becomes one of these types exactly once, at the boundary a query
// returns across (SoftwareStyle: parse, don't validate) — a stored ticket
// state becomes a TicketState here and is never re-checked by a caller.
package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

// TicketID identifies a Ticket in the factory's own store, minted by the
// ticket table's identity column.
//
// It is a distinct type from a GitHub issue number on purpose: ADR-0012 fixes
// "Ticket" and "Issue" as two different things in two different systems, and
// internal/work.Ticket already names the GitHub-issue-shaped one. Reusing that
// name here for an unrelated integer would be exactly the confusion the
// vocabulary split exists to prevent.
type TicketID int64

// TicketState is a Ticket's lifecycle state.
//
// Four values, enforced twice: the ticket table's CHECK constraint is the
// database's wall, and Valid is Go's. Neither is a formality — a state read
// out of a row is trusted afterwards precisely because it passed one of these
// on the way in.
type TicketState struct{ value ticketStateValue }

type ticketStateValue uint8

// The target workflow owns active Tickets. The two legacy values remain only
// so the pre-activation cutover inventory compiles; they are not valid domain
// values and the final schema rejects them.
const (
	ticketStateOpen ticketStateValue = iota + 1
	ticketStateWorking
	ticketStateReview
	ticketStateActive
	ticketStateDone
	ticketStateFailed
)

var (
	// TicketOpen is filed, not started.
	TicketOpen = TicketState{value: ticketStateOpen}
	// TicketWorking means a legacy Run is in flight.
	TicketWorking = TicketState{value: ticketStateWorking}
	// TicketReview means a legacy Run produced a pull request; waiting on a human.
	TicketReview = TicketState{value: ticketStateReview}
	// TicketActive means a target Run owns the Ticket through ActiveRunID.
	TicketActive = TicketState{value: ticketStateActive}
	// TicketDone is terminal, and satisfies dependencies.
	TicketDone = TicketState{value: ticketStateDone}
	// TicketFailed is terminal, and does not satisfy dependencies. Never
	// auto-retried — a human moves a Ticket back to TicketOpen.
	TicketFailed = TicketState{value: ticketStateFailed}
)

// Valid reports whether s is one of the four states the schema enforces.
func (s TicketState) Valid() bool {
	switch s {
	case TicketOpen, TicketActive, TicketDone, TicketFailed:
		return true
	default:
		return false
	}
}

// String returns the one wire/database spelling of s.
func (s TicketState) String() string {
	switch s {
	case TicketOpen:
		return "open"
	case TicketWorking:
		return "working"
	case TicketReview:
		return "review"
	case TicketActive:
		return "active"
	case TicketDone:
		return "done"
	case TicketFailed:
		return "failed"
	default:
		return ""
	}
}

// ParseTicketState converts the one wire/database spelling of a state into a
// known domain value. Callers pass the result onwards rather than validating
// strings at each layer.
func ParseTicketState(value string) (TicketState, error) {
	switch value {
	case "open":
		return TicketOpen, nil
	case "active":
		return TicketActive, nil
	case "done":
		return TicketDone, nil
	case "failed":
		return TicketFailed, nil
	default:
		return TicketState{}, fmt.Errorf("%q is not a TicketState", value)
	}
}

// UnmarshalText parses a query or path state once at the HTTP boundary.
func (s *TicketState) UnmarshalText(value []byte) error {
	parsed, err := ParseTicketState(string(value))
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

// UnmarshalJSON parses a JSON state once at the HTTP boundary.
func (s *TicketState) UnmarshalJSON(value []byte) error {
	var wire string
	if err := json.Unmarshal(value, &wire); err != nil {
		return fmt.Errorf("decode TicketState: %w", err)
	}
	return s.UnmarshalText([]byte(wire))
}

// MarshalJSON writes the one wire/database spelling of s, symmetric with
// UnmarshalJSON. Required for store.Ticket to cross a Temporal activity
// boundary at all: the SDK's default data converter is JSON, and TicketState's
// only field is unexported, so without this a Ticket carrying its State
// through workflow.ExecuteActivity would encode as "{}" and fail to decode
// back, not just print oddly.
func (s TicketState) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// CanTransitionTo reports whether moving from one state to another follows
// the Ticket lifecycle. Keeping this table here makes lifecycle policy a
// domain decision rather than a property of any HTTP handler.
func (s TicketState) CanTransitionTo(next TicketState) bool {
	switch s {
	case TicketActive:
		return next == TicketDone || next == TicketOpen || next == TicketFailed
	case TicketFailed:
		return next == TicketOpen
	case TicketDone:
		return false
	default:
		return false
	}
}

// Ticket is a unit of work in the factory's own store.
//
// It is not a GitHub issue — see internal/work.Ticket for that, and ADR-0012's
// vocabulary table for why the two are never used interchangeably. There is no
// Source field: ADR-0012 records that one is trivially backfilled if a second
// origin ever exists, and adding it now would be speculative.
type Ticket struct {
	ID    TicketID
	Title string
	Body  string
	State TicketState
	// ActiveRunID is the target Run which currently owns an active Ticket.
	// It remains empty for legacy workflow states and terminal Tickets.
	ActiveRunID string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// InFlightTicket is one Ticket the Ticket-driven FactoryDispatcher believes is
// currently being worked — its own in-memory shape's counterpart,
// work.InFlightTicket, keyed by a GitHub issue number rather than a TicketID.
// The two are never interchangeable (ADR-0012's vocabulary table), which is
// also why this workflow's in-flight set is not persisted through the legacy
// dispatcher_state row: that row's InFlight is []work.InFlightTicket, shaped
// around GitHub issue numbers, and the Ticket dispatcher pointing at it is
// later work, not part of standing the second pipeline up.
type InFlightTicket struct {
	TicketID  TicketID
	RunID     string
	StartedAt time.Time
}

// ActiveTargetRunOwner is the durable ownership pair maintenance must verify
// before releasing a Ticket after its Temporal workflow was terminated.
//
// It does not claim that the Run is still live. That fact comes from Temporal's
// strongly consistent execution lookup; this pair is the Store predicate that
// prevents a stale maintenance pass from reopening a replacement Run's Ticket.
type ActiveTargetRunOwner struct {
	TicketID TicketID
	RunID    string
}

// AttemptResult is how an Attempt ended, once it has.
//
// Empty means the attempt has not ended yet — EndAttempt has not been called
// for it — which is a different fact from either outcome below and must not be
// collapsed into one of them.
type AttemptResult string

// The two ways a recorded Attempt can end.
const (
	AttemptSucceeded AttemptResult = "succeeded"
	AttemptFailed    AttemptResult = "failed"
)

// Valid reports whether r is empty (not yet ended) or one of the two outcomes
// the schema allows.
func (r AttemptResult) Valid() bool {
	switch r {
	case "", AttemptSucceeded, AttemptFailed:
		return true
	default:
		return false
	}
}

// Attempt is one execution of a Step: which attempt it is, what it ran on,
// what it cost, and how it ended.
//
// Key identifies the Step (and, through StageKey.Ticket, the Run's Ticket) this
// attempt belongs to. The row's own primary key is (run, stage, turn,
// attempt_no) — Key.Ticket travels along for logging and error context, not
// because it is part of that key.
type Attempt struct {
	Key       work.StageKey
	AttemptNo int

	// Model is the model and reasoning effort this attempt ran on — on the
	// Attempt, never the Run, because a per-stage override can change it
	// between Steps in the same Run (ADR-0012).
	Model work.Model

	// Usage is the four token counts RecordAttempt was given. Zero and
	// Measured false means unknown, never zero spend — see Measured.
	Usage work.Usage

	// Measured reports whether this attempt actually ran Codex. A resumed
	// attempt returns a stored result without running anything and reports
	// Measured false with a zero Usage; rendering that as zero spend is #426,
	// which this field exists to stop reproducing.
	Measured bool

	StartedAt time.Time
	// EndedAt is the zero time until EndAttempt is called.
	EndedAt time.Time
	// Result is empty until EndAttempt is called.
	Result AttemptResult
}

// Transcript is one Attempt's compressed raw event stream, plus what is
// needed to store and verify it: which compression was used, the
// uncompressed size, and a checksum. Kept forever — ADR-0012 v0 has no
// retention marker.
type Transcript struct {
	Key                   work.StageKey
	AttemptNo             int
	CompressedBytes       []byte
	Compression           string
	UncompressedSizeBytes int64
	Checksum              []byte
}

// Run is one attempt at a whole Ticket — one Temporal workflow execution.
//
// ID is Temporal's run id for the enclosing workflow. Outcome and Failure are
// internal/work's own types, at their zero values (work.Outcome("") and
// work.FailureNone) until EndRun is called.
type Run struct {
	ID        string
	TicketID  TicketID
	StartedAt time.Time
	// EndedAt is the zero time until EndRun is called.
	EndedAt time.Time
	Outcome work.Outcome
	Failure work.FailureKind
	// TargetOutcome and TargetFailure are additive until the cutover removes
	// the legacy outcome vocabulary.
	TargetOutcome work.RunOutcome
	TargetFailure work.RunFailureKind
	ReviewedHead  string
	MergeSHA      string
}

// Step is one instance of a Stage inside a Run — exactly internal/work's
// StageKey, reused rather than redefined so a Step's identity never has two
// spellings.
type Step = work.StageKey

// RunDetail is a Run together with every Step it has recorded and every
// Attempt of each Step, oldest first — the console detail view's shape.
type RunDetail struct {
	Run   Run
	Steps []StepDetail
}

// StepDetail is one Step and the Attempts recorded against it.
type StepDetail struct {
	Stage    work.Stage
	Turn     int
	Attempts []Attempt
}

// DispatcherState is the legacy GitHub-backed dispatcher's post-tick
// decision (#551): what it is running under, what it holds a slot for, and
// what it would claim next. It intentionally uses internal/work's own types
// and GitHub issue numbers rather than a TicketID — this ticket's scope is
// explicit that the dispatcher still reads GitHub issues, not Tickets from
// this store; switching its work source is the later cutover ticket.
// ADR-0012's second, Ticket-reading dispatcher is what eventually points at
// this same row instead.
type DispatcherState struct {
	// Config is the operator surface this tick ran under: paused, the
	// concurrency cap, poll interval, orphan grace, breaker cooldown, and the
	// model each stage runs on.
	Config work.Config
	// ConfigError is why the last signalled update was rejected, empty if none
	// was.
	ConfigError string
	// Breaker is the zero Breaker (never tripped) until the dispatcher trips it.
	Breaker work.Breaker
	// InFlight is the tickets this tick believes are being worked.
	InFlight []work.InFlightTicket
	// Candidates is the eligible `auto` tickets this tick computed, in the
	// order it would claim them — the one thing nothing records today.
	Candidates []int
	// FreeSlots is how many of Config.MaxInFlight were open this tick.
	FreeSlots int
	WrittenAt time.Time
}
