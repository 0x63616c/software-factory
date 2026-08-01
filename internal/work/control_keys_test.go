package work_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

// The JSON keys are a contract with whoever hand-writes an UpdateConfig signal,
// and a round-trip test cannot see them: Go marshals and unmarshals a renamed
// tag just as happily as the right one. So the wire names are asserted
// literally, and renaming one breaks a test rather than an operator's signal.

func TestConfigUpdateAcceptsTheKeysAnOperatorWrites(t *testing.T) {
	t.Parallel()

	const signal = `{
		"paused": true,
		"pauseReason": "rotating the app credentials",
		"maxInFlight": 3,
		"breakerCooldownSeconds": 600,
		"pollIntervalSeconds": 15,
		"orphanGraceSeconds": 900,
		"defaultModel": {"name": "gpt-5.6-terra", "effort": "high"},
		"stageModels": {"review": {"name": "another-model", "effort": "high"}}
	}`

	var update work.ConfigUpdate
	if err := json.Unmarshal([]byte(signal), &update); err != nil {
		t.Fatalf("an operator writing every documented key must not be rejected: %v", err)
	}

	if update.Paused == nil || !*update.Paused {
		t.Fatal("paused did not decode")
	}
	if update.PauseReason == nil || *update.PauseReason != "rotating the app credentials" {
		t.Fatal("pauseReason did not decode")
	}
	if update.MaxInFlight == nil || *update.MaxInFlight != 3 {
		t.Fatal("maxInFlight did not decode")
	}
	if update.BreakerCooldownSeconds == nil || *update.BreakerCooldownSeconds != 600 {
		t.Fatal("breakerCooldownSeconds did not decode")
	}
	if update.PollIntervalSeconds == nil || *update.PollIntervalSeconds != 15 {
		t.Fatal("pollIntervalSeconds did not decode")
	}
	if update.OrphanGraceSeconds == nil || *update.OrphanGraceSeconds != 900 {
		t.Fatal("orphanGraceSeconds did not decode")
	}
	if update.StageModels == nil || update.StageModels.Review == nil {
		t.Fatal("stageModels did not decode")
	}
}

func TestConfigUpdateNamesAMisspeltKeyRatherThanIgnoringIt(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"pollIntervalSecconds": `{"pollIntervalSecconds": 15}`,
		"orphanGrace":          `{"orphanGrace": 900}`,
		"pauseReson":           `{"pauseReson": "typo"}`,
	}

	for key, signal := range cases {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			var update work.ConfigUpdate
			err := json.Unmarshal([]byte(signal), &update)
			if err == nil {
				t.Fatal("a misspelt key that decodes silently is an update that succeeds and changes nothing, " +
					"which a signal's sender cannot tell from one that worked")
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error %q does not name the offending key", err)
			}
		})
	}
}

func TestTheNewConfigKeysReadBackThroughTheirAccessors(t *testing.T) {
	t.Parallel()

	config := work.DefaultConfig()

	if got := config.PollInterval().Seconds(); int64(got) != config.PollIntervalSeconds {
		t.Fatalf("PollInterval = %vs, want %ds", got, config.PollIntervalSeconds)
	}
	if got := config.OrphanGrace().Seconds(); int64(got) != config.OrphanGraceSeconds {
		t.Fatalf("OrphanGrace = %vs, want %ds", got, config.OrphanGraceSeconds)
	}
}

func TestConfigRefusesIntervalsThatStopTheLoopDoingAnything(t *testing.T) {
	t.Parallel()

	cases := map[string]func(c *work.Config){
		"no poll interval": func(c *work.Config) { c.PollIntervalSeconds = 0 },
		"no orphan grace":  func(c *work.Config) { c.OrphanGraceSeconds = 0 },
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := work.DefaultConfig()
			breakIt(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("rejected rather than clamped: a dispatcher that never wakes, or a sweep with no floor")
			}
		})
	}
}
