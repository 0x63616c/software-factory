package work

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/blobs"
	"go.temporal.io/sdk/converter"
)

type unsafePayloadKeyContext struct {
	name      string
	namespace string
	workflow  string
}

var unsafePayloadKeyContexts = []unsafePayloadKeyContext{
	{name: "empty namespace", workflow: "workflow"},
	{name: "empty workflow ID", namespace: "namespace"},
	{name: "namespace slash", namespace: "namespace/other", workflow: "workflow"},
	{name: "workflow ID slash", namespace: "namespace", workflow: "workflow/other"},
	{name: "namespace backslash", namespace: `namespace\x`, workflow: "workflow"},
	{name: "workflow ID backslash", namespace: "namespace", workflow: `workflow\x`},
	{name: "namespace dot", namespace: ".", workflow: "workflow"},
	{name: "workflow ID dot", namespace: "namespace", workflow: "."},
	{name: "namespace dot dot", namespace: "..", workflow: "workflow"},
	{name: "workflow ID dot dot", namespace: "namespace", workflow: ".."},
}

func TestNewPayloadKeyWithoutContextIsUnkeyed(t *testing.T) {
	t.Parallel()

	key := NewPayloadKey(nil, []byte("stored payload"))
	const want = "_unkeyed/14be817553264c4c1fb599964dc7ad063d9498dce264ac5e34d1993e455e98ae"
	if got := key.String(); got != want {
		t.Errorf("NewPayloadKey(nil, stored).String() = %q, want %q", got, want)
	}
}

func TestNewPayloadKeyFromWorkflowContext(t *testing.T) {
	t.Parallel()

	key := NewPayloadKey(converter.WorkflowSerializationContext{
		Namespace:  "namespace",
		WorkflowID: "workflow-id",
	}, []byte("stored payload"))
	const want = "namespace/workflow-id/14be817553264c4c1fb599964dc7ad063d9498dce264ac5e34d1993e455e98ae"
	if got := key.String(); got != want {
		t.Errorf("NewPayloadKey(workflow context, stored).String() = %q, want %q", got, want)
	}
}

func TestNewPayloadKeyFromActivityContext(t *testing.T) {
	t.Parallel()

	key := NewPayloadKey(converter.ActivitySerializationContext{
		Namespace:  "namespace",
		WorkflowID: "workflow-id",
	}, []byte("stored payload"))
	const want = "namespace/workflow-id/14be817553264c4c1fb599964dc7ad063d9498dce264ac5e34d1993e455e98ae"
	if got := key.String(); got != want {
		t.Errorf("NewPayloadKey(activity context, stored).String() = %q, want %q", got, want)
	}
}

func TestPayloadKeyDigestIsSHA256OfStoredBytes(t *testing.T) {
	t.Parallel()

	key := NewPayloadKey(nil, []byte("payload bytes"))
	const want = "5043c48a936e796a7d6d31fdebb464e52df0e5d2a855e167ea694015ba1641e7"
	if key.Digest != want {
		t.Errorf("NewPayloadKey(nil, stored).Digest = %q, want %q", key.Digest, want)
	}
}

func TestNewPayloadKeyRejectsUnsafeContextFields(t *testing.T) {
	t.Parallel()

	const want = "_unkeyed/14be817553264c4c1fb599964dc7ad063d9498dce264ac5e34d1993e455e98ae"
	for _, test := range unsafePayloadKeyContexts {
		t.Run(test.name, func(t *testing.T) {
			key := NewPayloadKey(converter.WorkflowSerializationContext{
				Namespace:  test.namespace,
				WorkflowID: test.workflow,
			}, []byte("stored payload"))
			if got := key.String(); got != want {
				t.Errorf("NewPayloadKey(unsafe context, stored).String() = %q, want %q", got, want)
			}
		})
	}
}

func TestPayloadKeyIsAValidBlobsKeyPath(t *testing.T) {
	t.Parallel()

	keys := []PayloadKey{
		NewPayloadKey(nil, []byte("stored payload")),
		NewPayloadKey(converter.WorkflowSerializationContext{Namespace: "namespace", WorkflowID: "workflow-id"}, []byte("stored payload")),
		NewPayloadKey(converter.ActivitySerializationContext{Namespace: "namespace", WorkflowID: "workflow-id"}, []byte("stored payload")),
	}
	for _, test := range unsafePayloadKeyContexts {
		keys = append(keys, NewPayloadKey(converter.WorkflowSerializationContext{
			Namespace:  test.namespace,
			WorkflowID: test.workflow,
		}, []byte("stored payload")))
	}

	for _, key := range keys {
		if _, err := blobs.NewKey(blobs.BucketPayloads, key.String()); err != nil {
			t.Errorf("blobs.NewKey(BucketPayloads, %q) returned error: %v", key.String(), err)
		}
	}
}

func TestTicketWorkflowIDsUseADisjointNamespace(t *testing.T) {
	t.Parallel()

	for _, id := range []int64{0, 1, 7, 99} {
		// The retired GitHub-backed pipeline (#559) claimed `work-ticket-<n>`.
		// Temporal lets a closed run's ID be reused, so a small Ticket id under
		// that prefix would share a history lineage with the issue of the same
		// number — which is why this prefix must stay disjoint from it even now
		// that nothing mints the old one.
		if strings.HasPrefix(TicketWorkflowID(id), "work-ticket-") {
			t.Fatalf("Ticket id %d claims the retired GitHub-issue workflow ID namespace", id)
		}
		if !strings.HasPrefix(TicketWorkflowID(id), "factory-ticket-") {
			t.Fatalf("TicketWorkflowID(%d) = %q", id, TicketWorkflowID(id))
		}
	}
}

func TestParseFactoryTicketBranchNameInvertsTheConstructor(t *testing.T) {
	t.Parallel()

	for _, id := range []int64{1, 7, 99, 123456} {
		for _, runID := range []string{"019a3f2c-7b1e-4f9a-9c2d-3e5f6a7b8c9d", "run"} {
			branch := FactoryTicketBranchName(id, runID)
			got, ok := ParseFactoryTicketBranchName(branch)
			if !ok {
				t.Fatalf("ParseFactoryTicketBranchName(%q) ok = false, want true", branch)
			}
			if got != id {
				t.Errorf("ParseFactoryTicketBranchName(%q) = %d, want %d", branch, got, id)
			}
		}
	}
}

func TestParseFactoryTicketBranchNameRejectsAnythingElse(t *testing.T) {
	t.Parallel()

	// branch is attacker-controllable: it arrives off a pull_request webhook
	// payload from anyone who can open a PR against this repo. Every case here
	// must fail closed rather than resolve to some TicketID.
	cases := []string{
		"",
		"main",
		"software-factory/ticket-42/run", // legacy GitHub-issue branch, disjoint prefix
		"software-factory/factory-ticket-/run",
		"software-factory/factory-ticket-abc/run",
		"software-factory/factory-ticket-42",        // missing run segment
		"software-factory/factory-ticket-42/run/rm", // extra segment
		"software-factory/factory-ticket-42/",       // empty run segment
		"software-factory/factory-ticket-0/run",     // not a positive TicketID
		"software-factory/factory-ticket--1/run",    // signed
		"software-factory/factory-ticket-01/run",    // leading zero
		"SOFTWARE-FACTORY/factory-ticket-42/run",    // case must match exactly
		"other-prefix/factory-ticket-42/run",
	}
	for _, branch := range cases {
		if id, ok := ParseFactoryTicketBranchName(branch); ok {
			t.Errorf("ParseFactoryTicketBranchName(%q) = (%d, true), want ok = false", branch, id)
		}
	}
}

func TestFactoryTicketBranchBelongsToItsRun(t *testing.T) {
	runID := "019fb900-0000-7000-8000-000000000001"
	if !FactoryTicketBranchBelongsToRun(FactoryTicketBranchName(42, runID), runID) {
		t.Fatal("FactoryTicketBranchBelongsToRun rejected the branch derived for the Run")
	}
	for _, branch := range []string{
		FactoryTicketBranchName(42, "019fb900-0000-7000-8000-000000000002"),
		"software-factory/factory-ticket-42",
		"main",
	} {
		if FactoryTicketBranchBelongsToRun(branch, runID) {
			t.Errorf("FactoryTicketBranchBelongsToRun(%q, %q) = true", branch, runID)
		}
	}
}

func TestTargetDispatcherStartsWithOneTicketAtATime(t *testing.T) {
	t.Parallel()

	if got := DefaultFactoryConfig().MaxInFlight; got != 1 {
		t.Fatalf("DefaultFactoryConfig().MaxInFlight = %d, want 1", got)
	}
}

func TestRepoDirIsInsideTheWorkspaceRootWithoutBeingIt(t *testing.T) {
	// Inside, because transfer.go confines every write to the Run Worker root.
	// Not equal to it, because the run's own scaffolding lives at the root and
	// a checkout over the top of that puts prompts inside the git working tree.
	if !strings.HasPrefix(RepoDir, WorkspaceRoot+"/") {
		t.Errorf("RepoDir = %q, want a path under %q", RepoDir, WorkspaceRoot)
	}
	if RepoDir == WorkspaceRoot {
		t.Errorf("RepoDir must not be the Run Worker root itself: %q", RepoDir)
	}
}
