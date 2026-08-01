// Package githubpolicy verifies the split ruleset shape required for
// autonomous merge: the App may bypass approval, but a separate ruleset whose
// checks it cannot bypass still gates the merge.
package githubpolicy

import (
	"path"
	"sort"
)

// Ruleset is the subset of GitHub's detailed repository-ruleset response the
// verifier consumes.
type Ruleset struct {
	Name         string        `json:"name"`
	Enforcement  string        `json:"enforcement"`
	Target       string        `json:"target"`
	Conditions   Conditions    `json:"conditions"`
	BypassActors []BypassActor `json:"bypass_actors"`
	Rules        []Rule        `json:"rules"`
}

// Conditions contains the target-ref selection attached to a branch ruleset.
type Conditions struct {
	RefName RefNameCondition `json:"ref_name"`
}

// RefNameCondition is GitHub's include/exclude selection for branch refs.
type RefNameCondition struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// BypassActor is one actor-level exception attached to a ruleset.
type BypassActor struct {
	ActorID    int64  `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	BypassMode string `json:"bypass_mode"`
}

// Rule is one rule and the parameters relevant to autonomous merge.
type Rule struct {
	Type       string     `json:"type"`
	Parameters Parameters `json:"parameters"`
}

// Parameters is the union of fields consumed from supported rule types.
type Parameters struct {
	RequiredApprovingReviewCount int           `json:"required_approving_review_count"`
	RequiredStatusChecks         []StatusCheck `json:"required_status_checks"`
}

// StatusCheck names one required status-check context.
type StatusCheck struct {
	Context string `json:"context"`
}

// Report is a stable, machine-readable policy gate result.
type Report struct {
	Version         int      `json:"version"`
	Ready           bool     `json:"ready"`
	Branch          string   `json:"branch"`
	ApprovalRuleset string   `json:"approvalRuleset,omitempty"`
	ChecksRulesets  []string `json:"checksRulesets"`
	RequiredChecks  []string `json:"requiredChecks"`
	ExpectedChecks  []string `json:"expectedChecks"`
	Missing         []string `json:"missing"`
}

// Verify requires two independently enforced facts: an active approval rule
// the App can bypass through a PR, and active checks in a ruleset where that
// App is not a bypass actor.
func Verify(rulesets []Ruleset, appID int64, branch string, expectedChecks []string) Report {
	report := Report{Version: 1, Branch: branch, ChecksRulesets: []string{}, RequiredChecks: []string{}, ExpectedChecks: append([]string(nil), expectedChecks...), Missing: []string{}}
	checks := map[string]struct{}{}
	for _, ruleset := range rulesets {
		if ruleset.Enforcement != "active" || !appliesToBranch(ruleset, branch) {
			continue
		}
		appBypasses := bypasses(ruleset, appID)
		for _, rule := range ruleset.Rules {
			switch rule.Type {
			case "pull_request":
				if report.ApprovalRuleset == "" && appBypasses && rule.Parameters.RequiredApprovingReviewCount > 0 {
					report.ApprovalRuleset = ruleset.Name
				}
			case "required_status_checks":
				if appBypasses || len(rule.Parameters.RequiredStatusChecks) == 0 {
					continue
				}
				report.ChecksRulesets = append(report.ChecksRulesets, ruleset.Name)
				for _, check := range rule.Parameters.RequiredStatusChecks {
					if check.Context != "" {
						checks[check.Context] = struct{}{}
					}
				}
			}
		}
	}
	for check := range checks {
		report.RequiredChecks = append(report.RequiredChecks, check)
	}
	sort.Strings(report.ChecksRulesets)
	sort.Strings(report.RequiredChecks)
	sort.Strings(report.ExpectedChecks)
	if report.ApprovalRuleset == "" {
		report.Missing = append(report.Missing, "active approval rule bypassable by the GitHub App through pull requests")
	}
	for _, expected := range report.ExpectedChecks {
		if _, protected := checks[expected]; !protected {
			report.Missing = append(report.Missing, "non-bypassable required check "+expected)
		}
	}
	report.Ready = len(report.Missing) == 0
	return report
}

func appliesToBranch(ruleset Ruleset, branch string) bool {
	if ruleset.Target != "branch" || branch == "" {
		return false
	}
	ref := "refs/heads/" + branch
	if !matchesAny(ruleset.Conditions.RefName.Include, ref) {
		return false
	}
	return !matchesAny(ruleset.Conditions.RefName.Exclude, ref)
}

func matchesAny(patterns []string, ref string) bool {
	for _, pattern := range patterns {
		switch pattern {
		case "~ALL", "~DEFAULT_BRANCH":
			return true
		}
		matched, err := path.Match(pattern, ref)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func bypasses(ruleset Ruleset, appID int64) bool {
	for _, actor := range ruleset.BypassActors {
		if actor.ActorID == appID && actor.ActorType == "Integration" && actor.BypassMode == "pull_request" {
			return true
		}
	}
	return false
}
