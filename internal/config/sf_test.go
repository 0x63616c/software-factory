package config

import (
	"testing"
	"time"
)

func TestResolveSFContextUsesContextAndEnvOverrides(t *testing.T) {
	cfg := SFConfig{
		Version:       1,
		ActiveContext: "main",
		Contexts: map[string]SFContext{
			"main": {
				APIURL:      "https://cfg.example.com",
				BearerToken: "cfg-token",
				Output:      "json",
			},
		},
	}
	t.Setenv("SF_CONTEXT", "main")
	resolved, err := ResolveSFContext(cfg, SFClientOptions{})
	if err != nil {
		t.Fatalf("resolve context failed: %v", err)
	}
	if resolved.APIURL != "https://cfg.example.com" {
		t.Fatalf("unexpected api url: %q", resolved.APIURL)
	}
	if resolved.BearerToken != "cfg-token" {
		t.Fatalf("expected config bearer token, got %q", resolved.BearerToken)
	}
	if resolved.Output != OutputJSON {
		t.Fatalf("expected output json, got %q", resolved.Output)
	}
}

func TestResolveSFContextSupportsWideOutputAndAuthPrecedence(t *testing.T) {
	cfg := SFConfig{
		Version:       1,
		ActiveContext: "main",
		Contexts: map[string]SFContext{
			"main": {APIURL: "https://cfg.example.com", CfJwt: "cfg-cf"},
		},
	}
	t.Setenv("SF_OUTPUT", "wide")
	t.Setenv("SF_BEARER_TOKEN", "env-bearer")

	resolved, err := ResolveSFContext(cfg, SFClientOptions{ContextName: "main"})
	if err != nil {
		t.Fatalf("resolve context failed: %v", err)
	}
	if resolved.Output != OutputWide {
		t.Fatalf("expected wide output, got %q", resolved.Output)
	}
	if resolved.BearerToken != "env-bearer" {
		t.Fatalf("expected bearer token env override, got %q", resolved.BearerToken)
	}
}

func TestResolveSFContextRejectsInvalidOutput(t *testing.T) {
	cfg := SFConfig{
		Contexts: map[string]SFContext{
			"main": {APIURL: "https://cfg.example.com", BearerToken: "token"},
		},
		ActiveContext: "main",
	}
	if _, err := ResolveSFContext(cfg, SFClientOptions{Output: "bad"}); err == nil {
		t.Fatalf("expected invalid output validation error")
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/sf/config.json"
	t.Setenv("SF_CONFIG_PATH", "")
	_ = path
	cfg := SFConfig{Version: 1, ActiveContext: "main", Contexts: map[string]SFContext{"main": {APIURL: "https://cfg.example.com", BearerToken: "token"}}}
	if err := saveSFConfigAt(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	loaded, err := loadSFConfigAt(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.ActiveContext != "main" || len(loaded.Contexts) != 1 {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}
}

func TestResolvedTimeoutsAndPollIntervals(t *testing.T) {
	cfg := SFConfig{
		ActiveContext: "main",
		Contexts:      map[string]SFContext{"main": {APIURL: "http://example.com", PollInterval: "2s", Timeout: "3s", BearerToken: "token"}},
	}
	resolved, err := ResolveSFContext(cfg, SFClientOptions{})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.PollInterval != 2*time.Second {
		t.Fatalf("expected poll interval 2s, got %v", resolved.PollInterval)
	}
	if resolved.Timeout != 3*time.Second {
		t.Fatalf("expected timeout 3s, got %v", resolved.Timeout)
	}
}
