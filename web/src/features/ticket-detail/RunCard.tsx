import type { RunOutput } from "@/api/generated";
import { TemporalLink } from "@/components/TemporalLink";
import { formatDuration } from "@/features/ticket-detail/duration";
import { formatMachineLabel } from "@/features/ticket-detail/format";
import { StepList } from "@/features/ticket-detail/StepList";
import { formatUsage } from "@/features/ticket-detail/usage";
import { temporalRunUrl } from "@/lib/temporal";

// runStatus renders outcome/failureKind as one short phrase. Both are empty
// strings until the Run ends — a Run in flight has neither, and that is a
// distinct, renderable state, not an omission to guess at.
function runStatus(run: RunOutput): string {
  if (run.active) return "running";
  if (run.outcome === "failed" && run.failureKind !== "") return `failed (${run.failureKind})`;
  return run.outcome || "ended";
}

function runPillClass(run: RunOutput): string {
  if (run.active) return "pill pill-working";
  if (run.outcome === "failed" || run.outcome === "exhausted") return "pill pill-failed";
  if (run.outcome === "succeeded") return "pill pill-done";
  return "pill pill-blocked";
}

export function RunCard({ run }: { run: RunOutput }) {
  return (
    <article className="run-card">
      <header>
        <strong>Run {run.id}</strong>
        <span className={runPillClass(run)}>{runStatus(run)}</span>
        {run.endedAt !== null && (
          <span className="row-meta">{formatDuration(run.startedAt, run.endedAt)}</span>
        )}
        <span className="spacer" />
        <TemporalLink
          href={temporalRunUrl(run.ticketId, run.id)}
          label="Temporal execution (technical retries)"
        />
      </header>
      <p className="run-phase">Phase: {formatMachineLabel(run.phase) || "Not started"}</p>
      {run.confirmedMerge && (
        <p className="confirmed-merge">
          Confirmed Merge: <code>{run.confirmedMerge.mergeSha}</code>{" "}
          <span className="row-meta">(reviewed head {run.confirmedMerge.reviewedHead})</span>
        </p>
      )}
      <p className="usage">Usage: {formatUsage(run.usage)}</p>
      <StepList steps={run.steps ?? []} />
    </article>
  );
}
