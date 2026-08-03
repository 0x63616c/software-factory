package main

import (
	"context"
	"fmt"

	temporalapi "github.com/0x63616c/software-factory/internal/clients/temporal"
	"github.com/0x63616c/software-factory/internal/work"
	"github.com/0x63616c/software-factory/internal/workflows"
)

// dispatcherPolicyPublicationRequest is one worker boot's idempotent policy
// publication. RequestID is Temporal's Update ID; Fingerprint is policy equality.
type dispatcherPolicyPublicationRequest struct {
	RequestID   string
	Fingerprint string
	Policy      work.DispatcherPolicy
}

// dispatcherPolicyPublisher hides Temporal's client protocol behind the one
// startup outcome the worker needs before it may poll its main queue.
type dispatcherPolicyPublisher interface {
	PublishDispatcherPolicy(context.Context, dispatcherPolicyPublicationRequest) (workflows.DispatcherPublication, error)
}

// targetDispatcherPolicyPublisher adapts the Temporal client boundary to the
// worker's startup gate without making the SDK's options types a cmd concern.
// It is deliberately not constructed by run() before PR 8 activates the path.
type targetDispatcherPolicyPublisher struct {
	publisher *temporalapi.DispatcherPublisher
	input     workflows.DispatcherInput
}

var _ dispatcherPolicyPublisher = targetDispatcherPolicyPublisher{}

func (p targetDispatcherPolicyPublisher) PublishDispatcherPolicy(ctx context.Context, request dispatcherPolicyPublicationRequest) (workflows.DispatcherPublication, error) {
	input := p.input
	input.Policy = request.Policy
	return p.publisher.PublishDispatcherPolicy(ctx, temporalapi.DispatcherPolicyPublication{RequestID: request.RequestID, Input: input})
}

func defaultDispatcherPolicyPublicationRequest(requestID string) dispatcherPolicyPublicationRequest {
	policy, fingerprint := resolvedDefaultDispatcherPolicy()
	return dispatcherPolicyPublicationRequest{RequestID: requestID, Fingerprint: fingerprint, Policy: policy}
}

func resolvedDefaultDispatcherPolicy() (work.DispatcherPolicy, string) {
	policy := work.DefaultDispatcherPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		panic(fmt.Sprintf("default dispatcher policy is invalid: %v", err))
	}
	return policy, fingerprint
}

// deployedDispatcherPolicyPublicationRequest scopes Temporal's idempotency key
// to one CI deployment while leaving the fingerprint as policy content identity.
func deployedDispatcherPolicyPublicationRequest(deployID string) dispatcherPolicyPublicationRequest {
	policy, fingerprint := resolvedDefaultDispatcherPolicy()
	return dispatcherPolicyPublicationRequest{
		RequestID:   "startup-" + deployID + "-" + fingerprint,
		Fingerprint: fingerprint,
		Policy:      policy,
	}
}

// ensureTargetDispatcherPolicy is the startup gate for the inactive target
// path. Only an acknowledged APPLIED or ALREADY_CURRENT response permits a
// caller to start polling a main task queue.
func ensureTargetDispatcherPolicy(ctx context.Context, publisher dispatcherPolicyPublisher, request dispatcherPolicyPublicationRequest) error {
	if request.RequestID == "" {
		return fmt.Errorf("publishing target dispatcher policy: request ID is required")
	}
	fingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("publishing target dispatcher policy: %w", err)
	}
	if request.Fingerprint != fingerprint {
		return fmt.Errorf("publishing target dispatcher policy: fingerprint does not match the resolved policy")
	}
	outcome, err := publisher.PublishDispatcherPolicy(ctx, request)
	if err != nil {
		return fmt.Errorf("publishing target dispatcher policy: %w", err)
	}
	if outcome == workflows.DispatcherPublicationApplied || outcome == workflows.DispatcherPublicationAlreadyCurrent {
		return nil
	}
	return fmt.Errorf("publishing target dispatcher policy: Temporal returned %q", outcome)
}
