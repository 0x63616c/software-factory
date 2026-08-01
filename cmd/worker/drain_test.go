package main

import (
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

// podGracePeriod is what F1's Deployment gives this pod after SIGTERM:
// TERMINATION_GRACE_SECONDS in infra/src/software-factory.ts.
//
// It is a literal here because Go and TypeScript cannot share a constant, and a
// bound asserted in one language against a number written in the other is the
// best available. That makes this test the thing that fails when either side
// moves alone, which is the point — ADR-0011 requires the grace period to sit
// above the drain window, and nothing else in either tree checks it.
const podGracePeriod = 120 * time.Second

// TestTheDrainIsNotZero is the whole of the bug this constant exists to fix.
//
// worker.Options{} leaves WorkerStopTimeout at zero, and zero does not mean
// "wait forever": the SDK starts an already-fired timer, so the drain returns
// immediately, logs "graceful stop timed out" and cancels every in-flight
// activity's context. A worker that claims to drain and cancels instead is
// worse than one that says it cancels.
func TestTheDrainIsNotZero(t *testing.T) {
	t.Parallel()

	if workerStopTimeout <= 0 {
		t.Fatalf("workerStopTimeout = %s; a zero stop timeout cancels every in-flight activity the moment SIGTERM lands, which is the opposite of a drain",
			workerStopTimeout)
	}
}

// TestTheDrainFitsInsideThePodsGracePeriod pins the relationship ADR-0011
// states: terminationGracePeriodSeconds above the drain window.
//
// Below it, the kubelet SIGKILLs the process partway through the drain it was
// configured to wait for — so the drain window would be a number that reads as
// correct and never elapses. shutdownGrace is in the sum because the metrics
// server is stopped after the worker drains, on the same clock.
func TestTheDrainFitsInsideThePodsGracePeriod(t *testing.T) {
	t.Parallel()

	if total := workerStopTimeout + shutdownGrace; total >= podGracePeriod {
		t.Errorf("the drain needs %s (workerStopTimeout %s + shutdownGrace %s) but the pod is killed after %s; raise TERMINATION_GRACE_SECONDS in infra/src/software-factory.ts or lower the drain",
			total, workerStopTimeout, shutdownGrace, podGracePeriod)
	}
}

// TestTheDrainDoesNotPretendToOutlastAStage records the honest half of the
// trade, so nobody "fixes" the drain by raising it to a stage's length.
//
// Stages run on sandbox Session workers, not this main worker. The main drain
// must therefore not borrow a stage's 60-minute duration as its target:
// raising this window towards MaxStageDuration would buy nothing and would blow
// the grace period above.
func TestTheDrainDoesNotPretendToOutlastAStage(t *testing.T) {
	t.Parallel()

	if workerStopTimeout >= work.MaxStageDuration {
		t.Errorf("workerStopTimeout = %s, which is not less than a stage's %s; the main worker does not host stages, so a stage-length drain buys nothing and cannot fit in the pod grace period",
			workerStopTimeout, work.MaxStageDuration)
	}
}
