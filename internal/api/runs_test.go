package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
	"github.com/0x63616c/software-factory/internal/work"
)

// runsResponse mirrors the wire shape enough for these tests to assert on it
// without hand-decoding raw JSON at every call site.
type runsResponse struct {
	Runs []struct {
		ID             string  `json:"id"`
		TicketID       int64   `json:"ticketId"`
		StartedAt      string  `json:"startedAt"`
		EndedAt        *string `json:"endedAt"`
		Outcome        string  `json:"outcome"`
		FailureKind    string  `json:"failureKind"`
		Phase          string  `json:"phase"`
		ConfirmedMerge *struct {
			ReviewedHead string `json:"reviewedHead"`
			MergeSHA     string `json:"mergeSha"`
		} `json:"confirmedMerge"`
		Usage struct {
			InputTokens int64 `json:"inputTokens"`
			Complete    bool  `json:"complete"`
		} `json:"usage"`
		Steps []struct {
			Ordinal   int             `json:"ordinal"`
			Kind      string          `json:"kind"`
			Iteration int             `json:"iteration"`
			Reason    string          `json:"reason"`
			State     string          `json:"state"`
			Result    json.RawMessage `json:"result"`
			Attempts  []struct {
				AttemptNo      int             `json:"attemptNo"`
				AgentStage     string          `json:"agentStage"`
				State          string          `json:"state"`
				UsageState     string          `json:"usageState"`
				Measured       bool            `json:"measured"`
				InputTokens    *int64          `json:"inputTokens"`
				ExecutionID    string          `json:"executionId"`
				Result         json.RawMessage `json:"result"`
				HasTranscript  bool            `json:"hasTranscript"`
				TranscriptPath string          `json:"transcriptPath"`
			} `json:"attempts"`
		} `json:"steps"`
	} `json:"runs"`
}

func TestGetTicketRunsProjectsTargetHistoryFromPostgresModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := storefake.New()
	service := New("test-build", nil, fake)

	ticket, err := fake.CreateTicket(ctx, "Target", "body", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	started := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	if _, err := fake.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: started}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepCloneRepository, StartedAt: started}); err != nil {
		t.Fatalf("StartStep(clone): %v", err)
	}
	if _, err := fake.CompleteStep(ctx, runID, 1, started.Add(time.Minute), json.RawMessage(`{"kind":"cloned"}`)); err != nil {
		t.Fatalf("CompleteStep(clone): %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 2, Kind: work.StepPlan, Iteration: 1, Reason: "initial", StartedAt: started.Add(time.Minute)}); err != nil {
		t.Fatalf("StartStep(plan): %v", err)
	}
	attemptID := store.TargetAttemptID{RunID: runID, StepOrdinal: 2, AttemptNo: 1}
	if _, err := fake.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: attemptID, AgentStage: work.AgentStagePlan, Model: work.Model{Name: "gpt-5.6", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: started.Add(time.Minute)}); err != nil {
		t.Fatalf("StartAgentAttempt: %v", err)
	}
	if err := fake.BindCheckpointCapability(ctx, attemptID, "capability"); err != nil {
		t.Fatalf("BindCheckpointCapability: %v", err)
	}
	rawTranscript := []byte(`{"event":"plan"}` + "\n")
	compressed := gzipBytes(t, rawTranscript)
	checksum := sha256.Sum256(rawTranscript)
	if _, err := fake.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{
		ID: attemptID, Capability: "capability", ExecutionID: "opaque-execution-1",
		State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured,
		Usage:   work.Usage{InputTokens: 120, CachedInputTokens: 20, OutputTokens: 40, ReasoningTokens: 10},
		EndedAt: started.Add(2 * time.Minute), Result: json.RawMessage(`{"kind":"planned"}`),
		Transcript: &store.TargetTranscript{CompressedBytes: compressed, Compression: "gzip", UncompressedSizeBytes: int64(len(rawTranscript)), Checksum: checksum[:]},
	}); err != nil {
		t.Fatalf("CheckpointAgentAttempt: %v", err)
	}
	if _, err := fake.CompleteStep(ctx, runID, 2, started.Add(2*time.Minute), json.RawMessage(`{"kind":"planned"}`)); err != nil {
		t.Fatalf("CompleteStep(plan): %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 3, Kind: work.StepAwaitCI, Iteration: 1, Reason: "pull_request_updated", StartedAt: started.Add(3 * time.Minute)}); err != nil {
		t.Fatalf("StartStep(await ci): %v", err)
	}

	response := ticketRequest(t, service, http.MethodGet, "/v1/tickets/1/runs", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET runs status = %d: %s", response.Code, response.Body.String())
	}
	var body runsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, response.Body.String())
	}
	if len(body.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(body.Runs))
	}
	run := body.Runs[0]
	if run.Phase != string(work.StepAwaitCI) {
		t.Fatalf("phase = %q, want active Step %q", run.Phase, work.StepAwaitCI)
	}
	if run.ConfirmedMerge != nil {
		t.Fatalf("confirmedMerge = %+v, want null before merge", run.ConfirmedMerge)
	}
	if len(run.Steps) != 3 || run.Steps[0].Ordinal != 1 || run.Steps[1].Ordinal != 2 || run.Steps[2].Ordinal != 3 {
		t.Fatalf("steps = %+v, want ordered ordinal history", run.Steps)
	}
	if len(run.Steps[0].Attempts) != 0 || len(run.Steps[2].Attempts) != 0 {
		t.Fatalf("infrastructure attempts = clone:%d await-ci:%d, want zero", len(run.Steps[0].Attempts), len(run.Steps[2].Attempts))
	}
	if string(run.Steps[0].Result) != `{"kind":"cloned"}` || run.Steps[1].Reason != "initial" {
		t.Fatalf("step projection = %+v, want durable result and reason", run.Steps)
	}
	if len(run.Steps[1].Attempts) != 1 {
		t.Fatalf("plan attempts = %d, want one semantic attempt (no Temporal retry rows)", len(run.Steps[1].Attempts))
	}
	attempt := run.Steps[1].Attempts[0]
	if attempt.AgentStage != string(work.AgentStagePlan) || attempt.UsageState != string(work.UsageMeasured) || attempt.ExecutionID != "opaque-execution-1" || string(attempt.Result) != `{"kind":"planned"}` {
		t.Fatalf("attempt = %+v, want durable target identity, usage state, and result", attempt)
	}
	for _, legacyField := range [][]byte{[]byte(`"providerThreadId"`), []byte(`"stage"`), []byte(`"turn"`)} {
		if bytes.Contains(response.Body.Bytes(), legacyField) {
			t.Fatalf("target run response contains legacy field %s: %s", legacyField, response.Body.String())
		}
	}
	wantPath := "/v1/tickets/1/runs/" + runID + "/steps/2/attempts/1/transcript"
	if attempt.TranscriptPath != wantPath {
		t.Fatalf("transcriptPath = %q, want %q", attempt.TranscriptPath, wantPath)
	}
	transcript := ticketRequest(t, service, http.MethodGet, wantPath, "")
	if transcript.Code != http.StatusOK || transcript.Body.String() != string(rawTranscript) {
		t.Fatalf("ordinal transcript = %d %q, want 200 %q", transcript.Code, transcript.Body.String(), rawTranscript)
	}
}

func TestGetTicketRunsFallsBackToLatestTerminalStepAndProjectsConfirmedMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := storefake.New()
	service := New("test-build", nil, fake)
	ticket, err := fake.CreateTicket(ctx, "Merged", "body", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	if _, err := fake.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: started}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepMergePullRequest, StartedAt: started}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	if _, err := fake.FinalizeConfirmedMerge(ctx, store.ConfirmedMergeInput{RunID: runID, TicketID: ticket.ID, StepOrdinal: 1, ReviewedHead: "head-sha", MergeSHA: "merge-sha", EndedAt: started.Add(time.Minute)}); err != nil {
		t.Fatalf("FinalizeConfirmedMerge: %v", err)
	}

	response := ticketRequest(t, service, http.MethodGet, "/v1/tickets/1/runs", "")
	var body runsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, response.Body.String())
	}
	run := body.Runs[0]
	if run.Phase != string(work.StepMergePullRequest) {
		t.Fatalf("phase = %q, want latest terminal Step %q", run.Phase, work.StepMergePullRequest)
	}
	if run.Outcome != string(work.RunOutcomeSucceeded) || run.ConfirmedMerge == nil || run.ConfirmedMerge.ReviewedHead != "head-sha" || run.ConfirmedMerge.MergeSHA != "merge-sha" {
		t.Fatalf("terminal run = %+v, want successful Confirmed Merge", run)
	}
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return compressed.Bytes()
}

func TestGetTicketRunsRollsUpUsageAndFlagsIncompleteRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := storefake.New()
	service := New("test-build", nil, fake)

	ticket, err := fake.CreateTicket(ctx, "T", "body", nil)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	runID := "11111111-1111-1111-1111-111111111111"
	started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	if _, err := fake.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: ticket.ID, RunID: runID, StartedAt: started}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepImplement, Iteration: 1, StartedAt: started}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	first := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 1}
	if _, err := fake.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: first, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "gpt-5.6", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: started}); err != nil {
		t.Fatalf("StartAgentAttempt(1): %v", err)
	}
	if err := fake.BindCheckpointCapability(ctx, first, "capability"); err != nil {
		t.Fatalf("BindCheckpointCapability: %v", err)
	}
	if _, err := fake.CheckpointAgentAttempt(ctx, store.AgentCheckpointInput{
		ID: first, Capability: "capability", ExecutionID: "opaque-execution-1",
		State: work.AgentAttemptSucceeded, UsageState: work.UsageMeasured,
		Usage:   work.Usage{InputTokens: 100, CachedInputTokens: 10, OutputTokens: 40, ReasoningTokens: 5},
		EndedAt: started.Add(time.Minute), Result: []byte(`{"kind":"done"}`),
		Transcript: &store.TargetTranscript{CompressedBytes: []byte("transcript"), Compression: "zstd", UncompressedSizeBytes: 10, Checksum: []byte("checksum")},
	}); err != nil {
		t.Fatalf("CheckpointAgentAttempt(1): %v", err)
	}
	secondID := store.TargetAttemptID{RunID: runID, StepOrdinal: 1, AttemptNo: 2}
	if _, err := fake.StartAgentAttempt(ctx, store.StartAgentAttemptInput{ID: secondID, AgentStage: work.AgentStageImplement, Model: work.Model{Name: "gpt-5.6", Effort: "medium"}, UsageState: work.UsageUnknown, StartedAt: started.Add(time.Minute)}); err != nil {
		t.Fatalf("StartAgentAttempt(2): %v", err)
	}

	response := ticketRequest(t, service, http.MethodGet, "/v1/tickets/1/runs", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET runs status = %d: %s", response.Code, response.Body.String())
	}

	var body runsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, response.Body.String())
	}
	if len(body.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(body.Runs))
	}
	run := body.Runs[0]
	if run.ID != runID || run.TicketID != int64(ticket.ID) {
		t.Fatalf("run identity = %+v", run)
	}
	if run.Usage.Complete {
		t.Fatalf("run usage complete = true, want false: attempt 2 was never measured")
	}
	if run.Usage.InputTokens != 100 {
		t.Fatalf("run usage inputTokens = %d, want 100 (only the measured attempt)", run.Usage.InputTokens)
	}
	if len(run.Steps) != 1 || len(run.Steps[0].Attempts) != 2 {
		t.Fatalf("steps = %+v, want one step with two attempts", run.Steps)
	}
	second := run.Steps[0].Attempts[1]
	if second.Measured {
		t.Fatalf("attempt 2 measured = true, want false")
	}
	if second.InputTokens != nil {
		t.Fatalf("attempt 2 inputTokens = %d, want null: an unmeasured attempt's usage is unknown, not zero", *second.InputTokens)
	}
	if second.HasTranscript {
		t.Fatalf("attempt 2 hasTranscript = true, want false: none was stored")
	}
}

func TestGetTicketRunsForAnUnknownTicketIs404(t *testing.T) {
	t.Parallel()
	service := New("test-build", nil, storefake.New())
	response := ticketRequest(t, service, http.MethodGet, "/v1/tickets/999/runs", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestDownloadAttemptTranscriptForWrongTicketIs404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := storefake.New()
	service := New("test-build", nil, fake)

	_, err := fake.CreateTicket(ctx, "A", "body", nil)
	if err != nil {
		t.Fatalf("CreateTicket(A): %v", err)
	}
	other, err := fake.CreateTicket(ctx, "B", "body", nil)
	if err != nil {
		t.Fatalf("CreateTicket(B): %v", err)
	}
	runID := "33333333-3333-3333-3333-333333333333"
	started := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := fake.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: other.ID, RunID: runID, StartedAt: started}); err != nil {
		t.Fatalf("ClaimAndStartRun: %v", err)
	}
	if _, err := fake.StartStep(ctx, store.StartStepInput{RunID: runID, Ordinal: 1, Kind: work.StepPlan, StartedAt: started}); err != nil {
		t.Fatalf("StartStep: %v", err)
	}

	// runID belongs to ticket B; asking for it under ticket A's path is a 404,
	// not a leak of another ticket's run.
	response := ticketRequest(t, service, http.MethodGet, "/v1/tickets/1/runs/"+runID+"/steps/1/attempts/1/transcript", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestLegacyStageTurnTranscriptRouteIsNotRegistered(t *testing.T) {
	t.Parallel()
	service := New("test-build", nil, storefake.New())
	response := ticketRequest(t, service, http.MethodGet, "/v1/tickets/1/runs/33333333-3333-4333-8333-333333333333/stages/plan/turns/1/attempts/1/transcript", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("legacy transcript route status = %d, want 404: %s", response.Code, response.Body.String())
	}
}
