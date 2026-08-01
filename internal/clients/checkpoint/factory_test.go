package checkpoint

import (
	"io/fs"
	"net/http"
	"testing"

	"github.com/0x63616c/software-factory/internal/store"
)

func TestFactoryReadsTheProjectedCapabilityForEveryActivityInvocation(t *testing.T) {
	t.Parallel()
	current := "attempt-one"
	factory, err := NewFactory("https://factory.example", "/projected/capability", http.DefaultClient, func(path string) ([]byte, error) {
		if path != "/projected/capability" {
			t.Fatalf("path = %q", path)
		}
		return []byte(current), nil
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	id := store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 1, AttemptNo: 1}
	first, err := factory.Open(id)
	if err != nil {
		t.Fatalf("Open(first): %v", err)
	}
	current = "attempt-two"
	second, err := factory.Open(id)
	if err != nil {
		t.Fatalf("Open(second): %v", err)
	}
	if first.capability != "attempt-one" || second.capability != "attempt-two" {
		t.Fatalf("capabilities = %q, %q", first.capability, second.capability)
	}
}

func TestFactoryDoesNotTreatAnUnreadableProjectionAsAnEmptyCapability(t *testing.T) {
	t.Parallel()
	factory, err := NewFactory("https://factory.example", "/projected/capability", http.DefaultClient, func(string) ([]byte, error) { return nil, fs.ErrPermission })
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	_, err = factory.Open(store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 1, AttemptNo: 1})
	if err == nil {
		t.Fatal("Open succeeded with unreadable projected capability")
	}
}
