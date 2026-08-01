package codexauth

import (
	"context"

	"github.com/0x63616c/software-factory/internal/work"
)

// SecretStore reads and writes the keys of the one Kubernetes Secret holding
// the credential.
//
// Which Secret that is — namespace and name — is bound when the implementation
// is constructed, not passed per call. A caller cannot address a different
// Secret through this seam, which is what keeps the blast radius of the only
// component holding a refresh token down to a single object.
//
// Put must be durable before it returns. The stored refresh token is
// single-use: if a rotation is performed and the new token is lost, the old one
// is already spent, and recovery is a human running a browser login. The same
// fact is why the seam is a compare-and-swap rather than a plain write — a
// stale write silently applied over a concurrent rotation does not lose an
// update, it kills the credential.
//
// None of that is expressible in Go's type system, so it is a test instead:
// codexauthtest.RunSecretStoreContract holds every implementation to it, and a
// new one is expected to run it rather than to reread this comment.
type SecretStore interface {
	// Get returns every key of the Secret together with the version of the
	// object they were read from, which is the precondition a write derived
	// from them may apply.
	Get(ctx context.Context) (values map[string][]byte, version work.SecretVersion, err error)

	// Put applies every key of values at one point, and only if the stored
	// object still matches precondition. It returns the version the write
	// produced, so a caller taking a lease and then settling it CASes on what
	// its own lease write left behind rather than re-reading and adopting
	// whatever landed in between.
	//
	// Both properties are the mechanism, not caution. The precondition is what
	// makes a write a lease, and writing the lease marker and the rotated
	// credential together is what puts them at one linearization point; split
	// across two writes the lease is not a lease. Keys absent from values are
	// left alone; a key can be blanked but never removed, which is why the
	// lease marker is a state this package can read as "no attempt in flight"
	// rather than a key whose absence means the same thing.
	//
	// It returns an error satisfying errors.Is(err, work.ErrVersionConflict)
	// when, and only when, the precondition no longer holds, and one satisfying
	// errors.Is(err, work.ErrNoPrecondition) when the precondition names none —
	// a distinct sentinel because contention is worth retrying and a forgotten
	// version never is. An overwrite with no precondition has to be asked for
	// by name, with work.Unconditional.
	Put(ctx context.Context, values map[string][]byte, precondition work.SecretVersion) (work.SecretVersion, error)
}
