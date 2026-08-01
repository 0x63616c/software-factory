// Package checkpoint defines the narrow Run Worker checkpoint HTTP protocol.
package checkpoint

import (
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

// AttemptPath renders one exact Agent Attempt checkpoint path.
func AttemptPath(runID string, stepOrdinal, attemptNo int) string {
	return "/v1/run-worker/runs/" + url.PathEscape(runID) + "/steps/" + strconv.Itoa(stepOrdinal) + "/attempts/" + strconv.Itoa(attemptNo) + "/checkpoint"
}

// RepositoryPathFor renders the checkpoint path owned by one Run Worker generation.
func RepositoryPathFor(runID string, generation int) string {
	return "/v1/run-worker/runs/" + url.PathEscape(runID) + "/generations/" + strconv.Itoa(generation) + "/repository-checkpoint"
}

const (
	// CapabilityHeader carries one active Agent Attempt's checkpoint capability.
	CapabilityHeader = "X-Software-Factory-Checkpoint-Capability"
	// Path is the exact-attempt checkpoint route registered by the factory API.
	Path = "/v1/run-worker/runs/{runID}/steps/{stepOrdinal}/attempts/{attemptNo}/checkpoint"
	// PutServeMuxPattern and GetServeMuxPattern are the only method/path pairs
	// mounted outside the legacy API authentication middleware.
	PutServeMuxPattern = "PUT " + Path
	// GetServeMuxPattern mounts exact-attempt reconciliation outside broad authentication.
	GetServeMuxPattern = "GET " + Path
	// RepositoryCapabilityHeader carries one Run Worker generation's distinct
	// repository checkpoint capability.
	RepositoryCapabilityHeader = "X-Software-Factory-Repository-Capability"
	// RepositoryPath is the generation-scoped Git/PR checkpoint route.
	RepositoryPath = "/v1/run-worker/runs/{runID}/generations/{generation}/repository-checkpoint"
	// RepositoryPutServeMuxPattern mounts generation-scoped writes outside broad authentication.
	RepositoryPutServeMuxPattern = "PUT " + RepositoryPath
	// RepositoryGetServeMuxPattern mounts generation-scoped reads outside broad authentication.
	RepositoryGetServeMuxPattern = "GET " + RepositoryPath
	// RepositoryEffectPatchServeMuxPattern records an external effect without
	// completing its Store Step.
	RepositoryEffectPatchServeMuxPattern = "PATCH " + RepositoryPath
)

// Attempt is running progress or terminal evidence for one active Agent Attempt.
type Attempt struct {
	ExecutionID string                 `json:"executionId,omitempty" doc:"The opaque identity of the active execution."`
	State       work.AgentAttemptState `json:"state" enum:"running,succeeded,failed" doc:"The execution state this checkpoint proves."`
	FailureKind work.RunFailureKind    `json:"failureKind,omitempty" doc:"The classified terminal failure, when state is failed."`
	UsageState  work.UsageState        `json:"usageState" enum:"unknown,measured" doc:"Whether provider usage is available."`
	Usage       Usage                  `json:"usage" doc:"Provider-reported token usage; all fields are zero while usage is unknown."`
	EndedAt     *time.Time             `json:"endedAt,omitempty" doc:"RFC3339 UTC terminal time; absent while running."`
	Result      json.RawMessage        `json:"result,omitempty" doc:"The terminal provider envelope; absent while running."`
	Transcript  *Transcript            `json:"transcript,omitempty" doc:"Durable partial or terminal transcript material."`
}

// Repository is the durable Git/PR position for the latest repository-affine
// Step or deferred external effect in a Run.
type Repository struct {
	StepOrdinal       int             `json:"stepOrdinal" minimum:"1" doc:"The repository-affine Step ordinal that owns this evidence."`
	Branch            string          `json:"branch" minLength:"1" doc:"The Run-owned branch."`
	PushedHead        string          `json:"pushedHead" doc:"The latest head accepted by GitHub, when available."`
	ObservedBase      string          `json:"observedBase" doc:"The target branch head observed by this Step, when available."`
	PullRequestNumber int             `json:"pullRequestNumber" minimum:"0" doc:"The pull request number, or zero before one exists."`
	PullRequestNodeID string          `json:"pullRequestNodeId" doc:"The GraphQL pull request node identity, when one exists."`
	StepResult        json.RawMessage `json:"stepResult" doc:"The kind-specific durable Step or external-effect result."`
}

// RepositoryWrite supplies the effect time atomically recorded with a
// repository checkpoint. PUT also uses it as the Step completion time; PATCH
// leaves that Step running for terminal finalization.
type RepositoryWrite struct {
	Repository
	CompletedAt time.Time `json:"completedAt" doc:"RFC3339 UTC time at which the represented operation completed."`
}

// Usage is the provider's four token counters.
type Usage struct {
	InputTokens       int64 `json:"inputTokens" minimum:"0" doc:"Whole input tokens, including cached input."`
	CachedInputTokens int64 `json:"cachedInputTokens" minimum:"0" doc:"Input tokens served from cache."`
	OutputTokens      int64 `json:"outputTokens" minimum:"0" doc:"Whole output tokens, including reasoning."`
	ReasoningTokens   int64 `json:"reasoningTokens" minimum:"0" doc:"Output tokens spent reasoning."`
}

// Transcript is compressed transcript material and its integrity metadata.
type Transcript struct {
	CompressedBytes       []byte `json:"compressedBytes" doc:"Compressed transcript bytes, base64 encoded on the wire."`
	Compression           string `json:"compression" minLength:"1" doc:"The transcript compression codec."`
	UncompressedSizeBytes int64  `json:"uncompressedSizeBytes" minimum:"0" doc:"Transcript size before compression."`
	Checksum              []byte `json:"checksum" doc:"Transcript checksum, base64 encoded on the wire."`
}
