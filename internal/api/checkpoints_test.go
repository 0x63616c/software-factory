package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

type checkpointStoreFake struct {
	input      store.AgentCheckpointInput
	err        error
	calls      int
	loaded     store.AgentAttempt
	transcript *store.TargetTranscript
	loadFound  bool
	loadErr    error
}

func TestAgentCheckpointReadReconcilesDurableTerminalEvidence(t *testing.T) {
	t.Parallel()

	endedAt := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	fake := &checkpointStoreFake{
		loadFound:  true,
		loaded:     store.AgentAttempt{ExecutionID: "opaque-execution-9", State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured, Usage: work.Usage{InputTokens: 100, OutputTokens: 30}, EndedAt: endedAt, Result: json.RawMessage(`{"kind":"done"}`)},
		transcript: &store.TargetTranscript{CompressedBytes: []byte("terminal"), Compression: "zstd", UncompressedSizeBytes: 8, Checksum: []byte("checksum")},
	}
	request := httptest.NewRequest(http.MethodGet, checkpoint.AttemptPath("0f466627-b3ae-4ba2-9c96-6ef44ec6f578", 4, 2), nil)
	request.Header.Set(checkpoint.CapabilityHeader, "attempt-two-capability")
	response := httptest.NewRecorder()
	NewWithCheckpointStore("test-build", nil, fake).Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET checkpoint = %d: %s", response.Code, response.Body.String())
	}
	var got checkpoint.Attempt
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if got.ExecutionID != "opaque-execution-9" || got.State != work.AgentAttemptSucceeded || got.Transcript == nil || string(got.Transcript.CompressedBytes) != "terminal" {
		t.Fatalf("GET checkpoint = %+v, want terminal evidence", got)
	}
	if strings.Contains(response.Body.String(), "attempt-two-capability") {
		t.Fatal("GET checkpoint leaked its capability")
	}
}

func TestAgentCheckpointStoresTerminalEvidenceBeforeAcknowledgement(t *testing.T) {
	t.Parallel()

	fake := &checkpointStoreFake{}
	response := checkpointRequest(t, NewWithCheckpointStore("test-build", nil, fake),
		"0f466627-b3ae-4ba2-9c96-6ef44ec6f578", 4, 2, "attempt-two-capability", terminalCheckpointBody(t))

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("terminal checkpoint response = (%d, %q), want empty 204", response.Code, response.Body.String())
	}
	wantEndedAt := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	if fake.calls != 1 || fake.input.State != work.AgentAttemptSucceeded || fake.input.ExecutionID != "opaque-execution-9" || fake.input.UsageState != work.UsageMeasured || !fake.input.EndedAt.Equal(wantEndedAt) {
		t.Fatalf("terminal checkpoint = %+v across %d calls, want succeeded provider evidence", fake.input, fake.calls)
	}
	if fake.input.Usage != (work.Usage{InputTokens: 100, CachedInputTokens: 25, OutputTokens: 30, ReasoningTokens: 10}) || string(fake.input.Result) != `{"kind":"done"}` {
		t.Fatalf("terminal result and usage = %s / %+v", fake.input.Result, fake.input.Usage)
	}
	if fake.input.Transcript == nil || string(fake.input.Transcript.CompressedBytes) != "terminal" {
		t.Fatalf("terminal transcript = %+v, want durable transcript", fake.input.Transcript)
	}
}

func TestAgentCheckpointRejectsMissingCapabilitiesAndInvalidEvidenceBeforeTheStore(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		capability string
		body       string
		wantStatus int
		wantReason string
	}{
		"missing capability": {
			body:       `{"executionId":"opaque-execution-9","state":"running","usageState":"unknown","usage":{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningTokens":0}}`,
			wantStatus: http.StatusUnauthorized, wantReason: "checkpoint_unauthorized",
		},
		"running without provider identity": {
			capability: "attempt-capability",
			body:       `{"state":"running","usageState":"unknown","usage":{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningTokens":0}}`,
			wantStatus: http.StatusUnprocessableEntity, wantReason: "invalid_checkpoint",
		},
		"terminal success without result and transcript": {
			capability: "attempt-capability",
			body:       `{"executionId":"opaque-execution-9","state":"succeeded","usageState":"measured","usage":{"inputTokens":1,"cachedInputTokens":0,"outputTokens":1,"reasoningTokens":0},"endedAt":"2026-07-31T20:00:00Z"}`,
			wantStatus: http.StatusUnprocessableEntity, wantReason: "invalid_checkpoint",
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &checkpointStoreFake{}
			response := checkpointRequest(t, NewWithCheckpointStore("test-build", nil, fake), "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", 1, 1, test.capability, test.body)
			if response.Code != test.wantStatus || ticketErrorReason(t, response) != test.wantReason {
				t.Fatalf("checkpoint response = (%d, %s), want %d/%s", response.Code, response.Body.String(), test.wantStatus, test.wantReason)
			}
			if fake.calls != 0 {
				t.Fatalf("store called %d times for rejected input", fake.calls)
			}
		})
	}
}

func TestAgentCheckpointMapsOwnershipConflictsAndOutagesWithoutLeakingTheCapability(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err        error
		wantStatus int
		wantReason string
	}{
		"foreign attempt":   {err: store.ErrRunOwnership, wantStatus: http.StatusUnauthorized, wantReason: "checkpoint_unauthorized"},
		"conflicting retry": {err: work.ErrPermanent, wantStatus: http.StatusConflict, wantReason: "checkpoint_conflict"},
		"store outage":      {err: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable, wantReason: "checkpoint_unavailable"},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &checkpointStoreFake{err: test.err}
			response := checkpointRequest(t, NewWithCheckpointStore("test-build", nil, fake), "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", 1, 1, "do-not-leak", `{"executionId":"opaque-execution-1","state":"running","usageState":"unknown","usage":{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningTokens":0}}`)
			if response.Code != test.wantStatus || ticketErrorReason(t, response) != test.wantReason {
				t.Fatalf("checkpoint response = (%d, %s), want %d/%s", response.Code, response.Body.String(), test.wantStatus, test.wantReason)
			}
			if strings.Contains(response.Body.String(), "do-not-leak") {
				t.Fatal("checkpoint response leaked its capability")
			}
		})
	}
}

func TestAgentCheckpointCapabilityRotatesWithTheActiveAttempt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := storefake.New()
	ticket, err := fake.CreateTicket(ctx, "rotate", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := "0f466627-b3ae-4ba2-9c96-6ef44ec6f578"
	startedAt := time.Date(2026, 7, 31, 19, 0, 0, 0, time.UTC)
	if _, err := fake.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: startedAt}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, Iteration: 1, StartedAt: startedAt}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	for attemptNo, capability := range map[int]string{1: "attempt-one-capability", 2: "attempt-two-capability"} {
		id := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: attemptNo}
		if _, err := fake.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: id, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "gpt-5", Effort: "high"}, UsageState: work.UsageUnknown, StartedAt: startedAt}); err != nil {
			t.Fatalf("StartAgentAttempt(%d): %v", attemptNo, err)
		}
		if err := fake.BindCheckpointCapability(ctx, id, capability); err != nil {
			t.Fatalf("BindCheckpointCapability(%d): %v", attemptNo, err)
		}
	}
	service := NewWithCheckpointStore("test-build", nil, fake)
	body := `{"executionId":"opaque-execution-2","state":"running","usageState":"unknown","usage":{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningTokens":0}}`

	foreign := checkpointRequest(t, service, runID, 1, 2, "attempt-one-capability", body)
	if foreign.Code != http.StatusUnauthorized {
		t.Fatalf("attempt-one capability against attempt two = %d: %s", foreign.Code, foreign.Body.String())
	}
	accepted := checkpointRequest(t, service, runID, 1, 2, "attempt-two-capability", body)
	if accepted.Code != http.StatusNoContent {
		t.Fatalf("attempt-two checkpoint = %d: %s", accepted.Code, accepted.Body.String())
	}
	retry := checkpointRequest(t, service, runID, 1, 2, "attempt-two-capability", body)
	if retry.Code != http.StatusNoContent {
		t.Fatalf("idempotent attempt-two checkpoint = %d: %s", retry.Code, retry.Body.String())
	}
}

func checkpointRequest(t *testing.T, service *Service, runID string, stepOrdinal, attemptNo int, capability, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/v1/run-worker/runs/" + runID + "/steps/" + strconv.Itoa(stepOrdinal) + "/attempts/" + strconv.Itoa(attemptNo) + "/checkpoint"
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set(checkpoint.CapabilityHeader, capability)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	return response
}

func (fake *checkpointStoreFake) CheckpointAgentAttempt(_ context.Context, input store.AgentCheckpointInput) (store.AgentAttempt, error) {
	fake.input = input
	fake.calls++
	return store.AgentAttempt{ID: input.ID, State: input.State}, fake.err
}

func (fake *checkpointStoreFake) LoadAgentCheckpoint(_ context.Context, _ store.TargetAttemptID, _ string) (store.AgentAttempt, *store.TargetTranscript, bool, error) {
	return fake.loaded, fake.transcript, fake.loadFound, fake.loadErr
}

func TestAgentCheckpointStoresRunningProviderProgressForTheExactAttempt(t *testing.T) {
	t.Parallel()

	fake := &checkpointStoreFake{}
	service := NewWithCheckpointStore("test-build", nil, fake)
	request := httptest.NewRequest(http.MethodPut,
		"/v1/run-worker/runs/0f466627-b3ae-4ba2-9c96-6ef44ec6f578/steps/4/attempts/2/checkpoint",
		strings.NewReader(`{
			"executionId":"opaque-execution-9",
			"state":"running",
			"usageState":"unknown",
			"usage":{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningTokens":0},
			"transcript":{"compressedBytes":"cGFydGlhbA==","compression":"zstd","uncompressedSizeBytes":7,"checksum":"Y2hlY2tzdW0="}
		}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(checkpoint.CapabilityHeader, "attempt-two-capability")
	response := httptest.NewRecorder()

	service.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("checkpoint response = (%d, %q), want empty 204", response.Code, response.Body.String())
	}
	wantID := store.TargetAttemptID{RunID: "0f466627-b3ae-4ba2-9c96-6ef44ec6f578", StepOrdinal: 4, AttemptNo: 2}
	if fake.calls != 1 || fake.input.ID != wantID || fake.input.Capability != "attempt-two-capability" {
		t.Fatalf("checkpoint store input = %+v across %d calls, want exact attempt and dedicated capability", fake.input, fake.calls)
	}
	if fake.input.ExecutionID != "opaque-execution-9" || fake.input.State != work.AgentAttemptRunning || fake.input.UsageState != work.UsageUnknown || !fake.input.EndedAt.IsZero() || len(fake.input.Result) != 0 {
		t.Fatalf("running checkpoint = %+v, want durable non-terminal provider progress", fake.input)
	}
	if fake.input.Transcript == nil || string(fake.input.Transcript.CompressedBytes) != "partial" || fake.input.Transcript.Compression != "zstd" || fake.input.Transcript.UncompressedSizeBytes != 7 || string(fake.input.Transcript.Checksum) != "checksum" {
		t.Fatalf("running transcript = %+v, want decoded partial transcript", fake.input.Transcript)
	}
	if strings.Contains(response.Body.String(), "attempt-two-capability") {
		t.Fatal("checkpoint capability appeared in the response")
	}
}

func terminalCheckpointBody(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"executionId": "opaque-execution-9",
		"state":       "succeeded",
		"usageState":  "measured",
		"usage": map[string]int64{
			"inputTokens": 100, "cachedInputTokens": 25, "outputTokens": 30, "reasoningTokens": 10,
		},
		"endedAt": time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC),
		"result":  json.RawMessage(`{"kind":"done"}`),
		"transcript": map[string]any{
			"compressedBytes": "dGVybWluYWw=", "compression": "zstd", "uncompressedSizeBytes": 8, "checksum": "Y2hlY2tzdW0=",
		},
	})
	if err != nil {
		t.Fatalf("marshal terminal checkpoint: %v", err)
	}
	return string(body)
}
