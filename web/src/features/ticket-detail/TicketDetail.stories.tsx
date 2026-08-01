import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  fixtureAttempt,
  fixtureRun,
  fixtureStep,
  fixtureTicket,
  fixtureUnmeasuredAttempt,
} from "@/features/ticket-detail/fixtures";
import { TicketDetail } from "@/features/ticket-detail/TicketDetail";

const meta = {
  title: "TicketDetail/TicketDetail",
  component: TicketDetail,
  tags: ["autodocs"],
} satisfies Meta<typeof TicketDetail>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Loading: Story = {
  args: { state: { kind: "loading" } },
};

export const ErrorState: Story = {
  args: { state: { kind: "error", message: "Network Error" } },
};

// #556 acceptance: "a single-turn run" — one Step, one Attempt, quiet
// rendering (no attempt count badge).
export const SingleTurnRun: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [fixtureRun()],
    },
  },
};

// Target projection: semantic repetition belongs to ordered Steps rather than
// being inferred from Temporal activity attempts.
export const MultiTurnRun: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [
            fixtureStep({ ordinal: 1, kind: "plan", iteration: 1 }),
            fixtureStep({ ordinal: 2, kind: "implement", iteration: 1 }),
            fixtureStep({
              ordinal: 3,
              kind: "implement",
              iteration: 2,
              reason: "review_findings",
              attempts: [
                fixtureAttempt({
                  attemptNo: 1,
                  startedAt: "2026-07-31T11:00:00Z",
                  endedAt: "2026-07-31T11:32:00Z",
                }),
              ],
            }),
            fixtureStep({
              ordinal: 4,
              kind: "review",
              iteration: 1,
            }),
          ],
        }),
      ],
    },
  },
};

// Multiple Agent Attempts are explicit workflow-authorized executions. Native
// Temporal activity tries never appear as extra rows in this story.
export const StepWithSeveralAttempts: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [
            fixtureStep({
              attempts: [
                fixtureAttempt({
                  attemptNo: 1,
                  state: "failed",
                  failureKind: "agent_unrecoverable",
                  result: { kind: "provider_failed" },
                  endedAt: "2026-07-31T10:05:00Z",
                }),
                fixtureAttempt({
                  attemptNo: 2,
                  startedAt: "2026-07-31T10:05:00Z",
                  endedAt: "2026-07-31T10:52:00Z",
                }),
              ],
            }),
          ],
        }),
      ],
    },
  },
};

// Target infrastructure Steps are first-class history even though they have
// no Agent Attempt and spend no model tokens.
export const InfrastructureStepsAndActivePhase: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket({ state: "active" }),
      runs: [
        fixtureRun({
          phase: "await_ci",
          steps: [
            fixtureStep({
              ordinal: 1,
              kind: "clone_repository",
              iteration: 0,
              reason: "",
              attempts: [],
              result: { kind: "cloned" },
            }),
            fixtureStep({
              ordinal: 2,
              kind: "await_ci",
              iteration: 1,
              reason: "pull_request_updated",
              state: "running",
              endedAt: null,
              attempts: [],
              result: null,
            }),
          ],
        }),
      ],
    },
  },
};

// Confirmed Merge evidence is shown from the terminal Run record rather than
// inferred from a webhook or a merely closed pull request.
export const ConfirmedMerge: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket({ state: "done", ready: false }),
      runs: [
        fixtureRun({
          active: false,
          endedAt: "2026-07-31T11:00:00Z",
          outcome: "succeeded",
          phase: "merge_pull_request",
          confirmedMerge: { reviewedHead: "abc123", mergeSha: "def456" },
        }),
      ],
    },
  },
};

// An Attempt whose provider did not report usage renders unknown, never zero,
// and flags the Run's rollup incomplete.
export const UnmeasuredAttempt: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [
            fixtureStep({
              attempts: [
                fixtureAttempt({ attemptNo: 1 }),
                fixtureUnmeasuredAttempt({ attemptNo: 2 }),
              ],
            }),
          ],
        }),
      ],
    },
  },
};

// #556 acceptance: "a run with no transcript" — the Attempt says so plainly
// rather than rendering an empty pane.
export const RunWithNoTranscript: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [fixtureStep({ attempts: [fixtureAttempt({ hasTranscript: false })] })],
        }),
      ],
    },
  },
};

// #556 acceptance: "a failed run".
export const FailedRun: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket({ state: "failed", ready: false }),
      runs: [
        fixtureRun({
          endedAt: "2026-07-31T11:00:00Z",
          outcome: "failed",
          failureKind: "github_unavailable",
        }),
      ],
    },
  },
};

// #556 acceptance: "a Ticket with several Runs" — the retry-after-failure
// case, most recent first.
export const TicketWithSeveralRuns: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({ id: "run-2", startedAt: "2026-07-31T12:00:00Z" }),
        fixtureRun({
          id: "run-1",
          startedAt: "2026-07-31T09:00:00Z",
          endedAt: "2026-07-31T09:40:00Z",
          outcome: "failed",
          failureKind: "infrastructure",
        }),
      ],
    },
  },
};

// #556 acceptance: dependencies render, linked, with their states.
export const WithDependencies: Story = {
  args: {
    state: {
      kind: "ready",
      ticket: fixtureTicket({
        blockers: [
          {
            id: 40,
            title: "Ticket detail API",
            state: "done",
            ready: false,
            createdAt: "2026-07-25T00:00:00Z",
            updatedAt: "2026-07-29T00:00:00Z",
          },
        ],
        blocks: [
          {
            id: 58,
            title: "Ticket-backed dispatcher",
            state: "open",
            ready: false,
            createdAt: "2026-07-28T00:00:00Z",
            updatedAt: "2026-07-28T00:00:00Z",
          },
        ],
      }),
      runs: [fixtureRun()],
    },
  },
};
