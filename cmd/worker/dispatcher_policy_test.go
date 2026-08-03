package main

import (
	"context"
	"errors"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

type fakeDispatcherPolicyPublisher struct {
	request dispatcherPolicyPublicationRequest
	result  workflows.DispatcherPublication
	err     error
}

func (f *fakeDispatcherPolicyPublisher) PublishDispatcherPolicy(_ context.Context, request dispatcherPolicyPublicationRequest) (workflows.DispatcherPublication, error) {
	f.request = request
	return f.result, f.err
}

func TestEnsureTargetDispatcherPolicyAcceptsAppliedPublication(t *testing.T) {
	t.Parallel()

	publisher := &fakeDispatcherPolicyPublisher{result: workflows.DispatcherPublicationApplied}
	request := defaultDispatcherPolicyPublicationRequest("request-1")
	if err := ensureTargetDispatcherPolicy(context.Background(), publisher, request); err != nil {
		t.Fatalf("ensureTargetDispatcherPolicy: %v", err)
	}
	if publisher.request.RequestID != "request-1" {
		t.Fatalf("publication request ID = %q, want the stable request ID", publisher.request.RequestID)
	}
}

func TestEnsureTargetDispatcherPolicyAcceptsAlreadyCurrentPublication(t *testing.T) {
	t.Parallel()

	publisher := &fakeDispatcherPolicyPublisher{result: workflows.DispatcherPublicationAlreadyCurrent}
	if err := ensureTargetDispatcherPolicy(context.Background(), publisher, defaultDispatcherPolicyPublicationRequest("retry-same-request")); err != nil {
		t.Fatalf("ensureTargetDispatcherPolicy: %v", err)
	}
}

func TestEnsureTargetDispatcherPolicyFailsClosedWhenPublicationIsUnconfirmed(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("Temporal unavailable")
	publisher := &fakeDispatcherPolicyPublisher{err: sentinel}
	err := ensureTargetDispatcherPolicy(context.Background(), publisher, defaultDispatcherPolicyPublicationRequest("request-3"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("ensureTargetDispatcherPolicy error = %v, want wrapped %v", err, sentinel)
	}
}

func TestEnsureTargetDispatcherPolicyRejectsDrainAndUnexpectedResponses(t *testing.T) {
	t.Parallel()

	for _, outcome := range []workflows.DispatcherPublication{workflows.DispatcherPublicationDraining, "unexpected"} {
		outcome := outcome
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			err := ensureTargetDispatcherPolicy(context.Background(), &fakeDispatcherPolicyPublisher{result: outcome}, defaultDispatcherPolicyPublicationRequest("request-4"))
			if err == nil {
				t.Fatalf("ensureTargetDispatcherPolicy accepted %q", outcome)
			}
		})
	}
}

func TestDispatcherPolicyPublicationUsesTheResolvedFingerprintInsteadOfRequestIdentity(t *testing.T) {
	t.Parallel()

	first := defaultDispatcherPolicyPublicationRequest("request-a")
	second := defaultDispatcherPolicyPublicationRequest("request-b")
	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("same resolved policy fingerprints = %q and %q, want one non-empty equality fingerprint", first.Fingerprint, second.Fingerprint)
	}
	if first.RequestID == second.RequestID {
		t.Fatal("publication request IDs collapsed into the equality fingerprint")
	}
}

func TestDispatcherPolicyPublicationCarriesAnImmutableResolvedPolicy(t *testing.T) {
	t.Parallel()

	request := defaultDispatcherPolicyPublicationRequest("request-policy")
	want, err := work.DefaultDispatcherPolicy().Fingerprint()
	if err != nil {
		t.Fatalf("fingerprinting default policy: %v", err)
	}
	if request.Fingerprint != want {
		t.Fatalf("published fingerprint = %q, want resolved default %q", request.Fingerprint, want)
	}
}

func TestDeploymentScopesDispatcherPolicyPublicationIdentity(t *testing.T) {
	t.Parallel()

	first := deployedDispatcherPolicyPublicationRequest("1785790005-1")
	retry := deployedDispatcherPolicyPublicationRequest("1785790005-1")
	later := deployedDispatcherPolicyPublicationRequest("1785790005-2")
	want := "startup-1785790005-1-" + first.Fingerprint
	if first.RequestID != want {
		t.Fatalf("first deployment request ID = %q, want %q", first.RequestID, want)
	}
	if retry.RequestID != first.RequestID {
		t.Fatalf("same deployment request IDs = %q and %q, want one idempotent identity", first.RequestID, retry.RequestID)
	}
	if later.RequestID == first.RequestID {
		t.Fatalf("later deployment request ID = %q, want a new activation identity", later.RequestID)
	}
	if later.Fingerprint != first.Fingerprint {
		t.Fatalf("unchanged policy fingerprints = %q and %q, want content identity independent of deployment", first.Fingerprint, later.Fingerprint)
	}
}
