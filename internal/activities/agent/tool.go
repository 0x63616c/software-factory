package agentactivities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/0x63616c/software-factory/internal/agent"
	"github.com/0x63616c/software-factory/internal/agenttool"
	"github.com/0x63616c/software-factory/internal/blobs"
	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/telemetry"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// ToolActivities owns Run Worker generic tool dispatch and retry records.
type ToolActivities struct {
	blobs         blobs.Store
	artifacts     agent.ArtifactStore
	conversations agent.ConversationStore
	transcripts   agent.TranscriptStore
	clock         clock.Clock
	metrics       AgentMetrics
	logger        *slog.Logger
	toolsets      map[agent.ToolsetID]agenttool.Set
}

// NewToolActivities constructs the Run Worker generic tool activity.
func NewToolActivities(blobStore blobs.Store, clk clock.Clock, toolsets ...agenttool.Set) (*ToolActivities, error) {
	return newToolActivities(blobStore, clk, noopAgentMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)), toolsets...)
}

// NewObservedToolActivities constructs production Run Worker tool activities.
func NewObservedToolActivities(
	blobStore blobs.Store,
	clk clock.Clock,
	metrics AgentMetrics,
	logger *slog.Logger,
	toolsets ...agenttool.Set,
) (*ToolActivities, error) {
	return newToolActivities(blobStore, clk, metrics, logger, toolsets...)
}

func newToolActivities(
	blobStore blobs.Store,
	clk clock.Clock,
	metrics AgentMetrics,
	logger *slog.Logger,
	toolsets ...agenttool.Set,
) (*ToolActivities, error) {
	if blobStore == nil {
		return nil, fmt.Errorf("agent tool activities need a blob store")
	}
	if clk == nil {
		return nil, fmt.Errorf("agent tool activities need a clock")
	}
	if metrics == nil {
		return nil, fmt.Errorf("agent tool activities need metrics")
	}
	if logger == nil {
		return nil, fmt.Errorf("agent tool activities need a logger")
	}
	byID := make(map[agent.ToolsetID]agenttool.Set, len(toolsets))
	for _, toolset := range toolsets {
		if _, exists := byID[toolset.ID()]; exists {
			return nil, fmt.Errorf("agent tool activities received duplicate toolset %q", toolset.ID())
		}
		byID[toolset.ID()] = toolset
	}
	return &ToolActivities{
		blobs: blobStore, artifacts: agent.NewArtifactStore(blobStore),
		conversations: agent.NewConversationStore(blobStore), transcripts: agent.NewTranscriptStore(blobStore),
		clock: clk, metrics: metrics, logger: logger, toolsets: byID,
	}, nil
}

// Tool executes one typed tool call exactly once and appends its provider output.
func (activities *ToolActivities) Tool(ctx context.Context, input agent.ToolInput) (agent.ToolOutput, error) {
	toolset, ok := activities.toolsets[input.ToolsetID]
	if !ok {
		return agent.ToolOutput{}, invalidInput("unknown agent toolset %q", input.ToolsetID)
	}
	if input.ToolsetFingerprint == "" || input.ToolsetFingerprint != toolset.Fingerprint() {
		return agent.ToolOutput{}, invalidInput(
			"agent toolset %q fingerprint changed: got %q, want %q",
			input.ToolsetID, input.ToolsetFingerprint, toolset.Fingerprint(),
		)
	}
	identity, err := activities.conversations.Identity(input.ConversationRef)
	if err != nil {
		return agent.ToolOutput{}, invalidInput("resolve tool conversation identity: %v", err)
	}
	attempt := activityAttempt(ctx)
	if attempt > 1 {
		activities.metrics.AgentActivityRetry(agent.ToolActivityName)
	}
	startedKey, completedKey, err := operationKeys(identity, input.ConversationRef.Revision, input.Call.CallID)
	if err != nil {
		return agent.ToolOutput{}, invalidInput("name tool operation: %v", err)
	}
	if encoded, err := activities.blobs.Get(ctx, completedKey); err == nil {
		var recorded agent.ToolOutput
		if err := json.Unmarshal(encoded, &recorded); err != nil {
			return agent.ToolOutput{}, transientFailure("decode completed tool operation", err)
		}
		return recorded, nil
	} else if !errors.Is(err, blobs.ErrNotFound) {
		return agent.ToolOutput{}, transientFailure("load completed tool operation", err)
	}
	arguments, err := activities.artifacts.LoadArguments(ctx, input.Call.ArgumentsRef)
	if err != nil {
		return agent.ToolOutput{}, transientFailure("load tool arguments", err)
	}
	if _, err := activities.blobs.Get(ctx, startedKey); err == nil {
		return agent.ToolOutput{}, temporal.NewNonRetryableApplicationError(
			"tool execution was interrupted after it started", agent.ErrorTypeAmbiguousToolExecution, nil,
		)
	} else if !errors.Is(err, blobs.ErrNotFound) {
		return agent.ToolOutput{}, transientFailure("load started tool operation", err)
	}
	if err := activities.blobs.Put(ctx, startedKey, []byte(input.Call.CallID)); err != nil {
		return agent.ToolOutput{}, transientFailure("record started tool operation", err)
	}
	stopHeartbeat := startToolHeartbeat(ctx, input.Call.Name)
	defer stopHeartbeat()
	started := activities.clock.Now()
	activities.logger.InfoContext(ctx, "agent tool call started",
		slog.String("agent", identity), slog.String("tool", input.Call.Name),
		slog.String("call_id", input.Call.CallID), slog.Int("activity_attempt", int(attempt)),
	)
	result, err := toolset.Execute(ctx, input.Call.Name, arguments)
	if err != nil {
		took := activities.clock.Now().Sub(started)
		activities.metrics.AgentToolCall(input.Call.Name, telemetry.AgentOutcomeFailed, input.ConversationRef.Bytes, took)
		activities.logger.WarnContext(ctx, "agent tool call failed",
			slog.String("agent", identity), slog.String("tool", input.Call.Name),
			slog.String("call_id", input.Call.CallID), slog.Int("activity_attempt", int(attempt)),
			slog.Int64("duration_millis", took.Milliseconds()),
		)
		return agent.ToolOutput{}, transientFailure(fmt.Sprintf("execute agent tool %q", input.Call.Name), err)
	}
	conversationRef, err := activities.conversations.Append(ctx, identity, &input.ConversationRef, []agent.ConversationItem{{
		Kind: agent.ItemFunctionOutput, CallID: input.Call.CallID, Output: result.Content,
	}})
	if err != nil {
		return agent.ToolOutput{}, transientFailure("store tool output conversation", err)
	}
	output := agent.ToolOutput{CallID: input.Call.CallID, ConversationRef: conversationRef, IsError: result.IsError}
	outcome := telemetry.AgentOutcomeSucceeded
	if result.IsError {
		outcome = telemetry.AgentOutcomeToolError
	}
	took := activities.clock.Now().Sub(started)
	activities.metrics.AgentToolCall(input.Call.Name, outcome, conversationRef.Bytes, took)
	activities.logger.InfoContext(ctx, "agent tool call completed",
		slog.String("agent", identity), slog.String("tool", input.Call.Name),
		slog.String("call_id", input.Call.CallID), slog.Int("activity_attempt", int(attempt)),
		slog.String("outcome", string(outcome)), slog.Int64("conversation_bytes", conversationRef.Bytes),
		slog.Int64("duration_millis", took.Milliseconds()),
	)
	if input.TranscriptRef.Key != "" {
		output.TranscriptRef, err = activities.transcripts.Append(ctx, identity, &input.TranscriptRef, agent.TranscriptEvent{
			Type: agent.EventToolCompleted, ToolName: input.Call.Name, CallID: input.Call.CallID,
			IsError: result.IsError, DurationMillis: took.Milliseconds(),
		})
		if err != nil {
			return agent.ToolOutput{}, transientFailure("append tool transcript event", err)
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return agent.ToolOutput{}, fmt.Errorf("encode completed tool operation: %w", err)
	}
	if err := activities.blobs.Put(ctx, completedKey, encoded); err != nil {
		return agent.ToolOutput{}, transientFailure("record completed tool operation", err)
	}
	return output, nil
}

func startToolHeartbeat(ctx context.Context, name string) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	done := make(chan struct{})
	activity.RecordHeartbeat(ctx, struct {
		Tool string
	}{Tool: name})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, struct {
					Tool string
				}{Tool: name})
			}
		}
	}()
	return func() { close(done) }
}

func operationKeys(identity string, revision int, callID string) (blobs.Key, blobs.Key, error) {
	if callID == "" {
		return blobs.Key{}, blobs.Key{}, fmt.Errorf("call id is blank")
	}
	digestBytes := sha256.Sum256([]byte(callID))
	operation := fmt.Sprintf("%s/operations/%d/%s", identity, revision, hex.EncodeToString(digestBytes[:]))
	started, err := blobs.NewKey(blobs.BucketConversations, operation+"/started")
	if err != nil {
		return blobs.Key{}, blobs.Key{}, err
	}
	completed, err := blobs.NewKey(blobs.BucketConversations, operation+"/completed")
	return started, completed, err
}
