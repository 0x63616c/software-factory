import type { AttemptOutput, RunOutput, StepOutput, TicketResponse } from "@/api/generated";

// Fixture builders for stories and tests: sensible defaults, overridable per
// field, so each story/test states only what it actually varies.

export function fixtureTicket(overrides: Partial<TicketResponse> = {}): TicketResponse {
  return {
    id: 42,
    title: "Console — ticket detail",
    body: "Every Run, every Step, every Attempt, how long each took, how many times it ran, what it spent — and the transcript.",
    state: "active",
    ready: true,
    blockers: [],
    blocks: [],
    createdAt: "2026-07-28T09:00:00Z",
    updatedAt: "2026-07-31T10:00:00Z",
    ...overrides,
  };
}

export function fixtureAttempt(overrides: Partial<AttemptOutput> = {}): AttemptOutput {
  return {
    attemptNo: 1,
    agentStage: "implement",
    model: "gpt-5.6-terra",
    effort: "medium",
    state: "succeeded",
    failureKind: "",
    executionId: "opaque-execution-implement-1",
    usageState: "measured",
    measured: true,
    inputTokens: 12_400,
    cachedInputTokens: 8_000,
    outputTokens: 3_200,
    reasoningTokens: 1_100,
    startedAt: "2026-07-31T10:00:00Z",
    endedAt: "2026-07-31T10:47:00Z",
    result: { kind: "changes_pushed", head: "abc123" },
    hasTranscript: true,
    transcriptPath:
      "/v1/tickets/42/runs/019fb6a0-c159-7a3a-9067-eda7a63a8ac7/steps/1/attempts/1/transcript",
    ...overrides,
  };
}

export function fixtureUnmeasuredAttempt(overrides: Partial<AttemptOutput> = {}): AttemptOutput {
  return fixtureAttempt({
    attemptNo: 2,
    state: "running",
    usageState: "unknown",
    measured: false,
    inputTokens: null,
    cachedInputTokens: null,
    outputTokens: null,
    reasoningTokens: null,
    result: null,
    hasTranscript: false,
    transcriptPath: "",
    ...overrides,
  });
}

export function fixtureStep(overrides: Partial<StepOutput> = {}): StepOutput {
  const attempts = overrides.attempts ?? [fixtureAttempt()];
  return {
    ordinal: 1,
    kind: "implement",
    iteration: 1,
    reason: "initial",
    state: "completed",
    startedAt: attempts[0]?.startedAt ?? "2026-07-31T10:00:00Z",
    endedAt: attempts.at(-1)?.endedAt ?? null,
    result: { kind: "changes_pushed", head: "abc123" },
    attempts,
    usage: rollup(attempts),
    ...overrides,
  };
}

export function fixtureRun(overrides: Partial<RunOutput> = {}): RunOutput {
  const steps = overrides.steps ?? [fixtureStep()];
  const allAttempts = steps.flatMap((step) => step.attempts ?? []);
  return {
    id: "019fb6a0-c159-7a3a-9067-eda7a63a8ac7",
    ticketId: 42,
    startedAt: "2026-07-31T10:00:00Z",
    endedAt: null,
    outcome: "",
    failureKind: "",
    active: true,
    phase: steps.at(-1)?.kind ?? "",
    steps,
    usage: rollup(allAttempts),
    ...overrides,
  };
}

function rollup(attempts: AttemptOutput[]) {
  let inputTokens = 0;
  let cachedInputTokens = 0;
  let outputTokens = 0;
  let reasoningTokens = 0;
  let complete = true;
  for (const attempt of attempts) {
    if (!attempt.measured || attempt.inputTokens === null) {
      complete = false;
      continue;
    }
    inputTokens += attempt.inputTokens;
    cachedInputTokens += attempt.cachedInputTokens ?? 0;
    outputTokens += attempt.outputTokens ?? 0;
    reasoningTokens += attempt.reasoningTokens ?? 0;
  }
  return { inputTokens, cachedInputTokens, outputTokens, reasoningTokens, complete };
}
