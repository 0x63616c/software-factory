package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/0x63616c/software-factory/internal/config"
)

func addContextCommands(root *cobra.Command) {
	contexts := &cobra.Command{
		Use:   "context",
		Short: "Inspect or edit sf contexts",
	}
	root.AddCommand(contexts)

	contexts.AddCommand(&cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List configured contexts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadSFConfig()
			if err != nil {
				return err
			}
			for _, name := range config.ListContextNames(cfg) {
				prefix := ""
				if name == cfg.ActiveContext {
					prefix = "*"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s%s\n", prefix, name)
			}
			return nil
		},
	})

	contexts.AddCommand(&cobra.Command{
		Use:   "use <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Set the active context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadSFConfig()
			if err != nil {
				return err
			}
			target := strings.TrimSpace(args[0])
			if _, found := cfg.Contexts[target]; !found {
				return fmt.Errorf("context %q is not configured", target)
			}
			cfg.ActiveContext = target
			if err := config.SaveSFConfig(cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "active context set to %s\n", target)
			return nil
		},
	})

	contextSet := &cobra.Command{
		Use:   "set <name> [api_url=... cf_jwt=... bearer=... bearer_token=... timeout=... poll_interval=... output=...]",
		Args:  cobra.MinimumNArgs(1),
		Short: "Create or overwrite one context",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("context name is required")
			}
			cfg, err := config.LoadSFConfig()
			if err != nil {
				return err
			}
			if cfg.Contexts == nil {
				cfg.Contexts = map[string]config.SFContext{}
			}
			current := cfg.Contexts[name]
			if len(args) > 1 {
				updates, parseErr := parseContextAssignments(args[1:])
				if parseErr != nil {
					return parseErr
				}
				if updates.APIURL != "" {
					current.APIURL = updates.APIURL
				}
				if updates.CfJwt != "" {
					current.CfJwt = updates.CfJwt
				}
				if updates.BearerToken != "" {
					current.BearerToken = updates.BearerToken
				}
				if updates.Timeout != "" {
					current.Timeout = updates.Timeout
				}
				if updates.PollInterval != "" {
					current.PollInterval = updates.PollInterval
				}
				if updates.Output != "" {
					current.Output = updates.Output
				}
			}
			cfg.Contexts[name] = current
			if cfg.ActiveContext == "" {
				cfg.ActiveContext = name
			}
			if err := config.SaveSFConfig(cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "context %s saved\n", name)
			return nil
		},
	}
	contexts.AddCommand(contextSet)

	contextView := &cobra.Command{
		Use:   "view",
		Args:  cobra.NoArgs,
		Short: "Show resolved runtime configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadSFConfig()
			if err != nil {
				return err
			}
			resolved, err := resolveSFContext(cfg)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "name=%s\napi_url=%s\noutput=%s\ntimeout=%s\npoll_interval=%s\n", resolved.Name, resolved.APIURL, resolved.Output, resolved.Timeout, resolved.PollInterval)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "has_cf_jwt=%v\nhas_bearer=%v\n", resolved.CfJwt != "", resolved.BearerToken != "")
			return nil
		},
	}
	contexts.AddCommand(contextView)
}

type sfContextOverrides struct {
	APIURL       string
	CfJwt        string
	BearerToken  string
	Timeout      string
	PollInterval string
	Output       string
}

func parseContextAssignments(raw []string) (sfContextOverrides, error) {
	var out sfContextOverrides
	for _, token := range raw {
		key, value, ok := strings.Cut(strings.TrimSpace(token), "=")
		if !ok {
			return sfContextOverrides{}, fmt.Errorf("context assignment must be key=value: %q", token)
		}
		if key == "" {
			return sfContextOverrides{}, fmt.Errorf("context assignment key is empty in %q", token)
		}
		switch key {
		case "api_url", "cf_jwt", "bearer", "bearer_token", "timeout", "poll_interval", "poll-interval", "output":
			if value == "" {
				return sfContextOverrides{}, fmt.Errorf("context value for %s is required", key)
			}
			switch key {
			case "api_url":
				out.APIURL = value
			case "cf_jwt":
				out.CfJwt = value
			case "bearer", "bearer_token":
				out.BearerToken = value
			case "timeout":
				out.Timeout = value
			case "poll_interval", "poll-interval":
				out.PollInterval = value
			case "output":
				out.Output = value
			}
		default:
			return sfContextOverrides{}, fmt.Errorf("unknown context key %q", key)
		}
	}
	return out, nil
}
