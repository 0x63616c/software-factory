package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/sf"
)

var (
	rootContextName  string
	rootAPIURL       string
	rootCfJWT        string
	rootBearerToken  string
	rootOutput       string
	rootTimeout      string
	rootPollInterval string
	rootNoColor      bool
)

type sfRuntime struct {
	Actions      *sf.Actions
	Config       config.SFResolvedConfig
	Format       sf.OutputFormat
	PollInterval time.Duration
	Timeout      time.Duration
	Out          io.Writer
	Err          io.Writer
}

func sfExitCodeOrDefault(err error, fallback int) int {
	if err == nil {
		return 0
	}
	var apiErr sf.APIError
	if errors.As(err, &apiErr) {
		return sf.ExitCode(apiErr)
	}
	return fallback
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "sf",
		Short:         "Software Factory CLI and TUI",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&rootContextName, "context", "", "Active sf context")
	root.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Software Factory API URL")
	root.PersistentFlags().StringVar(&rootCfJWT, "cf-jwt", "", "Cloudflare Access JWT assertion token")
	root.PersistentFlags().StringVar(&rootBearerToken, "bearer-token", "", "Authorization bearer token")
	root.PersistentFlags().StringVar(&rootOutput, "output", "", "Output format: table, json, yaml, wide")
	root.PersistentFlags().StringVar(&rootTimeout, "timeout", "", "Request timeout duration (for example 10s)")
	root.PersistentFlags().StringVar(&rootPollInterval, "poll-interval", "", "TUI refresh interval (for example 2s)")
	root.PersistentFlags().BoolVar(&rootNoColor, "no-color", false, "Reserved for future support")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print sf version",
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildVersion)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Environment-first authentication helper (sf uses token environment variables in v0)",
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "v0 login is env-first")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Use --bearer-token, --cf-jwt, or environment variables:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "SF_BEARER_TOKEN or SF_CF_ACCESS_JWT")
		},
	})

	completionCmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Args:  cobra.ExactArgs(1),
		Short: "Generate shell completion scripts",
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := strings.TrimSpace(args[0])
			var err error
			switch shell {
			case "bash":
				err = root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				err = root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				err = root.GenFishCompletion(cmd.OutOrStdout(), true)
			default:
				return fmt.Errorf("unsupported shell %q (expected bash, zsh, or fish)", shell)
			}
			return err
		},
	}
	root.AddCommand(completionCmd)

	addTicketCommands(root)
	addFactoryCommands(root)
	addRunCommands(root)
	addContextCommands(root)
	addTUICommand(root)

	configCmd := &cobra.Command{Use: "config", Short: "Inspect resolved configuration"}
	configCmd.AddCommand(&cobra.Command{
		Use:   "view",
		Args:  cobra.NoArgs,
		Short: "Print resolved runtime configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadSFConfig()
			if err != nil {
				return err
			}
			resolved, err := resolveSFContext(cfg)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "context=%s\napi_url=%s\noutput=%s\npoll_interval=%s\ntimeout=%s\n", resolved.Name, resolved.APIURL, resolved.Output, resolved.PollInterval, resolved.Timeout)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "has_cf_jwt=%v\nhas_bearer=%v\n", resolved.CfJwt != "", resolved.BearerToken != "")
			return nil
		},
	})
	root.AddCommand(configCmd)
	return root
}

func buildRuntime(cmd *cobra.Command) (*sfRuntime, error) {
	cfg, err := config.LoadSFConfig()
	if err != nil {
		return nil, fmt.Errorf("loading sf configuration: %w", err)
	}
	resolved, err := resolveSFContext(cfg)
	if err != nil {
		return nil, err
	}
	credentialedClient, err := sf.NewClient(resolved.APIURL, sf.Credentials{
		CfAccessToken: resolved.CfJwt,
		BearerToken:   resolved.BearerToken,
	}, resolved.Timeout, &http.Client{})
	if err != nil {
		return nil, err
	}
	return &sfRuntime{
		Actions:      &sf.Actions{Client: credentialedClient},
		Config:       resolved,
		Format:       sf.ParseOutputFormat(string(resolved.Output)),
		PollInterval: resolved.PollInterval,
		Timeout:      resolved.Timeout,
		Out:          cmd.OutOrStdout(),
		Err:          cmd.ErrOrStderr(),
	}, nil
}

func resolveSFContext(cfg config.SFConfig) (config.SFResolvedConfig, error) {
	return config.ResolveSFContext(cfg, config.SFClientOptions{
		ContextName:  rootContextName,
		APIURL:       rootAPIURL,
		CfJwt:        rootCfJWT,
		BearerToken:  rootBearerToken,
		Output:       rootOutput,
		Timeout:      rootTimeout,
		PollInterval: rootPollInterval,
	})
}

func withRuntime(cmd *cobra.Command, fn func(context.Context, *sfRuntime) error) error {
	runtime, err := buildRuntime(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtime.Config.Timeout)
	defer cancel()
	return fn(ctx, runtime)
}

func writeError(writer io.Writer, err error) error {
	var apiErr sf.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Reason != "" {
			_, writeErr := fmt.Fprintf(writer, "sf %s: %v\n", apiErr.Reason, apiErr.Detail)
			if writeErr != nil {
				return writeErr
			}
			return err
		}
	}
	_, writeErr := fmt.Fprintf(writer, "sf: %v\n", err)
	if writeErr != nil {
		return writeErr
	}
	return err
}

func parseIDArg(raw string, field string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", field, raw, err)
	}
	return parsed, nil
}

func parseIntArg(raw string, field string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", field, raw, err)
	}
	return value, nil
}

func resolveOutputFormat(base sf.OutputFormat, useJSON bool, useYAML bool, useWide bool) (sf.OutputFormat, error) {
	selected := 0
	if useJSON {
		selected++
	}
	if useYAML {
		selected++
	}
	if useWide {
		selected++
	}
	if selected > 1 {
		return base, fmt.Errorf("only one output mode is allowed")
	}
	if useJSON {
		return sf.OutputFormatJSON, nil
	}
	if useYAML {
		return sf.OutputFormatYAML, nil
	}
	if useWide {
		return sf.OutputFormatWide, nil
	}
	return base, nil
}
