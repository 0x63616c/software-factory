import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  fixtureAttempt,
  fixtureRun,
  fixtureStep,
  fixtureTicket,
  fixtureUnmeasuredAttempt,
} from "@/features/ticket-detail/fixtures";
import { TicketDetail } from "@/features/ticket-detail/TicketDetail";
import type { TicketDetailState } from "@/features/ticket-detail/useTicketDetail";

// Every render needs a QueryClientProvider: AttemptRow renders
// TranscriptViewer for any Attempt with hasTranscript, and that component
// calls a generated react-query hook (collapsed or not) just to construct.
function renderReady(state: TicketDetailState) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <TicketDetail state={state} />
    </QueryClientProvider>,
  );
}

describe("TicketDetail", () => {
  it("renders a loading state", () => {
    render(<TicketDetail state={{ kind: "loading" }} />);
    expect(screen.getByTestId("ticket-detail")).toHaveTextContent("Loading ticket");
  });

  it("renders an error without pretending a ticket loaded", () => {
    render(<TicketDetail state={{ kind: "error", message: "Network Error" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Network Error");
  });

  it("renders the ordered Step identity and semantic iteration", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [fixtureRun({ steps: [fixtureStep({ ordinal: 7, kind: "implement", iteration: 3 })] })],
    });
    expect(screen.getByText("#7 Implement")).toBeInTheDocument();
    expect(screen.getByTestId("step-row")).toHaveTextContent(/iteration 3/);
  });

  it("renders an infrastructure Step without inventing an Agent Attempt", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket({ state: "active" }),
      runs: [
        fixtureRun({
          phase: "await_ci",
          steps: [
            fixtureStep({
              kind: "await_ci",
              state: "running",
              attempts: [],
              endedAt: null,
              result: null,
            }),
          ],
        }),
      ],
    });
    expect(screen.getByTestId("step-row")).toHaveTextContent("No Agent Attempt");
    expect(screen.queryByTestId("attempt-row")).not.toBeInTheDocument();
  });

  it("does not show an attempt count for a Step with only one Attempt", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [fixtureRun({ steps: [fixtureStep({ attempts: [fixtureAttempt()] })] })],
    });
    expect(screen.getByTestId("step-row")).not.toHaveTextContent(/Agent Attempts/);
  });

  it("shows the attempt count only when a Step ran more than once", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [
            fixtureStep({
              attempts: [
                fixtureAttempt({ attemptNo: 1, state: "failed", result: { kind: "failed" } }),
                fixtureAttempt({ attemptNo: 2 }),
              ],
            }),
          ],
        }),
      ],
    });
    expect(screen.getByTestId("step-row")).toHaveTextContent("2 Agent Attempts");
  });

  it("renders an unmeasured attempt's usage as unknown, never 0", () => {
    renderReady({
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
    });
    const rows = screen.getAllByTestId("attempt-row");
    const unmeasuredRow = rows[1];
    expect(unmeasuredRow).toHaveTextContent("unknown");
    expect(unmeasuredRow).not.toHaveTextContent(/Usage:\s*0/);
  });

  it("flags a Run's usage as incomplete when it contains an unmeasured attempt", () => {
    renderReady({
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
    });
    // The Run-level usage line (first "Usage:" in the card, before the Step's
    // own usage line) must say incomplete rather than a confident total.
    const usageLines = screen.getAllByText(/^Usage:/);
    expect(usageLines[0]).toHaveTextContent("incomplete");
  });

  it("says an attempt has no transcript rather than rendering an empty pane", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [fixtureStep({ attempts: [fixtureAttempt({ hasTranscript: false })] })],
        }),
      ],
    });
    expect(screen.getByText(/No transcript stored/)).toBeInTheDocument();
    expect(screen.queryByText("Download transcript")).not.toBeInTheDocument();
  });

  it("offers a download link for an attempt with a transcript", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          steps: [fixtureStep({ attempts: [fixtureAttempt({ hasTranscript: true })] })],
        }),
      ],
    });
    expect(screen.getByText("Download transcript")).toHaveAttribute(
      "href",
      "/api/v1/tickets/42/runs/019fb6a0-c159-7a3a-9067-eda7a63a8ac7/steps/1/attempts/1/transcript",
    );
  });

  it("renders current phase and immutable Confirmed Merge evidence", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket({ state: "done" }),
      runs: [
        fixtureRun({
          active: false,
          endedAt: "2026-07-31T11:00:00Z",
          outcome: "succeeded",
          phase: "merge_pull_request",
          confirmedMerge: { reviewedHead: "head-sha", mergeSha: "merge-sha" },
        }),
      ],
    });
    expect(screen.getByText(/Phase:\s*Merge Pull Request/)).toBeInTheDocument();
    expect(screen.getByText("merge-sha")).toBeInTheDocument();
    expect(screen.getByText(/reviewed head head-sha/)).toBeInTheDocument();
  });

  it("renders the Ticket's dependencies with their states", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket({
        blockers: [
          { id: 40, title: "Upstream", state: "done", ready: false, createdAt: "", updatedAt: "" },
        ],
        blocks: [
          {
            id: 58,
            title: "Downstream",
            state: "open",
            ready: false,
            createdAt: "",
            updatedAt: "",
          },
        ],
      }),
      runs: [],
    });
    expect(screen.getByText(/Upstream/)).toBeInTheDocument();
    expect(screen.getByText(/Downstream/)).toBeInTheDocument();
  });

  it("never renders a monetary value anywhere on the page", () => {
    renderReady({
      kind: "ready",
      ticket: fixtureTicket(),
      runs: [
        fixtureRun({
          outcome: "failed",
          failureKind: "github_unavailable",
          endedAt: "2026-07-31T11:00:00Z",
        }),
      ],
    });
    expect(document.body).not.toHaveTextContent(/\$|USD|cost/i);
  });
});
