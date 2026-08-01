package codexauth_test

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"testing"

	"github.com/0x63616c/software-factory/internal/clients/codexauth"
	"github.com/0x63616c/software-factory/internal/clients/codexauth/codexauthtest"
	"github.com/0x63616c/software-factory/internal/work"
)

// fakeStore is the compare-and-swap the real Secret gets from the apiserver,
// with a counter standing in for a resourceVersion. It exists to prove the seam
// can express a lease at all: a store that cannot refuse a stale write cannot
// be one.
type fakeStore struct {
	values  map[string][]byte
	version int
}

func newFakeStore(seed map[string][]byte) *fakeStore {
	values := make(map[string][]byte, len(seed))
	maps.Copy(values, seed)
	return &fakeStore{values: values, version: 1}
}

func (s *fakeStore) Get(context.Context) (map[string][]byte, work.SecretVersion, error) {
	return maps.Clone(s.values), work.ObservedVersion(strconv.Itoa(s.version)), nil
}

func (s *fakeStore) Put(_ context.Context, values map[string][]byte, precondition work.SecretVersion) (work.SecretVersion, error) {
	resourceVersion, err := precondition.Precondition()
	if err != nil {
		return work.SecretVersion{}, fmt.Errorf("refusing a write to the fake secret: %w", err)
	}
	// Empty here can only have come from Unconditional, so it is a blind write
	// somebody asked for rather than one that leaked out of a dropped version.
	if resourceVersion != "" && resourceVersion != strconv.Itoa(s.version) {
		return work.SecretVersion{}, work.ErrVersionConflict
	}
	maps.Copy(s.values, values)
	s.version++
	return work.ObservedVersion(strconv.Itoa(s.version)), nil
}

var _ codexauth.SecretStore = (*fakeStore)(nil)

func TestTheFakeStoreHonoursTheSecretStoreContract(t *testing.T) {
	t.Parallel()

	codexauthtest.RunSecretStoreContract(t, func(_ *testing.T, seed map[string][]byte) codexauth.SecretStore {
		return newFakeStore(seed)
	})
}
