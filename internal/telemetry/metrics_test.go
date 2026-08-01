package telemetry_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/0x63616c/software-factory/internal/telemetry"
	"github.com/0x63616c/software-factory/internal/work"
)

// These metric names and label sets are asserted literally, not derived. They
// are a wire format: a Grafana panel and an alert rule are written against
// them, so renaming one silently blanks a dashboard rather than failing a
// build. A change here has to be a deliberate edit to this test.

var (
	planModel = work.Model{Name: "gpt-5.6-terra", Effort: "medium"}
	usage     = work.Usage{
		InputTokens:       1000,
		CachedInputTokens: 800,
		OutputTokens:      300,
		ReasoningTokens:   250,
	}
)

func TestStageFinishedRecordsTokensByStageAndModel(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).StageFinished(work.StagePlan, planModel, telemetry.OutcomeSuccess, usage, 42*time.Second)

	// Input is split into two disjoint counters that each carry one price.
	// Verified against codex rust-v0.145.0: input_tokens INCLUDES
	// cached_input_tokens (non_cached_input() subtracts one from the other), so
	// exporting the reported input alongside the cached part would let any
	// sum over both count the cache hits twice, at the uncached rate.
	want := `
# HELP software_factory_stage_cached_input_tokens_total Input tokens served from the provider's prompt cache, disjoint from the uncached counter.
# TYPE software_factory_stage_cached_input_tokens_total counter
software_factory_stage_cached_input_tokens_total{effort="medium",model="gpt-5.6-terra",stage="plan"} 800
# HELP software_factory_stage_uncached_input_tokens_total Input tokens the provider had to read, excluding those served from its prompt cache.
# TYPE software_factory_stage_uncached_input_tokens_total counter
software_factory_stage_uncached_input_tokens_total{effort="medium",model="gpt-5.6-terra",stage="plan"} 200
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want),
		"software_factory_stage_uncached_input_tokens_total",
		"software_factory_stage_cached_input_tokens_total"); err != nil {
		t.Error(err)
	}
}

func TestOutputTokensArePricedWholeAndReasoningIsASubsetOfThem(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).StageFinished(work.StagePlan, planModel, telemetry.OutcomeSuccess, usage, time.Second)

	// Verified against codex rust-v0.145.0: reasoning_output_tokens is a part
	// of output_tokens, not a peer of it — blended_total() adds output once and
	// never adds reasoning, and the CLI renders it as "output=N (reasoning M)".
	// So output is the billable figure and reasoning is reported beside it for
	// insight; the two must never be summed.
	if got := testutil.ToFloat64(mustGather(t, reg, "software_factory_stage_output_tokens_total")); got != 300 {
		t.Errorf("output tokens = %v, want the reported 300 — reasoning bills at the output rate and is already inside it", got)
	}
	if got := testutil.ToFloat64(mustGather(t, reg, "software_factory_stage_reasoning_tokens_total")); got != 250 {
		t.Errorf("reasoning tokens = %v, want 250", got)
	}
}

func TestACacheHitLargerThanTheInputCannotDriveACounterNegative(t *testing.T) {
	t.Parallel()

	// A Prometheus counter that goes backwards makes rate() report a reset and
	// invent a spike. Codex clamps the same subtraction (non_cached_input uses
	// .max(0)), so a provider reporting the two inconsistently must not be able
	// to corrupt a week of dashboards here either.
	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).StageFinished(work.StagePlan, planModel, telemetry.OutcomeSuccess,
		work.Usage{InputTokens: 100, CachedInputTokens: 900}, time.Second)

	if got := testutil.ToFloat64(mustGather(t, reg, "software_factory_stage_uncached_input_tokens_total")); got != 0 {
		t.Errorf("uncached input tokens = %v, want 0", got)
	}
}

func TestStagesTotalCountsAttemptsByOutcome(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetrics(reg)
	metrics.StageFinished(work.StageImplement, planModel, telemetry.OutcomeFailed, work.Usage{}, time.Second)
	metrics.StageFinished(work.StageImplement, planModel, telemetry.OutcomeRateLimited, work.Usage{}, time.Second)
	metrics.StageFinished(work.StageImplement, planModel, telemetry.OutcomeRateLimited, work.Usage{}, time.Second)

	want := `
# HELP software_factory_stages_total Stage attempts that finished, by how they finished.
# TYPE software_factory_stages_total counter
software_factory_stages_total{effort="medium",model="gpt-5.6-terra",outcome="failed",stage="implement"} 1
software_factory_stages_total{effort="medium",model="gpt-5.6-terra",outcome="rate_limited",stage="implement"} 2
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want), "software_factory_stages_total"); err != nil {
		t.Error(err)
	}
}

func TestOutcomesAreDistinctEnoughToExplainWhyTheBreakerTripped(t *testing.T) {
	t.Parallel()

	// ADR-0011 treats rate-limit and auth failures as different from an
	// ordinary failure: one trips the breaker and one is dead until a human
	// re-seeds. If they collapsed into "failed" the metric could not tell an
	// operator which of the two is happening.
	seen := map[telemetry.Outcome]bool{}
	for _, outcome := range []telemetry.Outcome{
		telemetry.OutcomeSuccess,
		telemetry.OutcomeFailed,
		telemetry.OutcomeRateLimited,
		telemetry.OutcomeAuthFailed,
	} {
		if outcome == "" {
			t.Error("an outcome is empty; an empty label value reads as an unset dimension")
		}
		if seen[outcome] {
			t.Errorf("outcome %q is duplicated; two causes would be indistinguishable", outcome)
		}
		seen[outcome] = true
	}
}

func TestStageDurationIsRecordedInSecondsUnderTheStagesOwnLabels(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).StageFinished(work.StageReview, planModel, telemetry.OutcomeSuccess, work.Usage{}, 90*time.Second)

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() = %v", err)
	}
	for _, family := range gathered {
		if family.GetName() != "software_factory_stage_duration_seconds" {
			continue
		}
		histogram := family.GetMetric()[0].GetHistogram()
		if got := histogram.GetSampleSum(); got != 90 {
			t.Errorf("duration sum = %v, want 90 — ADR-0011 sets stage timeouts as a guess until real timings exist, and this is where they come from", got)
		}
		if got := histogram.GetSampleCount(); got != 1 {
			t.Errorf("duration count = %v, want 1", got)
		}
		return
	}
	t.Error("no software_factory_stage_duration_seconds was registered")
}

func TestDurationBucketsCoverAWholeStageTimeout(t *testing.T) {
	t.Parallel()

	// Stages get 60 minutes each. A histogram whose largest finite bucket is
	// below that answers "how long do stages take" with "more than the top
	// bucket" for every slow stage — which is the only case anyone asks about.
	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).StageFinished(work.StagePlan, planModel, telemetry.OutcomeSuccess, work.Usage{}, time.Second)

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() = %v", err)
	}
	for _, family := range gathered {
		if family.GetName() != "software_factory_stage_duration_seconds" {
			continue
		}
		buckets := family.GetMetric()[0].GetHistogram().GetBucket()
		if len(buckets) == 0 {
			t.Fatal("the duration histogram has no buckets")
		}
		if largest := buckets[len(buckets)-1].GetUpperBound(); largest < 3600 {
			t.Errorf("largest duration bucket is %vs, want at least the 3600s stage timeout", largest)
		}
		return
	}
	t.Error("no software_factory_stage_duration_seconds was registered")
}

func TestPayloadLayerAppliedRecordsBytesAndDurationByEncoding(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).PayloadLayerApplied("binary/zstd", 12, 5, time.Second)

	want := `
# HELP software_factory_payload_layer_bytes_in_total Bytes received by a payload codec layer.
# TYPE software_factory_payload_layer_bytes_in_total counter
software_factory_payload_layer_bytes_in_total{encoding="binary/zstd"} 12
# HELP software_factory_payload_layer_bytes_out_total Bytes emitted by a payload codec layer.
# TYPE software_factory_payload_layer_bytes_out_total counter
software_factory_payload_layer_bytes_out_total{encoding="binary/zstd"} 5
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want),
		"software_factory_payload_layer_bytes_in_total",
		"software_factory_payload_layer_bytes_out_total"); err != nil {
		t.Error(err)
	}

	family := gatherFamily(t, reg, "software_factory_payload_layer_duration_seconds")
	histogram := family.GetMetric()[0].GetHistogram()
	if got := histogram.GetSampleCount(); got != 1 {
		t.Errorf("duration count = %d, want 1", got)
	}
	if got := histogram.GetSampleSum(); got != 1 {
		t.Errorf("duration sum = %v, want 1", got)
	}
}

func TestRegisteringTwiceIsACrashRatherThanASilentSecondSetOfCounters(t *testing.T) {
	t.Parallel()

	// The worker has one composition root, so two Metrics on one registry is a
	// wiring bug. Prometheus's own answer is a panic on duplicate registration;
	// MustRegister keeps it, because the alternative is two objects counting
	// half the stages each.
	defer func() {
		if recover() == nil {
			t.Error("registering twice did not panic; the second set of counters would silently record half the work")
		}
	}()

	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg)
	telemetry.NewMetrics(reg)
}

func mustGather(t *testing.T, reg *prometheus.Registry, name string) prometheus.Collector {
	t.Helper()

	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() = %v", err)
	}
	for _, family := range gathered {
		if family.GetName() == name {
			counter := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: family.GetHelp()})
			counter.Add(family.GetMetric()[0].GetCounter().GetValue())
			return counter
		}
	}
	t.Fatalf("%s was not registered", name)
	return nil
}

func TestOperatorTypoesInAModelNameCannotGrowTheSeriesSetWithoutBound(t *testing.T) {
	t.Parallel()

	// model and effort are config, not user input, but config is edited by hand
	// in an UpdateConfig signal and a typo is permanent: every distinct value is
	// a series the server stores forever. The backstop is the same one
	// packages/platform/metrics/bounded.ts applies to every label on the TS
	// side — the first values through are exported as written, the rest fold
	// into `other`, so a leak shows up as a large `other` bucket instead of an
	// unbounded /metrics endpoint.
	reg := prometheus.NewRegistry()
	m := telemetry.NewMetrics(reg)

	for i := range telemetry.LabelValueLimit {
		m.StageFinished(work.StagePlan, work.Model{Name: fmt.Sprintf("model-%d", i), Effort: "medium"},
			telemetry.OutcomeSuccess, usage, time.Second)
	}
	if got := countSeries(t, reg, "software_factory_stage_output_tokens_total"); got != telemetry.LabelValueLimit {
		t.Fatalf("%d series after %d distinct models, want one each", got, telemetry.LabelValueLimit)
	}

	for i := range 50 {
		m.StageFinished(work.StagePlan, work.Model{Name: fmt.Sprintf("typo-%d", i), Effort: "medium"},
			telemetry.OutcomeSuccess, usage, time.Second)
	}

	if got, want := countSeries(t, reg, "software_factory_stage_output_tokens_total"), telemetry.LabelValueLimit+1; got != want {
		t.Errorf("%d series after 50 further models, want %d — everything past the limit belongs in one bucket", got, want)
	}
	if got := seriesValue(t, reg, "software_factory_stage_output_tokens_total", telemetry.OtherLabelValue); got != 50*float64(usage.OutputTokens) {
		t.Errorf("model=%q counter = %v, want the 50 folded attempts' output tokens; a dropped attempt is worse than a coarse label", telemetry.OtherLabelValue, got)
	}
}

func TestAnEmptyModelNameIsLabelledRatherThanLeftBlank(t *testing.T) {
	t.Parallel()

	// A blank label is indistinguishable from a missing dimension in a query,
	// and the zero Config can reach here (ModelFor on an unvalidated config
	// returns Model{}). Name it instead.
	reg := prometheus.NewRegistry()
	telemetry.NewMetrics(reg).StageFinished(work.StagePlan, work.Model{}, telemetry.OutcomeSuccess, usage, time.Second)

	if got := seriesValue(t, reg, "software_factory_stage_output_tokens_total", telemetry.OtherLabelValue); got != float64(usage.OutputTokens) {
		t.Errorf("model=%q counter = %v, want %d", telemetry.OtherLabelValue, got, usage.OutputTokens)
	}
}

func TestAgentModelTurnRecordsProviderUsageLatencyAndConversationSize(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetrics(reg)
	metrics.AgentModelTurn(
		planModel,
		telemetry.AgentOutcomeFinalText,
		work.Usage{InputTokens: 12, OutputTokens: 3},
		true,
		4096,
		1500*time.Millisecond,
	)

	want := `
# HELP software_factory_agent_input_tokens_total Input tokens reported by completed agent model turns.
# TYPE software_factory_agent_input_tokens_total counter
software_factory_agent_input_tokens_total{effort="medium",model="gpt-5.6-terra"} 12
# HELP software_factory_agent_model_turns_total Direct provider turns, by bounded model configuration and outcome.
# TYPE software_factory_agent_model_turns_total counter
software_factory_agent_model_turns_total{effort="medium",model="gpt-5.6-terra",outcome="final_text"} 1
# HELP software_factory_agent_output_tokens_total Output tokens reported by completed agent model turns.
# TYPE software_factory_agent_output_tokens_total counter
software_factory_agent_output_tokens_total{effort="medium",model="gpt-5.6-terra"} 3
# HELP software_factory_agent_usage_reports_total Completed agent model turns, split by whether provider usage was measured.
# TYPE software_factory_agent_usage_reports_total counter
software_factory_agent_usage_reports_total{effort="medium",model="gpt-5.6-terra",status="measured"} 1
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want),
		"software_factory_agent_model_turns_total",
		"software_factory_agent_input_tokens_total",
		"software_factory_agent_output_tokens_total",
		"software_factory_agent_usage_reports_total",
	); err != nil {
		t.Error(err)
	}

	provider := gatherFamily(t, reg, "software_factory_agent_provider_duration_seconds").GetMetric()[0].GetHistogram()
	if provider.GetSampleCount() != 1 || provider.GetSampleSum() != 1.5 {
		t.Errorf("provider duration = count %d sum %v, want count 1 sum 1.5", provider.GetSampleCount(), provider.GetSampleSum())
	}
	conversation := gatherFamily(t, reg, "software_factory_agent_conversation_bytes").GetMetric()[0].GetHistogram()
	if conversation.GetSampleCount() != 1 || conversation.GetSampleSum() != 4096 {
		t.Errorf("conversation bytes = count %d sum %v, want count 1 sum 4096", conversation.GetSampleCount(), conversation.GetSampleSum())
	}
}

func TestAgentToolAndLifecycleMetricsUseBoundedOperationalLabels(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	metrics := telemetry.NewMetrics(reg)
	metrics.AgentToolCall("read_file", telemetry.AgentOutcomeSucceeded, 8192, 250*time.Millisecond)
	metrics.AgentActivityRetry("agent.tool")
	metrics.AgentBudgetExhausted("tool_calls")
	metrics.AgentChildFinished(telemetry.AgentOutcomeCancelled)

	want := `
# HELP software_factory_agent_activity_retries_total Retried agent activity invocations observed after attempt one.
# TYPE software_factory_agent_activity_retries_total counter
software_factory_agent_activity_retries_total{activity="agent.tool"} 1
# HELP software_factory_agent_budget_exhaustions_total Agent workflows stopped by a fixed resource budget.
# TYPE software_factory_agent_budget_exhaustions_total counter
software_factory_agent_budget_exhaustions_total{budget="tool_calls"} 1
# HELP software_factory_agent_child_outcomes_total Agent child workflow terminal outcomes observed before return.
# TYPE software_factory_agent_child_outcomes_total counter
software_factory_agent_child_outcomes_total{outcome="cancelled"} 1
# HELP software_factory_agent_tool_calls_total Sandbox tool calls, by bounded tool name and outcome.
# TYPE software_factory_agent_tool_calls_total counter
software_factory_agent_tool_calls_total{outcome="succeeded",tool="read_file"} 1
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want),
		"software_factory_agent_tool_calls_total",
		"software_factory_agent_activity_retries_total",
		"software_factory_agent_budget_exhaustions_total",
		"software_factory_agent_child_outcomes_total",
	); err != nil {
		t.Error(err)
	}

	toolDuration := gatherFamily(t, reg, "software_factory_agent_tool_duration_seconds").GetMetric()[0].GetHistogram()
	if toolDuration.GetSampleCount() != 1 || toolDuration.GetSampleSum() != 0.25 {
		t.Errorf("tool duration = count %d sum %v, want count 1 sum 0.25", toolDuration.GetSampleCount(), toolDuration.GetSampleSum())
	}
}

// countSeries is how many label combinations a metric currently has.
func countSeries(t *testing.T, reg *prometheus.Registry, name string) int {
	t.Helper()
	return len(gatherFamily(t, reg, name).GetMetric())
}

// seriesValue is the counter value carried by the series whose model label is
// model, or 0 if there is none.
func seriesValue(t *testing.T, reg *prometheus.Registry, name, model string) float64 {
	t.Helper()
	for _, metric := range gatherFamily(t, reg, name).GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == "model" && label.GetValue() == model {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func gatherFamily(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() = %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("no metric family %q was registered", name)
	return nil
}
