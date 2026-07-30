# Ticket 2.2 — Typed fetch client + TanStack Query hooks

**Phase**: 2 — Contract bridge
**Blocks**: —
**Blocked by**: 2.1
**Files touched**:
- `tools/usage-dashboard/frontend/src/api/client.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/index.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useSummary.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useTimeseries.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useModels.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useAccounts.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useRequests.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useErrors.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useProviders.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useEndpoints.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/usePrices.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useAliases.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/useCollectorHealth.ts` (new)
- `tools/usage-dashboard/frontend/src/api/hooks/__tests__/useSummary.test.tsx` (new)

---

## 🎯 Goal

Every API route has a typed TanStack Query hook. The fetch wrapper infers
request/response types from `paths` in `types.ts`. A test asserts that
renaming a field server-side breaks the front-end type check.

This ticket delivers the **typed contract** that Phase 3 (overview) and
Phase 4 (usage detail) build on.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. The first test is a compile-time guarantee: if the
generated types don't include a route, the test does not type-check.

---

## 🪜 Steps

### Step 1 — Red: typed client contract test

```typescript
// frontend/src/api/client.test.ts
import { describe, it, expect } from "vitest";
import { apiGet } from "./client";
import type { paths } from "./types";

describe("apiGet", () => {
  it("returns typed response for /api/v1/summary", async () => {
    // Mock fetch
    const mockResponse = {
      range: "24h", models_filter: [], accounts_filter: [],
      summary: { requests: 5, total_tokens: 100, input_tokens: 60,
                 output_tokens: 40, reasoning_tokens: 0, cached_tokens: 0,
                 cache_read_tokens: 0, cache_creation_tokens: 0, failed: 0,
                 success_latency_ms: 500, success_requests: 5,
                 success_ttft_ms: 100, estimated_cost: 0.01,
                 estimated_cost_currency: "USD" },
      accounts: [], models: [], hours: [], price_coverage: "partial",
    };
    global.fetch = vi.fn(async () => ({
      ok: true, status: 200,
      json: async () => mockResponse,
    })) as unknown as typeof fetch;

    const data = await apiGet("/api/v1/summary", { range: "24h" });
    // Compile-time: data must match the route's 200 response shape
    const check: paths["/api/v1/summary"]["get"]["responses"]["200"]["content"]["application/json"] = data;
    expect(check.summary.requests).toBe(5);
  });
});
```

**Verify red**: `pnpm test` fails — `client.ts` does not exist.

Commit: `test(api-client): red — typed fetch wrapper`

### Step 2 — Green: implement client.ts

```typescript
// frontend/src/api/client.ts
import type { paths } from "./types";

type GetParams<P extends keyof paths> = paths[P] extends { get: { parameters: { query: any } } }
  ? paths[P]["get"]["parameters"]["query"]
  : Record<string, string | string[] | undefined>;

type GetResponse<P extends keyof paths> =
  paths[P] extends { get: { responses: { 200: { content: { "application/json": any } } } } }
    ? paths[P]["get"]["responses"]["200"]["content"]["application/json"]
    : never;

export async function apiGet<P extends keyof paths & string>(
  path: P,
  params?: GetParams<P>,
  token?: string,
): Promise<GetResponse<P>> {
  const url = new URL(path, window.location.origin);
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      if (v === undefined) continue;
      if (Array.isArray(v)) v.forEach((x) => url.searchParams.append(k, String(x)));
      else url.searchParams.set(k, String(v));
    }
  }
  const headers: Record<string, string> = {};
  if (token) headers["X-Dashboard-Token"] = token;
  const resp = await fetch(url, { headers });
  if (!resp.ok) {
    let detail = "request failed";
    try { detail = (await resp.json()).detail ?? detail; } catch {}
    throw new Error(`${resp.status}: ${detail}`);
  }
  return resp.json() as Promise<GetResponse<P>>;
}

export async function apiPut<P extends keyof paths & string>(
  path: P,
  body: unknown,
  token?: string,
): Promise<unknown> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["X-Dashboard-Token"] = token;
  const resp = await fetch(path, {
    method: "PUT", headers,
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`${resp.status}`);
  return resp.json();
}

export async function apiDelete<P extends keyof paths & string>(path: P, token?: string): Promise<unknown> {
  const headers: Record<string, string> = {};
  if (token) headers["X-Dashboard-Token"] = token;
  const resp = await fetch(path, { method: "DELETE", headers });
  if (!resp.ok) throw new Error(`${resp.status}`);
  return resp.json();
}
```

**Verify green**:
```bash
pnpm test src/api/client.test.ts
pnpm typecheck
```

Commit: `feat(api-client): typed fetch wrapper — green`

### Step 3 — Green: implement hooks

Create one hook per route. Example:

```typescript
// frontend/src/api/hooks/useSummary.ts
import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";

export type SummaryFilters = {
  range?: string;
  from?: string;
  to?: string;
  model?: string[];
  account?: string[];
};

export function useSummary(filters: SummaryFilters, token?: string) {
  return useQuery({
    queryKey: ["summary", filters],
    queryFn: () => apiGet("/api/v1/summary", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}
```

Analogous hooks for every other route. All in `src/api/hooks/`.

Add an integration test for at least one hook using MSW or a fetch mock:

```typescript
// frontend/src/api/hooks/__tests__/useSummary.test.tsx
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useSummary } from "../useSummary";

const wrapper = ({ children }: { children: React.ReactNode }) => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
};

it("fetches summary", async () => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200,
    json: async () => ({
      range: "24h", models_filter: [], accounts_filter: [],
      summary: { /* ...full shape... */ },
      accounts: [], models: [], hours: [], price_coverage: "empty",
    }),
  })) as unknown as typeof fetch;

  const { result } = renderHook(() => useSummary({ range: "24h" }), { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.summary.requests).toBe(0);
});
```

**Verify green**:
```bash
pnpm test
pnpm typecheck
```

Commit: `feat(api-hooks): TanStack Query hooks for all read-only routes`

### Step 4 — Refactor: shared query key factory

```typescript
// frontend/src/api/keys.ts
export const qk = {
  summary: (f: SummaryFilters) => ["summary", f] as const,
  timeseries: (f: SummaryFilters & { group_by?: string }) => ["timeseries", f] as const,
  models: (f: SummaryFilters) => ["models", f] as const,
  accounts: (f: SummaryFilters) => ["accounts", f] as const,
  requests: (f: SummaryFilters & { cursor?: string; limit?: number }) => ["requests", f] as const,
  errors: (f: SummaryFilters) => ["errors", f] as const,
  providers: (f: SummaryFilters) => ["providers", f] as const,
  endpoints: (f: SummaryFilters) => ["endpoints", f] as const,
  prices: () => ["prices"] as const,
  aliases: () => ["aliases"] as const,
  health: () => ["health"] as const,
};
```

Refactor hooks to use `qk.*` for their query keys.

Commit: `refactor(api-hooks): shared query key factory`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` — every hook is fully typed against `types.ts` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test` (client + hooks) |
| 5 | Integration Tests | Manual: Vite dev proxy → FastAPI, hit a hook from React DevTools, observe 200 |
| 6 | Functional Tests | Rename a back-end response field → `pnpm typecheck` fails |
| 7 | Contract Tests | `apiGet` return type matches `paths[P]["get"]["responses"]["200"]` |
| 8 | E2E | N/A |
| 9 | Code Review | Every route in `paths` has a hook; no `any` types |

All green → Ticket 2.3.
