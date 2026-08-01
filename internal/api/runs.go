package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// usageOutput is the four token counts ADR-0012 fixes, rolled up across
// whatever level (Step, Run) requested them. Complete is false when the sum
// omits at least one unmeasured Attempt — the console must show that as
// "incomplete", never as a confident total that quietly left something out.
type usageOutput struct {
	InputTokens       int64 `json:"inputTokens" doc:"The whole input, including cachedInputTokens."`
	CachedInputTokens int64 `json:"cachedInputTokens" doc:"The part of inputTokens served from the provider's prompt cache."`
	OutputTokens      int64 `json:"outputTokens" doc:"The whole output, including reasoningTokens."`
	ReasoningTokens   int64 `json:"reasoningTokens" doc:"The part of outputTokens spent reasoning."`
	Complete          bool  `json:"complete" doc:"False when this total omits at least one unmeasured Attempt; render it as incomplete rather than a confident sum."`
}

// attemptOutput is one execution of a Step.
//
// Its four token fields are nullable scalars rather than a nested usageOutput
// object: huma has no supported way to mark a $ref'd object schema nullable
// (see schema.go's own panic on that combination), and there is nothing to
// roll up at this level — Complete only means something once more than one
// Attempt's numbers are summed, which happens at Step and Run, not here.
type attemptOutput struct {
	AttemptNo         int             `json:"attemptNo" doc:"Which semantic Agent Attempt of this Step this is, starting at 1. Temporal activity tries are not represented."`
	AgentStage        string          `json:"agentStage" enum:"plan,implement,review" doc:"The agent role for this semantic execution: plan, implement, or review."`
	Model             string          `json:"model" doc:"The model this attempt ran on."`
	Effort            string          `json:"effort" doc:"The reasoning effort this attempt ran on."`
	State             string          `json:"state" enum:"running,succeeded,failed" doc:"The durable Attempt lifecycle state."`
	FailureKind       string          `json:"failureKind" enum:",invalid_input,agent_unrecoverable,agent_attempt_budget,review_budget,ci_unobserved,github_auth,github_ruleset,github_unavailable,run_worker_unavailable,persistence_unavailable,infrastructure" doc:"The terminal failure category, including retained historical values; empty unless this Attempt failed."`
	ExecutionID       string          `json:"executionId" doc:"The opaque identity captured for this semantic Attempt."`
	UsageState        string          `json:"usageState" enum:"unknown,measured" doc:"Whether token usage is unknown or measured."`
	Measured          bool            `json:"measured" doc:"Compatibility projection of usageState == measured."`
	InputTokens       *int64          `json:"inputTokens" doc:"The whole input, including cachedInputTokens. Null unless usageState is measured."`
	CachedInputTokens *int64          `json:"cachedInputTokens" doc:"The part of inputTokens served from the provider's prompt cache. Null unless usageState is measured."`
	OutputTokens      *int64          `json:"outputTokens" doc:"The whole output, including reasoningTokens. Null unless usageState is measured."`
	ReasoningTokens   *int64          `json:"reasoningTokens" doc:"The part of outputTokens spent reasoning. Null unless usageState is measured."`
	StartedAt         string          `json:"startedAt" doc:"RFC3339 UTC."`
	EndedAt           *string         `json:"endedAt" doc:"RFC3339 UTC. Null until the attempt ends."`
	Result            json.RawMessage `json:"result" doc:"The durable structured result envelope. Null until one is recorded."`
	HasTranscript     bool            `json:"hasTranscript" doc:"Whether a transcript is stored for this attempt."`
	TranscriptPath    string          `json:"transcriptPath" doc:"The download path for this transcript, empty when none is stored."`
}

// stepOutput is one durable target Step.
type stepOutput struct {
	Ordinal   int             `json:"ordinal" doc:"The stable Step identity within this Run, starting at 1."`
	Kind      string          `json:"kind" enum:"create_run_worker,acquire_run_worker_session,clone_repository,plan,implement,sync_pull_request,await_ci,review,mark_pull_request_ready,merge_pull_request" doc:"The operation this Step records."`
	Iteration int             `json:"iteration" doc:"The semantic repetition number for this Step kind, when applicable."`
	Reason    string          `json:"reason" doc:"Why this Step was authorized, when applicable."`
	State     string          `json:"state" enum:"running,completed,failed" doc:"The durable Step lifecycle state."`
	StartedAt string          `json:"startedAt" doc:"RFC3339 UTC."`
	EndedAt   *string         `json:"endedAt" doc:"RFC3339 UTC. Null while its last Attempt is still running."`
	Result    json.RawMessage `json:"result" doc:"The durable structured result envelope. Null until one is recorded."`
	Attempts  []attemptOutput `json:"attempts" doc:"This Step's workflow-authorized semantic Agent Attempts, oldest first. Native Temporal activity tries are absent."`
	Usage     usageOutput     `json:"usage" doc:"Rolled up across this Step's Attempts."`
}

type confirmedMergeOutput struct {
	ReviewedHead string `json:"reviewedHead" doc:"The exact pull-request head reviewed before merge."`
	MergeSHA     string `json:"mergeSha" doc:"The authoritative merge commit SHA."`
}

// runOutput is one attempt at a whole Ticket.
type runOutput struct {
	ID             string                `json:"id" doc:"Temporal's run id for this Run."`
	TicketID       int64                 `json:"ticketId" doc:"The Ticket this Run belongs to."`
	StartedAt      string                `json:"startedAt" doc:"RFC3339 UTC."`
	EndedAt        *string               `json:"endedAt" doc:"RFC3339 UTC. Null until the Run ends."`
	Outcome        string                `json:"outcome" enum:",succeeded,canceled,exhausted,failed" doc:"The target Run outcome, including retained historical exhausted Runs. Empty until terminal."`
	FailureKind    string                `json:"failureKind" enum:",invalid_input,agent_unrecoverable,agent_attempt_budget,review_budget,ci_unobserved,github_auth,github_ruleset,github_unavailable,run_worker_unavailable,persistence_unavailable,semantic_deadline,infrastructure" doc:"The target Run failure category, including retained historical budget values. Empty when not failed."`
	Active         bool                  `json:"active" doc:"Whether this Run is still nonterminal."`
	Phase          string                `json:"phase" enum:",create_run_worker,acquire_run_worker_session,clone_repository,plan,implement,sync_pull_request,await_ci,review,mark_pull_request_ready,merge_pull_request" doc:"The latest active Step kind, falling back to the latest terminal Step kind."`
	ConfirmedMerge *confirmedMergeOutput `json:"confirmedMerge,omitempty" doc:"Immutable merge evidence, absent unless this Run ended in a Confirmed Merge."`
	Steps          []stepOutput          `json:"steps" doc:"This Run's Steps, in pipeline order."`
	Usage          usageOutput           `json:"usage" doc:"Rolled up across this Run's Steps."`
}

type ticketRunsInput struct {
	TicketID int64 `path:"ticketID" minimum:"1" doc:"The Ticket identifier."`
}

type ticketRunsOutput struct {
	Body struct {
		Runs []runOutput `json:"runs" doc:"This Ticket's Runs, most recent first."`
	}
}

// getTicketRuns returns every Run of a Ticket, each with its Steps and
// Attempts and rolled-up token usage — the console's ticket detail view.
func (service *Service) getTicketRuns(ctx context.Context, input *ticketRunsInput) (*ticketRunsOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	ticketID := store.TicketID(input.TicketID)
	if _, err := service.ticket(ctx, ticketID); err != nil {
		return nil, ticketStoreError(err)
	}
	runs, err := service.tickets.RunsForTicket(ctx, ticketID)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	output := &ticketRunsOutput{}
	for _, run := range runs {
		detail, err := service.tickets.TargetRunDetail(ctx, run.ID)
		if err != nil {
			return nil, ticketStoreError(err)
		}
		output.Body.Runs = append(output.Body.Runs, runOutputFrom(detail))
	}
	return output, nil
}

func runOutputFrom(detail store.TargetRunDetail) runOutput {
	usage, complete := historyUsage(detail.Steps)
	outcome := string(detail.Run.TargetOutcome)
	failureKind := string(detail.Run.TargetFailure)
	out := runOutput{
		ID:          detail.Run.ID,
		TicketID:    int64(detail.Run.TicketID),
		StartedAt:   wireTime(detail.Run.StartedAt),
		EndedAt:     optionalWireTime(detail.Run.EndedAt),
		Outcome:     outcome,
		FailureKind: failureKind,
		Active:      detail.Run.EndedAt.IsZero(),
		Phase:       historyPhase(detail.Steps),
		Usage:       usageOutputFrom(usage, complete),
	}
	if detail.Run.ReviewedHead != "" && detail.Run.MergeSHA != "" {
		out.ConfirmedMerge = &confirmedMergeOutput{ReviewedHead: detail.Run.ReviewedHead, MergeSHA: detail.Run.MergeSHA}
	}
	for _, step := range detail.Steps {
		out.Steps = append(out.Steps, stepOutputFrom(step, detail.Run.TicketID))
	}
	return out
}

func stepOutputFrom(detail store.TargetStepDetail, ticketID store.TicketID) stepOutput {
	step := detail.Step
	usage, complete := attemptsUsage(detail.Attempts)
	out := stepOutput{
		Ordinal:   step.Ordinal,
		Kind:      string(step.Kind),
		Iteration: step.Iteration,
		Reason:    step.Reason,
		State:     string(step.State),
		StartedAt: wireTime(step.StartedAt),
		EndedAt:   optionalWireTime(step.EndedAt),
		Result:    step.Result,
		Usage:     usageOutputFrom(usage, complete),
	}
	for _, attempt := range detail.Attempts {
		out.Attempts = append(out.Attempts, attemptOutputFrom(attempt, ticketID, step))
	}
	return out
}

func attemptOutputFrom(attempt store.AgentAttempt, ticketID store.TicketID, step store.RunStep) attemptOutput {
	out := attemptOutput{
		AttemptNo:     attempt.ID.AttemptNo,
		AgentStage:    string(attempt.AgentStage),
		Model:         attempt.Model.Name,
		Effort:        attempt.Model.Effort,
		State:         string(attempt.State),
		FailureKind:   string(attempt.FailureKind),
		ExecutionID:   attempt.ExecutionID,
		UsageState:    string(attempt.UsageState),
		Measured:      attempt.UsageState == work.UsageMeasured,
		StartedAt:     wireTime(attempt.StartedAt),
		EndedAt:       optionalWireTime(attempt.EndedAt),
		Result:        attempt.Result,
		HasTranscript: attempt.TranscriptPresent,
	}
	if attempt.TranscriptPresent {
		out.TranscriptPath = fmt.Sprintf("/v1/tickets/%d/runs/%s/steps/%d/attempts/%d/transcript", ticketID, attempt.ID.RunID, step.Ordinal, attempt.ID.AttemptNo)
	}
	if attempt.UsageState == work.UsageMeasured {
		out.InputTokens = &attempt.Usage.InputTokens
		out.CachedInputTokens = &attempt.Usage.CachedInputTokens
		out.OutputTokens = &attempt.Usage.OutputTokens
		out.ReasoningTokens = &attempt.Usage.ReasoningTokens
	}
	return out
}

func attemptsUsage(attempts []store.AgentAttempt) (work.Usage, bool) {
	complete := true
	var usage work.Usage
	for _, attempt := range attempts {
		if attempt.UsageState != work.UsageMeasured {
			complete = false
			continue
		}
		usage.InputTokens += attempt.Usage.InputTokens
		usage.CachedInputTokens += attempt.Usage.CachedInputTokens
		usage.OutputTokens += attempt.Usage.OutputTokens
		usage.ReasoningTokens += attempt.Usage.ReasoningTokens
	}
	return usage, complete
}

func historyUsage(steps []store.TargetStepDetail) (work.Usage, bool) {
	complete := true
	var usage work.Usage
	for _, step := range steps {
		stepUsage, stepComplete := attemptsUsage(step.Attempts)
		usage.InputTokens += stepUsage.InputTokens
		usage.CachedInputTokens += stepUsage.CachedInputTokens
		usage.OutputTokens += stepUsage.OutputTokens
		usage.ReasoningTokens += stepUsage.ReasoningTokens
		complete = complete && stepComplete
	}
	return usage, complete
}

func historyPhase(steps []store.TargetStepDetail) string {
	for index := len(steps) - 1; index >= 0; index-- {
		if steps[index].Step.State == work.StepStateRunning {
			return string(steps[index].Step.Kind)
		}
	}
	if len(steps) == 0 {
		return ""
	}
	return string(steps[len(steps)-1].Step.Kind)
}

func usageOutputFrom(usage work.Usage, complete bool) usageOutput {
	return usageOutput{
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		Complete:          complete,
	}
}

// optionalWireTime renders t as RFC3339 UTC, or nil for the zero time — the
// convention EndedAt uses everywhere: zero means "has not ended yet", not
// the Unix epoch.
func optionalWireTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	formatted := wireTime(t)
	return &formatted
}

type targetRunTranscriptInput struct {
	TicketID  int64  `path:"ticketID" minimum:"1" doc:"The Ticket identifier."`
	RunID     string `path:"runID" doc:"The Run's Temporal run id."`
	Ordinal   int    `path:"ordinal" minimum:"1" doc:"The target Step's ordinal identity."`
	AttemptNo int    `path:"attemptNo" minimum:"1" doc:"Which semantic Agent Attempt to download."`
}

type transcriptOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	Body               []byte
}

// getTargetAttemptTranscript downloads one target transcript by the durable ordinal Step identity.
func (service *Service) getTargetAttemptTranscript(ctx context.Context, input *targetRunTranscriptInput) (*transcriptOutput, error) {
	if service.tickets == nil {
		return nil, clientError(http.StatusServiceUnavailable, "store_unavailable", "ticket store is not configured")
	}
	run, err := service.tickets.Run(ctx, input.RunID)
	if err != nil {
		return nil, ticketStoreError(err)
	}
	if run.TicketID != store.TicketID(input.TicketID) {
		return nil, clientError(http.StatusNotFound, "not_found", "run does not belong to this ticket")
	}
	id := store.TargetAttemptID{RunID: input.RunID, StepOrdinal: input.Ordinal, AttemptNo: input.AttemptNo}
	transcript, err := service.tickets.TargetTranscript(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, clientError(http.StatusNotFound, "not_found", "no transcript is stored for this attempt")
		}
		return nil, ticketStoreError(err)
	}
	raw, err := decompressBytes(transcript.CompressedBytes, transcript.Compression)
	if err != nil {
		return nil, clientError(http.StatusInternalServerError, "internal", err.Error())
	}
	filename := fmt.Sprintf("ticket-%d-step%d-attempt%d-%s.jsonl", run.TicketID, input.Ordinal, input.AttemptNo, input.RunID)
	return &transcriptOutput{
		ContentType:        "application/x-ndjson",
		ContentDisposition: fmt.Sprintf(`attachment; filename="%s"`, filename),
		Body:               raw,
	}, nil
}

func decompressBytes(compressed []byte, compression string) ([]byte, error) {
	switch compression {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, fmt.Errorf("opening gzip transcript: %w", err)
		}
		defer func() { _ = reader.Close() }()
		raw, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("reading gzip transcript: %w", err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("transcript uses unknown compression %q", compression)
	}
}
