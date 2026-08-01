import type { AttemptOutput } from "@/api/generated";
import { formatDuration } from "@/features/ticket-detail/duration";
import { formatStructuredResult } from "@/features/ticket-detail/format";
import { TranscriptViewer } from "@/features/ticket-detail/TranscriptViewer";
import { transcriptDownloadUrl } from "@/features/ticket-detail/transcriptUrl";
import { formatAttemptUsage } from "@/features/ticket-detail/usage";

function resultLabel(state: string): string {
  return state === "running" ? "in progress" : state;
}

function resultPillClass(state: string): string {
  if (state === "succeeded") return "pill pill-done";
  if (state === "failed") return "pill pill-failed";
  return "pill pill-working";
}

export function AttemptRow({
  attempt,
  showAttemptNumber,
}: {
  attempt: AttemptOutput;
  showAttemptNumber: boolean;
}) {
  const result = formatStructuredResult(attempt.result);
  return (
    <div className="attempt-row" data-testid="attempt-row">
      <div className="row-line">
        {showAttemptNumber && <span>Attempt {attempt.attemptNo}</span>}
        <span className={resultPillClass(attempt.state)}>{resultLabel(attempt.state)}</span>
        <span className="row-meta">
          {attempt.agentStage} · {formatDuration(attempt.startedAt, attempt.endedAt)} ·{" "}
          {attempt.model} ({attempt.effort}) · usage {attempt.usageState}
        </span>
      </div>
      <p className="usage">Usage: {formatAttemptUsage(attempt)}</p>
      {attempt.hasTranscript ? (
        <p>
          <a href={transcriptDownloadUrl(attempt.transcriptPath)} download>
            Download transcript
          </a>{" "}
          <TranscriptViewer transcriptPath={attempt.transcriptPath} />
        </p>
      ) : (
        <p className="row-meta">No transcript stored for this attempt.</p>
      )}
      {attempt.executionId && <p className="row-meta">Execution: {attempt.executionId}</p>}
      {attempt.failureKind && <p className="row-meta">Failure: {attempt.failureKind}</p>}
      {result && (
        <details>
          <summary>Attempt Result</summary>
          <pre>{result}</pre>
        </details>
      )}
    </div>
  );
}
