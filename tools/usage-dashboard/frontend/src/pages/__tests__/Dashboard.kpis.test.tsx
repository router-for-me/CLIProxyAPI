import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { describe, it, expect, vi, beforeAll } from "vitest";
import Dashboard from "../Dashboard";

const mockSummary = {
  range: "24h", models_filter: [], accounts_filter: [],
  summary: {
    requests: 100, total_tokens: 50000, input_tokens: 30000,
    output_tokens: 20000, reasoning_tokens: 0, cached_tokens: 0,
    cache_read_tokens: 0, cache_creation_tokens: 0, failed: 5,
    success_latency_ms: 12000, success_requests: 95, success_ttft_ms: 2400,
    estimated_cost: 1.23, estimated_cost_currency: "USD",
  },
  accounts: [], models: [], hours: [], price_coverage: "partial",
};

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200, json: async () => mockSummary,
  })) as unknown as typeof fetch;
});

function renderDashboard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Dashboard KPIs", () => {
  it("renders 8 KPI cards after data loads", async () => {
    renderDashboard();
    await waitFor(() => expect(screen.getByText("100")).toBeInTheDocument());
    for (const label of ["API 密钥", "账号数", "今日请求", "活跃密钥",
                         "今日 Token", "总 Token", "成功率", "平均响应"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });

  it("shows total tokens value formatted", async () => {
    renderDashboard();
    await waitFor(() => expect(screen.getByText(/50[,.]?0?K|50,?000/i)).toBeInTheDocument());
  });
});