import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ErrorsTable from "../ErrorsTable";

const mockErrors = {
  range: "24h", models_filter: [], accounts_filter: [],
  errors: [
    { fail_status: 429, model: "gpt-4", count: 12, percentage: 60.0, last_seen: "2026-01-01T00:00:00Z" },
    { fail_status: 500, model: "claude", count: 8, percentage: 40.0, last_seen: "2026-01-01T01:00:00Z" },
  ],
};

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200, json: async () => mockErrors,
  })) as unknown as typeof fetch;
});

describe("ErrorsTable", () => {
  it("renders aggregated error rows", async () => {
    render(<QueryClientProvider client={new QueryClient()}><ErrorsTable /></QueryClientProvider>);
    await waitFor(() => expect(screen.getAllByRole("row").length).toBe(3)); // header + 2
    expect(screen.getByText("429")).toBeInTheDocument();
    expect(screen.getByText(/60.0%/)).toBeInTheDocument();
  });

  it("clicking a row switches to Usage tab with filter", async () => {
    const user = userEvent.setup();
    const onDrillDown = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><ErrorsTable onDrillDown={onDrillDown} /></QueryClientProvider>);
    await waitFor(() => expect(screen.getByText("429")).toBeInTheDocument());
    await user.click(screen.getByText("429"));
    expect(onDrillDown).toHaveBeenCalledWith({ model: "gpt-4" });
  });

  it("shows empty state when no errors", async () => {
    global.fetch = vi.fn(async () => ({
      ok: true, status: 200, json: async () => ({ ...mockErrors, errors: [] }),
    })) as unknown as typeof fetch;
    render(<QueryClientProvider client={new QueryClient()}><ErrorsTable /></QueryClientProvider>);
    await waitFor(() => expect(screen.getByText(/暂无数据/i)).toBeInTheDocument());
  });
});