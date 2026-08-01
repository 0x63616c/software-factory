package activities

import (
	"context"
	"testing"

	"github.com/0x63616c/software-factory/internal/store"
	"github.com/0x63616c/software-factory/internal/store/storefake"
)

func mustNewTargetMaintenance(t *testing.T, store TargetMaintenanceStore) *TargetMaintenanceActivities {
	t.Helper()
	activities, err := NewTargetMaintenanceActivities(store)
	if err != nil {
		t.Fatalf("NewTargetMaintenanceActivities: %v", err)
	}
	return activities
}

func TestTargetMaintenanceListsAndConditionallyReleasesActiveOwners(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backing := storefake.New()
	first, err := backing.CreateTicket(ctx, "first", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(first): %v", err)
	}
	second, err := backing.CreateTicket(ctx, "second", "", nil)
	if err != nil {
		t.Fatalf("CreateTicket(second): %v", err)
	}
	firstRun := "019fb900-0000-7000-8000-000000000001"
	secondRun := "019fb900-0000-7000-8000-000000000002"
	if _, err := backing.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: first.ID, RunID: firstRun, StartedAt: fixedTestTime}); err != nil {
		t.Fatalf("ClaimAndStartRun(first): %v", err)
	}
	if _, err := backing.ClaimAndStartRun(ctx, store.ClaimRunInput{TicketID: second.ID, RunID: secondRun, StartedAt: fixedTestTime}); err != nil {
		t.Fatalf("ClaimAndStartRun(second): %v", err)
	}

	acts := mustNewTargetMaintenance(t, backing)
	owners, err := acts.ListActiveTargetRunOwners(ctx)
	if err != nil {
		t.Fatalf("ListActiveTargetRunOwners: %v", err)
	}
	want := []store.ActiveTargetRunOwner{{TicketID: first.ID, RunID: firstRun}, {TicketID: second.ID, RunID: secondRun}}
	if len(owners) != len(want) {
		t.Fatalf("owners = %+v, want %+v", owners, want)
	}
	for index := range want {
		if owners[index] != want[index] {
			t.Errorf("owner %d = %+v, want %+v", index, owners[index], want[index])
		}
	}

	reopened, err := acts.ReconcileAbandonedTargetRun(ctx, firstRun, first.ID)
	if err != nil {
		t.Fatalf("ReconcileAbandonedTargetRun: %v", err)
	}
	if !reopened {
		t.Fatal("ReconcileAbandonedTargetRun = false, want released active ownership")
	}
}
