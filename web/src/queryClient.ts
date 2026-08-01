import { QueryClient } from "@tanstack/react-query";

// The console has no live connection to the worker/API (ADR-0012 "Live updates:
// polling" chose polling over SSE/WebSockets deliberately: a Run is hours long,
// so three-second granularity is invisible and polling is the mechanism that
// cannot break). One default interval lives here rather than being repeated
// on every `useQuery` call, so tuning it is a one-line change.
const DEFAULT_POLL_INTERVAL_MS = 10_000;

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        refetchInterval: DEFAULT_POLL_INTERVAL_MS,
        refetchOnWindowFocus: true,
      },
    },
  });
}
