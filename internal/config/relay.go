package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	envRelayListenAddr  = "LISTEN_ADDR"
	envRelayMetricsAddr = "METRICS_ADDR"
	envRelaySecret      = "GITHUB_BOT_APP__WEBHOOK_SECRET"
	envRelayTargets     = "RELAY_TARGETS"
)

// Relay is the complete, parsed startup configuration for the webhook relay.
type Relay struct {
	ListenAddr    string
	MetricsAddr   string
	WebhookSecret []byte
	Targets       []RelayTarget
}

// RelayTarget is one named in-cluster webhook consumer.
type RelayTarget struct {
	Name string
	URL  string
}

// LoadRelay reads and parses the relay's complete environment once at startup.
func LoadRelay() (Relay, error) {
	cfg := Relay{
		ListenAddr:    os.Getenv(envRelayListenAddr),
		MetricsAddr:   os.Getenv(envRelayMetricsAddr),
		WebhookSecret: []byte(os.Getenv(envRelaySecret)),
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return Relay{}, fmt.Errorf("%s is required: the public relay listener", envRelayListenAddr)
	}
	if strings.TrimSpace(cfg.MetricsAddr) == "" {
		return Relay{}, fmt.Errorf("%s is required: the Prometheus listener", envRelayMetricsAddr)
	}
	if len(cfg.WebhookSecret) == 0 {
		return Relay{}, fmt.Errorf("%s is required: the GitHub HMAC secret", envRelaySecret)
	}
	if err := json.Unmarshal([]byte(os.Getenv(envRelayTargets)), &cfg.Targets); err != nil {
		return Relay{}, fmt.Errorf("%s must be a JSON target list: %w", envRelayTargets, err)
	}
	if len(cfg.Targets) == 0 {
		return Relay{}, fmt.Errorf("%s must contain at least one target", envRelayTargets)
	}
	seen := make(map[string]struct{}, len(cfg.Targets))
	for index, target := range cfg.Targets {
		if strings.TrimSpace(target.Name) == "" {
			return Relay{}, fmt.Errorf("%s target %d has an empty name", envRelayTargets, index)
		}
		if _, ok := seen[target.Name]; ok {
			return Relay{}, fmt.Errorf("%s has duplicate target name %q", envRelayTargets, target.Name)
		}
		seen[target.Name] = struct{}{}
		parsed, err := url.ParseRequestURI(target.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Relay{}, fmt.Errorf("%s target %q has invalid url %q", envRelayTargets, target.Name, target.URL)
		}
	}
	return cfg, nil
}
