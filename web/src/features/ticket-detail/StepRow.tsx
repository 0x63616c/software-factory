import type { StepOutput } from "@/api/generated";
import { AttemptRow } from "@/features/ticket-detail/AttemptRow";
import { formatDuration } from "@/features/ticket-detail/duration";
import { formatMachineLabel, formatStructuredResult } from "@/features/ticket-detail/format";
import { formatUsage } from "@/features/ticket-detail/usage";

// StepRow renders the durable ordinal lifecycle. Agent Attempts are semantic
// executions authorized by the workflow; Temporal's technical retries remain
// behind the Run-level Temporal link and never inflate this count.
export function StepRow({ step }: { step: StepOutput }) {
  const attempts = step.attempts ?? [];
  const result = formatStructuredResult(step.result);
  return (
    <div className="step-row" data-testid="step-row">
      <div className="row-line">
        <strong>
          #{step.ordinal} {formatMachineLabel(step.kind)}
        </strong>
        <span
          className={
            step.state === "running"
              ? "pill pill-working"
              : step.state === "failed"
                ? "pill pill-failed"
                : "pill pill-done"
          }
        >
          {step.state}
        </span>
        <span className="row-meta">
          {step.iteration > 0 && `· iteration ${step.iteration} · `}
          {formatDuration(step.startedAt, step.endedAt)}
          {attempts.length > 1 && ` · ${attempts.length} Agent Attempts`}
        </span>
      </div>
      {step.reason && <p className="row-meta">Reason: {formatMachineLabel(step.reason)}</p>}
      <p className="usage">Usage: {formatUsage(step.usage)}</p>
      {attempts.length === 0 ? (
        <p className="row-meta">No Agent Attempt (infrastructure Step).</p>
      ) : (
        <ul className="attempt-list">
          {attempts.map((attempt) => (
            <li key={attempt.attemptNo}>
              <AttemptRow attempt={attempt} showAttemptNumber={attempts.length > 1} />
            </li>
          ))}
        </ul>
      )}
      {result && (
        <details>
          <summary>Step Result</summary>
          <pre>{result}</pre>
        </details>
      )}
    </div>
  );
}
