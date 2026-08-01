import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { TicketSummary } from "@/api/generated";
import { StatePill } from "@/components/StatePill";
import { TemporalLink } from "@/components/TemporalLink";
import { RunList } from "@/features/ticket-detail/RunList";
import type { TicketDetailState } from "@/features/ticket-detail/useTicketDetail";
import { temporalTicketUrl } from "@/lib/temporal";

// Presentational only, driven by the discriminated union useTicketDetail
// produces — Storybook and tests exercise every state without a network.
export function TicketDetail({ state }: { state: TicketDetailState }) {
  switch (state.kind) {
    case "loading":
      return <p data-testid="ticket-detail">Loading ticket…</p>;
    case "error":
      return (
        <p data-testid="ticket-detail" role="alert">
          Could not reach the API: {state.message}
        </p>
      );
    case "ready":
      return (
        <article data-testid="ticket-detail">
          <header className="ticket-head">
            <h1>
              #{state.ticket.id} {state.ticket.title}
            </h1>
            <StatePill ticket={state.ticket} />
            <span className="spacer" />
            <TemporalLink href={temporalTicketUrl(state.ticket.id)} />
          </header>
          {/* react-markdown emits no raw HTML by default, so a Ticket body
              can never inject markup — GFM covers the task lists and tables
              GitHub-style bodies actually use. */}
          <div className="ticket-body">
            <Markdown remarkPlugins={[remarkGfm]}>{state.ticket.body}</Markdown>
          </div>
          <DependencyList title="Blocked by" tickets={state.ticket.blockers} />
          <DependencyList title="Blocks" tickets={state.ticket.blocks} />
          <section>
            <h2>Runs</h2>
            <RunList runs={state.runs} />
          </section>
        </article>
      );
  }
}

function DependencyList({ title, tickets }: { title: string; tickets: TicketSummary[] | null }) {
  const list = tickets ?? [];
  if (list.length === 0) return null;
  return (
    <section className="ticket-deps">
      <h3>{title}</h3>
      <ul>
        {list.map((ticket) => (
          <li key={ticket.id}>
            <a href={`#/tickets/${ticket.id}`}>
              #{ticket.id} {ticket.title}
            </a>{" "}
            <StatePill ticket={ticket} />
          </li>
        ))}
      </ul>
    </section>
  );
}
