package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x63616c/software-factory/internal/sf"
)

func addFactoryCommands(root *cobra.Command) {
	factory := &cobra.Command{
		Use:   "factory",
		Short: "Factory lifecycle controls",
	}
	root.AddCommand(factory)

	factoryStatus := &cobra.Command{
		Use:   "status",
		Args:  cobra.NoArgs,
		Short: "Show factory status summary",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				status, err := runtime.Actions.Status(ctx)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				switch runtime.Format {
				case sf.OutputFormatJSON:
					encoded, err := json.MarshalIndent(map[string]any{
						"total":     status.Total,
						"open":      status.Open,
						"active":    status.Active,
						"done":      status.Done,
						"failed":    status.Failed,
						"ready":     status.Ready,
						"not_ready": status.NotReady,
					}, "", "  ")
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(runtime.Out, string(encoded))
				default:
					_, _ = fmt.Fprintf(runtime.Out, "total=%d open=%d active=%d done=%d failed=%d ready=%d not-ready=%d\n", status.Total, status.Open, status.Active, status.Done, status.Failed, status.Ready, status.NotReady)
				}
				return nil
			})
		},
	}
	factory.AddCommand(factoryStatus)

	factoryPause := &cobra.Command{
		Use:   "pause",
		Args:  cobra.NoArgs,
		Short: "Pause dispatching",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				if err := runtime.Actions.PauseFactory(ctx); err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintln(runtime.Out, "dispatcher paused")
				return nil
			})
		},
	}
	factory.AddCommand(factoryPause)

	factoryResume := &cobra.Command{
		Use:   "resume",
		Args:  cobra.NoArgs,
		Short: "Resume dispatching",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				if err := runtime.Actions.ResumeFactory(ctx); err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintln(runtime.Out, "dispatcher resumed")
				return nil
			})
		},
	}
	factory.AddCommand(factoryResume)

	var maxInFlight int
	factoryMax := &cobra.Command{
		Use:   "set-max-in-flight <value>",
		Args:  cobra.ExactArgs(1),
		Short: "Set max in-flight run count",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			maxInFlight, err = parseIntArg(args[0], "max-in-flight")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				if err := runtime.Actions.SetMaxInFlight(ctx, maxInFlight); err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintf(runtime.Out, "max in-flight set to %d\n", maxInFlight)
				return nil
			})
		},
	}
	factory.AddCommand(factoryMax)
}
