import { describe, it, expect, vi, beforeEach } from "vitest";
import { apiGet } from "./client";
import type { paths } from "./types";

describe("apiGet", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("returns typed response for /api/v1/summary", async () => {
    const mockResponse = {
      range: "24h",
      models_filter: [],
      accounts_filter: [],
      summary: {
        requests: 5,
        total_tokens: 100,
        input_tokens: 60,
        output_tokens: 40,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        failed: 0,
        success_latency_ms: 500,
        success_requests: 5,
        success_ttft_ms: 100,
        estimated_cost: 0.01,
        estimated_cost_currency: "USD",
      },
      accounts: [],
      models: [],
      hours: [],
      price_coverage: "partial",
    };
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => mockResponse,
    })) as unknown as typeof fetch;

    const data = await apiGet("/api/v1/summary", { range: "24h" });
    // Compile-time: data must match the route's 200 response shape
    const check: paths["/api/v1/summary"]["get"]["responses"]["200"]["content"]["application/json"] =
      data;
    expect(check.summary.requests).toBe(5);
  });

  it("throws on non-ok response", async () => {
    global.fetch = vi.fn(async () => ({
      ok: false,
      status: 400,
      json: async () => ({ detail: "bad request" }),
    })) as unknown as typeof fetch;

    await expect(apiGet("/api/v1/summary")).rejects.toThrow("400: bad request");
  });

  it("passes auth token header", async () => {
    const mockFetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({}),
    }));
    global.fetch = mockFetch as unknown as typeof fetch;

    await apiGet("/api/v1/summary", undefined, "secret-token");
    expect(mockFetch).toHaveBeenCalledWith(
      expect.objectContaining({
        href: expect.stringContaining("/api/v1/summary"),
      }),
      expect.objectContaining({
        headers: { "X-Dashboard-Token": "secret-token" },
      }),
    );
  });
});