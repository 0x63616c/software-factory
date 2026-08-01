package main

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/activities"
)

type activityRegistration struct {
	value any
}

type activityRegistrarProbe struct{ registrations []activityRegistration }

func (p *activityRegistrarProbe) RegisterActivity(value any) {
	p.registrations = append(p.registrations, activityRegistration{value: value})
}

func TestRunWorkerRegistersRepositoryActivitiesOnly(t *testing.T) {
	t.Parallel()

	registrar := &activityRegistrarProbe{}
	register(registrar, &activities.RunWorkerActivities{})

	if len(registrar.registrations) != 1 {
		t.Fatalf("registered activity count = %d, want 1", len(registrar.registrations))
	}
	if _, ok := registrar.registrations[0].value.(*activities.RunWorkerActivities); !ok {
		t.Fatalf("registration = %T, want repository-affine RunWorkerActivities", registrar.registrations[0].value)
	}
}
