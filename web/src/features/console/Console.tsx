import { useState } from "react";
import type { ConsoleResponse } from "@/api/generated";
import { StatePill } from "@/components/StatePill";

export type ConsoleState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; snapshot: ConsoleResponse }
  | { kind: "refetch-error"; message: string; snapshot: ConsoleResponse };

type Ticket = NonNullable<ConsoleResponse["tickets"]>[number];

function age(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  return `${Math.floor(seconds / 3600)}h`;
}

// updatedAgo renders a Ticket's last update as a short age against now. The
// console refetches on an interval, so "now" moving between renders is the
// intended behavior here, unlike duration.ts's completed-span rule.
function updatedAgo(iso: string): string {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (seconds < 86_400) return `${age(seconds)} ago`;
  return `${Math.floor(seconds / 86_400)}d ago`;
}

type SortKey = "createdAt" | "updatedAt";
type SortDir = "desc" | "asc";

function sortTickets(tickets: Ticket[], key: SortKey, dir: SortDir): Ticket[] {
  const sorted = [...tickets];
  sorted.sort((a, b) => {
    const delta = new Date(a[key]).getTime() - new Date(b[key]).getTime();
    if (delta !== 0) return dir === "asc" ? delta : -delta;
    return dir === "asc" ? a.id - b.id : b.id - a.id;
  });
  return sorted;
}

function SortHeader({
  label,
  column,
  sortKey,
  sortDir,
  onSort,
}: {
  label: string;
  column: SortKey;
  sortKey: SortKey;
  sortDir: SortDir;
  onSort: (column: SortKey) => void;
}) {
  const active = sortKey === column;
  return (
    <th
      scope="col"
      aria-sort={active ? (sortDir === "asc" ? "ascending" : "descending") : undefined}
    >
      <button type="button" className="sort-button" onClick={() => onSort(column)}>
        {label}
        {/* Always present so activating a sort never shifts the column. */}
        <span className="sort-arrow" aria-hidden="true">
          {active ? (sortDir === "asc" ? "↑" : "↓") : ""}
        </span>
      </button>
    </th>
  );
}

function TicketTable({ tickets }: { readonly tickets: Ticket[] }) {
  const [sortKey, setSortKey] = useState<SortKey>("createdAt");
  const [sortDir, setSortDir] = useState<SortDir>("desc");

  function onSort(column: SortKey) {
    if (sortKey !== column) {
      setSortKey(column);
      setSortDir("desc");
    } else {
      setSortDir(sortDir === "desc" ? "asc" : "desc");
    }
  }

  const sorted = sortTickets(tickets, sortKey, sortDir);
  return (
    <table className="ticket-table">
      <thead>
        <tr>
          <th scope="col">ID</th>
          <th scope="col">Title</th>
          <th scope="col">State</th>
          <SortHeader
            label="Created"
            column="createdAt"
            sortKey={sortKey}
            sortDir={sortDir}
            onSort={onSort}
          />
          <SortHeader
            label="Updated"
            column="updatedAt"
            sortKey={sortKey}
            sortDir={sortDir}
            onSort={onSort}
          />
        </tr>
      </thead>
      <tbody>
        {sorted.map((ticket) => (
          <tr
            id={`ticket-${ticket.id}`}
            className="ticket-row"
            key={ticket.id}
            onClick={() => {
              window.location.hash = `#/tickets/${ticket.id}`;
            }}
          >
            <td className="ticket-id">
              <a href={`#/tickets/${ticket.id}`}>#{ticket.id}</a>
            </td>
            <td>
              <a href={`#/tickets/${ticket.id}`}>{ticket.title}</a>
            </td>
            <td>
              <StatePill ticket={ticket} />
            </td>
            <td className="row-meta" title={new Date(ticket.createdAt).toLocaleString()}>
              {updatedAgo(ticket.createdAt)}
            </td>
            <td className="row-meta" title={new Date(ticket.updatedAt).toLocaleString()}>
              {updatedAgo(ticket.updatedAt)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Snapshot({
  snapshot,
  unconfirmedMessage,
}: {
  readonly snapshot: ConsoleResponse;
  readonly unconfirmedMessage?: string;
}) {
  const tickets = snapshot.tickets ?? [];
  return (
    <>
      {unconfirmedMessage && (
        <section className="factory-alert" role="alert">
          <strong>Refresh failed.</strong> Showing the last snapshot, which may no longer be
          current: {unconfirmedMessage}
        </section>
      )}
      <main className="console-grid">
        <section className="console-tickets" aria-labelledby="tickets-heading">
          <h2 id="tickets-heading">Tickets</h2>
          <p className="section-note">Every factory Ticket, newest first</p>
          {tickets.length === 0 ? (
            <p className="section-empty">No Tickets have been recorded.</p>
          ) : (
            <TicketTable tickets={tickets} />
          )}
        </section>
      </main>
    </>
  );
}

export function Console({ state }: { readonly state: ConsoleState }) {
  switch (state.kind) {
    case "loading":
      return (
        <main>
          <p>Loading factory state…</p>
        </main>
      );
    case "error":
      return (
        <main>
          <p role="alert">Could not reach the factory API: {state.message}</p>
        </main>
      );
    case "ready":
      return <Snapshot snapshot={state.snapshot} />;
    case "refetch-error":
      return <Snapshot snapshot={state.snapshot} unconfirmedMessage={state.message} />;
  }
}
