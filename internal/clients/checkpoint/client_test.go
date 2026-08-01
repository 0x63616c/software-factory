package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	checkpointprotocol "github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

func TestCheckpointSendsTerminalEvidenceToItsScopedAttempt(t *testing.T) {
	t.Parallel()

	var received struct {
		method, path, capability string
		body                     []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received.method, received.path = request.Method, request.URL.Path
		received.capability = request.Header.Get(checkpointprotocol.CapabilityHeader)
		var err error
		received.body, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read checkpoint request: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	id := store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 4, AttemptNo: 2}
	client, err := New(server.URL, id, "attempt-two-capability", server.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	endedAt := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	input := checkpointprotocol.Attempt{
		ExecutionID: "opaque-execution-9", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured,
		Usage:   checkpointprotocol.Usage{InputTokens: 100, CachedInputTokens: 25, OutputTokens: 30, ReasoningTokens: 10},
		EndedAt: &endedAt, Result: json.RawMessage(`{"kind":"done"}`),
		Transcript: &checkpointprotocol.Transcript{CompressedBytes: []byte("terminal"), Compression: "zstd", UncompressedSizeBytes: 8, Checksum: []byte("checksum")},
	}
	if err := client.Checkpoint(context.Background(), input); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	wantPath := "/v1/run-worker/runs/0f466627-b3ae-4ba2-9c96-6ef44ec6f578/steps/4/attempts/2/checkpoint"
	if received.method != http.MethodPut || received.path != wantPath || received.capability != "attempt-two-capability" {
		t.Fatalf("request = %s %s capability %q, want scoped checkpoint request", received.method, received.path, received.capability)
	}
	if strings.Contains(string(received.body), "attempt-two-capability") {
		t.Fatalf("checkpoint body contains capability: %s", received.body)
	}
	var sent checkpointprotocol.Attempt
	if err := json.Unmarshal(received.body, &sent); err != nil {
		t.Fatalf("decode checkpoint body: %v", err)
	}
	if sent.ExecutionID != input.ExecutionID || sent.State != input.State || string(sent.Result) != string(input.Result) || sent.Transcript == nil || string(sent.Transcript.CompressedBytes) != "terminal" {
		t.Fatalf("checkpoint body = %+v, want terminal evidence", sent)
	}
}

func TestLoadReadsTheDurableAttemptBeforeAProviderStarts(t *testing.T) {
	t.Parallel()

	want := terminalCheckpoint()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", request.Method)
		}
		if request.Header.Get(checkpointprotocol.CapabilityHeader) != "attempt-capability" {
			t.Fatal("GET omitted its scoped capability")
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(want)
	}))
	t.Cleanup(server.Close)

	client, err := New(server.URL, store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 1, AttemptNo: 1}, "attempt-capability", server.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, found, err := client.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found || got.ExecutionID != want.ExecutionID || got.State != want.State || string(got.Result) != string(want.Result) {
		t.Fatalf("Load = (%+v, %v), want durable terminal checkpoint", got, found)
	}
}

func TestLoadReportsAnAuthorizedAttemptWithoutAProviderCheckpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL, store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 1, AttemptNo: 1}, "attempt-capability", server.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, found, err := client.Load(context.Background())
	if err != nil || found {
		t.Fatalf("Load = (found %v, error %v), want authorized empty checkpoint", found, err)
	}
}

func TestCheckpointClassifiesBoundaryFailuresWithoutExposingItsCapability(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status int
		want   error
	}{
		"unauthorized":                        {status: http.StatusUnauthorized, want: ErrUnauthorized},
		"foreign attempt hidden as not found": {status: http.StatusNotFound, want: ErrUnauthorized},
		"conflicting checkpoint":              {status: http.StatusConflict, want: ErrConflict},
		"invalid checkpoint":                  {status: http.StatusUnprocessableEntity, want: ErrInvalid},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"detail":"attempt-capability"}`))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.URL, store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 1, AttemptNo: 1}, "attempt-capability", server.Client())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = client.Checkpoint(context.Background(), runningCheckpoint())
			if !errors.Is(err, test.want) {
				t.Fatalf("Checkpoint error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "attempt-capability") {
				t.Fatalf("Checkpoint error leaked capability: %v", err)
			}
		})
	}
}

func TestCheckpointClientsRotateCapabilityWithTheAttemptIdentity(t *testing.T) {
	t.Parallel()

	type requestIdentity struct{ path, capability string }
	requests := make(chan requestIdentity, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- requestIdentity{path: request.URL.Path, capability: request.Header.Get(checkpointprotocol.CapabilityHeader)}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	for attemptNo, capability := range map[int]string{1: "attempt-one", 2: "attempt-two"} {
		client, err := New(server.URL, store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 4, AttemptNo: attemptNo}, capability, server.Client())
		if err != nil {
			t.Fatalf("New(%d): %v", attemptNo, err)
		}
		if err := client.Checkpoint(context.Background(), runningCheckpoint()); err != nil {
			t.Fatalf("Checkpoint(%d): %v", attemptNo, err)
		}
	}
	seen := map[string]string{}
	for range 2 {
		request := <-requests
		seen[request.path] = request.capability
	}
	if seen[checkpointprotocol.AttemptPath("0f466627-b3ae-4ba2-9c96-6ef44ec6f578", 4, 1)] != "attempt-one" || seen[checkpointprotocol.AttemptPath("0f466627-b3ae-4ba2-9c96-6ef44ec6f578", 4, 2)] != "attempt-two" {
		t.Fatalf("scoped requests = %#v, want distinct per-Attempt capabilities", seen)
	}
}

func TestNewRejectsAnUnscopedCheckpointClient(t *testing.T) {
	t.Parallel()

	validID := store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 1, AttemptNo: 1}
	for _, test := range []struct {
		name, baseURL, capability string
		id                        store.TargetAttemptID
		httpClient                *http.Client
	}{
		{name: "relative URL", baseURL: "/factory", id: validID, capability: "cap", httpClient: http.DefaultClient},
		{name: "missing run", baseURL: "https://factory.example", id: store.TargetAttemptID{StepOrdinal: 1, AttemptNo: 1}, capability: "cap", httpClient: http.DefaultClient},
		{name: "missing capability", baseURL: "https://factory.example", id: validID, httpClient: http.DefaultClient},
		{name: "missing HTTP client", baseURL: "https://factory.example", id: validID, capability: "cap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.baseURL, test.id, test.capability, test.httpClient); err == nil {
				t.Fatal("New succeeded for an unscoped checkpoint client")
			}
		})
	}
}

func runningCheckpoint() checkpointprotocol.Attempt {
	return checkpointprotocol.Attempt{
		ExecutionID: "opaque-execution-1", State: work.AgentAttemptRunning, UsageState: work.UsageUnknown,
		Usage: checkpointprotocol.Usage{},
	}
}

func terminalCheckpoint() checkpointprotocol.Attempt {
	endedAt := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	return checkpointprotocol.Attempt{
		ExecutionID: "opaque-execution-1", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured,
		Usage: checkpointprotocol.Usage{InputTokens: 3, OutputTokens: 2}, EndedAt: &endedAt,
		Result: json.RawMessage(`{"kind":"done"}`), Transcript: &checkpointprotocol.Transcript{CompressedBytes: []byte("transcript"), Compression: "zstd", UncompressedSizeBytes: 10, Checksum: []byte("sum")},
	}
}
