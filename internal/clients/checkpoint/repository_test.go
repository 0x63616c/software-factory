package checkpoint

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	checkpointprotocol "github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

func TestRepositoryClientScopesRequestsAndRoundTripsPositions(t *testing.T) {
	identity := work.RunWorkerIdentity{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", Generation: 3}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != checkpointprotocol.RepositoryPathFor(identity.RunID, identity.Generation) || request.Header.Get(checkpointprotocol.RepositoryCapabilityHeader) != "repository-capability" {
			t.Fatalf("request = %s capability %q", request.URL.Path, request.Header.Get(checkpointprotocol.RepositoryCapabilityHeader))
		}
		if request.Method == http.MethodPut {
			var body checkpointprotocol.RepositoryWrite
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.StepOrdinal != 4 || body.PushedHead != "head-4" || body.CompletedAt.IsZero() {
				t.Fatalf("PUT body = %+v", body)
			}
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(checkpointprotocol.Repository{StepOrdinal: 4, Branch: "factory/run", PushedHead: "head-4", StepResult: json.RawMessage(`{"kind":"synced"}`)})
	}))
	defer server.Close()
	client, err := NewRepository(server.URL, identity, "repository-capability", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	written, err := client.Checkpoint(context.Background(), store.GitCheckpointInput{GitCheckpoint: store.GitCheckpoint{RunID: identity.RunID, StepOrdinal: 4, Branch: "factory/run", PushedHead: "head-4", StepResult: json.RawMessage(`{"kind":"synced"}`)}, CompletedAt: completedAt})
	if err != nil || written.RunID != identity.RunID || written.PushedHead != "head-4" {
		t.Fatalf("Checkpoint = %+v, %v", written, err)
	}
	loaded, found, err := client.Load(context.Background())
	if err != nil || !found || loaded.RunID != identity.RunID || loaded.StepOrdinal != 4 || calls != 2 {
		t.Fatalf("Load = %+v found %t calls %d error %v", loaded, found, calls, err)
	}
}

func TestRepositoryFactoryReadsTheProjectedCapabilityOnEveryOpen(t *testing.T) {
	identity := work.RunWorkerIdentity{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", Generation: 1}
	values := [][]byte{[]byte("first"), []byte("second")}
	reads := 0
	factory, err := NewRepositoryFactory("https://factory.example", "/projected/capability", http.DefaultClient, func(string) ([]byte, error) {
		value := values[reads]
		reads++
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := factory.Open(identity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := factory.Open(identity)
	if err != nil {
		t.Fatal(err)
	}
	if first.capability != "first" || second.capability != "second" || reads != 2 {
		t.Fatalf("capabilities = %q/%q reads %d", first.capability, second.capability, reads)
	}
}
