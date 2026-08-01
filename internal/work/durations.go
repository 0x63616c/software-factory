package work

import "time"

// The deadlines a run is bounded by, in one place because they are one ladder.
//
// Four different systems enforce these — Temporal enforces the activity and run
// timeouts, Kubernetes enforces the pod's, and codexauth sizes a credential's
// refresh margin against the stage — and each of them holds only its own end.
// Written where each is used, the ladder would be four numbers that have to
// agree and nothing that notices when they stop. The inequalities between them
// are tested in durations_test.go and durations_inv3_test.go; a value tuned
// without re-deriving the rest fails there rather than in production.
//
// Nothing here is configurable. These are the deadlines the system is designed
// around, not knobs: an operator wanting a longer stage is asking for a
// different credential margin and a different pod deadline too, which is a
// change to this file and a deploy, not an environment variable.
const (
	// MaxStageDuration is the longest one stage may run, and the
	// StartToCloseTimeout of the activity that runs it.
	//
	// ADR-0011 sets it at 60 minutes, "deliberately generous until real timings
	// exist". It is also an input to INV-3 in codexauth: a sandbox holds a
	// credential copy it cannot refresh, so the refresh margin has to outlast a
	// whole stage. codexauth takes it positionally, with no default, precisely
	// so this constant is the only place it is written.
	MaxStageDuration = 60 * time.Minute

	// StageHeartbeatTimeout is how long a stage may emit no event before it is
	// treated as dead rather than slow.
	//
	// Each emitted stage event records the activity heartbeat, so this timeout
	// bounds time to the first event as well as silence between later events;
	// it is not an independent periodic liveness signal. Five minutes lets
	// codex work through a large prompt before it can emit its first event, but
	// a continued five-minute-silent event stream is still treated as dead.
	StageHeartbeatTimeout = 5 * time.Minute

	// StageRetryInitialInterval is the delay before a stage's first retry.
	// Temporal multiplies it by StageRetryBackoffCoefficient after each
	// failure, giving transient provider-capacity failures room to clear.
	StageRetryInitialInterval = time.Second

	// StageRetryBackoffCoefficient gives transient provider failures
	// progressively wider recovery windows without waiting five minutes after
	// the first failure.
	StageRetryBackoffCoefficient = 5.0

	// StageRetryMaximumInterval caps later retry delays so a sustained outage
	// cannot make one stage wait ever longer.
	StageRetryMaximumInterval = 5 * time.Minute

	// MaxRunDuration is the workflow run timeout for one ticket: above
	// RunPolicy.RunBudget()'s worst case (MaxStageInvocations stage
	// invocations at MaxStageDuration each — 19 hours today), with room for
	// the cheap activities between them — labels, status comments, the
	// Run Worker's own lifecycle.
	//
	// This grew from 6 hours under the five-stages-once pipeline to 24 under
	// the pipeline rewrite's implement/review loop: MaxStageInvocations is 19
	// rather than 5, and 19*MaxStageDuration (19h) does not fit inside 6h. It
	// is a theoretical ceiling, not the expected case — "Sizing, measured" in
	// the pipeline-rewrite spec measured real implement turns at 62s-617s, so
	// a 15-turn run in practice finishes in low single-digit hours, not 19 —
	// but Validate checks the ceiling every stage using its whole timeout
	// could reach, not the common case, so this has to fit it.
	MaxRunDuration = 24 * time.Hour

	// SandboxDeadline is the pod's activeDeadlineSeconds, above the run
	// timeout so Kubernetes never kills a sandbox a live run still expects.
	// A stage whose pod vanished under it reports an exec failure with no
	// cause, which is the most expensive kind of failure to diagnose.
	SandboxDeadline = 25 * time.Hour

	// SandboxDeadlineSeconds is that deadline in the units Kubernetes takes,
	// converted here rather than at the call site so the units cannot be got
	// wrong twice.
	SandboxDeadlineSeconds = int64(SandboxDeadline / time.Second)
)

// Step 3 (D1/D4 in the addendum to the sandbox-as-worker spec, #434) adds two
// more deadlines to the same ladder: workflow.SessionOptions.ExecutionTimeout
// and .CreationTimeout, for the Session the WorkTicket workflow holds open
// across a run's sandbox pod. Both are var, not const, because the second is
// derived from DefaultRunPolicy() rather than written as a literal — see its
// own comment — and a const cannot call a function. Neither is configurable
// for the same reason nothing else on this ladder is.
var (
	// SessionExecutionTimeout is workflow.SessionOptions.ExecutionTimeout: how
	// long the Session covering one ticket's run may stay open.
	//
	// At or above MaxRunDuration, the same direction durations_test.go already
	// checks for SandboxDeadline against MaxRunDuration: a session that expires
	// before the run it serves fails every stage after it, while one that
	// outlives the run is merely pointless rather than actively wrong. Set
	// equal to MaxRunDuration rather than padded further above it — nothing
	// yet needs headroom beyond the run's own bound, and a second, larger
	// number would only invite the two to drift apart silently, which is
	// exactly what this ladder exists to catch instead.
	SessionExecutionTimeout = MaxRunDuration

	// SessionCreationTimeout is workflow.SessionOptions.CreationTimeout: how
	// long workflow.CreateSession waits for a sandbox pod's embedded worker to
	// claim the session-creation task before failing loudly. Verified against
	// the SDK's own source that this is a real, enforced bound and not merely
	// documentation (sdk-go@v1.47.0: session creation is a regular activity
	// with ScheduleToStartTimeout: CreationTimeout, and ScheduleToStartTimeout
	// is always non-retryable) — so sizing it is meaningful.
	//
	// Derived, not guessed: D1 ships no warm pool, so every pod is created
	// fresh for its own ticket, which makes CreateSession's wait exactly the
	// same wait WaitSandboxReady already performs today — "pod created until
	// that pod can do the next thing asked of it" — just observed a
	// different way (there, an activity polling pod readiness; here, the
	// pod's own embedded worker polling its task queue). WaitSandboxReady
	// runs under controlOptions() (workticket.go), so its own existing bound
	// is DefaultRunPolicy's ControlTimeout retried ControlAttempts times, and
	// this reuses that bound directly rather than writing a second number
	// that could disagree with it.
	SessionCreationTimeout = DefaultRunPolicy().ControlTimeout * time.Duration(DefaultRunPolicy().ControlAttempts)
)
