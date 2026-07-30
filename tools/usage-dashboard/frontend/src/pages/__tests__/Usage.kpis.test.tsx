import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { describe, it, expect, vi, beforeAll } from "vitest";
import Usage from "../Usage";

// Mock useRequestsInfinite so UsageRequestsTable doesn't blow up
vi.mock("@/api/hooks/useRequestsInfinite", () => ({
  useRequestsInfinite: () => ({
    data: { pages: [{ requests: [], next_cursor: null, limit: 50, models_filter: [], accounts_filter: [] }] },
    fetchNextPage: () => {},
    hasNextPage: false,
    isFetchingNextPage: false,
    isLoading: false,
  }),
}));

const mockSummary = {
  range: "24h", models_filter: [], accounts_filter: [],
  summary: { requests: 50, total_tokens: 25000, input_tokens: 15000,
             output_tokens: 10000, reasoning_tokens: 0, cached_tokens: 0,
             cache_read_tokens: 0, cache_creation_tokens: 0, failed: 2,
             success_latency_ms: 5000, success_requests: 48,
             success_ttft_ms: 1000, estimated_cost: 0.5,
             estimated_cost_currency: "USD" },
  accounts: [], models: [], hours: [], price_coverage: "partial",
};

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200, json: async () => mockSummary,
  })) as unknown as typeof fetch;
});

describe("Usage KPIs", () => {
  it("renders 4 KPI cards", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={qc}><MemoryRouter><Usage /></MemoryRouter></QueryClientProvider>);
    for (const label of ["总请求数", "总 Token", "总费用", "平均耗时"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    await waitFor(() => expect(screen.getByText("50")).toBeInTheDocument());
  });
});