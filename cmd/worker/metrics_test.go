package main

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/telemetry"
)

// TestTheStageMetricsRecordIntoTheRegistryThatIsServed is the assertion behind
// "exactly one construction site".
//
// The loud failure — two constructions against one registry — Prometheus
// already covers by panicking. The quiet one is the reason this test exists: a
// track that builds its own Metrics against its own registry panics at nothing,
// increments happily, and serves nothing, because /metrics only ever gathers
// the registry built here. Asserting that a SECOND NewMetrics against this
// registry panics is what proves the first one registered into this registry
// rather than into one that is dropped on the floor.
func TestTheStageMetricsRecordIntoTheRegistryThatIsServed(t *testing.T) {
	t.Parallel()

	registry, metrics := newObservability()
	if metrics == nil {
		t.Fatal("newObservability returned no stage metrics; nothing would record and /metrics would stay empty")
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("a second telemetry.NewMetrics against the served registry did not panic, so the first one did not register into it; a track that built its own would serve nothing and fail nowhere")
		}
		if got := toString(recovered); !strings.Contains(got, "duplicate") && !strings.Contains(got, "already registered") {
			t.Errorf("panicked with %q, want a duplicate-registration panic", got)
		}
	}()
	_ = telemetry.NewMetrics(registry)
}

// TestEveryProcessGetsItsOwnRegistry guards the other direction: the singleton
// is per-process, not a package-level global that two calls would share. A
// global would make this untestable and would turn the panic above into a
// startup crash for anything that constructed twice for legitimate reasons.
func TestEveryProcessGetsItsOwnRegistry(t *testing.T) {
	t.Parallel()

	first, _ := newObservability()
	second, _ := newObservability()

	if first == second {
		t.Error("newObservability handed out the same registry twice; it must build a fresh one so construction stays a per-process act")
	}
}

func toString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
