package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/0x63616c/software-factory/internal/sf"
)

func addRunCommands(root *cobra.Command) {
	run := &cobra.Command{
		Use:   "run",
		Short: "Run introspection commands",
	}
	root.AddCommand(run)

	var ticketIDRaw string
	runList := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List Runs by ticket id",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ticketID, err := parseIDArg(ticketIDRaw, "ticket-id")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				runs, err := runtime.Actions.ListTicketRuns(ctx, ticketID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				return sf.RenderRuns(runtime.Out, runs, runtime.Format)
			})
		},
	}
	runList.Flags().StringVar(&ticketIDRaw, "ticket", "", "Ticket id")
	runList.MarkFlagRequired("ticket")
	run.AddCommand(runList)

	var outRunFile string
	runGet := &cobra.Command{
		Use:   "get <ticket-id> <run-id>",
		Args:  cobra.ExactArgs(2),
		Short: "Show one Run",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			runID := strings.TrimSpace(args[1])
			if runID == "" {
				return fmt.Errorf("run-id is required")
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				run, err := runtime.Actions.GetRun(ctx, ticketID, runID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				return sf.RenderRuns(runtime.Out, []sf.RunOutput{run}, runtime.Format)
			})
		},
	}
	run.AddCommand(runGet)

	runTranscript := &cobra.Command{
		Use:   "transcript <ticket-id> <run-id> <attempt-no> <ordinal>",
		Args:  cobra.ExactArgs(4),
		Short: "Download run transcript from run/attempt",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			runID := strings.TrimSpace(args[1])
			attempt, err := strconv.Atoi(strings.TrimSpace(args[2]))
			if err != nil {
				return fmt.Errorf("parse attempt-no %q: %w", args[2], err)
			}
			ordinal, err := strconv.Atoi(strings.TrimSpace(args[3]))
			if err != nil {
				return fmt.Errorf("parse ordinal %q: %w", args[3], err)
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				if runID == "" {
					return fmt.Errorf("run-id is required")
				}
				if _, err := runtime.Actions.GetRun(ctx, ticketID, runID); err != nil {
					return err
				}
				content, err := runtime.Actions.Client.GetTranscript(ctx, ticketID, runID, ordinal, attempt)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				if outRunFile != "" {
					return os.WriteFile(outRunFile, content, 0o600)
				}
				_, _ = runtime.Out.Write(content)
				if !strings.HasSuffix(string(content), "\n") {
					_, _ = fmt.Fprintln(runtime.Out)
				}
				return nil
			})
		},
	}
	runTranscript.Flags().StringVar(&outRunFile, "out", "", "Write transcript to file")
	run.AddCommand(runTranscript)
}
