// Deep links into the Temporal Web UI. The base URL and namespace mirror the
// worker's status renderer (internal/status/renderer.go runURL): same origin,
// same namespace, same workflow-id scheme (work.FactoryTicketWorkflowID).
const TEMPORAL_UI_BASE = "https://temporal-ui.worldwidewebb.co";
const TEMPORAL_NAMESPACE = "software-factory";

function factoryTicketWorkflowId(ticketId: number): string {
  return `factory-ticket-${ticketId}`;
}

/** The history page for one Run (Temporal run id) of a Ticket's workflow. */
export function temporalRunUrl(ticketId: number, runId: string): string {
  return `${TEMPORAL_UI_BASE}/namespaces/${TEMPORAL_NAMESPACE}/workflows/${factoryTicketWorkflowId(ticketId)}/${encodeURIComponent(runId)}/history`;
}

/** The workflow page for a Ticket — Temporal resolves the latest run. */
export function temporalTicketUrl(ticketId: number): string {
  return `${TEMPORAL_UI_BASE}/namespaces/${TEMPORAL_NAMESPACE}/workflows/${factoryTicketWorkflowId(ticketId)}`;
}
