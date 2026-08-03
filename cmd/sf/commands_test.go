// Package main tests the sf CLI command surface.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func useTempConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func resetRootFlags() {
	rootContextName = ""
	rootAPIURL = ""
	rootCfJWT = ""
	rootBearerToken = ""
	rootOutput = ""
	rootTimeout = ""
	rootPollInterval = ""
	rootNoColor = false
}

func runCommand(root *cobra.Command, args ...string) error {
	_, err := runCommandWithOutput(root, args...)
	return err
}

func runCommandWithOutput(root *cobra.Command, args ...string) (string, error) {
	var configureOutput func(*cobra.Command, *bytes.Buffer)
	configureOutput = func(command *cobra.Command, out *bytes.Buffer) {
		command.SetOut(out)
		command.SetErr(out)
		for _, child := range command.Commands() {
			configureOutput(child, out)
		}
	}

	root.SetArgs(args)
	var out bytes.Buffer
	configureOutput(root, &out)
	if err := root.Execute(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

func TestParseContextAssignments(t *testing.T) {
	got, err := parseContextAssignments([]string{"api_url=http://example.com", "cf_jwt=cf", "bearer=token", "timeout=10s", "poll_interval=5s", "output=json"})
	if err != nil {
		t.Fatalf("expected parse success: %v", err)
	}
	if got.APIURL != "http://example.com" || got.CfJwt != "cf" || got.BearerToken != "token" || got.Timeout != "10s" || got.PollInterval != "5s" || got.Output != "json" {
		t.Fatalf("unexpected assignments: %+v", got)
	}

	if _, err := parseContextAssignments([]string{"badkey=value"}); err == nil {
		t.Fatalf("expected unknown key error")
	}
}

func TestContextCommandsStoreAndUse(t *testing.T) {
	useTempConfigDir(t)
	resetRootFlags()

	root := newRootCommand()
	root.SetArgs([]string{"context", "set", "work", "api_url=http://work", "bearer=token", "output=json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("context set failed: %v", err)
	}

	root.SetArgs([]string{"context", "set", "ci", "api_url=http://ci", "cf_jwt=cf"})
	if err := root.Execute(); err != nil {
		t.Fatalf("context set failed: %v", err)
	}

	root.SetArgs([]string{"context", "use", "ci"})
	if err := root.Execute(); err != nil {
		t.Fatalf("context use failed: %v", err)
	}

	root.SetArgs([]string{"context", "list"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("context list failed: %v", err)
	}
	if !strings.Contains(out.String(), "*ci") {
		t.Fatalf("expected active context marker in list, got %q", out.String())
	}
}

func TestTicketSetStateRejectsInvalidState(t *testing.T) {
	resetRootFlags()
	root := newRootCommand()
	root.SetArgs([]string{
		"ticket",
		"set-state",
		"42",
		"--state",
		"not-real",
		"--api-url",
		"https://example.com",
		"--bearer-token",
		"token",
	})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected invalid ticket state error")
	}
	if !strings.Contains(err.Error(), "invalid ticket state") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTicketWorkAndCancelCommandsCallExpectedRoutes(t *testing.T) {
	resetRootFlags()
	workCalled := false
	cancelCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tickets/11/work":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /v1/tickets/11/work, got %s", r.Method)
			}
			workCalled = true
			w.WriteHeader(http.StatusNoContent)
		case "/v1/tickets/11/cancel":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /v1/tickets/11/cancel, got %s", r.Method)
			}
			cancelCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	root := newRootCommand()
	if err := runCommand(root, "ticket", "work", "11", "--api-url", ts.URL, "--bearer-token", "token"); err != nil {
		t.Fatalf("ticket work failed: %v", err)
	}
	if err := runCommand(root, "ticket", "cancel", "11", "--api-url", ts.URL, "--bearer-token", "token"); err != nil {
		t.Fatalf("ticket cancel failed: %v", err)
	}
	if !workCalled {
		t.Fatalf("work endpoint not hit")
	}
	if !cancelCalled {
		t.Fatalf("cancel endpoint not hit")
	}
}

func TestTicketSetStateCallsAPI(t *testing.T) {
	resetRootFlags()
	var stateFromRequest string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("expected patch, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tickets/11/state" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		stateFromRequest = payload.State
		_, _ = w.Write([]byte(`{"id":11,"title":"T","body":"B","state":"` + payload.State + `","ready":true}`))
	}))
	defer ts.Close()

	root := newRootCommand()
	root.SetArgs([]string{
		"ticket",
		"set-state",
		"11",
		"--state",
		"active",
		"--api-url",
		ts.URL,
		"--bearer-token",
		"token",
	})
	var out bytes.Buffer
	root.SetOut(&out)
	err := root.Execute()
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if stateFromRequest != "active" {
		t.Fatalf("unexpected state payload: %q", stateFromRequest)
	}
	if !strings.Contains(out.String(), "State:\tactive") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestTicketCreateWithBodyFile(t *testing.T) {
	useTempConfigDir(t)
	resetRootFlags()
	bodyPath := t.TempDir() + "/body.txt"
	if err := os.WriteFile(bodyPath, []byte("detailed body"), 0o600); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected post, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tickets" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var payload struct {
			Title     string  `json:"title"`
			Body      string  `json:"body"`
			BlockedBy []int64 `json:"blockedBy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Title != "T1" {
			t.Fatalf("unexpected title %q", payload.Title)
		}
		if payload.Body != "detailed body" {
			t.Fatalf("unexpected body %q", payload.Body)
		}
		_, _ = w.Write([]byte(`{"id":11,"title":"T1","body":"detailed body","state":"open","ready":true}`))
	}))
	defer ts.Close()

	root := newRootCommand()
	root.SetOut(new(bytes.Buffer))
	err := runCommand(root, "ticket", "create", "--title", "T1", "--body-file", bodyPath, "--api-url", ts.URL, "--bearer-token", "token")
	if err != nil {
		t.Fatalf("expected create success, got: %v", err)
	}
}

func TestRunCommandsCallExpectedRoutes(t *testing.T) {
	resetRootFlags()
	calledList := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tickets/33/runs":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET, got %s", r.Method)
			}
			calledList = true
			_, _ = w.Write([]byte(`{"runs":[{"id":"run-1","ticketId":33,"startedAt":"now","outcome":"running","phase":"phase"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	root := newRootCommand()
	out, err := runCommandWithOutput(root, "run", "list", "--ticket", "33", "--api-url", ts.URL, "--bearer-token", "token")
	if err != nil {
		t.Fatalf("run list failed: %v", err)
	}
	if !calledList {
		t.Fatalf("run list endpoint was not called")
	}
	if out == "" {
		t.Fatalf("expected run output, got empty (calledList=%v)", calledList)
	}

	out, err = runCommandWithOutput(root, "run", "get", "33", "run-1", "--api-url", ts.URL, "--bearer-token", "token")
	if err != nil {
		t.Fatalf("run get failed: %v", err)
	}
	if !strings.Contains(out, "run-1") {
		t.Fatalf("expected run-1 in output, got %q", out)
	}
}

func TestRunTranscriptRoute(t *testing.T) {
	resetRootFlags()
	listCalled := false
	transcriptCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tickets/44/runs" {
			if r.Method != http.MethodGet {
				t.Fatalf("expected get, got %s", r.Method)
			}
			listCalled = true
			_, _ = w.Write([]byte(`{"runs":[{"id":"run-2","ticketId":44,"startedAt":"now","outcome":"ok","phase":"phase"}]}`))
			return
		}
		if r.Method != http.MethodGet {
			t.Fatalf("expected get, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tickets/44/runs/run-2/steps/7/attempts/3/transcript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		transcriptCalled = true
		_, _ = w.Write([]byte("hello"))
	}))
	defer ts.Close()

	path := t.TempDir() + "/out.txt"
	root := newRootCommand()
	if err := runCommand(root,
		"run",
		"transcript",
		"44",
		"run-2",
		"3",
		"7",
		"--out",
		path,
		"--api-url",
		ts.URL,
		"--bearer-token",
		"token"); err != nil {
		t.Fatalf("run transcript failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("expected hello transcript, got %q", string(got))
	}
	if !listCalled {
		t.Fatalf("run transcript did not call run lookup")
	}
	if !transcriptCalled {
		t.Fatalf("run transcript did not call transcript endpoint")
	}
}

func TestTicketListJsonFiltersByTitle(t *testing.T) {
	resetRootFlags()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected get, got %s", r.Method)
		}
		if r.URL.Query().Get("state") != "open" {
			t.Fatalf("expected state=open, got %q", r.URL.Query().Get("state"))
		}
		response := struct {
			Tickets []map[string]any `json:"tickets"`
		}{
			Tickets: []map[string]any{
				{"id": 1, "title": "Build queue", "state": "open", "ready": true, "updatedAt": "now", "createdAt": "now"},
				{"id": 2, "title": "Alpha ticket", "state": "open", "ready": false, "updatedAt": "now", "createdAt": "now"},
			},
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		_, _ = w.Write(encoded)
	}))
	defer ts.Close()

	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{
		"ticket",
		"list",
		"--state",
		"open",
		"--title-contains",
		"alpha",
		"--json",
		"--api-url",
		ts.URL,
		"--bearer-token",
		"token",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("expected ticket list success, got: %v", err)
	}

	var payload struct {
		Tickets []struct {
			ID int64 `json:"id"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(payload.Tickets) != 1 {
		t.Fatalf("expected one ticket in filtered output, got %d", len(payload.Tickets))
	}
	if payload.Tickets[0].ID != 2 {
		t.Fatalf("expected alpha ticket id 2, got %d", payload.Tickets[0].ID)
	}
}

func TestTicketListReadyFlagRejectsInvalid(t *testing.T) {
	resetRootFlags()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tickets": []}`))
	}))
	defer ts.Close()

	root := newRootCommand()
	root.SetArgs([]string{
		"ticket",
		"list",
		"--ready",
		"bad",
		"--api-url",
		ts.URL,
		"--bearer-token",
		"token",
	})
	if err := root.Execute(); err == nil {
		t.Fatalf("expected ready validation error")
	} else if !strings.Contains(err.Error(), "invalid --ready value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFactoryPauseResumeSetMaxInFlight(t *testing.T) {
	resetRootFlags()
	var calledPause, calledResume, calledSetMax bool
	var postedMax int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/factory/pause":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST pause, got %s", r.Method)
			}
			calledPause = true
			w.WriteHeader(http.StatusNoContent)
		case "/v1/factory/resume":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST resume, got %s", r.Method)
			}
			calledResume = true
			w.WriteHeader(http.StatusNoContent)
		case "/v1/factory/max-in-flight":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST set max, got %s", r.Method)
			}
			var payload struct {
				MaxInFlight int `json:"maxInFlight"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			postedMax = payload.MaxInFlight
			calledSetMax = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	root := newRootCommand()
	if err := runCommand(root, "factory", "pause", "--api-url", ts.URL, "--bearer-token", "token"); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if !calledPause {
		t.Fatalf("pause endpoint not hit")
	}

	root = newRootCommand()
	if err := runCommand(root, "factory", "resume", "--api-url", ts.URL, "--bearer-token", "token"); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !calledResume {
		t.Fatalf("resume endpoint not hit")
	}

	root = newRootCommand()
	if err := runCommand(root, "factory", "set-max-in-flight", "7", "--api-url", ts.URL, "--bearer-token", "token"); err != nil {
		t.Fatalf("set-max-in-flight failed: %v", err)
	}
	if !calledSetMax {
		t.Fatalf("set max endpoint not hit")
	}
	if postedMax != 7 {
		t.Fatalf("unexpected max-in-flight %d", postedMax)
	}
}

func TestFactoryStatusSupportsJSON(t *testing.T) {
	resetRootFlags()
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/console" {
			t.Fatalf("expected GET /v1/console, got %s %s", r.Method, r.URL.Path)
		}
		called = true
		_, _ = w.Write([]byte(`{"tickets":[{"id":1,"title":"t1","state":"open","ready":true,"createdAt":"now","updatedAt":"now"},{"id":2,"state":"done","ready":false,"title":"t2","createdAt":"now","updatedAt":"now"}]}`))
	}))
	defer ts.Close()

	out, err := runCommandWithOutput(newRootCommand(),
		"factory", "status",
		"--output", "json",
		"--api-url", ts.URL,
		"--bearer-token", "token")
	if err != nil {
		t.Fatalf("factory status failed: %v", err)
	}
	if !called {
		t.Fatalf("factory status did not call /v1/console")
	}
	type statusPayload struct {
		Open int `json:"open"`
		Done int `json:"done"`
	}
	var got statusPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("status output is not valid json: %v", err)
	}
	if got.Open != 1 || got.Done != 1 {
		t.Fatalf("expected summarized JSON output, got %q", out)
	}
}

func TestTicketListAndGetOutputFormats(t *testing.T) {
	resetRootFlags()
	listCalled := false
	getCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tickets":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET /v1/tickets, got %s", r.Method)
			}
			listCalled = true
			_, _ = w.Write([]byte(`{"tickets":[{"id":9,"title":"hello","state":"open","ready":true,"createdAt":"now","updatedAt":"now"}]}`))
		case "/v1/tickets/9":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET /v1/tickets/9, got %s", r.Method)
			}
			getCalled = true
			_, _ = w.Write([]byte(`{"id":9,"title":"hello","body":"body","state":"open","ready":true,"createdAt":"now","updatedAt":"now","blockers":[],"blocks":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	out, err := runCommandWithOutput(newRootCommand(),
		"ticket",
		"list",
		"--yaml",
		"--state", "open",
		"--api-url", server.URL,
		"--bearer-token", "token")
	if err != nil {
		t.Fatalf("ticket list yaml failed: %v", err)
	}
	if !listCalled {
		t.Fatalf("ticket list did not call endpoint")
	}
	if !strings.Contains(out, "tickets:") || !strings.Contains(out, "id: 9") {
		t.Fatalf("expected yaml output, got %q", out)
	}

	out, err = runCommandWithOutput(newRootCommand(),
		"ticket",
		"get",
		"9",
		"--output", "json",
		"--api-url", server.URL,
		"--bearer-token", "token")
	if err != nil {
		t.Fatalf("ticket get json failed: %v", err)
	}
	if !getCalled {
		t.Fatalf("ticket get did not call endpoint")
	}
	if !strings.Contains(out, "\"id\": 9") {
		t.Fatalf("expected json output for ticket get, got %q", out)
	}
}

func TestRunListRequiresTicketFlag(t *testing.T) {
	resetRootFlags()
	root := newRootCommand()
	root.SetArgs([]string{
		"run",
		"list",
		"--api-url",
		"https://example.com",
		"--bearer-token",
		"token",
	})
	if err := root.Execute(); err == nil {
		t.Fatalf("expected required --ticket error")
	}
}

func TestTicketTranscriptWritesToFile(t *testing.T) {
	resetRootFlags()
	const transcript = "hello transcript"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tickets/11/runs/run-1/steps/2/attempts/1/transcript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(transcript))
	}))
	defer ts.Close()

	path := t.TempDir() + "/transcript.txt"
	root := newRootCommand()
	root.SetArgs([]string{
		"ticket",
		"transcript",
		"11",
		"run-1",
		"1",
		"2",
		"--out",
		path,
		"--api-url",
		ts.URL,
		"--bearer-token",
		"token",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("expected command success: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(got) != transcript {
		t.Fatalf("expected transcript %q got %q", transcript, string(got))
	}
}
