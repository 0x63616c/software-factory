package workflows

import "github.com/0x63616c/software-factory/internal/work"

// sameCheckFailures reports whether a and b identify exactly the same failed
// check runs, independent of their order.
//
// This is rule 1 of the implement/review loop's progress detection: a strict
// subset is evidence of progress, as is a same-named check with a different
// fingerprint. The caller only invokes this when a is non-empty.
func sameCheckFailures(a, b []work.CheckFailure) bool {
	for _, failure := range a {
		if failure.Fingerprint == "" {
			return false
		}
	}
	for _, failure := range b {
		if failure.Fingerprint == "" {
			return false
		}
	}

	aSet := make(map[checkFailureIdentity]struct{}, len(a))
	for _, failure := range a {
		aSet[checkFailureIdentity{Name: failure.Name, Fingerprint: failure.Fingerprint}] = struct{}{}
	}
	bSet := make(map[checkFailureIdentity]struct{}, len(b))
	for _, x := range b {
		bSet[checkFailureIdentity{Name: x.Name, Fingerprint: x.Fingerprint}] = struct{}{}
	}
	if len(aSet) != len(bSet) {
		return false
	}
	for failure := range aSet {
		if _, ok := bSet[failure]; !ok {
			return false
		}
	}
	return true
}

// checkFailureIdentity excludes bounded evidence deliberately: it is handoff
// context for an implementer, not CI progress control vocabulary.
type checkFailureIdentity struct {
	Name        string
	Fingerprint string
}

// intersects reports whether a and b share at least one element.
//
// This is rule 2: the same blocking review finding id held across two
// review turns. A review turn that raises only new blocking findings, with
// none surviving from before, does not trip it.
func intersects(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, x := range b {
		set[x] = struct{}{}
	}
	for _, x := range a {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}
