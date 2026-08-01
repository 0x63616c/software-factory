// Package telemetry holds this service's Prometheus metrics: what is measured,
// what it is called, and which labels it is broken down by.
//
// The names and labels here are a published interface, not an internal detail.
// Grafana panels and alert rules are written against them and live outside this
// repository, so renaming a metric breaks a dashboard silently — there is no
// compiler on that side. Treat a rename as a wire-format change.
//
// Metrics are constructed against an injected registry rather than the global
// default one, so a test observes exactly what one Metrics recorded and nothing
// a previous test left behind.
package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/0x63616c/software-factory/internal/work"
)

// namespace prefixes every metric, so this service's series are one selector
// away in a cluster that runs several.
const namespace = "software_factory"

// Label names. They are constants because the same three describe most of the
// metrics here, and a metric that spelled one of them differently would look
// like a different dimension in every query that joined them.
const (
	labelStage    = "stage"
	labelModel    = "model"
	labelEffort   = "effort"
	labelOutcome  = "outcome"
	labelEncoding = "encoding"
	labelStatus   = "status"
	labelSource   = "source"
	labelTool     = "tool"
	labelActivity = "activity"
	labelBudget   = "budget"
)

// AgentOutcome is one bounded provider, tool, or child outcome label.
type AgentOutcome string

const (
	// AgentOutcomeFinalText is a provider turn that returned terminal text.
	AgentOutcomeFinalText AgentOutcome = "final_text"
	// AgentOutcomeToolCalls is a provider turn that requested tools.
	AgentOutcomeToolCalls AgentOutcome = "tool_calls"
	// AgentOutcomeFailed is a provider, tool, or child failure.
	AgentOutcomeFailed AgentOutcome = "failed"
	// AgentOutcomeCancelled is cancellation rather than failure.
	AgentOutcomeCancelled AgentOutcome = "cancelled"
	// AgentOutcomeSucceeded is a successfully completed tool or child.
	AgentOutcomeSucceeded AgentOutcome = "succeeded"
	// AgentOutcomeToolError is a tool-visible, non-infrastructure failure.
	AgentOutcomeToolError AgentOutcome = "tool_error"
)

// Outcome is how a stage attempt finished, and the value of the `outcome`
// label.
//
// It has four values rather than two because ADR-0011 treats two failures as
// categorically different from the rest: a rate limit trips the dispatcher's
// breaker and is expected to clear on its own, and an auth failure stops the
// service until a human re-seeds a credential. Collapsing either into a general
// failure would leave "why did everything stop" unanswerable from metrics.
type Outcome string

// The outcomes a stage attempt can have.
const (
	OutcomeSuccess     Outcome = "success"
	OutcomeFailed      Outcome = "failed"
	OutcomeRateLimited Outcome = "rate_limited"
	OutcomeAuthFailed  Outcome = "auth_failed"
)

// stageLabels are the dimensions every stage metric carries. Model and effort
// are separate labels rather than one composite so a query can aggregate over
// effort while comparing models.
//
// None of them is attacker-controlled — they come from this service's own
// config and its own Stage and Outcome types. That is not the same as bounded:
// model and effort are free strings an operator hand-writes into an
// UpdateConfig signal, so every typo is a series that never goes away. Values
// are passed through boundedLabels for that reason; see bounded.go.
var stageLabels = []string{labelStage, labelModel, labelEffort}

// durationBuckets span a whole stage timeout. ADR-0011 gives each stage 60
// minutes, deliberately generous until real timings exist — so the top finite
// bucket is that ceiling, and anything above it is a stage that was killed
// rather than one that was slow.
var durationBuckets = []float64{10, 30, 60, 120, 300, 600, 1200, 1800, 2700, 3600}

// Metrics records what one stage attempt cost and how it ended.
//
// The surface is one method, deliberately. Token counts, the outcome and the
// duration are all known at exactly one moment — when a stage returns — and a
// type with a method per counter would let a caller record three of the four
// and leave the series that says how the stage ended with a hole in it.
type Metrics struct {
	uncachedInputTokens    *prometheus.CounterVec
	cachedInputTokens      *prometheus.CounterVec
	outputTokens           *prometheus.CounterVec
	reasoningTokens        *prometheus.CounterVec
	stages                 *prometheus.CounterVec
	stageDuration          *prometheus.HistogramVec
	payloadLayerBytesIn    *prometheus.CounterVec
	payloadLayerBytesOut   *prometheus.CounterVec
	payloadLayerDuration   *prometheus.HistogramVec
	agentModelTurns        *prometheus.CounterVec
	agentProviderDuration  *prometheus.HistogramVec
	agentInputTokens       *prometheus.CounterVec
	agentOutputTokens      *prometheus.CounterVec
	agentUsageReports      *prometheus.CounterVec
	agentConversationBytes *prometheus.HistogramVec
	agentToolCalls         *prometheus.CounterVec
	agentToolDuration      *prometheus.HistogramVec
	agentActivityRetries   *prometheus.CounterVec
	agentBudgetExhaustions *prometheus.CounterVec
	agentChildOutcomes     *prometheus.CounterVec

	// bounded caps how many distinct values each label key can export.
	bounded *boundedLabels
}

// NewMetrics registers this service's metrics on reg.
//
// It panics if they are already registered, which is Prometheus's own answer
// and the right one here: the worker has a single composition root, so a second
// registration is a wiring bug, and the alternative to a crash is two sets of
// counters each recording half the work.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		uncachedInputTokens: counter(reg, "stage_uncached_input_tokens_total",
			"Input tokens the provider had to read, excluding those served from its prompt cache.", stageLabels),
		cachedInputTokens: counter(reg, "stage_cached_input_tokens_total",
			"Input tokens served from the provider's prompt cache, disjoint from the uncached counter.", stageLabels),
		outputTokens: counter(reg, "stage_output_tokens_total",
			"Output tokens produced, including the reasoning tokens counted separately below.", stageLabels),
		reasoningTokens: counter(reg, "stage_reasoning_tokens_total",
			"The part of the output tokens that was reasoning. A subset of the output counter, never a peer of it.", stageLabels),
		stages: counter(reg, "stages_total",
			"Stage attempts that finished, by how they finished.", append(append([]string{}, stageLabels...), labelOutcome)),
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "stage_duration_seconds",
			Help:      "Wall-clock time one stage attempt took.",
			Buckets:   durationBuckets,
		}, stageLabels),
		payloadLayerBytesIn: counter(reg, "payload_layer_bytes_in_total",
			"Bytes received by a payload codec layer.", []string{labelEncoding}),
		payloadLayerBytesOut: counter(reg, "payload_layer_bytes_out_total",
			"Bytes emitted by a payload codec layer.", []string{labelEncoding}),
		payloadLayerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "payload_layer_duration_seconds",
			Help:      "Wall-clock time one payload codec layer operation took.",
			Buckets:   []float64{0.001, 0.01, 0.1, 1, 5, 10, 30},
		}, []string{labelEncoding}),
		agentModelTurns: counter(reg, "agent_model_turns_total",
			"Direct provider turns, by bounded model configuration and outcome.", []string{labelModel, labelEffort, labelOutcome}),
		agentProviderDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "agent_provider_duration_seconds",
			Help:      "Wall-clock latency of one direct provider turn.",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120},
		}, []string{labelModel, labelEffort, labelOutcome}),
		agentInputTokens: counter(reg, "agent_input_tokens_total",
			"Input tokens reported by completed agent model turns.", []string{labelModel, labelEffort}),
		agentOutputTokens: counter(reg, "agent_output_tokens_total",
			"Output tokens reported by completed agent model turns.", []string{labelModel, labelEffort}),
		agentUsageReports: counter(reg, "agent_usage_reports_total",
			"Completed agent model turns, split by whether provider usage was measured.", []string{labelModel, labelEffort, labelStatus}),
		agentConversationBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "agent_conversation_bytes",
			Help:      "Cumulative immutable conversation bytes after a completed model turn or tool call.",
			Buckets:   prometheus.ExponentialBuckets(1024, 2, 12),
		}, []string{labelSource}),
		agentToolCalls: counter(reg, "agent_tool_calls_total",
			"Sandbox tool calls, by bounded tool name and outcome.", []string{labelTool, labelOutcome}),
		agentToolDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "agent_tool_duration_seconds",
			Help:      "Wall-clock duration of one sandbox tool call.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 120},
		}, []string{labelTool, labelOutcome}),
		agentActivityRetries: counter(reg, "agent_activity_retries_total",
			"Retried agent activity invocations observed after attempt one.", []string{labelActivity}),
		agentBudgetExhaustions: counter(reg, "agent_budget_exhaustions_total",
			"Agent workflows stopped by a fixed resource budget.", []string{labelBudget}),
		agentChildOutcomes: counter(reg, "agent_child_outcomes_total",
			"Agent child workflow terminal outcomes observed before return.", []string{labelOutcome}),
		bounded: newBoundedLabels(LabelValueLimit),
	}
	reg.MustRegister(m.stageDuration)
	reg.MustRegister(m.payloadLayerDuration)
	reg.MustRegister(m.agentProviderDuration)
	reg.MustRegister(m.agentConversationBytes)
	reg.MustRegister(m.agentToolDuration)
	return m
}

// AgentToolCall records one sandbox tool invocation at its activity boundary.
func (m *Metrics) AgentToolCall(tool string, outcome AgentOutcome, conversationBytes int64, took time.Duration) {
	labels := prometheus.Labels{
		labelTool:    m.bounded.fold(labelTool, tool),
		labelOutcome: m.bounded.fold(labelOutcome, string(outcome)),
	}
	m.agentToolCalls.With(labels).Inc()
	m.agentToolDuration.With(labels).Observe(max(took.Seconds(), 0))
	m.agentConversationBytes.WithLabelValues(m.bounded.fold(labelSource, "tool")).Observe(float64(max(conversationBytes, 0)))
}

// AgentActivityRetry records one invocation whose Temporal attempt is greater than one.
func (m *Metrics) AgentActivityRetry(activityName string) {
	m.agentActivityRetries.WithLabelValues(m.bounded.fold(labelActivity, activityName)).Inc()
}

// AgentBudgetExhausted records the fixed limit that stopped one child.
func (m *Metrics) AgentBudgetExhausted(budget string) {
	m.agentBudgetExhaustions.WithLabelValues(m.bounded.fold(labelBudget, budget)).Inc()
}

// AgentChildFinished records one terminal child outcome.
func (m *Metrics) AgentChildFinished(outcome AgentOutcome) {
	m.agentChildOutcomes.WithLabelValues(m.bounded.fold(labelOutcome, string(outcome))).Inc()
}

// AgentModelTurn records one direct provider invocation at the activity boundary.
func (m *Metrics) AgentModelTurn(
	model work.Model,
	outcome AgentOutcome,
	usage work.Usage,
	usageMeasured bool,
	conversationBytes int64,
	took time.Duration,
) {
	labels := prometheus.Labels{
		labelModel:   m.bounded.fold(labelModel, model.Name),
		labelEffort:  m.bounded.fold(labelEffort, model.Effort),
		labelOutcome: m.bounded.fold(labelOutcome, string(outcome)),
	}
	m.agentModelTurns.With(labels).Inc()
	m.agentProviderDuration.With(labels).Observe(max(took.Seconds(), 0))
	usageLabels := prometheus.Labels{labelModel: labels[labelModel], labelEffort: labels[labelEffort]}
	m.agentInputTokens.With(usageLabels).Add(float64(max(usage.InputTokens, 0)))
	m.agentOutputTokens.With(usageLabels).Add(float64(max(usage.OutputTokens, 0)))
	status := "unknown"
	if usageMeasured {
		status = "measured"
	}
	reportLabels := prometheus.Labels{
		labelModel: labels[labelModel], labelEffort: labels[labelEffort],
		labelStatus: m.bounded.fold(labelStatus, status),
	}
	m.agentUsageReports.With(reportLabels).Inc()
	m.agentConversationBytes.WithLabelValues(m.bounded.fold(labelSource, "model")).Observe(float64(max(conversationBytes, 0)))
}

// PayloadLayerApplied records bytes and duration for one payload codec layer operation.
func (m *Metrics) PayloadLayerApplied(encoding string, bytesIn, bytesOut int, took time.Duration) {
	labels := prometheus.Labels{labelEncoding: m.bounded.fold(labelEncoding, encoding)}
	m.payloadLayerBytesIn.With(labels).Add(float64(max(bytesIn, 0)))
	m.payloadLayerBytesOut.With(labels).Add(float64(max(bytesOut, 0)))
	m.payloadLayerDuration.With(labels).Observe(took.Seconds())
}

// StageFinished records one stage attempt: what it spent, how it ended, and how
// long it took.
//
// Usage arrives as the provider reports it, and the split into disjoint input
// counters happens here rather than at the call site — so every caller records
// the same arithmetic, and there is one place to correct if the provider's
// accounting changes.
func (m *Metrics) StageFinished(stage work.Stage, model work.Model, outcome Outcome, usage work.Usage, took time.Duration) {
	// Every label goes through the cardinality guard, not only the two that are
	// hand-written today: Stage and Outcome are Go types but neither is a closed
	// set the compiler enforces, and the guard is free for a set of five.
	labels := prometheus.Labels{
		labelStage:  m.bounded.fold(labelStage, string(stage)),
		labelModel:  m.bounded.fold(labelModel, model.Name),
		labelEffort: m.bounded.fold(labelEffort, model.Effort),
	}

	m.uncachedInputTokens.With(labels).Add(float64(uncachedInput(usage)))
	m.cachedInputTokens.With(labels).Add(float64(max(usage.CachedInputTokens, 0)))
	m.outputTokens.With(labels).Add(float64(max(usage.OutputTokens, 0)))
	m.reasoningTokens.With(labels).Add(float64(max(usage.ReasoningTokens, 0)))
	m.stageDuration.With(labels).Observe(took.Seconds())

	outcomeLabels := prometheus.Labels{labelOutcome: m.bounded.fold(labelOutcome, string(outcome))}
	for name, value := range labels {
		outcomeLabels[name] = value
	}
	m.stages.With(outcomeLabels).Inc()
}

// uncachedInput is the input the provider actually read.
//
// Usage.InputTokens includes the cached part, so the two are separated here
// rather than exported as reported: summing a reported input against a cached
// input counts every cache hit twice, at the wrong price. The result is clamped
// at zero because a counter that moves backwards makes rate() report a reset
// and invent a spike — codex clamps the same subtraction for the same reason.
func uncachedInput(usage work.Usage) int64 {
	return max(usage.InputTokens-max(usage.CachedInputTokens, 0), 0)
}

// counter registers one labelled counter in this service's namespace.
func counter(reg prometheus.Registerer, name, help string, labels []string) *prometheus.CounterVec {
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	}, labels)
	reg.MustRegister(vec)
	return vec
}
