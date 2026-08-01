package config

import (
	"strings"
	"testing"
)

func TestLoadRelayParsesNamedTargets(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":8080")
	t.Setenv("METRICS_ADDR", ":9464")
	t.Setenv("GITHUB_BOT_APP__WEBHOOK_SECRET", "test-secret")
	t.Setenv("RELAY_TARGETS", `[{"name":"control-center","url":"http://api.control-center.svc.cluster.local:4201/hooks/github"}]`)

	cfg, err := LoadRelay()
	if err != nil {
		t.Fatalf("LoadRelay: %v", err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "control-center" {
		t.Fatalf("Targets = %+v, want the named control-center target", cfg.Targets)
	}
}

func TestLoadRelayRejectsDuplicateTargetNames(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":8080")
	t.Setenv("METRICS_ADDR", ":9464")
	t.Setenv("GITHUB_BOT_APP__WEBHOOK_SECRET", "test-secret")
	t.Setenv("RELAY_TARGETS", `[{"name":"same","url":"http://one.example"},{"name":"same","url":"http://two.example"}]`)

	_, err := LoadRelay()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("LoadRelay error = %v, want duplicate target error", err)
	}
}

func TestLoadRelayRejectsMissingAndMalformedConfiguration(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":8080")
	t.Setenv("METRICS_ADDR", ":9464")
	t.Setenv("GITHUB_BOT_APP__WEBHOOK_SECRET", "test-secret")
	t.Setenv("RELAY_TARGETS", "not json")

	_, err := LoadRelay()
	if err == nil || !strings.Contains(err.Error(), "RELAY_TARGETS") {
		t.Fatalf("LoadRelay error = %v, want RELAY_TARGETS error", err)
	}
}
