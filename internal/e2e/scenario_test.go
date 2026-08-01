//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/0x63616c/software-factory/internal/config"
)

type scenarioTrace struct {
	Version uint8              `json:"version"`
	Name    string             `json:"name"`
	Seed    uint64             `json:"seed,omitempty"`
	CI      []scenarioCIResult `json:"ci"`
	Reviews []scenarioReview   `json:"reviews"`
	Merges  []scenarioMerge    `json:"merges"`
}

type scenarioCIResult string

const (
	scenarioCIFailure scenarioCIResult = "failure"
	scenarioCISuccess scenarioCIResult = "success"
)

type scenarioReview string

const (
	scenarioReviewBlocking scenarioReview = "blocking"
	scenarioReviewClean    scenarioReview = "clean"
)

type scenarioMerge string

const (
	scenarioMergeConfirmed   scenarioMerge = "confirmed"
	scenarioMergeHeadChanged scenarioMerge = "head_changed"
)

type fakeScenario struct {
	trace scenarioTrace

	mu             sync.Mutex
	ciIndex        int
	reviewIndex    int
	mergeIndex     int
	publishedHeads []string
	checkedHeads   map[string]struct{}
	reviewedHeads  map[string]struct{}
	mergedHeads    []string
	currentHeadID  string
	confirmedHead  string
	externalHeads  int
}

func newFakeScenario(t *testing.T, trace scenarioTrace) *fakeScenario {
	t.Helper()
	return &fakeScenario{
		trace: trace, checkedHeads: make(map[string]struct{}), reviewedHeads: make(map[string]struct{}),
	}
}

func (script *fakeScenario) nextCI() (scenarioCIResult, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.ciIndex >= len(script.trace.CI) {
		return "", fmt.Errorf("CI trace exhausted after %d observations", script.ciIndex)
	}
	outcome := script.trace.CI[script.ciIndex]
	script.ciIndex++
	return outcome, nil
}

func (script *fakeScenario) nextReview() (scenarioReview, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.reviewIndex >= len(script.trace.Reviews) {
		return "", fmt.Errorf("review trace exhausted after %d reviews", script.reviewIndex)
	}
	outcome := script.trace.Reviews[script.reviewIndex]
	script.reviewIndex++
	return outcome, nil
}

func (script *fakeScenario) nextMerge() (scenarioMerge, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.mergeIndex >= len(script.trace.Merges) {
		return "", fmt.Errorf("merge trace exhausted after %d merge attempts", script.mergeIndex)
	}
	outcome := script.trace.Merges[script.mergeIndex]
	script.mergeIndex++
	return outcome, nil
}

func (script *fakeScenario) currentHead() string {
	script.mu.Lock()
	defer script.mu.Unlock()
	return script.currentHeadID
}

func (script *fakeScenario) publishHead() string {
	script.mu.Lock()
	defer script.mu.Unlock()
	head := fmt.Sprintf("candidate-head-%d", len(script.publishedHeads)+1)
	script.currentHeadID = head
	script.publishedHeads = append(script.publishedHeads, head)
	return head
}

func (script *fakeScenario) advanceHead() string {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.externalHeads++
	head := fmt.Sprintf("candidate-head-external-%d", script.externalHeads)
	script.currentHeadID = head
	return head
}

func (script *fakeScenario) recordCheck(head string) error {
	script.mu.Lock()
	defer script.mu.Unlock()
	if head != script.currentHeadID {
		return fmt.Errorf("checks requested for %q, current head is %q", head, script.currentHeadID)
	}
	script.checkedHeads[head] = struct{}{}
	return nil
}

func (script *fakeScenario) recordConfirmedMerge(head string) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.mergedHeads = append(script.mergedHeads, head)
	script.confirmedHead = head
}

func (script *fakeScenario) recordReview(head string) error {
	script.mu.Lock()
	defer script.mu.Unlock()
	if head != script.currentHeadID {
		return fmt.Errorf("review requested for %q, current head is %q", head, script.currentHeadID)
	}
	script.reviewedHeads[head] = struct{}{}
	return nil
}

func (script *fakeScenario) assertExhausted() error {
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.ciIndex != len(script.trace.CI) {
		return fmt.Errorf("consumed %d of %d CI outcomes", script.ciIndex, len(script.trace.CI))
	}
	if script.reviewIndex != len(script.trace.Reviews) {
		return fmt.Errorf("consumed %d of %d review outcomes", script.reviewIndex, len(script.trace.Reviews))
	}
	if script.mergeIndex != len(script.trace.Merges) {
		return fmt.Errorf("consumed %d of %d merge outcomes", script.mergeIndex, len(script.trace.Merges))
	}
	return nil
}

func (script *fakeScenario) assertInvariants() (string, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.confirmedHead == "" {
		return "", fmt.Errorf("no confirmed merge")
	}
	if len(script.publishedHeads) == 0 {
		return "", fmt.Errorf("no candidate head was published")
	}
	for _, head := range script.publishedHeads {
		if _, checked := script.checkedHeads[head]; !checked {
			return "", fmt.Errorf("published head %q was never checked", head)
		}
	}
	for _, head := range script.mergedHeads {
		if _, checked := script.checkedHeads[head]; !checked {
			return "", fmt.Errorf("merged head %q was never checked", head)
		}
		if _, reviewed := script.reviewedHeads[head]; !reviewed {
			return "", fmt.Errorf("merged head %q was never reviewed", head)
		}
	}
	return script.confirmedHead, nil
}

// TestStatefulScenarioCorpus executes replayable recovery traces against the
// durable factory path. The trace is the interface: every external outcome is
// fixed before the test starts, while Temporal, Postgres, and workflow state
// remain real.
func TestStatefulScenarioCorpus(t *testing.T) {
	for _, name := range []string{
		"ci-failure-then-revision",
		"blocking-review-then-revision",
		"head-change-before-merge",
		"combined-recovery",
	} {
		t.Run(name, func(t *testing.T) {
			assertScenario(t, readScenarioTrace(t, filepath.Join("testdata", "scenarios", name+".json")))
		})
	}

	for _, seed := range []uint64{11, 17, 41} {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			assertScenario(t, generatedScenarioTrace(seed))
		})
	}
}

// TestScenarioReplay makes a saved failing trace independently reproducible.
// It is skipped during the normal corpus run; scripts/e2e.sh enables it for
// just scenario replay <trace>.
func TestScenarioReplay(t *testing.T) {
	path := config.ScenarioTracePath()
	if path == "" {
		t.Skip("set SOFTWARE_FACTORY_SCENARIO_TRACE to replay a saved scenario")
	}
	assertScenario(t, readScenarioTrace(t, path))
}

func assertScenario(t *testing.T, trace scenarioTrace) {
	t.Helper()
	if err := trace.Validate(); err != nil {
		t.Fatalf("validate scenario trace: %v", err)
	}
	saveReplayTraceOnFailure(t, trace)

	result := runScenario(t, trace)
	if result.TicketState != "done" || result.RunOutcome != "succeeded" {
		t.Fatalf("terminal lifecycle = ticket %q, run %q", result.TicketState, result.RunOutcome)
	}
	if result.RunCount != 1 {
		t.Fatalf("run count = %d, want exactly one terminal Run", result.RunCount)
	}
	if !result.Merge.ReviewedHeadMatched {
		t.Fatalf("merge evidence = %+v, want the confirmed reviewed head", result.Merge)
	}
	if result.ActiveRuns != 0 || result.RemainingRunWorkers != 0 {
		t.Fatalf("cleanup = %d active Runs, %d Run Workers", result.ActiveRuns, result.RemainingRunWorkers)
	}
	if result.ModelAdapter != "fake-responses" || result.GitHubAdapter != "fake" {
		t.Fatalf("external adapters = model %q, GitHub %q", result.ModelAdapter, result.GitHubAdapter)
	}
}

func readScenarioTrace(t *testing.T, path string) scenarioTrace {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scenario trace %s: %v", path, err)
	}
	var trace scenarioTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("decode scenario trace %s: %v", path, err)
	}
	return trace
}

func generatedScenarioTrace(seed uint64) scenarioTrace {
	random := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	ciFailures := random.IntN(3)
	blockingReviews := random.IntN(3)
	headChanges := random.IntN(3)

	trace := scenarioTrace{
		Version: 1,
		Name:    fmt.Sprintf("seed-%d", seed),
		Seed:    seed,
		CI:      make([]scenarioCIResult, 0, 1+ciFailures+blockingReviews+headChanges),
		Reviews: make([]scenarioReview, 0, 1+blockingReviews+headChanges),
		Merges:  make([]scenarioMerge, 0, 1+headChanges),
	}
	for range ciFailures {
		trace.CI = append(trace.CI, scenarioCIFailure)
	}
	trace.CI = append(trace.CI, scenarioCISuccess)
	for range blockingReviews {
		trace.Reviews = append(trace.Reviews, scenarioReviewBlocking)
		trace.CI = append(trace.CI, scenarioCISuccess)
	}
	trace.Reviews = append(trace.Reviews, scenarioReviewClean)
	for range headChanges {
		trace.Merges = append(trace.Merges, scenarioMergeHeadChanged)
		trace.CI = append(trace.CI, scenarioCISuccess)
		trace.Reviews = append(trace.Reviews, scenarioReviewClean)
	}
	trace.Merges = append(trace.Merges, scenarioMergeConfirmed)
	return trace
}

func (trace scenarioTrace) Validate() error {
	if trace.Version != 1 {
		return fmt.Errorf("version = %d, want 1", trace.Version)
	}
	if !validScenarioName(trace.Name) {
		return fmt.Errorf("name %q must be a lowercase alphanumeric slug", trace.Name)
	}
	if len(trace.CI) == 0 || len(trace.Reviews) == 0 || len(trace.Merges) == 0 {
		return fmt.Errorf("ci, reviews, and merges must each contain an outcome")
	}

	for _, outcome := range trace.CI {
		switch outcome {
		case scenarioCIFailure, scenarioCISuccess:
		default:
			return fmt.Errorf("unknown CI outcome %q", outcome)
		}
	}

	for _, outcome := range trace.Reviews {
		switch outcome {
		case scenarioReviewBlocking, scenarioReviewClean:
		default:
			return fmt.Errorf("unknown review outcome %q", outcome)
		}
	}
	for index, outcome := range trace.Merges {
		switch outcome {
		case scenarioMergeHeadChanged:
			if index+1 == len(trace.Merges) {
				return fmt.Errorf("final merge outcome is head_changed")
			}
		case scenarioMergeConfirmed:
			if index+1 != len(trace.Merges) {
				return fmt.Errorf("confirmed merge is not final")
			}
		default:
			return fmt.Errorf("unknown merge outcome %q", outcome)
		}
	}

	return nil
}

func validScenarioName(name string) bool {
	if name == "" || filepath.Base(name) != name {
		return false
	}
	for _, character := range name {
		if character == '-' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(name, "-") && !strings.HasSuffix(name, "-")
}

func saveReplayTraceOnFailure(t *testing.T, trace scenarioTrace) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		path := filepath.Join(".artifacts", "scenarios", trace.Name+".json")
		data, err := json.MarshalIndent(trace, "", "  ")
		if err != nil {
			t.Logf("encode failed scenario trace: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Logf("create failed scenario trace directory: %v", err)
			return
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Logf("write failed scenario trace: %v", err)
			return
		}
		t.Logf("replay with: just scenario replay %s", path)
	})
}
