import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import RecentUsageTable from "../RecentUsageTable";
import { DetailDrawer } from "../DetailDrawer";

const mockRequests = {
  requests: Array.from({ length: 12 }, (_, i) => ({
    id: i,
    request_id: `r${i}`,
    timestamp: "2026-01-01T00:00:00Z",
    account: `acc${i}`,
    model: `m${i % 3}`,
    endpoint: "e",
    provider: "p",
    total_tokens: 100 * i,
    latency_ms: 100,
    failed: 0,
    fail_status: 0,
    input_tokens: 60,
    output_tokens: 40,
    reasoning_tokens: 0,
    cached_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    ttft_ms: 50,
    alias: null,
  })),
  next_cursor: null,
  range: "24h",
  models_filter: [],
  accounts_filter: [],
};

beforeEach(() => {
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => mockRequests,
  })) as unknown as typeof fetch;
});

describe("RecentUsageTable", () => {
  it("renders 12 rows", async () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <RecentUsageTable />
      </QueryClientProvider>,
    );
    expect(await screen.findAllByRole("row")).toHaveLength(13); // header + 12
  });

  it("aligns each cell under its matching header column", async () => {
    // Regression: the row component must render a leading timestamp cell so
    // that account/model/token/latency/status do not shift one column left.
    render(
      <QueryClientProvider client={new QueryClient()}>
        <RecentUsageTable />
      </QueryClientProvider>,
    );
    const rows = await screen.findAllByRole("row");
    // Header row
    const headerCells = (rows[0] as HTMLElement).querySelectorAll("th");
    // First data row
    const firstRowCells = (rows[1] as HTMLElement).querySelectorAll("td");
    expect(headerCells.length).toBe(firstRowCells.length);
    // Spot-check: account header pairs with account cell, not time cell.
    const headerTexts = Array.from(headerCells).map((c) => c.textContent?.trim() ?? "");
    const cellTexts = Array.from(firstRowCells).map((c) => c.textContent?.trim() ?? "");
    const accountIdx = headerTexts.indexOf("账号");
    expect(accountIdx).toBeGreaterThan(-1);
    expect(cellTexts[accountIdx]).toBe("acc0");
    // Time header pairs with a timestamp cell, not account data.
    const timeIdx = headerTexts.indexOf("时间");
    expect(timeIdx).toBeGreaterThan(-1);
    expect(cellTexts[timeIdx]).toMatch(/2026/);
  });

  it("clicking a row opens the detail drawer", async () => {
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={new QueryClient()}>
        <RecentUsageTable />
        <DetailDrawer />
      </QueryClientProvider>,
    );
    const rows = await screen.findAllByRole("row");
    await user.click(rows[1]!);
    // Drawer body shows the request_id
    expect(await screen.findByText(/r0/)).toBeInTheDocument();
  });
});