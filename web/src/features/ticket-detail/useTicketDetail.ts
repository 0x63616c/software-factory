import type { RunOutput, TicketResponse } from "@/api/generated";
import { useGetV1TicketsByTicketId, useGetV1TicketsByTicketIdRuns } from "@/api/generated";

// A discriminated union (docs/writing-scalable-typescript, "01-impossible-states")
// rather than two independent query objects' isLoading/isError/data flags,
// which can disagree with each other (the ticket loaded but its runs errored,
// or vice versa) and force a caller to guess which combination is real.
export type TicketDetailState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; ticket: TicketResponse; runs: RunOutput[] };

// useTicketDetail combines the Ticket and its Runs into one state for
// #556's detail view. Both requests poll independently (queryClient's shared
// default interval); this hook only ever exposes the combined state.
export function useTicketDetail(ticketId: number): TicketDetailState {
  const ticket = useGetV1TicketsByTicketId(ticketId);
  const runs = useGetV1TicketsByTicketIdRuns(ticketId);

  if (ticket.isPending || runs.isPending) return { kind: "loading" };
  if (ticket.isError) return { kind: "error", message: errorMessage(ticket.error) };
  if (runs.isError) return { kind: "error", message: errorMessage(runs.error) };

  return { kind: "ready", ticket: ticket.data.data, runs: runs.data.data.runs ?? [] };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "unknown error";
}
