package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/0x63616c/software-factory/internal/workflows"
)

type activationWorkerProbe struct {
	name   string
	events *[]string
	err    error
}

func (p activationWorkerProbe) Start() error {
	*p.events = append(*p.events, p.name+".start")
	return p.err
}

func (p activationWorkerProbe) Stop() { *p.events = append(*p.events, p.name+".stop") }

type activationPublisherProbe struct {
	events *[]string
	result workflows.DispatcherPublication
	err    error
}

func (p activationPublisherProbe) PublishDispatcherPolicy(context.Context, dispatcherPolicyPublicationRequest) (workflows.DispatcherPublication, error) {
	*p.events = append(*p.events, "policy")
	return p.result, p.err
}

func TestActivationGatesMainQueuePollingOnReadinessPolicyAndSchedule(t *testing.T) {
	t.Parallel()
	events := []string{}
	stop, err := activateTargetWorkers(
		context.Background(),
		activationWorkerProbe{name: "control", events: &events},
		activationWorkerProbe{name: "main", events: &events},
		func(context.Context) error { events = append(events, "ready"); return nil },
		activationPublisherProbe{events: &events, result: workflows.DispatcherPublicationApplied},
		defaultDispatcherPolicyPublicationRequest("request"),
		func(context.Context) error { events = append(events, "schedule"); return nil },
	)
	if err != nil {
		t.Fatalf("activateTargetWorkers: %v", err)
	}
	if want := []string{"ready", "control.start", "policy", "schedule", "main.start"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("activation events = %v, want %v", events, want)
	}
	stop()
	stop()
	if want := []string{"ready", "control.start", "policy", "schedule", "main.start", "main.stop", "control.stop"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("activation and idempotent stop events = %v, want %v", events, want)
	}
}

func TestActivationFailureBeforeMainPollingStopsTheControlWorker(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("schedule unavailable")
	events := []string{}
	stop, err := activateTargetWorkers(
		context.Background(),
		activationWorkerProbe{name: "control", events: &events},
		activationWorkerProbe{name: "main", events: &events},
		func(context.Context) error { events = append(events, "ready"); return nil },
		activationPublisherProbe{events: &events, result: workflows.DispatcherPublicationAlreadyCurrent},
		defaultDispatcherPolicyPublicationRequest("request"),
		func(context.Context) error { events = append(events, "schedule"); return sentinel },
	)
	if !errors.Is(err, sentinel) || stop != nil {
		t.Fatalf("activateTargetWorkers returned stop=%t, err=%v; want nil stop and wrapped schedule error", stop != nil, err)
	}
	if want := []string{"ready", "control.start", "policy", "schedule", "control.stop"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("failed activation events = %v, want %v", events, want)
	}
}
