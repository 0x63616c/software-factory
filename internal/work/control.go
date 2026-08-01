package work

import (
	"errors"
	"fmt"
	"time"
)

// ErrWorkflowNotFound means Temporal has no workflow with the requested ID.
var ErrWorkflowNotFound = errors.New("temporal workflow not found")

// ErrWorkflowClosed means Temporal found the workflow but it cannot accept the command.
var ErrWorkflowClosed = errors.New("temporal workflow already closed")

// Config is everything about the dispatcher's behaviour that can change
// without a deploy: whether it starts work at all, how much at once, how long
// it stays quiet after hitting a wall, and which model each stage runs on.
//
// It is a value, and every method on it is pure. The dispatcher holds one in
// workflow state and replaces it wholesale when an update is accepted, so the
// config a cycle ran under is the config it was handed — never one another
// goroutine edited underneath it. That is also why StageModels is a struct of
// pointers rather than a map: a map would make Config a value carrying shared
// mutable state, and copying it would silently not copy the overrides.
type Config struct {
	// Paused stops the dispatcher starting new tickets. Work already in flight
	// runs to completion — pausing is "start nothing more", not "abandon".
	Paused bool `json:"paused"`

	// MaxInFlight is how many tickets may be in flight at once. ADR-0011: the
	// binding constraint is the plan's rate-limit window, not concurrency, so
	// this starts small and moves on measurement.
	MaxInFlight int `json:"maxInFlight"`

	// BreakerCooldownSeconds is how long the dispatcher stops starting new work
	// after a rate limit trips the breaker. Read it through BreakerCooldown;
	// the unit is in the name because this value is hand-written as JSON by
	// whoever is signalling a running dispatcher at the time.
	BreakerCooldownSeconds int64 `json:"breakerCooldownSeconds"`

	// PauseReason is why the dispatcher is paused, and empty when it is
	// running. The dispatcher writes it when it pauses itself on an unusable
	// credential; a human pausing by hand should set it too.
	//
	// It is here rather than on Status because it is part of the pause, not a
	// separate observation of it: Paused alone cannot tell a system that
	// stopped itself on a dead credential from a human stopping it
	// deliberately, and those call for opposite responses.
	PauseReason string `json:"pauseReason,omitempty"`

	// PollIntervalSeconds is how often the dispatcher wakes to reconcile and
	// start work. It bounds how stale the in-flight set can be and nothing
	// else: there is no WorkNow signal, because one would buy at most this
	// interval. Read it through PollInterval.
	PollIntervalSeconds int64 `json:"pollIntervalSeconds"`

	// OrphanGraceSeconds is both how often the orphan sweep runs and how old a
	// Run Worker pod must be before the sweep will delete it for having no live
	// run.
	//
	// One value for both because they are the same judgement — how long a pod
	// may exist unaccounted for — and two would invite a sweep that runs more
	// often than its own floor, which is a Kubernetes call per poll that can
	// never delete anything. Read it through OrphanGrace.
	OrphanGraceSeconds int64 `json:"orphanGraceSeconds"`

	// DefaultModel runs every stage that has no override.
	DefaultModel Model `json:"defaultModel"`

	// StageModels are the per-stage exceptions to that.
	StageModels StageModels `json:"stageModels"`
}

// maxAllowedInFlight is the ceiling on concurrent tickets.
//
// It is a guard against a typo, not a capacity plan: the default is 2, and the
// binding constraint is one subscription's rate-limit window, which a run of
// concurrent stages empties quickly. A missed keystroke turning 2 into 20 would
// otherwise validate, deploy, and spend the window before anyone read a
// dashboard — and this is the first path that puts the value into production.
//
// Chosen as "far above any value with a reason behind it, far below a typo".
// It is a policy number rather than a derived one; if a real workload ever
// wants more, this line and its justification are what should be argued with.
const maxAllowedInFlight = 10

// Default configuration. The cap comes from ADR-0011; the cooldown does not,
// and is a first guess to be moved on measurement. It is deliberately long:
// the plan's limit window is measured in hours, so a cooldown of a minute
// spends the retry budget re-hitting the same wall.
const (
	defaultMaxInFlight            = 3
	defaultBreakerCooldownSeconds = 15 * 60
	defaultPollIntervalSeconds    = 30
	defaultOrphanGraceSeconds     = 30 * 60
	defaultModelName              = "gpt-5.6-terra"
	defaultModelEffort            = "medium"
)

// DefaultConfig is what the dispatcher runs on before any update arrives.
//
// It is valid and it is running, not paused: a deploy that lost its config
// signal should do the normal thing loudly rather than sit idle looking
// healthy.
func DefaultConfig() Config {
	return Config{
		MaxInFlight:            defaultMaxInFlight,
		BreakerCooldownSeconds: defaultBreakerCooldownSeconds,
		PollIntervalSeconds:    defaultPollIntervalSeconds,
		OrphanGraceSeconds:     defaultOrphanGraceSeconds,
		DefaultModel:           Model{Name: defaultModelName, Effort: defaultModelEffort},
	}
}

// DefaultFactoryConfig is the independent starting configuration for the
// Ticket dispatcher. Both dispatchers draw from one Codex quota, so the new
// path starts at one in-flight Ticket until production measurement justifies
// spending more of that shared quota.
func DefaultFactoryConfig() Config {
	config := DefaultConfig()
	config.MaxInFlight = 1
	return config
}

// BreakerCooldown is how long the breaker stays open once tripped.
func (c Config) BreakerCooldown() time.Duration {
	return time.Duration(c.BreakerCooldownSeconds) * time.Second
}

// PollInterval is how often the dispatcher wakes.
func (c Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalSeconds) * time.Second
}

// OrphanGrace is how old an unaccounted-for Run Worker pod must be before the
// sweep deletes it, and how often that sweep runs.
func (c Config) OrphanGrace() time.Duration {
	return time.Duration(c.OrphanGraceSeconds) * time.Second
}

// ModelFor is the model a stage runs on: its override, or the default.
func (c Config) ModelFor(stage Stage) Model {
	if override := c.StageModels.For(stage); override != nil {
		return *override
	}
	return c.DefaultModel
}

// Validate reports whether this config can be run.
//
// A cap below one and a cooldown of zero are rejected rather than clamped.
// Each would otherwise be a second, quieter way to express something the
// config already says: a cap of zero is Paused spelled so that GetStatus
// reports the system as running, and a cooldown of zero is a breaker that
// reopens in the same instant it trips, which reads as no breaker at all.
func (c Config) Validate() error {
	if c.MaxInFlight < 1 {
		return fmt.Errorf("MaxInFlight must be at least 1, got %d: to stop starting work, set Paused", c.MaxInFlight)
	}
	if c.MaxInFlight > maxAllowedInFlight {
		return fmt.Errorf("MaxInFlight must be at most %d, got %d: every ticket in flight runs codex against one subscription, so a value this size spends the whole rate-limit window at once",
			maxAllowedInFlight, c.MaxInFlight)
	}
	if c.BreakerCooldownSeconds <= 0 {
		return fmt.Errorf("BreakerCooldownSeconds must be positive, got %d: a breaker that reopens immediately stops nothing", c.BreakerCooldownSeconds)
	}
	if c.PollIntervalSeconds <= 0 {
		return fmt.Errorf("PollIntervalSeconds must be positive, got %d: a dispatcher that never wakes starts nothing and reconciles nothing", c.PollIntervalSeconds)
	}
	if c.OrphanGraceSeconds <= 0 {
		return fmt.Errorf("OrphanGraceSeconds must be positive, got %d: a zero grace sweeps pods out from under the runs that just created them", c.OrphanGraceSeconds)
	}
	if err := c.DefaultModel.Validate(); err != nil {
		return fmt.Errorf("default model: %w", err)
	}
	return c.StageModels.Validate()
}

// Apply returns this config with the update's set fields replaced, or the
// config unchanged and an error if the result would not be valid.
//
// Nil means leave alone, which is what lets one signal serve both callers
// ADR-0011 names: a deploy pushing the config it knows about, and a human
// pausing the system by hand without having to restate the rest of it.
//
// Failure is wholesale. A partially applied update would leave the dispatcher
// running a configuration nobody wrote, and the sender cannot be told —
// Temporal signals carry no reply — so the only safe answer to a bad update is
// to keep the one that was already working and report it through Status.
func (c Config) Apply(u ConfigUpdate) (Config, error) {
	updated := c
	if u.Paused != nil {
		updated.Paused = *u.Paused
	}
	if u.MaxInFlight != nil {
		updated.MaxInFlight = *u.MaxInFlight
	}
	if u.BreakerCooldownSeconds != nil {
		updated.BreakerCooldownSeconds = *u.BreakerCooldownSeconds
	}
	if u.PollIntervalSeconds != nil {
		updated.PollIntervalSeconds = *u.PollIntervalSeconds
	}
	if u.OrphanGraceSeconds != nil {
		updated.OrphanGraceSeconds = *u.OrphanGraceSeconds
	}
	if u.PauseReason != nil {
		updated.PauseReason = *u.PauseReason
	}
	if u.DefaultModel != nil {
		updated.DefaultModel = *u.DefaultModel
	}
	if u.StageModels != nil {
		updated.StageModels = *u.StageModels
	}
	if err := updated.Validate(); err != nil {
		return c, fmt.Errorf("rejecting config update: %w", err)
	}
	return updated, nil
}

// ConfigUpdate is the payload of the UpdateConfig signal: the fields to
// change, and nothing about the fields to leave alone.
//
// Every scalar is a pointer for that reason — the zero value of every field
// here is a value someone might legitimately send (`false`, `0`), so absence
// has to be representable separately from it.
type ConfigUpdate struct {
	Paused                 *bool   `json:"paused,omitempty"`
	PauseReason            *string `json:"pauseReason,omitempty"`
	MaxInFlight            *int    `json:"maxInFlight,omitempty"`
	BreakerCooldownSeconds *int64  `json:"breakerCooldownSeconds,omitempty"`
	PollIntervalSeconds    *int64  `json:"pollIntervalSeconds,omitempty"`
	OrphanGraceSeconds     *int64  `json:"orphanGraceSeconds,omitempty"`
	DefaultModel           *Model  `json:"defaultModel,omitempty"`

	// StageModels replaces the whole override set rather than merging into it,
	// so an update names the overrides that should exist afterwards. Merging
	// would make "stop overriding review" inexpressible without a second
	// sentinel for "clear this one", and a set small enough to fit in one
	// signal is small enough to restate.
	StageModels *StageModels `json:"stageModels,omitempty"`
}

// StageModels are the per-stage model overrides: a field per stage rather than
// a map keyed by one.
//
// A map would accept a key that is not a stage — a typo in a hand-written
// signal — and do nothing about it, silently, at the one moment somebody is
// changing configuration under pressure. Here the stages are the type, so a
// stage added without being routed is caught by the exhaustive linter that CI
// runs (not by the compiler — a switch missing an arm still builds), and the
// unknown-key case is rejected at the JSON boundary by UnmarshalJSON rather
// than being unwritable.
type StageModels struct {
	Plan      *Model `json:"plan,omitempty"`
	Review    *Model `json:"review,omitempty"`
	Implement *Model `json:"implement,omitempty"`
}

// For returns the override for a stage, or nil if it has none.
func (m StageModels) For(stage Stage) *Model {
	switch stage {
	case StagePlan:
		return m.Plan
	case StageReview:
		return m.Review
	case StageImplement:
		return m.Implement
	default:
		return nil
	}
}

// Validate reports whether every override present is usable.
func (m StageModels) Validate() error {
	for _, stage := range Pipeline() {
		override := m.For(stage)
		if override == nil {
			continue
		}
		if err := override.Validate(); err != nil {
			return fmt.Errorf("%s model override: %w", stage, err)
		}
	}
	return nil
}

// Breaker is the dispatcher's circuit breaker: shut until a deadline, and why.
//
// It is a deadline rather than an open/closed flag with a timer beside it,
// because those two can disagree and this cannot. The zero value is a breaker
// that has never tripped, and reads as closed — see OpenAt.
type Breaker struct {
	// OpenUntil is when the dispatcher may start work again. Zero means the
	// breaker has never tripped.
	OpenUntil time.Time `json:"openUntil"`

	// Reason is the rate-limit message that tripped it, kept so an operator
	// reading GetStatus learns what the wall was rather than only that one
	// exists.
	Reason string `json:"reason"`
}

// OpenAt reports whether the breaker is still stopping new work at this
// instant. The deadline itself is when work resumes, not the last moment it is
// blocked, so a cooldown of exactly its length is exactly its length.
//
// The caller passes the time rather than the breaker reading a clock: the
// dispatcher is workflow code, and its now is workflow.Now.
func (b Breaker) OpenAt(now time.Time) bool {
	return now.Before(b.OpenUntil)
}

// Status is the result of the GetStatus query: what the dispatcher is doing,
// what it is doing it under, and what is stopping it.
//
// Config is here in full rather than a Paused bool, because the question asked
// of a live dispatcher is rarely "is it paused" alone — it is "did my config
// land", and the only way to answer that is to show the config it is running.
type Status struct {
	Config   Config  `json:"config"`
	InFlight []int   `json:"inFlight"`
	Breaker  Breaker `json:"breaker"`

	// ConfigError is why the last update was rejected, and empty if none was.
	// A signal cannot fail back to its sender, so without this a config change
	// that never took effect looks exactly like one that did.
	ConfigError string `json:"configError,omitempty"`
}

// FactoryDispatcherStatus is the acknowledged control state used by the
// one-time cutover gate. Its in-flight identifiers are factory Ticket IDs.
type FactoryDispatcherStatus struct {
	Config   Config  `json:"config"`
	InFlight []int64 `json:"inFlight"`
}
