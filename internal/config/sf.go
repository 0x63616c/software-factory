package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	cliContextEnv      = "SF_CONTEXT"
	cliAPIURLEnv       = "SF_API_URL"
	cliCfJWTEv         = "SF_CF_ACCESS_JWT"
	cliBearerTokenEnv  = "SF_BEARER_TOKEN"
	cliOutputEnv       = "SF_OUTPUT"
	cliTimeoutEnv      = "SF_TIMEOUT"
	cliPollIntervalEnv = "SF_POLL_INTERVAL"
)

// SFContext describes one API context for the sf command surface.
type SFContext struct {
	APIURL       string `json:"api_url"`
	CfJwt        string `json:"cf_jwt,omitempty"`
	BearerToken  string `json:"bearer_token,omitempty"`
	Timeout      string `json:"timeout,omitempty"`
	PollInterval string `json:"poll_interval,omitempty"`
	Output       string `json:"output,omitempty"`
}

// SFConfig is the file-backed CLI config for `sf`.
type SFConfig struct {
	Version       int                  `json:"version"`
	ActiveContext string               `json:"active_context"`
	Contexts      map[string]SFContext `json:"contexts"`
}

// SFClientOptions contains runtime overrides.
type SFClientOptions struct {
	ContextName  string
	APIURL       string
	CfJwt        string
	BearerToken  string
	Output       string
	Timeout      string
	PollInterval string
}

// SFResolvedConfig is a normalized context after CLI and environment resolution.
type SFResolvedConfig struct {
	Name         string
	APIURL       string
	CfJwt        string
	BearerToken  string
	Timeout      time.Duration
	PollInterval time.Duration
	Output       Output
}

// Output is the resolved output mode from config/env/flag values.
type Output string

// LoadSFConfig reads persisted contexts from the default config location.
func LoadSFConfig() (SFConfig, error) {
	path, err := sfConfigPath()
	if err != nil {
		return SFConfig{}, err
	}
	return loadSFConfigAt(path)
}

// SaveSFConfig persists command configuration.
func SaveSFConfig(cfg SFConfig) error {
	path, err := sfConfigPath()
	if err != nil {
		return err
	}
	return saveSFConfigAt(path, cfg)
}

// ListContextNames returns context names in lexicographic order.
func ListContextNames(cfg SFConfig) []string {
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolveSFContext returns merged CLI + env overrides from config values.
func ResolveSFContext(base SFConfig, options SFClientOptions) (SFResolvedConfig, error) {
	if len(base.Contexts) == 0 {
		base.Contexts = map[string]SFContext{}
	}
	name := strings.TrimSpace(options.ContextName)
	if name == "" {
		name = strings.TrimSpace(os.Getenv(cliContextEnv))
	}
	if name == "" {
		name = base.ActiveContext
	}
	if name == "" && len(base.Contexts) == 1 {
		for contextName := range base.Contexts {
			name = contextName
		}
	}

	contextValue, found := base.Contexts[name]
	if !found {
		if name != "" {
			return SFResolvedConfig{}, fmt.Errorf("sf context %q is not configured", name)
		}
		contextValue = SFContext{}
		name = "default"
	}

	apiURL := firstSet(options.APIURL, os.Getenv(cliAPIURLEnv), contextValue.APIURL)
	if strings.TrimSpace(apiURL) == "" {
		return SFResolvedConfig{}, fmt.Errorf("sf api url is required (set --api-url, SF_API_URL, or sf context api_url)")
	}
	cfJwt := firstSet(options.CfJwt, os.Getenv(cliCfJWTEv), contextValue.CfJwt)
	bearer := firstSet(options.BearerToken, os.Getenv(cliBearerTokenEnv), contextValue.BearerToken)
	if strings.TrimSpace(cfJwt) == "" && strings.TrimSpace(bearer) == "" {
		return SFResolvedConfig{}, fmt.Errorf("sf auth is required: one of --bearer-token, --cf-jwt, SF_BEARER_TOKEN, or SF_CF_ACCESS_JWT")
	}

	pollInterval := firstSet(options.PollInterval, os.Getenv(cliPollIntervalEnv), contextValue.PollInterval)
	pollValue := 10 * time.Second
	if pollInterval != "" {
		parsed, parseErr := time.ParseDuration(pollInterval)
		if parseErr != nil {
			return SFResolvedConfig{}, fmt.Errorf("parsing SF_POLL_INTERVAL: %w", parseErr)
		}
		pollValue = parsed
	}

	timeout := firstSet(options.Timeout, os.Getenv(cliTimeoutEnv), contextValue.Timeout)
	timeoutValue := 10 * time.Second
	if timeout != "" {
		parsed, parseErr := time.ParseDuration(timeout)
		if parseErr != nil {
			return SFResolvedConfig{}, fmt.Errorf("parsing timeout: %w", parseErr)
		}
		timeoutValue = parsed
	}

	outputRaw := strings.ToLower(strings.TrimSpace(firstSet(options.Output, os.Getenv(cliOutputEnv), contextValue.Output)))
	output := Output(outputRaw)
	if output == "" {
		output = OutputTable
	}
	if !isValidOutput(output) {
		return SFResolvedConfig{}, fmt.Errorf("sf output %q is invalid; use table, json, yaml, or wide", output)
	}
	return SFResolvedConfig{
		Name:         name,
		APIURL:       strings.TrimSpace(apiURL),
		CfJwt:        strings.TrimSpace(cfJwt),
		BearerToken:  strings.TrimSpace(bearer),
		Timeout:      timeoutValue,
		PollInterval: pollValue,
		Output:       output,
	}, nil
}

// Output values used by the CLI.
const (
	OutputTable = Output("table")
	OutputJSON  = Output("json")
	OutputYAML  = Output("yaml")
	OutputWide  = Output("wide")
)

func isValidOutput(output Output) bool {
	switch output {
	case OutputTable, OutputJSON, OutputYAML, OutputWide:
		return true
	default:
		return false
	}
}

func firstSet(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sfConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "sf", "config.json"), nil
}

func loadSFConfigAt(path string) (SFConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SFConfig{Version: 1, Contexts: map[string]SFContext{}}, nil
		}
		return SFConfig{}, fmt.Errorf("read sf config from %s: %w", path, err)
	}
	var cfg SFConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return SFConfig{}, fmt.Errorf("parse sf config in %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]SFContext{}
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	return cfg, nil
}

func saveSFConfigAt(path string, cfg SFConfig) error {
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sf config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create sf config directory: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write sf config to %s: %w", path, err)
	}
	return nil
}
