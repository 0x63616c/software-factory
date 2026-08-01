package main

import (
	"os"
	"strings"
	"testing"
)

// TestRunBuildsTheDirectModelCredentialSource is the source-level assertion
// that the composition root constructs one durable source and adapts it for
// direct Responses calls on the main worker.
//
// It reads main.go's source rather than executing newActivities, for the same
// reason TestRegisterRegistersBothWorkflowsAndTheActivities does: newActivities
// dials Kubernetes and reads process configuration, neither of which exists
// in a unit test.
func TestRunBuildsTheDirectModelCredentialSource(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := extractFuncBody(t, string(source), "func run(")

	for _, want := range []string{"newCodexAuthSource(", "codexresponses.NewManagedCredentialSource(", "codexresponses.New("} {
		if !strings.Contains(body, want) {
			t.Errorf("run() does not contain %q; the direct model credential seam is unwired", want)
		}
	}
}

// TestRunWorkerTemplateCarriesItsEnvironment keeps the activated private
// worker's deployment contract visible at the composition root.
func TestRunWorkerTemplateCarriesItsEnvironment(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	// Whitespace-collapsed before matching: gofmt aligns the values of a
	// multi-entry map literal, so an assertion on the exact spacing would fail
	// the next time an entry with a longer key is added.
	body := collapseSpace(extractFuncBody(t, string(source), "func newTargetRunWorkerControlActivities("))

	for _, tc := range []struct{ entry, why string }{
		{
			"work.GhConfigDirEnv: work.GhConfigDir",
			"gh looks in $HOME/.config/gh instead and finds no credential",
		},
		{
			"work.RunWorkerTemporalHostPortEnv: cfg.TemporalHostPort",
			"the Run Worker has no Temporal frontend to dial",
		},
		{
			"work.RunWorkerTemporalNamespaceEnv: cfg.TemporalNamespace",
			"the Run Worker would dial the wrong namespace, or none",
		},
		{
			"work.RunWorkerBlobsURLEnv: cfg.BlobsURL",
			"the Run Worker would use a different blob API than the main worker",
		},
	} {
		if !strings.Contains(body, tc.entry) {
			t.Errorf("the Run Worker template does not set %s; %s", tc.entry, tc.why)
		}
	}
}

// collapseSpace reduces every run of whitespace to a single space, so a match
// against source text is insensitive to gofmt's alignment.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// extractFuncBody returns the text of the named top-level function, so an
// assertion cannot pass by matching text anywhere else in the file. It does
// not handle a function containing a nested "\n}\n" of its own (a func
// literal at statement level) — none of the functions this file inspects has
// one.
func extractFuncBody(t *testing.T, source, signature string) string {
	t.Helper()

	start := strings.Index(source, signature)
	if start < 0 {
		t.Fatalf("main.go has no %q", signature)
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s", signature)
	}
	return source[start : start+end]
}
