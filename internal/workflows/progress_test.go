package workflows

import (
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestSameCheckFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b []work.CheckFailure
		want bool
	}{
		{"empty sets match", nil, nil, true},
		{"identical sets match despite order", []work.CheckFailure{{Name: "a", Fingerprint: "one"}, {Name: "b", Fingerprint: "two"}}, []work.CheckFailure{{Name: "b", Fingerprint: "two"}, {Name: "a", Fingerprint: "one"}}, true},
		{"strict subset does not match", []work.CheckFailure{{Name: "a", Fingerprint: "one"}}, []work.CheckFailure{{Name: "a", Fingerprint: "one"}, {Name: "b", Fingerprint: "two"}}, false},
		{"added failure does not match", []work.CheckFailure{{Name: "a", Fingerprint: "one"}, {Name: "b", Fingerprint: "two"}}, []work.CheckFailure{{Name: "a", Fingerprint: "one"}}, false},
		{"same check name with another failure does not match", []work.CheckFailure{{Name: "test", Fingerprint: "one"}}, []work.CheckFailure{{Name: "test", Fingerprint: "two"}}, false},
		{"evidence does not change an otherwise identical failure", []work.CheckFailure{{Name: "test", Fingerprint: "one", Evidence: "first bounded log"}}, []work.CheckFailure{{Name: "test", Fingerprint: "one", Evidence: "second bounded log"}}, true},
		{"missing fingerprints never claim stagnation", []work.CheckFailure{{Name: "test"}}, []work.CheckFailure{{Name: "test"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sameCheckFailures(tc.a, tc.b); got != tc.want {
				t.Errorf("sameCheckFailures(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestIntersects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"a shared id", []string{"x", "y"}, []string{"y", "z"}, true},
		{"disjoint sets", []string{"x"}, []string{"y"}, false},
		{"a is empty", nil, []string{"y"}, false},
		{"b is empty", []string{"x"}, nil, false},
		{"both empty", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := intersects(tc.a, tc.b); got != tc.want {
				t.Errorf("intersects(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
