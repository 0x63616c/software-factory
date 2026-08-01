import { useSyncExternalStore } from "react";
import { AppHeader } from "@/components/AppHeader";
import { Console } from "@/features/console/Console";
import { useConsole } from "@/features/console/useConsole";
import { TicketDetail } from "@/features/ticket-detail/TicketDetail";
import { useTicketDetail } from "@/features/ticket-detail/useTicketDetail";

// The console is two screens, not an app that needs a routing library: a
// hash is enough to make a Ticket's detail view a real, bookmarkable
// location (`#/tickets/42`) without adding react-router for two routes.
const TICKET_HASH = /^#\/tickets\/(\d+)$/;

function useHash(): string {
  return useSyncExternalStore(
    (onChange) => {
      window.addEventListener("hashchange", onChange);
      return () => window.removeEventListener("hashchange", onChange);
    },
    () => window.location.hash,
    () => "",
  );
}

function ticketIdFromHash(hash: string): number | null {
  const match = TICKET_HASH.exec(hash);
  return match ? Number(match[1]) : null;
}

// Each route owns the one data hook it needs, so App itself never calls a
// hook conditionally — it only ever chooses which of these to mount.
function ConsoleRoute() {
  return <Console state={useConsole()} />;
}

function TicketDetailRoute({ ticketId }: { readonly ticketId: number }) {
  return (
    <main className="ticket-page">
      <p>
        <a className="back-link" href="#/">
          ← Back to console
        </a>
      </p>
      <TicketDetail state={useTicketDetail(ticketId)} />
    </main>
  );
}

export function App() {
  const ticketId = ticketIdFromHash(useHash());
  return (
    <>
      <AppHeader />
      {ticketId !== null ? <TicketDetailRoute ticketId={ticketId} /> : <ConsoleRoute />}
    </>
  );
}
