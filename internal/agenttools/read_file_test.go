package agenttools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttools"
	"github.com/0x63616c/software-factory/internal/blobs"
)

func TestReadFileCannotEscapeTheRepositoryAndBoundsItsResult(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatalf("WriteFile(outside) error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "escape")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	large := strings.Repeat("bounded-output-", 20)
	if err := os.WriteFile(filepath.Join(repository, "large.txt"), []byte(large), 0o600); err != nil {
		t.Fatalf("WriteFile(large) error = %v", err)
	}
	blobStore := blobs.NewMemStore()
	tool, err := agenttools.NewReadFile(repository, "agent/run-7/plan", agent.NewArtifactStore(blobStore), 32)
	if err != nil {
		t.Fatalf("NewReadFile() error = %v", err)
	}

	for _, path := range []string{"../outside.txt", "escape"} {
		result, err := tool.Execute(t.Context(), []byte(`{"path":`+quoted(path)+`}`))
		if err != nil {
			t.Fatalf("Execute(%q) error = %v", path, err)
		}
		if !result.IsError || !strings.Contains(result.Content, "outside repository") {
			t.Fatalf("Execute(%q) result = %#v", path, result)
		}
	}

	result, err := tool.Execute(t.Context(), []byte(`{"path":"large.txt"}`))
	if err != nil {
		t.Fatalf("Execute(large) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute(large) result = %#v", result)
	}
	var output agenttools.ReadFileOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("Unmarshal(output) error = %v, content = %q", err, result.Content)
	}
	if len(output.Preview) != 32 || output.OutputRef.Key == "" || !output.Truncated {
		t.Fatalf("read output = %#v", output)
	}
	stored, err := agent.NewArtifactStore(blobStore).LoadOutput(t.Context(), output.OutputRef)
	if err != nil {
		t.Fatalf("LoadOutput() error = %v", err)
	}
	if string(stored) != large {
		t.Fatalf("LoadOutput() length = %d, want %d", len(stored), len(large))
	}
}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
