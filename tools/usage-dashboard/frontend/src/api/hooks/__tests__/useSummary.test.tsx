import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useSummary } from "../useSummary";

const wrapper = ({ children }: { children: React.ReactNode }) => {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
};

describe("useSummary", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("fetches summary", async () => {
    const mockResponse = {
      range: "24h",
      models_filter: [],
      accounts_filter: [],
      summary: {
        requests: 0,
        total_tokens: 0,
        input_tokens: 0,
        output_tokens: 0,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        failed: 0,
        success_latency_ms: 0,
        success_requests: 0,
        success_ttft_ms: 0,
        estimated_cost: 0,
        estimated_cost_currency: "USD",
      },
      accounts: [],
      models: [],
      hours: [],
      price_coverage: "empty",
    };
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => mockResponse,
    })) as unknown as typeof fetch;

    const { result } = renderHook(() => useSummary({ range: "24h" }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.summary.requests).toBe(0);
  });

  it("propagates fetch errors", async () => {
    global.fetch = vi.fn(async () => ({
      ok: false,
      status: 500,
      json: async () => ({ detail: "server error" }),
    })) as unknown as typeof fetch;

    const { result } = renderHook(() => useSummary({ range: "24h" }), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toBeDefined();
  });
});