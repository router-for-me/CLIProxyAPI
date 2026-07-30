# Ticket 4.1 — Usage page layout + 4 KPI cards

**Phase**: 4 — Usage detail
**Blocks**: 4.2, 4.3, 4.4, 4.5, 4.6
**Blocked by**: Phase 3 complete
**Files touched**:
- `tools/usage-dashboard/frontend/src/pages/Usage.tsx` (replace placeholder)
- `tools/usage-dashboard/frontend/src/pages/__tests__/Usage.kpis.test.tsx` (new)

---

## 🎯 Goal

The `/usage` page renders 4 KPI cards (Total Requests, Total Tokens, Total
Cost, Avg Duration) above an empty tab area. Subsequent tickets fill in the
filter bar, tabs, and charts.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: Usage KPI test

```tsx
// frontend/src/pages/__tests__/Usage.kpis.test.tsx
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { describe, it, expect, vi, beforeAll } from "vitest";
import Usage from "../Usage";

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
    for (const label of ["Total Requests", "Total Tokens", "Total Cost", "Avg Duration"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    await waitFor(() => expect(screen.getByText("50")).toBeInTheDocument());
  });
});
```

Commit: `test(usage): red — 4 KPI cards`

### Step 2 — Green: implement Usage page skeleton

```tsx
// frontend/src/pages/Usage.tsx
import { useSummary } from "@/api/hooks/useSummary";
import { useFilterKey } from "@/stores/filtersStore";
import { KpiCard } from "@/components/KpiCard";
import { formatTokens, formatMs } from "@/lib/format";

export default function Usage() {
  const filters = useFilterKey();
  const { data, isLoading } = useSummary(filters);
  const s = data?.summary;

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-4 gap-3">
        <KpiCard label="Total Requests" loading={isLoading} value={s?.requests} />
        <KpiCard label="Total Tokens" loading={isLoading} value={formatTokens(s?.total_tokens)} />
        <KpiCard label="Total Cost" loading={isLoading}
                 value={s ? `${s.estimated_cost.toFixed(4)} ${s.estimated_cost_currency}` : undefined}
                 sub={data?.price_coverage === "partial" ? "部分模型无定价" : undefined} />
        <KpiCard label="Avg Duration" loading={isLoading}
                 value={s ? formatMs(s.success_latency_ms / Math.max(s.success_requests, 1)) : undefined} />
      </div>
      <div className="text-muted-foreground">Tabs and charts land in subsequent tickets.</div>
    </div>
  );
}
```

**Verify green**:
```bash
pnpm test src/pages/__tests__/Usage.kpis.test.tsx
```

Commit: `feat(usage): 4 KPI cards — green`

### Step 3 — Refactor: extract KPI row component

Since the KPI row repeats across pages, extract a `UsageKpiRow` component
that takes `summary` and `loading`. Then both Dashboard and Usage pages
use it.

Commit: `refactor(usage): extract KpiRow component`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/pages/__tests__/Usage.kpis.test.tsx` |
| 5 | Integration Tests | Vite dev → `/usage` → 4 cards render |
| 6 | Functional Tests | Empty data → cards show "—" |
| 7 | Contract Tests | `useSummary` shared with Dashboard; cache reuse works |
| 8 | E2E | N/A (composed in 4.7) |
| 9 | Code Review | No state duplication between pages; filters shared via store |

All green → Tickets 4.2, 4.3, 4.4, 4.5, 4.6.
