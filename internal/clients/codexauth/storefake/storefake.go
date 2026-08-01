// Package storefake is an in-memory SecretStore with real compare-and-swap
// semantics, for tests that need a store rather than a Kubernetes cluster.
//
// It is its own package, not a fixture inside codexauth's tests, so that the
// conformance suite can be run against it: a fake the tests rest on is worth
// nothing if it is wrong about the contract, and the only way to check that is
// to hold it to the same suite as the real client. That check has to import
// both the suite and the fake, which a fixture inside the package under test
// cannot do.
//
// Its hooks exist to inject a failure at a named moment — a foreign writer
// landing between a read and the write derived from it, or a write that applies
// and then reports failure — deterministically, with no sleeping and no race.
package storefake

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/0x63616c/software-factory/internal/work"
)

// Store is the in-memory store. Set its hooks before the code under test runs;
// they are read under the same lock the store uses, so a hook may write to the
// store it is hooking.
type Store struct {
	mu      sync.Mutex
	values  map[string][]byte
	version int64
	gets    int
	puts    int
	putKeys [][]string

	// BeforeGet runs before a read is served, with the 1-based ordinal of the
	// call. Blocking in it is how a test pins an interleaving.
	BeforeGet func(n int)

	// AfterGet runs once a read has taken its version but before Get returns.
	// It is the only place a test can hold two readers at one version:
	// blocking before the read instead leaves a reader free to be overtaken by
	// the other's write in the window between the barrier and the read.
	AfterGet func(n int)

	// BeforePut runs before the precondition is checked, so a hook that writes
	// to the store is a foreign writer landing in exactly the window a
	// compare-and-swap exists to notice.
	BeforePut func(n int)

	// PutErr fails a write without applying it.
	PutErr func(n int) error

	// ApplyThenFail applies a write, advances the version, and then reports
	// failure: the landed-but-response-lost shape. It is the failure a naive
	// conflict check misreads as a foreign writer, and it is the likeliest one
	// in the system.
	ApplyThenFail func(n int) error

	// AfterPut observes every completed write, its keys and its result.
	AfterPut func(n int, keys []string, err error)
}

// New returns a store holding seed and nothing else.
func New(seed map[string][]byte) *Store {
	values := make(map[string][]byte, len(seed))
	for k, v := range seed {
		values[k] = append([]byte(nil), v...)
	}
	return &Store{values: values, version: 1}
}

// Get returns a copy of every key and the version they were read at.
func (s *Store) Get(ctx context.Context) (map[string][]byte, work.SecretVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, work.SecretVersion{}, err
	}
	s.mu.Lock()
	s.gets++
	n := s.gets
	hook := s.BeforeGet
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}

	s.mu.Lock()
	out := make(map[string][]byte, len(s.values))
	for k, v := range s.values {
		out[k] = append([]byte(nil), v...)
	}
	version := work.ObservedVersion(strconv.FormatInt(s.version, 10))
	after := s.AfterGet
	s.mu.Unlock()

	if after != nil {
		after(n)
	}
	return out, version, nil
}

// Put applies every key at one point, and only if the precondition still holds.
func (s *Store) Put(ctx context.Context, values map[string][]byte, precondition work.SecretVersion) (work.SecretVersion, error) {
	if err := ctx.Err(); err != nil {
		return work.SecretVersion{}, err
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	s.mu.Lock()
	s.puts++
	n := s.puts
	s.putKeys = append(s.putKeys, keys)
	before, failWith, applyThenFail, after := s.BeforePut, s.PutErr, s.ApplyThenFail, s.AfterPut
	s.mu.Unlock()

	version, err := s.apply(ctx, values, precondition, n, before, failWith, applyThenFail)
	if after != nil {
		after(n, keys, err)
	}
	return version, err
}

func (s *Store) apply(
	ctx context.Context,
	values map[string][]byte,
	precondition work.SecretVersion,
	n int,
	before func(int),
	failWith func(int) error,
	applyThenFail func(int) error,
) (work.SecretVersion, error) {
	if before != nil {
		before(n)
	}
	if failWith != nil {
		if err := failWith(n); err != nil {
			return work.SecretVersion{}, fmt.Errorf("writing the credential secret: %w", err)
		}
	}
	token, err := precondition.Precondition()
	if err != nil {
		return work.SecretVersion{}, fmt.Errorf("writing the credential secret: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return work.SecretVersion{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if token != "" && token != strconv.FormatInt(s.version, 10) {
		return work.SecretVersion{}, fmt.Errorf("writing the credential secret: %w", work.ErrVersionConflict)
	}
	for k, v := range values {
		s.values[k] = append([]byte(nil), v...)
	}
	s.version++
	current := work.ObservedVersion(strconv.FormatInt(s.version, 10))
	if applyThenFail != nil {
		if err := applyThenFail(n); err != nil {
			return work.SecretVersion{}, fmt.Errorf("writing the credential secret: %w", err)
		}
	}
	return current, nil
}

// ForceWrite lands a write with no precondition, for a hook playing a foreign
// writer.
func (s *Store) ForceWrite(values map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range values {
		s.values[k] = append([]byte(nil), v...)
	}
	s.version++
}

// Read returns one stored key.
func (s *Store) Read(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.values[key]...)
}

// Counts returns how many reads and writes the store has served.
func (s *Store) Counts() (gets, puts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.puts
}

// WrittenKeys returns the key set of every write attempted so far, in order. A
// test tells a lease write from a settle write by its shape.
func (s *Store) WrittenKeys() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.putKeys...)
}
