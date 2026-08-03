// Package tui provides the interactive Software Factory terminal client.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/sf"
)

type modelMode int

const (
	modeNormal modelMode = iota
	modeHelp
	modeFilter
	modeCommand
	modeSetState
)

type model struct {
	actions      *sf.Actions
	timeout      time.Duration
	pollInterval time.Duration
	clock        clock.Clock
	width        int
	height       int

	footer      string
	mode        modelMode
	inputText   string
	quitConfirm bool
	quitUntil   time.Time
	loading     bool

	tickets        []sf.TicketSummary
	filtered       []sf.TicketSummary
	selected       int
	selectedTicket *sf.TicketResponse
	runs           []sf.RunOutput
	selectedRunID  string

	filterTerm string
}

const (
	defaultFooter     = "h help | ^C quit"
	quitConfirmWindow = 2 * time.Second
)

type snapshotMsg struct {
	tickets []sf.TicketSummary
	err     error
}

type ticketDetailMsg struct {
	ticket sf.TicketResponse
	runs   []sf.RunOutput
	err    error
}

type actionMsg struct {
	message string
	err     error
}

type tickMsg struct{}

// Run starts the interactive terminal client until the user quits or it fails.
func Run(actions *sf.Actions, pollInterval time.Duration, timeout time.Duration, output io.Writer) error {
	if output == nil {
		output = os.Stdout
	}
	program := tea.NewProgram(
		newModel(actions, pollInterval, timeout, clock.System{}),
		tea.WithInput(os.Stdin),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	_, err := program.Run()
	return err
}

func newModel(actions *sf.Actions, pollInterval, timeout time.Duration, clk clock.Clock) model {
	return model{
		actions:      actions,
		timeout:      timeout,
		pollInterval: pollInterval,
		clock:        clk,
		footer:       defaultFooter,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchSnapshot(m.actions, m.timeout), poll(m.pollInterval))
}

func fetchSnapshot(actions *sf.Actions, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		tickets, err := actions.ConsoleTickets(ctx)
		return snapshotMsg{tickets: tickets, err: err}
	}
}

func fetchTicketDetails(actions *sf.Actions, timeout time.Duration, ticketID int64) tea.Cmd {
	return func() tea.Msg {
		if ticketID <= 0 {
			return ticketDetailMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var zero ticketDetailMsg
		ticket, err := actions.GetTicket(ctx, ticketID)
		if err != nil {
			return ticketDetailMsg{err: err}
		}
		runs, runErr := actions.ListTicketRuns(ctx, ticketID)
		if runErr != nil {
			zero.ticket = ticket
			zero.err = runErr
			return zero
		}
		return ticketDetailMsg{ticket: ticket, runs: runs}
	}
}

func poll(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(_ time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tickMsg:
		if m.quitConfirm && !m.quitUntil.IsZero() && m.clock.Now().After(m.quitUntil) {
			m.quitConfirm = false
			m.footer = defaultFooter
		}
		m.loading = true
		return m, fetchSnapshot(m.actions, m.timeout)
	case snapshotMsg:
		m.loading = false
		if msg.err != nil {
			m.footer = "error: " + msg.err.Error()
			return m, poll(m.pollInterval)
		}
		m.tickets = sortTickets(msg.tickets)
		m.applyFilter()
		if m.selected >= len(m.filtered) {
			m.selected = max(0, len(m.filtered)-1)
		}
		m.footer = defaultFooter
		if len(m.filtered) == 0 {
			m.selectedTicket = nil
			m.runs = nil
			m.selectedRunID = ""
			return m, poll(m.pollInterval)
		}
		return m, tea.Batch(fetchTicketDetails(m.actions, m.timeout, m.selectedTicketID()), poll(m.pollInterval))
	case ticketDetailMsg:
		if msg.err != nil {
			m.footer = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.ticket.ID != 0 {
			copied := msg.ticket
			m.selectedTicket = &copied
		}
		if msg.runs != nil {
			m.runs = msg.runs
		}
		if m.selectedRunID == "" && len(m.runs) > 0 {
			m.selectedRunID = m.runs[0].ID
		}
		return m, nil
	case actionMsg:
		m.footer = msg.message
		if msg.err != nil {
			m.footer = "error: " + msg.err.Error()
		}
		return m, fetchSnapshot(m.actions, m.timeout)
	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *model) appendInput(key tea.KeyMsg) {
	if len(key.String()) == 1 {
		m.inputText += key.String()
	}
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.mode == modeHelp {
		m.mode = modeNormal
		m.footer = defaultFooter
		return nil
	}

	if m.mode == modeFilter {
		return m.handleFilterMode(msg)
	}
	if m.mode == modeCommand {
		return m.handleCommandMode(msg)
	}
	if m.mode == modeSetState {
		return m.handleSetStateMode(msg)
	}

	if m.quitConfirm {
		if !m.quitUntil.IsZero() && m.clock.Now().After(m.quitUntil) {
			m.quitConfirm = false
			m.footer = defaultFooter
		} else if msg.String() == "ctrl+c" {
			return tea.Quit
		}
		if m.quitConfirm {
			m.quitConfirm = false
			m.footer = defaultFooter
		}
		return nil
	}

	switch msg.String() {
	case "ctrl+c":
		m.quitConfirm = true
		m.quitUntil = m.clock.Now().Add(quitConfirmWindow)
		m.footer = "Press again to quit"
	case "h", "?":
		m.mode = modeHelp
		m.footer = "press any key to return"
	case "r", "R":
		m.loading = true
		m.footer = "refreshing..."
		return fetchSnapshot(m.actions, m.timeout)
	case "j", "down":
		m.selected = min(len(m.filtered)-1, m.selected+1)
		m.selectedRunID = ""
		return fetchTicketDetails(m.actions, m.timeout, m.selectedTicketID())
	case "k", "up":
		m.selected = max(0, m.selected-1)
		m.selectedRunID = ""
		return fetchTicketDetails(m.actions, m.timeout, m.selectedTicketID())
	case "w":
		ticketID, ok := m.selectedTicketIDBool()
		if !ok {
			m.footer = "no ticket selected"
			return nil
		}
		return action("work requested for ticket "+fmt.Sprint(ticketID), func(ctx context.Context) error {
			return m.actions.WorkTicket(ctx, ticketID)
		}, m.timeout)
	case "c":
		ticketID, ok := m.selectedTicketIDBool()
		if !ok {
			m.footer = "no ticket selected"
			return nil
		}
		return action("cancel requested for ticket "+fmt.Sprint(ticketID), func(ctx context.Context) error {
			return m.actions.CancelTicket(ctx, ticketID)
		}, m.timeout)
	case "/":
		m.mode = modeFilter
		m.inputText = m.filterTerm
		m.footer = "filter: /" + m.inputText
	case ":":
		m.mode = modeCommand
		m.inputText = ""
		m.footer = ":"
	case "s":
		if _, ok := m.selectedTicketIDBool(); !ok {
			m.footer = "no ticket selected"
			return nil
		}
		m.mode = modeSetState
		m.inputText = ""
		m.footer = "state [open|active|failed|done]: "
	default:
		if m.footer == "" {
			m.footer = defaultFooter
		}
	}
	return nil
}

func (m *model) handleFilterMode(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "backspace", "delete":
		if len(m.inputText) > 0 {
			m.inputText = m.inputText[:len(m.inputText)-1]
		}
		m.footer = "filter: /" + m.inputText
		m.filterTerm = m.inputText
		m.applyFilter()
	case "esc":
		m.mode = modeNormal
		m.inputText = ""
		m.footer = defaultFooter
	case "enter":
		m.mode = modeNormal
		m.filterTerm = strings.TrimSpace(m.inputText)
		m.applyFilter()
		m.footer = defaultFooter
	default:
		if len(msg.String()) == 1 {
			m.appendInput(msg)
			m.footer = "filter: /" + m.inputText
			m.filterTerm = m.inputText
			m.applyFilter()
		}
	}
	return nil
}

func (m *model) handleCommandMode(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "backspace", "delete":
		if len(m.inputText) > 0 {
			m.inputText = m.inputText[:len(m.inputText)-1]
		}
		m.footer = ":" + m.inputText
	case "esc":
		m.mode = modeNormal
		m.footer = defaultFooter
	case "enter":
		m.mode = modeNormal
		m.footer = defaultFooter
		raw := strings.TrimSpace(m.inputText)
		m.inputText = ""
		if raw == "" {
			return nil
		}
		return m.runCommand(raw)
	default:
		if len(msg.String()) == 1 {
			m.appendInput(msg)
			m.footer = ":" + m.inputText
		}
	}
	return nil
}

func (m *model) handleSetStateMode(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "backspace", "delete":
		if len(m.inputText) > 0 {
			m.inputText = m.inputText[:len(m.inputText)-1]
		}
		m.footer = "state [open|active|failed|done]: " + m.inputText
	case "esc":
		m.mode = modeNormal
		m.inputText = ""
		m.footer = defaultFooter
	case "enter":
		state := strings.ToLower(strings.TrimSpace(m.inputText))
		m.mode = modeNormal
		m.inputText = ""
		if state == "" {
			m.footer = "state update cancelled"
			return nil
		}
		if !sf.IsValidTicketState(state) {
			m.footer = "invalid state: " + state
			return nil
		}
		ticketID, ok := m.selectedTicketIDBool()
		if !ok {
			m.footer = "no ticket selected"
			return nil
		}
		m.footer = "setting state " + state
		return action("state update requested for "+fmt.Sprint(ticketID), func(ctx context.Context) error {
			_, err := m.actions.SetTicketState(ctx, ticketID, state)
			return err
		}, m.timeout)
	default:
		if len(msg.String()) == 1 {
			m.appendInput(msg)
			m.footer = "state [open|active|failed|done]: " + m.inputText
		}
	}
	return nil
}

func (m *model) runCommand(raw string) tea.Cmd {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return nil
	}
	switch strings.ToLower(parts[0]) {
	case "help", "?":
		m.mode = modeHelp
		return nil
	case "q", "quit":
		return tea.Quit
	case "pause":
		return action("factory pause requested", func(ctx context.Context) error {
			return m.actions.PauseFactory(ctx)
		}, m.timeout)
	case "resume":
		return action("factory resume requested", func(ctx context.Context) error {
			return m.actions.ResumeFactory(ctx)
		}, m.timeout)
	case "set-max-in-flight":
		if len(parts) < 2 {
			m.footer = "set-max-in-flight requires an integer"
			return nil
		}
		value, err := strconv.Atoi(parts[1])
		if err != nil {
			m.footer = "invalid max-in-flight: " + err.Error()
			return nil
		}
		return action("set max-in-flight requested", func(ctx context.Context) error {
			return m.actions.SetMaxInFlight(ctx, value)
		}, m.timeout)
	}
	m.footer = "unknown command: " + raw
	return nil
}

func (m model) View() string {
	if m.mode == modeHelp {
		return m.renderHelp()
	}
	if m.height <= 0 {
		m.height = 24
	}
	if m.width <= 0 {
		m.width = 100
	}
	parts := []string{
		m.renderTicketList(),
		m.renderTicketDetail(),
		m.renderRunDetail(),
		horizontalRule(m.width),
		m.footerLine(),
	}
	return strings.Join(parts, "\n")
}

func (m model) renderHelp() string {
	if m.height <= 0 {
		m.height = 24
	}
	if m.width <= 0 {
		m.width = 100
	}

	lines := []string{
		"Software Factory TUI help",
		horizontalRule(m.width),
		"",
		"Navigation:",
		"  j / ↓       - next ticket",
		"  k / ↑       - previous ticket",
		"  /           - enter filter mode",
		"  s           - set selected ticket state (open|active|failed|done)",
		"  :           - open command mode",
		"",
		"Actions:",
		"  w           - request work for selected ticket",
		"  c           - cancel selected ticket",
		"  r           - refresh snapshot",
		"",
		"Command mode:",
		"  help        - show this help",
		"  pause       - pause dispatcher",
		"  resume      - resume dispatcher",
		"  set-max-in-flight N  - set dispatcher max in-flight",
		"",
		"Global:",
		"  h / ?       - open help (full-screen)",
		"  ^C          - quit confirmation, press again to exit",
		"",
		"press any key to return",
	}

	if len(lines) < m.height {
		padding := make([]string, m.height-len(lines))
		lines = append(lines, padding...)
	} else {
		lines = lines[:m.height]
	}
	for i := range lines {
		if len(lines[i]) > m.width {
			lines[i] = lines[i][:m.width]
		}
	}
	return strings.Join(lines, "\n")
}

func horizontalRule(width int) string {
	if width <= 0 {
		width = 1
	}
	return strings.Repeat("=", width)
}

func (m model) footerLine() string {
	if m.width <= 0 {
		return " " + m.footer
	}
	pad := max(1, m.width-len(m.footer))
	return strings.Repeat(" ", pad) + m.footer
}

func (m model) renderTicketList() string {
	rows := []string{"ID\tSTATE\tREADY\tTITLE"}
	for index, ticket := range m.filtered {
		marker := " "
		if index == m.selected {
			marker = ">"
		}
		rows = append(rows, fmt.Sprintf("%s\t%d\t%v\t%s", marker, ticket.ID, ticket.Ready, ticket.Title))
	}
	if len(rows) == 1 {
		rows = append(rows, "No tickets available")
	}
	if m.filterTerm != "" {
		head := fmt.Sprintf("[ticket list] filter=%q", m.filterTerm)
		rows = append(rows, head)
	} else {
		rows = append(rows, "[ticket list]")
	}
	return strings.Join(rows, "\n")
}

func (m model) renderTicketDetail() string {
	if m.selectedTicket == nil {
		if m.loading {
			return "[ticket detail]\nloading..."
		}
		return "[ticket detail]\nNo ticket selected"
	}
	return fmt.Sprintf("[ticket detail]\nID: %d\nState: %s\nReady: %v\nTitle: %s\nBody: %s", m.selectedTicket.ID, m.selectedTicket.State, m.selectedTicket.Ready, m.selectedTicket.Title, truncate(m.selectedTicket.Body, 400))
}

func (m model) renderRunDetail() string {
	if m.selectedTicket == nil {
		return "[run/transcript detail]\nNo ticket selected"
	}
	if len(m.runs) == 0 {
		return "[run/transcript detail]\nNo runs for selected ticket"
	}
	run := m.runs[0]
	if m.selectedRunID != "" {
		for _, candidate := range m.runs {
			if candidate.ID == m.selectedRunID {
				run = candidate
			}
		}
	}
	return fmt.Sprintf("[run/transcript detail]\nrun: %s\noutcome: %s\nphase: %s\nactive: %v\nselected transcript: step 0 attempt 0", run.ID, run.Outcome, run.Phase, run.Active)
}

func (m *model) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.filterTerm))
	if query == "" {
		m.filtered = append([]sf.TicketSummary(nil), m.tickets...)
		return
	}
	m.filtered = m.filtered[:0]
	for _, ticket := range m.tickets {
		if strings.Contains(strings.ToLower(ticket.Title), query) || strings.Contains(fmt.Sprint(ticket.ID), query) {
			m.filtered = append(m.filtered, ticket)
		}
	}
	if m.selected >= len(m.filtered) {
		m.selected = max(0, len(m.filtered)-1)
	}
}

func (m model) selectedTicketID() int64 {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		if len(m.filtered) == 0 {
			return 0
		}
		return m.filtered[0].ID
	}
	return m.filtered[m.selected].ID
}

func (m model) selectedTicketIDBool() (int64, bool) {
	id := m.selectedTicketID()
	if id == 0 {
		return 0, false
	}
	return id, true
}

func action(message string, doIt func(context.Context) error, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := doIt(ctx)
		if err != nil {
			return actionMsg{message: message, err: err}
		}
		return actionMsg{message: message}
	}
}

func sortTickets(tickets []sf.TicketSummary) []sf.TicketSummary {
	copied := append([]sf.TicketSummary(nil), tickets...)
	sort.SliceStable(copied, func(i, j int) bool {
		return copied[i].ID < copied[j].ID
	})
	return copied
}

func truncate(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return "..."
	}
	return value[:maxLen-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
