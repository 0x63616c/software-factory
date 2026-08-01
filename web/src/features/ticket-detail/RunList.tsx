import type { RunOutput } from "@/api/generated";
import { RunCard } from "@/features/ticket-detail/RunCard";

// RunList renders a Ticket's Runs, most recent first — the order the API
// already returns them in.
export function RunList({ runs }: { runs: RunOutput[] }) {
  if (runs.length === 0) return <p className="section-empty">No runs yet.</p>;
  return (
    <ul className="run-list" data-testid="run-list">
      {runs.map((run) => (
        <li key={run.id}>
          <RunCard run={run} />
        </li>
      ))}
    </ul>
  );
}
