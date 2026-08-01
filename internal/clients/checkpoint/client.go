// Package checkpoint writes durable Agent Attempt checkpoints through the factory API.
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
	"github.com/google/uuid"
)

var (
	// ErrUnauthorized reports a capability that does not own the target Attempt.
	ErrUnauthorized = errors.New("checkpoint capability is unauthorized")
	// ErrConflict reports evidence that conflicts with the durable checkpoint.
	ErrConflict = errors.New("checkpoint conflicts with durable state")
	// ErrInvalid reports evidence the checkpoint API rejected as malformed.
	ErrInvalid = errors.New("checkpoint evidence is invalid")
)

// Client is scoped to one workflow-authorized Agent Attempt and its rotating capability.
type Client struct {
	httpClient *http.Client
	endpoint   string
	capability string
}

// New constructs a client that can checkpoint only id.
func New(baseURL string, id store.TargetAttemptID, capability string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("constructing checkpoint client: base URL must be an absolute HTTP URL")
	}
	if _, err := uuid.Parse(id.RunID); err != nil || id.StepOrdinal <= 0 || id.AttemptNo <= 0 {
		return nil, fmt.Errorf("constructing checkpoint client: exact Agent Attempt identity is required")
	}
	if strings.TrimSpace(capability) == "" {
		return nil, fmt.Errorf("constructing checkpoint client: Agent Attempt capability is required")
	}
	if httpClient == nil {
		return nil, fmt.Errorf("constructing checkpoint client: HTTP client is required")
	}
	path := checkpointprotocol.AttemptPath(id.RunID, id.StepOrdinal, id.AttemptNo)
	endpoint, err := url.JoinPath(strings.TrimSuffix(base.String(), "/"), path)
	if err != nil {
		return nil, fmt.Errorf("constructing checkpoint client endpoint: %w", err)
	}
	return &Client{httpClient: httpClient, endpoint: endpoint, capability: capability}, nil
}

// Checkpoint stores running progress or terminal evidence before activity acknowledgement.
func (client *Client) Checkpoint(ctx context.Context, input checkpointprotocol.Attempt) (err error) {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("checkpointing Agent Attempt: encoding evidence: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("checkpointing Agent Attempt: building request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(checkpointprotocol.CapabilityHeader, client.capability)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("checkpointing Agent Attempt: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("checkpointing Agent Attempt: closing response: %w", closeErr))
		}
	}()

	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("checkpointing Agent Attempt: HTTP %d: %w", response.StatusCode, ErrUnauthorized)
	case http.StatusConflict:
		return fmt.Errorf("checkpointing Agent Attempt: HTTP %d: %w", response.StatusCode, ErrConflict)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("checkpointing Agent Attempt: HTTP %d: %w", response.StatusCode, ErrInvalid)
	default:
		return fmt.Errorf("checkpointing Agent Attempt: unexpected HTTP status %d", response.StatusCode)
	}
}

// Load reconciles the durable checkpoint before a retry can start provider
// work. A 204 is an authorized Attempt that has not exposed provider state.
func (client *Client) Load(ctx context.Context) (_ checkpointprotocol.Attempt, found bool, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
	if err != nil {
		return checkpointprotocol.Attempt{}, false, fmt.Errorf("loading Agent Attempt checkpoint: building request: %w", err)
	}
	request.Header.Set(checkpointprotocol.CapabilityHeader, client.capability)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return checkpointprotocol.Attempt{}, false, fmt.Errorf("loading Agent Attempt checkpoint: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("loading Agent Attempt checkpoint: closing response: %w", closeErr))
		}
	}()
	if response.StatusCode == http.StatusNoContent {
		return checkpointprotocol.Attempt{}, false, nil
	}
	if response.StatusCode == http.StatusOK {
		var attempt checkpointprotocol.Attempt
		if err := json.NewDecoder(response.Body).Decode(&attempt); err != nil {
			return checkpointprotocol.Attempt{}, false, fmt.Errorf("loading Agent Attempt checkpoint: decoding evidence: %w", err)
		}
		return attempt, true, nil
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return checkpointprotocol.Attempt{}, false, fmt.Errorf("loading Agent Attempt checkpoint: HTTP %d: %w", response.StatusCode, ErrUnauthorized)
	case http.StatusConflict:
		return checkpointprotocol.Attempt{}, false, fmt.Errorf("loading Agent Attempt checkpoint: HTTP %d: %w", response.StatusCode, ErrConflict)
	default:
		return checkpointprotocol.Attempt{}, false, fmt.Errorf("loading Agent Attempt checkpoint: unexpected HTTP status %d", response.StatusCode)
	}
}
