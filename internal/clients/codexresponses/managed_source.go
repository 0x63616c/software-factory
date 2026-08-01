package codexresponses

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/0x63616c/software-factory/internal/work"
)

// ManagedCredentialFileSource returns a current, refresh-token-free Codex document.
type ManagedCredentialFileSource interface {
	ManagedCredentialFile(ctx context.Context) (work.CredentialFile, error)
}

// ManagedCredentialSource adapts the durable codexauth source to direct calls.
type ManagedCredentialSource struct {
	source ManagedCredentialFileSource
}

// NewManagedCredentialSource constructs an adapter over a durable source.
func NewManagedCredentialSource(source ManagedCredentialFileSource) (*ManagedCredentialSource, error) {
	if source == nil {
		return nil, fmt.Errorf("a managed Codex credential source needs a credential-file source")
	}
	return &ManagedCredentialSource{source: source}, nil
}

// Credential returns only the access token and account needed on the wire.
func (s *ManagedCredentialSource) Credential(ctx context.Context) (Credential, error) {
	file, err := s.source.ManagedCredentialFile(ctx)
	if err != nil {
		return Credential{}, fmt.Errorf("loading the managed Codex credential: %w", err)
	}
	var document struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(file.Reveal(), &document); err != nil {
		return Credential{}, fmt.Errorf("the managed Codex credential is not a JSON object")
	}
	if document.Tokens.AccessToken == "" || document.Tokens.AccountID == "" {
		return Credential{}, fmt.Errorf("the managed Codex credential has no usable access token or account")
	}
	return Credential{
		AccessToken: work.NewCredential(document.Tokens.AccessToken),
		AccountID:   document.Tokens.AccountID,
	}, nil
}
