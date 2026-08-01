package agenttools_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/agenttools"
)

func TestApplyPatchChangesTheRepositoryAndReportsRejectedHunksAsToolErrors(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	if output, err := exec.Command("git", "init", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init error = %v, output = %s", err, output)
	}
	target := filepath.Join(repository, "message.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tool, err := agenttools.NewApplyPatch(repository, 5*time.Second)
	if err != nil {
		t.Fatalf("NewApplyPatch() error = %v", err)
	}

	patch := `diff --git a/message.txt b/message.txt
--- a/message.txt
+++ b/message.txt
@@ -1 +1 @@
-before
+after
`
	result, err := tool.Execute(t.Context(), []byte(`{"patch":`+quoted(patch)+`}`))
	if err != nil {
		t.Fatalf("Execute(valid) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute(valid) result = %#v", result)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "after\n" {
		t.Fatalf("file content = %q", content)
	}

	result, err = tool.Execute(t.Context(), []byte(`{"patch":`+quoted(patch)+`}`))
	if err != nil {
		t.Fatalf("Execute(rejected) error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "patch rejected") {
		t.Fatalf("Execute(rejected) result = %#v", result)
	}
}
