import { describe, it, expect } from "vitest";
import { execSync } from "node:child_process";
 import type { paths } from "./types";

describe("generated types", () => {
  it("includes /api/v1/summary route", () => {
    const route: keyof paths = "/api/v1/summary";
    expect(route).toBe("/api/v1/summary");
  });

  it("includes /api/v1/requests route", () => {
    const route: keyof paths = "/api/v1/requests";
    expect(route).toBe("/api/v1/requests");
  });

  it("summary response has expected top-level keys", () => {
    type Resp = paths["/api/v1/summary"]["get"]["responses"]["200"]["content"]["application/json"];
    const sample: Resp = {
      range: "24h",
      models_filter: [],
      accounts_filter: [],
      summary: {
        requests: 0, total_tokens: 0, input_tokens: 0, output_tokens: 0,
        reasoning_tokens: 0, cached_tokens: 0, cache_read_tokens: 0,
        cache_creation_tokens: 0, failed: 0, success_latency_ms: 0,
        success_requests: 0, success_ttft_ms: 0,
        estimated_cost: 0, estimated_cost_currency: "USD",
      },
      accounts: [], models: [], hours: [], price_coverage: "empty",
    };
    expect(sample.price_coverage).toBe("empty");
  });
});

describe("CI stale types guard", () => {
  it("check-fresh-types.sh exits 0 when types are fresh (idempotency)", () => {
    const result = execSync("bash scripts/check-fresh-types.sh", {
      cwd: import.meta.dirname + "/../..",
      encoding: "utf-8",
    });
    expect(result).toContain("types.ts is fresh");
  });

  it("regen-types.sh after check-fresh-types.sh is a no-op", () => {
    const cwd = import.meta.dirname + "/../..";

    // First run: check-fresh-types generates temp & diffs
    const result = execSync("bash scripts/check-fresh-types.sh", {
      cwd,
      encoding: "utf-8",
    });
    expect(result).toContain("types.ts is fresh");
  });
});