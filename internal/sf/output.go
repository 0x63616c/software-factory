// Package sf output rendering helpers.
package sf

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// RenderTickets prints ticket summaries in the selected output mode.
func RenderTickets(writer io.Writer, tickets []TicketSummary, format OutputFormat) error {
	switch format {
	case OutputFormatJSON:
		encoded, err := json.MarshalIndent(map[string][]TicketSummary{"tickets": tickets}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(writer, string(encoded))
		return err
	case OutputFormatYAML:
		encoded, err := yaml.Marshal(map[string][]TicketSummary{"tickets": tickets})
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(writer, string(encoded))
		return err
	case OutputFormatTable, OutputFormatWide:
		table := tabwriter.NewWriter(writer, 0, 2, 2, ' ', 0)
		_, err := fmt.Fprintln(table, "ID\tSTATE\tREADY\tTITLE\tUPDATED")
		if err != nil {
			return err
		}
		for _, ticket := range tickets {
			ready := "false"
			if ticket.Ready {
				ready = "true"
			}
			_, err = fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\n", ticket.ID, ticket.State, ready, ticket.Title, ticket.UpdatedAt)
			if err != nil {
				return err
			}
		}
		return table.Flush()
	default:
		return nil
	}
}

// RenderTicket prints one ticket in the selected output mode.
func RenderTicket(writer io.Writer, ticket TicketResponse, format OutputFormat) error {
	switch format {
	case OutputFormatJSON:
		encoded, err := json.MarshalIndent(ticket, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(writer, string(encoded))
		return err
	case OutputFormatYAML:
		encoded, err := yaml.Marshal(ticket)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(writer, string(encoded))
		return err
	case OutputFormatTable, OutputFormatWide:
		_, err := fmt.Fprintf(writer, "ID:\t%d\n", ticket.ID)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "Title:\t%s\n", ticket.Title)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "State:\t%s\n", ticket.State)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "Ready:\t%t\n", ticket.Ready)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "Created:\t%s\n", ticket.CreatedAt)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "Updated:\t%s\n", ticket.UpdatedAt)
		if err != nil {
			return err
		}
		if len(ticket.Blockers) > 0 {
			blockers := make([]string, 0, len(ticket.Blockers))
			for _, blocker := range ticket.Blockers {
				blockers = append(blockers, fmt.Sprintf("%d", blocker.ID))
			}
			_, err = fmt.Fprintf(writer, "Blockers:\t%s\n", strings.Join(blockers, ", "))
			if err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(writer, "Body:\n%s\n", ticket.Body)
		return err
	default:
		return nil
	}
}

// RenderRuns prints ticket runs in a compact table.
func RenderRuns(writer io.Writer, runs []RunOutput, format OutputFormat) error {
	switch format {
	case OutputFormatJSON:
		encoded, err := json.MarshalIndent(map[string][]RunOutput{"runs": runs}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(writer, string(encoded))
		return err
	case OutputFormatYAML:
		encoded, err := yaml.Marshal(map[string][]RunOutput{"runs": runs})
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(writer, string(encoded))
		return err
	case OutputFormatTable, OutputFormatWide:
		table := tabwriter.NewWriter(writer, 0, 2, 2, ' ', 0)
		_, err := fmt.Fprintln(table, "RUN_ID\tSTATE\tOUTCOME\tPHASE\tSTARTED")
		if err != nil {
			return err
		}
		for _, run := range runs {
			state := "terminal"
			if run.Active {
				state = "active"
			}
			_, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", run.ID, state, run.Outcome, run.Phase, run.StartedAt)
			if err != nil {
				return err
			}
		}
		return table.Flush()
	default:
		return nil
	}
}
