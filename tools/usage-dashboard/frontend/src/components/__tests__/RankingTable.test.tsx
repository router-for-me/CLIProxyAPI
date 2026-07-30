import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import RankingTable from "../RankingTable";

const defaultResponse = {
  range: "24h",
  models_filter: [],
  accounts_filter: [],
  summary: {
    requests: 100,
    total_tokens: 50000,
    input_tokens: 30000,
    output_tokens: 20000,
    reasoning_tokens: 0,
    cached_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    failed: 1,
    success_latency_ms: 5000,
    success_requests: 99,
    success_ttft_ms: 1000,
    estimated_cost: 0,
    estimated_cost_currency: "USD",
  },
  accounts: [
    {
      account: "Alice",
      requests: 60,
      total_tokens: 30000,
      input_tokens: 18000,
      output_tokens: 12000,
      reasoning_tokens: 0,
      failed: 1,
    },
    {
      account: "Bob",
      requests: 40,
      total_tokens: 20000,
      input_tokens: 12000,
      output_tokens: 8000,
      reasoning_tokens: 0,
      failed: 0,
    },
  ],
  models: [],
  hours: [],
  price_coverage: "empty",
};

beforeEach(() => {
  global.fetch = vi.fn() as unknown as typeof fetch;
});

describe("RankingTable", () => {
  it("renders accounts sorted by token volume", async () => {
    vi.mocked(global.fetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => defaultResponse,
    } as Response);

    render(
      <QueryClientProvider client={new QueryClient()}>
        <RankingTable />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Alice")).toBeInTheDocument());
    const rows = screen.getAllByRole("row");
    // row 0 = header, row 1 = Alice (30K tokens), row 2 = Bob (20K tokens)
    expect(rows[1]).toHaveTextContent("Alice");
    expect(rows[2]).toHaveTextContent("Bob");
  });

  it("clicking a row fires onSelect with the account hash", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    vi.mocked(global.fetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => defaultResponse,
    } as Response);

    render(
      <QueryClientProvider client={new QueryClient()}>
        <RankingTable onSelect={onSelect} />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Alice")).toBeInTheDocument());
    await user.click(screen.getByText("Alice").closest("tr")!);
    expect(onSelect).toHaveBeenCalledWith("Alice");
  });

  it("shows formatted token counts", async () => {
    vi.mocked(global.fetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => defaultResponse,
    } as Response);

    render(
      <QueryClientProvider client={new QueryClient()}>
        <RankingTable />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("30.0K")).toBeInTheDocument());
    expect(screen.getByText("20.0K")).toBeInTheDocument();
  });

  it("shows empty state when no accounts", async () => {
    vi.mocked(global.fetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ...defaultResponse, accounts: [] }),
    } as Response);

    render(
      <QueryClientProvider client={new QueryClient()}>
        <RankingTable />
      </QueryClientProvider>,
    );
    await waitFor(() =>
      expect(screen.getByText(/暂无数据/i)).toBeInTheDocument(),
    );
  });

  it("shows Cost column when pricing is configured", async () => {
    vi.mocked(global.fetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ...defaultResponse, price_coverage: "partial" }),
    } as Response);

    render(
      <QueryClientProvider client={new QueryClient()}>
        <RankingTable />
      </QueryClientProvider>,
    );
    await waitFor(() =>
      expect(screen.getByText("费用")).toBeInTheDocument(),
    );
    // Should still show regular columns
    expect(screen.getByText("账号")).toBeInTheDocument();
    expect(screen.getByText("请求数")).toBeInTheDocument();
    expect(screen.getByText("Tokens")).toBeInTheDocument();
  });

  it("hides Cost column when pricing is empty", async () => {
    vi.mocked(global.fetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => defaultResponse,
    } as Response);

    render(
      <QueryClientProvider client={new QueryClient()}>
        <RankingTable />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Alice")).toBeInTheDocument());
    expect(screen.queryByText("费用")).not.toBeInTheDocument();
  });
});