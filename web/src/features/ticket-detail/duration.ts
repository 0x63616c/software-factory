// formatDuration renders the gap between two RFC3339 timestamps as a short
// human duration ("47m", "1h 12m"), or "running" while endedAt is still null
// — never a duration against the current wall clock, which would tick on
// every render and misrepresent a value the API has not actually reported.
export function formatDuration(startedAt: string, endedAt: string | null): string {
  if (endedAt === null) return "running";
  const ms = new Date(endedAt).getTime() - new Date(startedAt).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "unknown";

  const totalMinutes = Math.round(ms / 60_000);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours === 0) return `${minutes}m`;
  return `${hours}h ${minutes}m`;
}
