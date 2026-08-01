package main

import (
	"context"
	"fmt"
	"sync"
)

type activationWorker interface {
	Start() error
	Stop()
}

// activateTargetWorkers crosses the code-side activation boundary in the only
// safe order. The control queue must poll before Update-With-Start can be
// acknowledged; the main queue must not poll until every gate is confirmed.
func activateTargetWorkers(
	ctx context.Context,
	controlWorker activationWorker,
	mainWorker activationWorker,
	ready func(context.Context) error,
	publisher dispatcherPolicyPublisher,
	request dispatcherPolicyPublicationRequest,
	ensureSchedule func(context.Context) error,
) (func(), error) {
	if err := ready(ctx); err != nil {
		return nil, fmt.Errorf("checking target activation readiness: %w", err)
	}
	if err := controlWorker.Start(); err != nil {
		return nil, fmt.Errorf("starting the target dispatcher control worker: %w", err)
	}
	stopControl := true
	defer func() {
		if stopControl {
			controlWorker.Stop()
		}
	}()
	if err := ensureTargetDispatcherPolicy(ctx, publisher, request); err != nil {
		return nil, err
	}
	if err := ensureSchedule(ctx); err != nil {
		return nil, fmt.Errorf("reconciling target maintenance schedule: %w", err)
	}
	if err := mainWorker.Start(); err != nil {
		return nil, fmt.Errorf("starting the main target worker: %w", err)
	}
	stopControl = false
	var once sync.Once
	return func() {
		once.Do(func() {
			mainWorker.Stop()
			controlWorker.Stop()
		})
	}, nil
}
