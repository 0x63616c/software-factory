package clock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/clock"
	"github.com/0x63616c/software-factory/internal/clock/clocktest"
)

// Both implementations must satisfy the seam, or a test proves nothing about
// the code that takes one.
var (
	_ clock.Clock = clock.System{}
	_ clock.Clock = (*clocktest.Fake)(nil)
)

func TestSystemReportsTimeInUTC(t *testing.T) {
	t.Parallel()

	if loc := (clock.System{}).Now().Location(); loc != time.UTC {
		t.Errorf("Now().Location() = %v, want UTC — this service is UTC-only", loc)
	}
}

func TestSystemSleepReturnsWhenTheDurationElapses(t *testing.T) {
	t.Parallel()

	if err := (clock.System{}).Sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("Sleep returned %v, want nil", err)
	}
}

func TestSystemSleepGivesUpWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// An hour, so a Sleep that ignored cancellation would hang the suite rather
	// than pass slowly.
	err := (clock.System{}).Sleep(ctx, time.Hour)
	if err == nil {
		t.Fatal("Sleep returned nil on a cancelled context; a shutting-down worker would wait out its full interval")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep returned %v, want context.Canceled", err)
	}
}

func TestFakeMovesOnlyWhenTheTestMovesIt(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	f := clocktest.NewFake(start)

	if got := f.Now(); !got.Equal(start) {
		t.Errorf("Now() = %v, want %v", got, start)
	}

	f.Advance(90 * time.Second)
	if got, want := f.Now(), start.Add(90*time.Second); !got.Equal(want) {
		t.Errorf("after Advance, Now() = %v, want %v", got, want)
	}
}

func TestFakeSleepRecordsTheDurationAndReturnsAtOnce(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	f := clocktest.NewFake(start)

	for _, d := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if err := f.Sleep(context.Background(), d); err != nil {
			t.Fatalf("Sleep returned %v, want nil", err)
		}
	}

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	got := f.Slept()
	if len(got) != len(want) {
		t.Fatalf("Slept() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Slept() = %v, want %v", got, want)
		}
	}

	if elapsed, wantElapsed := f.Now().Sub(start), 7*time.Second; elapsed != wantElapsed {
		t.Errorf("fake time advanced %v, want %v", elapsed, wantElapsed)
	}
}

func TestFakeSleepHonoursACancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := clocktest.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	if err := f.Sleep(ctx, time.Hour); err == nil {
		t.Error("Sleep returned nil on a cancelled context; the fake must not hide a cancellation bug the real clock would surface")
	}
	if len(f.Slept()) != 0 {
		t.Errorf("Slept() = %v, want empty — a cancelled sleep did not happen", f.Slept())
	}
}
