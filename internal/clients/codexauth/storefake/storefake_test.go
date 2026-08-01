package storefake_test

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/clients/codexauth/codexauthtest"
	"github.com/0x63616c/software-factory/internal/clients/codexauth/storefake"
)

func TestStoreSatisfiesTheSecretStoreContract(t *testing.T) {
	t.Parallel()
	// Every Source test rests on this store. One that was wrong about the
	// contract would prove the Source correct against a store that does not
	// exist.
	codexauthtest.RunSecretStoreContract(t, func(_ *testing.T, seed map[string][]byte) codexauth.SecretStore {
		return storefake.New(seed)
	})
}
