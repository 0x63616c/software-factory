export function formatMachineLabel(value: string): string {
  return value
    .split("_")
    .map((part) => (part === "ci" ? "CI" : `${part.slice(0, 1).toUpperCase()}${part.slice(1)}`))
    .join(" ");
}

export function formatStructuredResult(result: unknown): string | null {
  if (result === null || result === undefined) return null;
  return JSON.stringify(result, null, 2);
}
