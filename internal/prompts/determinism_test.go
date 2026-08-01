package prompts

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestWorkflowCodeIsForbiddenToImportThisPackage asserts the lint rule that
// keeps Render out of workflow code.
//
// Render mints a fresh nonce from an entropy source on every call. That is
// correct for the fence and fatal for a Temporal replay: a workflow that
// rendered a prompt would produce different bytes on the replay than it did on
// the original execution, and the run would corrupt days after the mistake was
// made, in a stack trace pointing anywhere but here.
//
// Nothing in the code can catch it. The entropy arrives as an injected
// io.Reader, so a workflow calling Render imports no banned package and reads
// as pure orchestration at the call site — depguard's deny list is the whole
// defence, and a deny list is only as good as its entries.
//
// It asserts the configuration rather than running the linter because
// `internal/workflows/` does not exist yet: there is no file for a fixture to
// live in, and a fixture that fails lint on purpose would have to be excluded
// from lint, which is the same hole one layer down. The real linter firing on
// this entry was verified by probe — `testdata/workflow_probe.go.txt` is that
// probe, kept out of the build graph, with the commands to run it in its own
// header. Reconstructing it is otherwise a research task for whoever next has
// to check the rule still bites.
//
// What every assertion below has in common, and the reason there are four
// rather than one: each names a separate FIELD that can silence this rule while
// leaving the others reading as correct. A deny entry is worth what its
// selector is worth; a selector is worth what the enabled linter is worth; and
// an exclusion can switch the whole thing off downstream of all three. Assert
// one and the guard still has three unlocked doors, each as quiet as the one
// that was locked.
//
// All four holes vanish under a test asserting the EFFECT instead of the text:
// run golangci-lint against the probe and assert a hit, which is exactly the
// manual check recorded in #370. That test needs a committed probe package and
// a golangci-lint binary on PATH inside `go test`, and both costs are real —
// hence text assertions as the affordable approximation. That approximation is
// only worth anything if it covers every field, which is what the four below
// are for. If those costs ever become payable, this whole file collapses into
// one assertion that cannot be fooled by any edit at all.
func TestWorkflowCodeIsForbiddenToImportThisPackage(t *testing.T) {
	t.Parallel()

	config, err := os.ReadFile("../../.golangci.yml")
	if err != nil {
		t.Fatalf("reading the linter config: %v", err)
	}

	rule := workflowRule(t, string(config))

	// Taken from a type in this package rather than written out, so moving the
	// package fails this test instead of silently emptying the rule.
	self := reflect.TypeOf(Input{}).PkgPath()
	if !strings.Contains(rule, self) {
		t.Errorf("the workflows-are-deterministic rule does not deny %s; workflow code could call Render and corrupt a replay", self)
	}

	// A deny list only fires on the files its rule selects, so the entry above
	// is worth exactly what this selector is worth. Repointing `files:` at a
	// path nothing matches — which is all a rename of internal/workflows/ is —
	// silences the whole rule while leaving every deny entry in place, reading
	// exactly as correct as it does now. That is this test's own failure mode,
	// one field over: a guard that looks present and does nothing.
	//
	// The selector is pinned to a literal deliberately. There is no
	// internal/workflows package yet, so there is nothing to derive it from,
	// and a rename *should* stop here: re-point the config and this line
	// together, as one deliberate act, rather than letting either drift.
	//
	// Once internal/workflows DOES exist, replace this literal with a
	// PkgPath()-derived value the way the deny entry above already is. Nothing
	// else will prompt that, so it is written down here rather than left to be
	// noticed.
	//
	// The config names internal/workflows/ twice — this selector and a
	// containedctx/contextcheck/fatcontext exclusion further down. Only this
	// one needs a rename assertion, and the reason is sharper than loud-vs-
	// silent: a stale exclusion excludes nothing, whereas a stale selector
	// selects nothing. The failure directions are opposite, so a rename of the
	// exclusion's path is harmless whether or not it makes any noise. What that
	// exclusion does admit is WIDENING, which is silent and is not harmless —
	// asserted at the end of this test.
	const selector = `"**/internal/workflows/**"`
	files := workflowRuleFiles(t, rule)
	if !strings.Contains(files, selector) {
		t.Errorf("the workflows-are-deterministic rule selects %s, not %s; the deny list below it fires on nothing", files, selector)
	}

	// A negation subtracts from the selector without touching it, so the
	// assertion above still passes while the rule matches nothing:
	//
	//	files:
	//	  - "**/internal/workflows/**"
	//	  - "!**/internal/workflows/**"
	//
	// That is not a contrived edit. The clients-seal-their-sdks rule twelve
	// lines further down uses exactly this `!` idiom to carve the k8s client
	// out of its own seal, so the shape is sitting there to be copied.
	//
	// Any negation is refused, not merely one that cancels the selector
	// exactly. This rule has one job and one entry, so in a list of one a
	// negation can only subtract from it — and deciding whether some narrower
	// exclusion still leaves workflow code selected is exactly the judgement
	// that should stop at a human rather than be approximated by string
	// matching. If a real need for one arrives, this is the deliberate stop.
	if strings.Contains(files, `"!`) {
		t.Errorf("the workflows-are-deterministic rule's files: list carries a negation:\n%s\na negation leaves the positive entry in place, so the selector still reads as correct while the rule selects nothing", files)
	}

	// The whole rule rides on depguard being enabled at all. Deleting one line
	// from linters.enable silences all four depguard rules — every deny entry
	// and every selector, the SDK seals included — and every text assertion
	// above still passes, because the text it reads is untouched. Strictly more
	// damage than a broken selector, and no louder.
	if enabled := enabledLinters(t, string(config)); !regexp.MustCompile(`(?m)^\s*-\s+depguard\b`).MatchString(enabled) {
		t.Errorf("depguard is not in linters.enable:\n%s\nwithout it this rule, the SDK seals and every other import boundary are inert", enabled)
	}

	// And an exclusion can switch it off after the fact. Appending `depguard`
	// to the linters: list of the internal/workflows/ exclusion further down is
	// an ordinary-looking edit that silences this rule for exactly the code it
	// exists to police.
	//
	// No exclusion anywhere may name depguard, rather than just that one. There
	// is no legitimate depguard exclusion in this config today; depguard is an
	// architectural boundary, and an exclusion is precisely how you would
	// dissolve one quietly. Asserting the narrow case would leave a second
	// exclusion entry, or a broader path, doing the same job.
	//
	// Note the asymmetry with a RENAME of that same exclusion's path, which
	// needs no assertion: a stale exclusion excludes nothing, whereas a stale
	// selector selects nothing. The failure directions are opposite. Widening
	// the exclusion is the direction that is both silent and harmful.
	for _, exclusion := range exclusionsNaming(t, string(config), "depguard") {
		t.Errorf("an exclusion switches depguard off:\n%s\ndepguard is an architectural boundary; an exclusion is how one gets dissolved without anything going red", exclusion)
	}
}

// workflowRule is the body of the workflows-are-deterministic rule: from its
// key to the next rule at the same indentation.
func workflowRule(t *testing.T, config string) string {
	t.Helper()

	const key = "workflows-are-deterministic:"
	_, rest, found := strings.Cut(config, key)
	if !found {
		t.Fatalf("the linter config has no %s rule at all", key)
	}
	// The next sibling rule ends this one. depguard rules sit at eight spaces.
	if at := regexp.MustCompile(`(?m)^ {8}[a-z][a-z0-9-]*:`).FindStringIndex(rest); at != nil {
		return rest[:at[0]]
	}
	return rest
}

// enabledLinters is the linters.enable list.
//
// It is cut from `linters:` first, because `formatters:` at the bottom of the
// file has an `enable:` of its own and matching that one would assert nothing.
func enabledLinters(t *testing.T, config string) string {
	t.Helper()

	_, rest, found := strings.Cut(config, "\nlinters:\n")
	if !found {
		t.Fatalf("the linter config has no linters: block")
	}
	at := regexp.MustCompile(`(?m)^  enable:\n`).FindStringIndex(rest)
	if at == nil {
		t.Fatalf("the linters: block has no enable: list, so nothing beyond golangci-lint's defaults is on")
	}
	rest = rest[at[1]:]
	// settings: and exclusions: are enable:'s siblings at two spaces.
	if end := regexp.MustCompile(`(?m)^  [a-z][a-z0-9-]*:`).FindStringIndex(rest); end != nil {
		return rest[:end[0]]
	}
	return rest
}

// exclusionsNaming is every entry of linters.exclusions.rules that mentions the
// given linter.
func exclusionsNaming(t *testing.T, config, linter string) []string {
	t.Helper()

	_, rest, found := strings.Cut(config, "\n  exclusions:\n")
	if !found {
		// No exclusions at all is not a failure — it is the strongest possible
		// version of what this assertion wants.
		return nil
	}
	if end := regexp.MustCompile(`(?m)^[a-z]`).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}

	// Each entry's `linters:` list is what decides, not the entry's prose: the
	// comment above one of these entries names three linters it is talking
	// about, and matching that would report an exclusion that does not exist.
	list := regexp.MustCompile(`(?m)^ *linters: *\[([^\]]*)\]`)

	var matched []string
	for _, entry := range regexp.MustCompile(`(?m)^      - `).Split(rest, -1)[1:] {
		names := list.FindStringSubmatch(entry)
		if names == nil {
			// An exclusion naming no linters silences EVERY linter for its
			// path, which certainly includes depguard.
			matched = append(matched, strings.TrimRight(entry, "\n"))
			continue
		}
		for _, name := range strings.Split(names[1], ",") {
			if strings.TrimSpace(name) == linter {
				matched = append(matched, strings.TrimRight(entry, "\n"))
				break
			}
		}
	}
	return matched
}

// workflowRuleFiles is that rule's `files:` list, up to its next sibling key.
func workflowRuleFiles(t *testing.T, rule string) string {
	t.Helper()

	_, rest, found := strings.Cut(rule, "files:")
	if !found {
		t.Fatalf("the workflows-are-deterministic rule has no files: selector, so it matches nothing")
	}
	// `files:` and `deny:` are siblings at ten spaces.
	if at := regexp.MustCompile(`(?m)^ {10}[a-z][a-z0-9-]*:`).FindStringIndex(rest); at != nil {
		return strings.TrimSpace(rest[:at[0]])
	}
	return strings.TrimSpace(rest)
}
