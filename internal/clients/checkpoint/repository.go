package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	checkpointprotocol "github.com/0x63616c/software-factory/internal/checkpoint"
	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/work"
)

// RepositoryClient is scoped to one Run Worker generation.
type RepositoryClient struct {
	httpClient *http.Client
	endpoint   string
	capability string
	runID      string
}

// NewRepository constructs a generation-scoped repository checkpoint client.
func NewRepository(baseURL string, identity work.RunWorkerIdentity, capability string, httpClient *http.Client) (*RepositoryClient, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("constructing repository checkpoint client: base URL must be an absolute HTTP URL")
	}
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("constructing repository checkpoint client: %w", err)
	}
	if strings.TrimSpace(capability) == "" || httpClient == nil {
		return nil, fmt.Errorf("constructing repository checkpoint client: capability and HTTP client are required")
	}
	endpoint, err := url.JoinPath(strings.TrimSuffix(base.String(), "/"), checkpointprotocol.RepositoryPathFor(identity.RunID, identity.Generation))
	if err != nil {
		return nil, fmt.Errorf("constructing repository checkpoint endpoint: %w", err)
	}
	return &RepositoryClient{httpClient: httpClient, endpoint: endpoint, capability: capability, runID: identity.RunID}, nil
}

// Load reconciles the latest durable repository position. A 204 means this
// generation is authorized but no repository Step has completed yet.
func (client *RepositoryClient) Load(ctx context.Context) (_ store.GitCheckpoint, found bool, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
	if err != nil {
		return store.GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: building request: %w", err)
	}
	request.Header.Set(checkpointprotocol.RepositoryCapabilityHeader, client.capability)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return store.GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("loading repository checkpoint: closing response: %w", closeErr))
		}
	}()
	if response.StatusCode == http.StatusNoContent {
		return store.GitCheckpoint{}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return store.GitCheckpoint{}, false, repositoryHTTPError("loading", response.StatusCode)
	}
	var position checkpointprotocol.Repository
	if err := json.NewDecoder(response.Body).Decode(&position); err != nil {
		return store.GitCheckpoint{}, false, fmt.Errorf("loading repository checkpoint: decoding position: %w", err)
	}
	return repositoryPosition(client.runID, position), true, nil
}

// Checkpoint stores a repository Step result before the activity acknowledges
// success.
func (client *RepositoryClient) Checkpoint(ctx context.Context, input store.GitCheckpointInput) (_ store.GitCheckpoint, err error) {
	return client.checkpoint(ctx, http.MethodPut, input)
}

// CheckpointEffect stores an external effect result while leaving terminal
// Store completion to its main-control transaction.
func (client *RepositoryClient) CheckpointEffect(ctx context.Context, input store.GitCheckpointInput) (_ store.GitCheckpoint, err error) {
	return client.checkpoint(ctx, http.MethodPatch, input)
}

func (client *RepositoryClient) checkpoint(ctx context.Context, method string, input store.GitCheckpointInput) (_ store.GitCheckpoint, err error) {
	body, err := json.Marshal(checkpointprotocol.RepositoryWrite{
		Repository: checkpointprotocol.Repository{
			StepOrdinal: input.StepOrdinal, Branch: input.Branch, PushedHead: input.PushedHead,
			ObservedBase: input.ObservedBase, PullRequestNumber: input.PullRequestNumber,
			PullRequestNodeID: input.PullRequestNodeID, StepResult: input.StepResult,
		},
		CompletedAt: input.CompletedAt,
	})
	if err != nil {
		return store.GitCheckpoint{}, fmt.Errorf("checkpointing repository Step: encoding position: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return store.GitCheckpoint{}, fmt.Errorf("checkpointing repository Step: building request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(checkpointprotocol.RepositoryCapabilityHeader, client.capability)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return store.GitCheckpoint{}, fmt.Errorf("checkpointing repository Step: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("checkpointing repository Step: closing response: %w", closeErr))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return store.GitCheckpoint{}, repositoryHTTPError("checkpointing", response.StatusCode)
	}
	var position checkpointprotocol.Repository
	if err := json.NewDecoder(response.Body).Decode(&position); err != nil {
		return store.GitCheckpoint{}, fmt.Errorf("checkpointing repository Step: decoding position: %w", err)
	}
	return repositoryPosition(client.runID, position), nil
}

func repositoryHTTPError(action string, status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%s repository checkpoint: HTTP %d: %w", action, status, ErrUnauthorized)
	case http.StatusConflict:
		return fmt.Errorf("%s repository checkpoint: HTTP %d: %w", action, status, ErrConflict)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%s repository checkpoint: HTTP %d: %w", action, status, ErrInvalid)
	default:
		return fmt.Errorf("%s repository checkpoint: unexpected HTTP status %d", action, status)
	}
}

func repositoryPosition(runID string, position checkpointprotocol.Repository) store.GitCheckpoint {
	return store.GitCheckpoint{
		RunID: runID, StepOrdinal: position.StepOrdinal, Branch: position.Branch, PushedHead: position.PushedHead,
		ObservedBase: position.ObservedBase, PullRequestNumber: position.PullRequestNumber,
		PullRequestNodeID: position.PullRequestNodeID, StepResult: position.StepResult,
	}
}

// RepositoryFactory reads the projected generation capability whenever the
// target composition opens a client.
type RepositoryFactory struct {
	baseURL        string
	capabilityFile string
	httpClient     *http.Client
	readFile       func(string) ([]byte, error)
}

// NewRepositoryFactory builds a projected generation-capability client factory.
func NewRepositoryFactory(baseURL, capabilityFile string, httpClient *http.Client, readFile func(string) ([]byte, error)) (*RepositoryFactory, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(capabilityFile) == "" || httpClient == nil || readFile == nil {
		return nil, fmt.Errorf("constructing repository checkpoint factory: URL, capability file, HTTP client, and file reader are required")
	}
	return &RepositoryFactory{baseURL: baseURL, capabilityFile: capabilityFile, httpClient: httpClient, readFile: readFile}, nil
}

// Open reads the current projected value and scopes a client to identity.
func (factory *RepositoryFactory) Open(identity work.RunWorkerIdentity) (*RepositoryClient, error) {
	raw, err := factory.readFile(factory.capabilityFile)
	if err != nil {
		return nil, fmt.Errorf("opening repository checkpoint client: reading projected capability: %w", err)
	}
	client, err := NewRepository(factory.baseURL, identity, strings.TrimSpace(string(raw)), factory.httpClient)
	if err != nil {
		return nil, fmt.Errorf("opening repository checkpoint client: %w", err)
	}
	return client, nil
}
