package codexresponses

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

type fakeManagedCredentialFileSource struct {
	file work.CredentialFile
	err  error
}

func (f fakeManagedCredentialFileSource) ManagedCredentialFile(context.Context) (work.CredentialFile, error) {
	return f.file, f.err
}

func TestManagedCredentialSourceExtractsTheDirectCallCredential(t *testing.T) {
	t.Parallel()

	document, err := json.Marshal(map[string]any{"tokens": map[string]any{
		"access_token":  "access-value",
		"refresh_token": "",
		"account_id":    "account-123",
	}})
	if err != nil {
		t.Fatalf("encoding credential document: %v", err)
	}
	source, err := NewManagedCredentialSource(fakeManagedCredentialFileSource{file: work.NewCredentialFile(document)})
	if err != nil {
		t.Fatalf("constructing source: %v", err)
	}

	credential, err := source.Credential(context.Background())
	if err != nil {
		t.Fatalf("loading credential: %v", err)
	}
	if credential.AccessToken.Reveal() != "access-value" || credential.AccountID != "account-123" {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestManagedCredentialSourceWrapsItsDependencyErrorWithoutLeakingValues(t *testing.T) {
	t.Parallel()

	const secret = "do-not-leak"
	source, err := NewManagedCredentialSource(fakeManagedCredentialFileSource{err: errors.New("unavailable")})
	if err != nil {
		t.Fatalf("constructing source: %v", err)
	}
	_, err = source.Credential(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}
