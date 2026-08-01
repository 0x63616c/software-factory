package work_test

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestDispatcherPolicyFingerprintIsStableAndChangesWithTheResolvedPolicy(t *testing.T) {
	t.Parallel()

	policy := work.DefaultDispatcherPolicy()
	first, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	second, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if first != second || first == "" {
		t.Fatalf("Fingerprint() = %q then %q, want one stable non-empty digest", first, second)
	}

	policy.MaxInFlight++
	changed, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint changed policy: %v", err)
	}
	if changed == first {
		t.Fatal("Fingerprint did not change when the resolved admission policy changed")
	}
}
