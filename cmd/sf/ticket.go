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

func addTicketCommands(root *cobra.Command) {
	ticket := &cobra.Command{
		Use:   "ticket",
		Short: "Manage Tickets from the API",
	}
	root.AddCommand(ticket)

	var (
		listState        string
		listReady        string
		listTitleContain string
		listJSON         bool
		listYAML         bool
		listWide         bool
	)
	ticketList := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List Tickets from the console snapshot",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				format, err := resolveOutputFormat(runtime.Format, listJSON, listYAML, listWide)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				if listReady != "" && listReady != "true" && listReady != "false" {
					return fmt.Errorf("invalid --ready value %q; use true or false", listReady)
				}
				tickets, err := runtime.Actions.ListTickets(ctx, listState, listReady)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				filtered := filterTicketsByTitle(tickets, listTitleContain)
				return sf.RenderTickets(runtime.Out, filtered, format)
			})
		},
	}
	ticketList.Flags().StringVar(&listState, "state", "", "Filter by state")
	ticketList.Flags().StringVar(&listReady, "ready", "", "Filter by readiness: true|false")
	ticketList.Flags().StringVar(&listTitleContain, "title-contains", "", "Filter by title substring")
	ticketList.Flags().BoolVar(&listJSON, "json", false, "JSON output")
	ticketList.Flags().BoolVar(&listYAML, "yaml", false, "YAML output")
	ticketList.Flags().BoolVar(&listWide, "wide", false, "Wide table output")
	ticket.AddCommand(ticketList)

	ticketGet := &cobra.Command{
		Use:   "get <ticket-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Read one Ticket",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				ticket, err := runtime.Actions.GetTicket(ctx, ticketID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				return sf.RenderTicket(runtime.Out, ticket, runtime.Format)
			})
		},
	}
	ticket.AddCommand(ticketGet)

	var createTitle string
	var createBody string
	var createBodyFile string
	ticketCreate := &cobra.Command{
		Use:   "create [<title> [body]]",
		Args:  cobra.MaximumNArgs(2),
		Short: "Create a Ticket",
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.TrimSpace(createTitle)
			if title == "" {
				if len(args) >= 1 {
					title = strings.TrimSpace(args[0])
				}
			}
			if title == "" {
				return fmt.Errorf("ticket title is required")
			}
			body := strings.TrimSpace(createBody)
			if body == "" {
				if len(args) >= 2 {
					body = args[1]
				}
			}
			resolvedBody := body
			if createBodyFile != "" && strings.TrimSpace(body) != "" {
				return fmt.Errorf("use either --body or --body-file, not both")
			}
			if strings.HasPrefix(body, "@") {
				payload, err := os.ReadFile(strings.TrimPrefix(body, "@"))
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				resolvedBody = string(payload)
			}
			if createBodyFile != "" {
				payload, err := os.ReadFile(createBodyFile)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				resolvedBody = string(payload)
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				ticket, err := runtime.Actions.CreateTicket(ctx, title, resolvedBody, nil)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				return sf.RenderTicket(runtime.Out, ticket, runtime.Format)
			})
		},
	}
	ticketCreate.Flags().StringVar(&createTitle, "title", "", "Ticket title")
	ticketCreate.Flags().StringVar(&createBody, "body", "", "Ticket body, use @file syntax for file input")
	ticketCreate.Flags().StringVar(&createBodyFile, "body-file", "", "Read ticket body from a file")
	ticket.AddCommand(ticketCreate)

	ticketWork := &cobra.Command{
		Use:   "work <ticket-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Request immediate work on one Ticket",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				err = runtime.Actions.WorkTicket(ctx, ticketID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintf(runtime.Out, "work requested for %d\n", ticketID)
				return nil
			})
		},
	}
	ticket.AddCommand(ticketWork)

	ticketCancel := &cobra.Command{
		Use:   "cancel <ticket-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Request cancellation for one Ticket run",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				err = runtime.Actions.CancelTicket(ctx, ticketID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintf(runtime.Out, "cancel requested for %d\n", ticketID)
				return nil
			})
		},
	}
	ticket.AddCommand(ticketCancel)

	var setState string
	ticketSetState := &cobra.Command{
		Use:   "set-state <ticket-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Set Ticket state",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			if setState == "" {
				return fmt.Errorf("--state is required")
			}
			state := strings.ToLower(strings.TrimSpace(setState))
			if !sf.IsValidTicketState(state) {
				return sf.ErrInvalidTicketState{State: state}
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				updated, err := runtime.Actions.SetTicketState(ctx, ticketID, state)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				return sf.RenderTicket(runtime.Out, updated, runtime.Format)
			})
		},
	}
	ticketSetState.Flags().StringVar(&setState, "state", "", "open, active, failed, done")
	ticket.AddCommand(ticketSetState)

	blockers := &cobra.Command{Use: "blockers", Short: "Manage Ticket blockers"}
	ticket.AddCommand(blockers)

	blockerAdd := &cobra.Command{
		Use:   "add <ticket-id> <blocker-ticket-id>",
		Args:  cobra.ExactArgs(2),
		Short: "Add a blocker edge",
		RunE: func(cmd *cobra.Command, args []string) error {
			blocked, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			blockerID, err := parseIDArg(args[1], "blocker-ticket-id")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				err := runtime.Actions.AddBlocker(ctx, blocked, blockerID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintf(runtime.Out, "%d blocked by %d\n", blocked, blockerID)
				return nil
			})
		},
	}
	blockers.AddCommand(blockerAdd)

	blockerRemove := &cobra.Command{
		Use:   "remove <ticket-id> <blocker-ticket-id>",
		Args:  cobra.ExactArgs(2),
		Short: "Remove a blocker edge",
		RunE: func(cmd *cobra.Command, args []string) error {
			blocked, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			blockerID, err := parseIDArg(args[1], "blocker-ticket-id")
			if err != nil {
				return err
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				err := runtime.Actions.RemoveBlocker(ctx, blocked, blockerID)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				_, _ = fmt.Fprintf(runtime.Out, "removed blocker %d from %d\n", blockerID, blocked)
				return nil
			})
		},
	}
	blockers.AddCommand(blockerRemove)

	var transcriptOut string
	ticketTranscript := &cobra.Command{
		Use:   "transcript <ticket-id> <run-id> <attempt-no> <ordinal>",
		Args:  cobra.ExactArgs(4),
		Short: "Download one Attempt transcript",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
			if err != nil {
				return err
			}
			runID := args[1]
			attemptNo, err := strconv.Atoi(strings.TrimSpace(args[2]))
			if err != nil {
				return fmt.Errorf("parse attempt-no %q: %w", args[2], err)
			}
			ordinal, err := strconv.Atoi(strings.TrimSpace(args[3]))
			if err != nil {
				return fmt.Errorf("parse ordinal %q: %w", args[3], err)
			}
			return withRuntime(cmd, func(ctx context.Context, runtime *sfRuntime) error {
				content, err := runtime.Actions.Client.GetTranscript(ctx, ticketID, runID, ordinal, attemptNo)
				if err != nil {
					return writeError(cmd.ErrOrStderr(), err)
				}
				if transcriptOut != "" {
					return os.WriteFile(transcriptOut, content, 0o600)
				}
				_, _ = runtime.Out.Write(content)
				if !strings.HasSuffix(string(content), "\n") {
					_, _ = fmt.Fprintln(runtime.Out)
				}
				return nil
			})
		},
	}
	ticketTranscript.Flags().StringVar(&transcriptOut, "out", "", "Write transcript to file")
	ticket.AddCommand(ticketTranscript)

	ticketRuns := &cobra.Command{
		Use:   "runs <ticket-id>",
		Args:  cobra.ExactArgs(1),
		Short: "List all runs for a Ticket",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticketID, err := parseIDArg(args[0], "ticket-id")
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
	ticket.AddCommand(ticketRuns)

	ticketGetRun := &cobra.Command{
		Use:   "run <ticket-id> <run-id>",
		Args:  cobra.ExactArgs(2),
		Short: "Show one run for one Ticket",
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
	ticket.AddCommand(ticketGetRun)
}

func filterTicketsByTitle(tickets []sf.TicketSummary, query string) []sf.TicketSummary {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return tickets
	}
	filtered := make([]sf.TicketSummary, 0, len(tickets))
	for _, ticket := range tickets {
		if strings.Contains(strings.ToLower(ticket.Title), query) {
			filtered = append(filtered, ticket)
		}
	}
	return filtered
}
