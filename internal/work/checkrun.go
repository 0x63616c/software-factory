package work

// CheckRun is one GitHub check run reported against a commit, as GitHub
// itself reports it.
//
// It is the vocabulary internal/clients/github's ChecksForRef reads GitHub's
// Checks API response into, and Activities.ObserveCI reduces a whole ref's
// worth of these into "concluded" and "green" for the implement/review
// loop's progress-detection rules — rule 1 (the same failed check identity
// twice),
// in the pipeline-rewrite spec's "The real stop condition" section.
type CheckRun struct {
	// Name is the check's own name, as GitHub reports it.
	Name string

	// FailureFingerprint identifies this check's failure output without
	// retaining that potentially large, untrusted output in workflow history.
	// It is meaningful for completed non-green checks only.
	FailureFingerprint string

	// FailureEvidence is bounded diagnostic output for a failed check. It is
	// untrusted and only target AwaitCI carries it to the next Implement Step.
	FailureEvidence string

	// Completed is whether this check run has finished — GitHub's own
	// "completed" status, as opposed to "queued" or "in_progress". A run
	// that has not completed has no meaningful Conclusion yet.
	Completed bool

	// Conclusion is GitHub's verdict once Completed is true: "success",
	// "failure", "neutral", "cancelled", "skipped", "timed_out" or
	// "action_required". Empty until Completed.
	Conclusion string
}

// CheckFailure is the stable identity of one failed GitHub check run.
// WorkTicket rule 1 compares these values turn over turn, so a same-named
// check with different CI output counts as progress rather than stagnation.
type CheckFailure struct {
	Name        string
	Fingerprint string
	Evidence    string
}

// Green reports whether this check run's conclusion counts as passing.
//
// "neutral" and "skipped" both count: GitHub itself does not fail a
// required check on either, so treating them as red here would make this
// stricter than GitHub's own merge-readiness rule for no reason this
// service's loop needs.
func (c CheckRun) Green() bool {
	return c.Completed && (c.Conclusion == "success" || c.Conclusion == "neutral" || c.Conclusion == "skipped")
}

// Superseded reports whether GitHub cancelled this check run rather than
// letting it reach a verdict.
//
// A cancelled run is almost always the workflow concurrency group killing an
// older run because a newer push replaced it, so it says nothing about the
// change under test. It is neither green nor a failure this loop may charge
// against a turn: ObserveCI keeps polling for the run that superseded it.
func (c CheckRun) Superseded() bool {
	return c.Completed && c.Conclusion == "cancelled"
}
