import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ConsoleResponse } from "@/api/generated";
import { Console } from "@/features/console/Console";

const snapshot = (tickets: ConsoleResponse["tickets"] = []): ConsoleResponse => ({ tickets });

const ticket = (id: number, title: string, state: string, createdAt: string) => ({
  id,
  title,
  state,
  ready: false,
  createdAt,
  updatedAt: createdAt,
});

const meta = { component: Console, tags: ["autodocs"] } satisfies Meta<typeof Console>;
export default meta;
type Story = StoryObj<typeof meta>;

export const NoTickets: Story = { args: { state: { kind: "ready", snapshot: snapshot() } } };

export const TicketsNewestFirst: Story = {
  args: {
    state: {
      kind: "ready",
      snapshot: snapshot([
        ticket(1, "Older Ticket", "done", "2026-07-30T10:00:00Z"),
        ticket(2, "Newest Ticket", "active", "2026-07-31T10:00:00Z"),
        ticket(3, "Middle Ticket", "open", "2026-07-30T18:00:00Z"),
      ]),
    },
  },
};

export const FailedRefetch: Story = {
  args: { state: { kind: "refetch-error", message: "Network Error", snapshot: snapshot() } },
};
