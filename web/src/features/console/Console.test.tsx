import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Console } from "@/features/console/Console";

describe("Console", () => {
  it("shows only tickets, newest first", () => {
    render(
      <Console
        state={{
          kind: "ready",
          snapshot: {
            tickets: [
              {
                id: 1,
                title: "Older ticket",
                state: "failed",
                ready: false,
                createdAt: "2026-07-30T12:00:00Z",
                updatedAt: "2026-07-31T12:00:00Z",
              },
              {
                id: 2,
                title: "Newer ticket",
                state: "open",
                ready: false,
                createdAt: "2026-07-31T12:00:00Z",
                updatedAt: "2026-07-31T12:00:00Z",
              },
            ],
          },
        }}
      />,
    );

    expect(screen.queryByText("In flight")).not.toBeInTheDocument();
    expect(screen.queryByText("Waiting on Tickets:")).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: /Created/ })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    expect(
      screen.getAllByRole("link", { name: /ticket$/ }).map((link) => link.textContent),
    ).toEqual(["Newer ticket", "Older ticket"]);
  });
});
