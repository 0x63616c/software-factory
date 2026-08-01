package agenttools_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttools"
	"github.com/0x63616c/software-factory/internal/blobs"
)

func TestExecCommandUsesArgvCancelsTheProcessAndBoundsBothStreams(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	blobStore := blobs.NewMemStore()
	tool, err := agenttools.NewExecCommand(
		repository,
		"agent/run-7/implement/1",
		agent.NewArtifactStore(blobStore),
		16,
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("NewExecCommand() error = %v", err)
	}

	arguments, _ := json.Marshal(agenttools.ExecCommandInput{Argv: []string{
		"sh", "-c", "pwd; printf 'stdout-is-longer-than-preview'; printf 'stderr-is-longer-than-preview' >&2",
	}})
	result, err := tool.Execute(t.Context(), arguments)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() result = %#v", result)
	}
	var output agenttools.ExecCommandOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("Unmarshal(output) error = %v, content = %q", err, result.Content)
	}
	if output.ExitCode != 0 || len(output.StdoutPreview) > 16 || len(output.StderrPreview) > 16 ||
		output.StdoutRef.Key == "" || output.StderrRef.Key == "" {
		t.Fatalf("command output = %#v", output)
	}
	stdout, err := agent.NewArtifactStore(blobStore).LoadOutput(t.Context(), output.StdoutRef)
	if err != nil {
		t.Fatalf("LoadOutput(stdout) error = %v", err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatalf("EvalSymlinks(repository) error = %v", err)
	}
	if !strings.HasPrefix(string(stdout), canonicalRepository+"\n") || !strings.Contains(string(stdout), "stdout-is-longer") {
		t.Fatalf("stored stdout = %q", stdout)
	}
	stderr, err := agent.NewArtifactStore(blobStore).LoadOutput(t.Context(), output.StderrRef)
	if err != nil {
		t.Fatalf("LoadOutput(stderr) error = %v", err)
	}
	if string(stderr) != "stderr-is-longer-than-preview" {
		t.Fatalf("stored stderr = %q", stderr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		result, err := tool.Execute(ctx, []byte(`{"argv":["sleep","30"]}`))
		if err == nil && result.IsError {
			err = errors.New(result.Content)
		}
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Execute() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command did not stop")
	}
}

func TestReadOnlyExecCommandRejectsSideEffectAndRepositoryEscapeOptions(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	tool, err := agenttools.NewReadOnlyExecCommand(
		repository,
		"agent/run-7/review/1",
		agent.NewArtifactStore(blobs.NewMemStore()),
		1024,
		time.Second,
	)
	if err != nil {
		t.Fatalf("NewReadOnlyExecCommand() error = %v", err)
	}

	for _, argv := range [][]string{
		{"/tmp/rg", "needle", "."},
		{"git", "diff", "--output=review.patch"},
		{"git", "grep", "--open-files-in-pager=touch marker", "needle"},
		{"rg", "--follow", "needle", "."},
		{"rg", "-L", "needle", "."},
	} {
		argv := argv
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			arguments, marshalErr := json.Marshal(agenttools.ExecCommandInput{Argv: argv})
			if marshalErr != nil {
				t.Fatalf("Marshal() error = %v", marshalErr)
			}
			result, executeErr := tool.Execute(t.Context(), arguments)
			if executeErr != nil {
				t.Fatalf("Execute() error = %v", executeErr)
			}
			if !result.IsError || !strings.Contains(result.Content, "rejected argv") {
				t.Fatalf("Execute() result = %#v, want policy rejection", result)
			}
		})
	}
}
