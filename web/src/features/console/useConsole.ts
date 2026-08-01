import { useGetV1Console } from "@/api/generated";
import type { ConsoleState } from "@/features/console/Console";

function messageFor(error: unknown): string {
  return error instanceof Error ? error.message : "unknown error";
}

export function useConsole(): ConsoleState {
  const query = useGetV1Console();
  if (query.isPending) return { kind: "loading" };
  if (query.isError && query.data)
    return { kind: "refetch-error", message: messageFor(query.error), snapshot: query.data.data };
  if (query.isError) return { kind: "error", message: messageFor(query.error) };
  return { kind: "ready", snapshot: query.data.data };
}
