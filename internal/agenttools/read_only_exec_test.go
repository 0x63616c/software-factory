package agenttools_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttools"
	"github.com/0x63616c/software-factory/internal/blobs"
)

func TestReadOnlyExecRejectsShellsAndMutatingCommands(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	if output, err := exec.Command("git", "init", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init error = %v, output = %s", err, output)
	}
	tool, err := agenttools.NewReadOnlyExecCommand(
		repository,
		"agent/run-7/plan",
		agent.NewArtifactStore(blobs.NewMemStore()),
		128,
		5*time.Second,
	)
	if err != nil {
		t.Fatalf("NewReadOnlyExecCommand() error = %v", err)
	}
	for _, arguments := range []string{
		`{"argv":["sh","-c","touch changed"]}`,
		`{"argv":["git","add","."]}`,
		`{"argv":["git","status","--work-tree=/tmp"]}`,
		`{"argv":["rg","needle","/etc"]}`,
	} {
		result, err := tool.Execute(t.Context(), []byte(arguments))
		if err != nil {
			t.Fatalf("Execute(%s) error = %v", arguments, err)
		}
		if !result.IsError || !strings.Contains(result.Content, "read-only") {
			t.Fatalf("Execute(%s) result = %#v", arguments, result)
		}
	}

	result, err := tool.Execute(t.Context(), []byte(`{"argv":["git","status","--short"]}`))
	if err != nil {
		t.Fatalf("Execute(git status) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute(git status) result = %#v", result)
	}
}
