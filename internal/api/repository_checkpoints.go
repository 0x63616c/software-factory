package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// RepositoryCheckpointStore is the only persistence authority exposed to a
// Run Worker generation's repository route.
type RepositoryCheckpointStore interface {
	LoadRepositoryCheckpoint(context.Context, work.RunWorkerIdentity, string) (store.GitCheckpoint, bool, error)
	CheckpointRepository(context.Context, store.RepositoryCheckpointInput) (store.GitCheckpoint, error)
	CheckpointRepositoryEffect(context.Context, store.RepositoryCheckpointInput) (store.GitCheckpoint, error)
}

type repositoryCheckpointReadInput struct {
	RunID      string `path:"runID" format:"uuid" doc:"The owning Run identity."`
	Generation int    `path:"generation" minimum:"1" doc:"The active Run Worker generation."`
	Capability string `header:"X-Software-Factory-Repository-Capability" doc:"The capability minted for this Run Worker generation."`
}

type repositoryCheckpointInput struct {
	RunID      string `path:"runID" format:"uuid" doc:"The owning Run identity."`
	Generation int    `path:"generation" minimum:"1" doc:"The active Run Worker generation."`
	Capability string `header:"X-Software-Factory-Repository-Capability" doc:"The capability minted for this Run Worker generation."`
	Body       checkpoint.RepositoryWrite
}

type repositoryCheckpointOutput struct{ Body checkpoint.Repository }

func repositoryCheckpointOperation(operation *huma.Operation) {
	operation.Summary = "Checkpoint a repository Step"
	operation.Description = "Atomically stores one Run Worker generation's monotonic Git/PR position and completed Step result."
	operation.Security = []map[string][]string{{"repositoryCheckpointCapability": {}}}
}

func repositoryEffectCheckpointOperation(operation *huma.Operation) {
	operation.Summary = "Checkpoint a deferred repository effect"
	operation.Description = "Stores one Run Worker generation's monotonic external-effect result without completing its Store Step; terminal finalization owns that transition."
	operation.Security = []map[string][]string{{"repositoryCheckpointCapability": {}}}
}

func readRepositoryCheckpointOperation(operation *huma.Operation) {
	operation.Summary = "Read a repository checkpoint"
	operation.Description = "Reconciles the latest completed Git/PR position using the capability scoped to one Run Worker generation."
	operation.Security = []map[string][]string{{"repositoryCheckpointCapability": {}}}
}

func (service *Service) loadRepositoryCheckpoint(ctx context.Context, input *repositoryCheckpointReadInput) (*repositoryCheckpointOutput, error) {
	identity, err := repositoryCheckpointIdentity(input.RunID, input.Generation, input.Capability)
	if err != nil {
		return nil, err
	}
	if service.repositoryCheckpoints == nil {
		return nil, clientError(http.StatusServiceUnavailable, "checkpoint_unavailable", "repository checkpoint store is not configured")
	}
	position, found, err := service.repositoryCheckpoints.LoadRepositoryCheckpoint(ctx, identity, input.Capability)
	if err != nil {
		return nil, checkpointStoreError(err)
	}
	if !found {
		return nil, huma.NewError(http.StatusNoContent, "")
	}
	return &repositoryCheckpointOutput{Body: repositoryCheckpointFromStore(position)}, nil
}

func (service *Service) checkpointRepository(ctx context.Context, input *repositoryCheckpointInput) (*repositoryCheckpointOutput, error) {
	return service.writeRepositoryCheckpoint(ctx, input, false)
}

func (service *Service) checkpointRepositoryEffect(ctx context.Context, input *repositoryCheckpointInput) (*repositoryCheckpointOutput, error) {
	return service.writeRepositoryCheckpoint(ctx, input, true)
}

func (service *Service) writeRepositoryCheckpoint(ctx context.Context, input *repositoryCheckpointInput, deferred bool) (*repositoryCheckpointOutput, error) {
	identity, err := repositoryCheckpointIdentity(input.RunID, input.Generation, input.Capability)
	if err != nil {
		return nil, err
	}
	if service.repositoryCheckpoints == nil {
		return nil, clientError(http.StatusServiceUnavailable, "checkpoint_unavailable", "repository checkpoint store is not configured")
	}
	if input.Body.StepOrdinal <= 0 || strings.TrimSpace(input.Body.Branch) == "" || input.Body.CompletedAt.IsZero() || len(input.Body.StepResult) == 0 || !json.Valid(input.Body.StepResult) {
		return nil, clientError(http.StatusUnprocessableEntity, "invalid_checkpoint", "repository checkpoint evidence is invalid")
	}
	checkpoint := store.RepositoryCheckpointInput{
		Identity: identity, Capability: input.Capability, CompletedAt: input.Body.CompletedAt.UTC(),
		GitCheckpoint: store.GitCheckpoint{
			RunID: input.RunID, StepOrdinal: input.Body.StepOrdinal, Branch: input.Body.Branch,
			PushedHead: input.Body.PushedHead, ObservedBase: input.Body.ObservedBase,
			PullRequestNumber: input.Body.PullRequestNumber, PullRequestNodeID: input.Body.PullRequestNodeID,
			StepResult: input.Body.StepResult,
		},
	}
	var position store.GitCheckpoint
	if deferred {
		position, err = service.repositoryCheckpoints.CheckpointRepositoryEffect(ctx, checkpoint)
	} else {
		position, err = service.repositoryCheckpoints.CheckpointRepository(ctx, checkpoint)
	}
	if err != nil {
		return nil, checkpointStoreError(err)
	}
	return &repositoryCheckpointOutput{Body: repositoryCheckpointFromStore(position)}, nil
}

func repositoryCheckpointIdentity(runID string, generation int, capability string) (work.RunWorkerIdentity, error) {
	if strings.TrimSpace(capability) == "" {
		return work.RunWorkerIdentity{}, clientError(http.StatusUnauthorized, "checkpoint_unauthorized", "repository checkpoint capability is required")
	}
	identity, err := work.NewRunWorkerIdentity(runID, generation)
	if err != nil {
		return work.RunWorkerIdentity{}, clientError(http.StatusUnprocessableEntity, "invalid_checkpoint", "Run Worker identity is invalid")
	}
	return identity, nil
}

func repositoryCheckpointFromStore(position store.GitCheckpoint) checkpoint.Repository {
	return checkpoint.Repository{
		StepOrdinal: position.StepOrdinal, Branch: position.Branch, PushedHead: position.PushedHead,
		ObservedBase: position.ObservedBase, PullRequestNumber: position.PullRequestNumber,
		PullRequestNodeID: position.PullRequestNodeID, StepResult: position.StepResult,
	}
}
