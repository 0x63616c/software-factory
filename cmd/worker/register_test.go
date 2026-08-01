package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegisterRegistersBothWorkflowsAndTheActivities is the demonstration
// register's own doc comment promises and did not yet have: the dispatcher
// and ticket workflows, and the activity sets, actually land on the worker
// rather than the function staying the one log line it started as (#340). See TestTheWorkerPollsTheQueueTheWorkflowsScheduleOnto for why this
// is a source-level assertion rather than a run against a live worker: the
// alternative is a live Temporal, and this file already has that pattern.
func TestRegisterExposesOnlyActivatedWorkflowsAndMainQueueActivities(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := extractRegisterBody(t, string(source))

	for _, want := range []string{
		"w.RegisterWorkflow(workflows.WorkOnTicket)",
		"w.RegisterWorkflow(workflows.MaintainFactory)",
		"w.RegisterActivity(targetRecordingActs)",
		"w.RegisterActivity(targetRecoveryActs)",
		"w.RegisterActivityWithOptions(targetEvidenceActs.Finalize, activity.RegisterOptions{Name: activities.TargetAgentEvidenceFinalizeActivityName})",
		"w.RegisterWorkflowWithOptions(workflows.AgentWorkflow",
		"agent.PrepareActivityName",
		"agent.ModelTurnActivityName",
		"agent.LifecycleActivityName",
		"agent.FinalizeActivityName",
		"promptActs.DecodeFinalOutput",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("register()'s body does not contain %q; the worker registers nothing it does not name here", want)
		}
	}
	for _, forbidden := range []string{
		"w.RegisterWorkflow(workflows.Dispatcher)",
		"w.RegisterWorkflow(workflows.FactoryWorkTicket)",
		"w.RegisterWorkflow(workflows.FactoryDispatcher)",
		"w.RegisterActivity(acts)",
		"w.RegisterActivity(targetEvidenceActs)",
		"acts.RunPlan",
		"acts.RunImplement",
		"acts.RunReview",
		"acts.CreateSandbox",
		"acts.WaitSandboxReady",
		"acts.CloneRepo",
		"acts.PushRepo",
		"acts.DeleteSandbox",
		"acts.EnablePullRequestAutoMerge",
		"acts.SweepOrphanSandboxes",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("register() exposes legacy CLI activity %q", forbidden)
		}
	}
}

func TestRegisterControlExposesOnlyDispatcherAdmission(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := extractNamedFunctionBody(t, string(source), "func registerControl(")
	for _, want := range []string{
		"w.RegisterWorkflow(workflows.Dispatcher)",
		"w.RegisterActivity(ticketActs.AwaitDispatchableTickets)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("registerControl() does not contain %q", want)
		}
	}
	for _, forbidden := range []string{"WorkOnTicket", "MaintainFactory", "RegisterActivity(ticketActs)"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("registerControl() exposes main-queue capability %q", forbidden)
		}
	}
}

func TestProductionSourceCannotInvokeCodexCLI(t *testing.T) {
	t.Parallel()

	moduleRoot := filepath.Clean(filepath.Join("..", ".."))
	forbidden := "codex" + " exec"
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "docs", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		base := filepath.Base(path)
		if filepath.Ext(path) != ".go" && filepath.Ext(path) != ".sh" && base != "Dockerfile" {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(content), forbidden) {
			t.Errorf("production source %s can invoke the removed Codex CLI", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning production source: %v", err)
	}
}

// extractRegisterBody returns the text of the register function, so the
// assertions above cannot pass by matching a registration call anywhere else
// in the file — the whole point of "one function, one call site" is that
// there is exactly one place to look.
func extractRegisterBody(t *testing.T, source string) string {
	t.Helper()
	return extractNamedFunctionBody(t, source, "func register(")
}

func extractNamedFunctionBody(t *testing.T, source, declaration string) string {
	t.Helper()

	start := strings.Index(source, declaration)
	if start < 0 {
		t.Fatalf("main.go has no %s function", declaration)
	}
	// The next top-level "\n}\n" after the opening brace ends the function;
	// register has no nested func literals today, so a brace-depth scan is
	// more machinery than this needs.
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of register()")
	}
	return source[start : start+end]
}
