package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0x63616c/software-factory/internal/clock/clocktest"
	"github.com/0x63616c/software-factory/internal/sf"
)

var testStart = time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)

func newTestModel(actions *sf.Actions) model {
	return newModel(actions, time.Second, time.Second, clocktest.NewFake(testStart))
}

func newTestActionsFromServer(t *testing.T, server *httptest.Server) *sf.Actions {
	client, err := sf.NewClient(server.URL, sf.Credentials{BearerToken: "token"}, 10*time.Second, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected client error: %v", err)
	}
	return &sf.Actions{Client: client}
}

func TestTUIHelpOverlayCloseOnAnyKey(t *testing.T) {
	m := newTestModel(nil)
	m.width = 80
	m.height = 20

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	helpModel := next.(model)
	if helpModel.mode != modeHelp {
		t.Fatalf("expected help mode")
	}
	if !strings.Contains(helpModel.View(), "Software Factory TUI help") {
		t.Fatalf("expected help content in view")
	}

	next, _ = helpModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	back := next.(model)
	if back.mode != modeNormal {
		t.Fatalf("expected normal mode")
	}
	if back.footer != defaultFooter {
		t.Fatalf("unexpected footer %q", back.footer)
	}
}

func TestTUIQuitConfirmationFlow(t *testing.T) {
	m := newTestModel(nil)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	afterFirst := next.(model)
	if !afterFirst.quitConfirm {
		t.Fatalf("expected quit confirm state")
	}
	if afterFirst.footer != "Press again to quit" {
		t.Fatalf("unexpected footer %q", afterFirst.footer)
	}

	next, _ = afterFirst.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	afterCancel := next.(model)
	if afterCancel.quitConfirm {
		t.Fatalf("expected quit confirm to cancel")
	}
	if afterCancel.footer != defaultFooter {
		t.Fatalf("expected default footer, got %q", afterCancel.footer)
	}
}

func TestTUIQuitConfirmationTimeout(t *testing.T) {
	clk := clocktest.NewFake(testStart)
	m := newModel(nil, time.Second, time.Second, clk)
	m.quitConfirm = true
	m.quitUntil = clk.Now().Add(time.Second)
	m.footer = "Press again to quit"
	clk.Advance(2 * time.Second)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	after := next.(model)
	if after.quitConfirm {
		t.Fatalf("expected timed-out quit confirm to reset")
	}
	if after.footer != defaultFooter {
		t.Fatalf("expected default footer after timeout, got %q", after.footer)
	}

	next, _ = after.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	rearmed := next.(model)
	if !rearmed.quitConfirm {
		t.Fatalf("expected quit confirm to re-arm after second ctrl+c")
	}
}

func TestTUIKeepsNonRuneKeysAsNoOpsInNormalMode(t *testing.T) {
	m := newTestModel(nil)
	m.tickets = []sf.TicketSummary{{ID: 1}, {ID: 2}}
	m.applyFilter()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.(model).selected; got != 0 {
		t.Fatalf("selection after non-rune down key = %d, want unchanged", got)
	}
}

func TestTUIMultiRuneTextDoesNotBecomeAControlKey(t *testing.T) {
	m := newTestModel(nil)
	m.mode = modeCommand

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	got := next.(model)
	if got.mode != modeCommand {
		t.Fatalf("mode after multi-rune text = %v, want command mode", got.mode)
	}
	if got.inputText != "" {
		t.Fatalf("input after multi-rune text = %q, want unchanged", got.inputText)
	}
}

func TestTUIInvalidSetStateShowsError(t *testing.T) {
	m := newTestModel(nil)
	m.tickets = []sf.TicketSummary{{ID: 10, Title: "sample"}}
	m.applyFilter()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	setState := next.(model)
	if setState.mode != modeSetState {
		t.Fatalf("expected set-state mode")
	}

	next, _ = setState.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := next.(model)
	if !strings.HasPrefix(result.footer, "invalid state:") {
		t.Fatalf("expected invalid state message, got %q", result.footer)
	}
}

func TestTUIWorkDispatchesAction(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tickets/55/work" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	actions := newTestActionsFromServer(t, server)
	m := newTestModel(actions)
	m.tickets = []sf.TicketSummary{{ID: 55, Title: "sample"}}
	m.applyFilter()

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	if cmd == nil {
		t.Fatalf("expected work action command")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("expected action message")
	} else if actionResult, ok := msg.(actionMsg); !ok {
		t.Fatalf("expected action message type, got %T", msg)
	} else if actionResult.err != nil {
		// this catches endpoint mismatch or unexpected API body responses
		t.Fatalf("unexpected action error: %v", actionResult.err)
	}
	if !called {
		t.Fatalf("expected /work endpoint call")
	}
}

func TestTUICommandModePauseAndMaxInFlight(t *testing.T) {
	paused := false
	var maxInFlight int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/factory/pause":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /v1/factory/pause, got %s", r.Method)
			}
			paused = true
			w.WriteHeader(http.StatusNoContent)
		case "/v1/factory/max-in-flight":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /v1/factory/max-in-flight, got %s", r.Method)
			}
			var payload struct {
				MaxInFlight int `json:"maxInFlight"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			maxInFlight = payload.MaxInFlight
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	actions := newTestActionsFromServer(t, server)
	m := newTestModel(actions)

	// Open command mode and dispatch :pause.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")})
	commandMode := next.(model)
	if commandMode.mode != modeCommand {
		t.Fatalf("expected command mode")
	}
	next, _ = commandMode.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	next, cmd := next.(model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected action command for :pause")
	}
	msg := cmd()
	if actionResult, ok := msg.(actionMsg); !ok || actionResult.err != nil {
		t.Fatalf("expected pause action success, got %T %v", msg, actionResult.err)
	}
	if !paused {
		t.Fatalf("expected pause endpoint call")
	}

	// Open command mode and set max-in-flight.
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")})
	commandMode = next.(model)
	if commandMode.mode != modeCommand {
		t.Fatalf("expected command mode for set-max command")
	}
	cmd = commandMode.runCommand("set-max-in-flight 3")
	if cmd == nil {
		t.Fatalf("expected action command for :set-max-in-flight")
	}
	msg = cmd()
	if actionResult, ok := msg.(actionMsg); !ok || actionResult.err != nil {
		t.Fatalf("expected set-max action success, got %T %v", msg, actionResult.err)
	}
	if maxInFlight != 3 {
		t.Fatalf("expected maxInFlight 3, got %d", maxInFlight)
	}
}

func TestTUIFilterMode(t *testing.T) {
	m := newTestModel(nil)
	m.tickets = []sf.TicketSummary{
		{ID: 1, Title: "build queue"},
		{ID: 2, Title: "alpha ticket"},
	}
	m.applyFilter()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	filter := next.(model)
	if filter.mode != modeFilter {
		t.Fatalf("expected filter mode")
	}

	next, _ = filter.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	filtered := next.(model)
	if filtered.filterTerm != "a" {
		t.Fatalf("expected filter term 'a'")
	}
	if len(filtered.filtered) != 1 || filtered.filtered[0].ID != 2 {
		t.Fatalf("expected one ticket match for alpha")
	}
}
