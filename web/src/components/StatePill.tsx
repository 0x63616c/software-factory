// One pill for every place a Ticket state appears — console rows, blockers,
// detail header, dependencies — so a state always looks the same.

// A Ticket's display status folds `state` and (for open Tickets) `ready`
// into one word the pill can carry.
function ticketStatus(ticket: { state: string; ready: boolean }): string {
  if (ticket.state === "open") return ticket.ready ? "ready" : "blocked";
  return ticket.state;
}

const PILL_CLASS: Record<string, string> = {
  active: "pill pill-working",
  done: "pill pill-done",
  failed: "pill pill-failed",
  ready: "pill pill-open",
  blocked: "pill pill-blocked",
};

export function StatePill({ ticket }: { readonly ticket: { state: string; ready: boolean } }) {
  const status = ticketStatus(ticket);
  return <span className={PILL_CLASS[status] ?? "pill"}>{status}</span>;
}
