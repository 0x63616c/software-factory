import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import axios from "axios";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "@/App";

// Only the transport is a test double. Everything above it , the generated
// hook, the query wiring, the App component , is the real production chain
// (#553 acceptance 4: Go type -> OpenAPI -> Orval -> React, no hand-written
// fetch, no fixture endpoint of our own).
vi.mock("axios");

function renderWithClient() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.resetAllMocks();
  window.location.hash = "";
});

describe("App", () => {
  it("renders Tickets returned by the generated client", async () => {
    vi.mocked(axios.get).mockResolvedValueOnce({
      data: {
        tickets: [
          {
            id: 1,
            title: "Factory Ticket",
            state: "open",
            ready: true,
            createdAt: "2026-07-31T12:00:00Z",
            updatedAt: "2026-07-31T12:00:00Z",
          },
        ],
      },
    });

    renderWithClient();

    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Factory Ticket" })).toBeInTheDocument();
    });
    expect(axios.get).toHaveBeenCalledWith("/v1/console", expect.anything());
  });

  it("shows an honest error instead of fake data when the API is unreachable", async () => {
    vi.mocked(axios.get).mockRejectedValueOnce(new Error("Network Error"));

    renderWithClient();

    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("Network Error");
    });
  });

  // #556: the Ticket detail view is otherwise unreachable dead code without
  // this — a hash is enough to make it a real, bookmarkable location.
  it("shows the Ticket detail view for a #/tickets/<id> hash, not the console", async () => {
    window.location.hash = "#/tickets/42";
    vi.mocked(axios.get).mockImplementation((url: string) => {
      if (url === "/v1/tickets/42") {
        return Promise.resolve({
          data: {
            id: 42,
            title: "Console ticket detail",
            body: "",
            state: "active",
            ready: false,
            createdAt: "2026-07-31T12:00:00Z",
            updatedAt: "2026-07-31T12:00:00Z",
            blockers: [],
            blocks: [],
          },
        });
      }
      if (url === "/v1/tickets/42/runs") {
        return Promise.resolve({ data: { runs: [] } });
      }
      return Promise.reject(new Error(`unexpected GET ${url}`));
    });

    renderWithClient();

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /42 Console ticket detail/ })).toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: /Back to console/ })).toHaveAttribute("href", "#/");
    // The console *page* still must not render on a ticket hash.
    expect(screen.queryByText("Every factory Ticket, newest first")).not.toBeInTheDocument();
  });
});
