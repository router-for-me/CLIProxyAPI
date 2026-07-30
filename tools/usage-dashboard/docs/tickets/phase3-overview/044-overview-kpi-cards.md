# Ticket 3.4 — Overview KPI cards

**Phase**: 3 — Overview
**Blocks**: 3.5, 3.6, 3.8
**Blocked by**: 3.2, 3.3
**Files touched**:
- `tools/usage-dashboard/frontend/src/pages/Dashboard.tsx` (replace placeholder)
- `tools/usage-dashboard/frontend/src/components/KpiCard.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/KpiCard.test.tsx` (new)
- `tools/usage-dashboard/frontend/src/pages/__tests__/Dashboard.kpis.test.tsx` (new)

---

## 🎯 Goal

The 8 KPI cards from the legacy overview (`API Keys`, `Accounts`, `Today
Requests`, `Active Keys`, `Today Tokens`, `Total Tokens`, `Performance`,
`Avg Response`) render with live data from `useSummary` + a "totals" query.

Card layout matches the legacy two-row grid.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: KpiCard test

```tsx
// frontend/src/components/__tests__/KpiCard.test.tsx
import { render, screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import { KpiCard } from "../KpiCard";

describe("KpiCard", () => {
  it("renders label, value, and sub", () => {
    render(<KpiCard label="Today Requests" value="1234" sub="↑ 12% vs yesterday" />);
    expect(screen.getByText("Today Requests")).toBeInTheDocument();
    expect(screen.getByText("1234")).toBeInTheDocument();
    expect(screen.getByText(/↑ 12%/)).toBeInTheDocument();
  });

  it("shows loading skeleton when value is undefined", () => {
    render(<KpiCard label="X" value={undefined} sub="" loading />);
    expect(screen.queryByText("X")).toBeInTheDocument();
    // Skeleton is an element with aria-busy
    expect(document.querySelector("[aria-busy='true']")).toBeInTheDocument();
  });
});
```

Commit: `test(kpi): red — KpiCard renders + loading`

### Step 2 — Green: implement KpiCard

```tsx
// frontend/src/components/KpiCard.tsx
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

interface KpiCardProps {
  label: string;
  value?: string | number;
  sub?: React.ReactNode;
  loading?: boolean;
  className?: string;
}

export function KpiCard({ label, value, sub, loading, className }: KpiCardProps) {
  return (
    <Card className={cn("p-4", className)}>
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <div aria-busy="true" className="mt-1 h-7 w-20 animate-pulse rounded bg-muted" />
      ) : (
        <div className="mt-1 text-xl font-semibold tabular-nums">{value ?? "—"}</div>
      )}
      {sub && <div className="mt-1 text-xs text-muted-foreground">{sub}</div>}
    </Card>
  );
}
```

### Step 3 — Red → Green: Dashboard KPI grid test + implementation

```tsx
// frontend/src/pages/__tests__/Dashboard.kpis.test.tsx
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { describe, it, expect, vi, beforeAll } from "vitest";
import Dashboard from "../Dashboard";
import { useFiltersStore } from "@/stores/filtersStore";

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
    for (const label of ["API Keys", "Accounts", "Today Requests", "Active Keys",
                         "Today Tokens", "Total Tokens", "Performance", "Avg Response"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });

  it("shows total tokens value formatted", async () => {
    renderDashboard();
    await waitFor(() => expect(screen.getByText(/50[,.]?0?K|50,?000/i)).toBeInTheDocument());
  });
});
```

Implement `Dashboard.tsx`:
```tsx
import { useSummary } from "@/api/hooks/useSummary";
import { useFilterKey } from "@/stores/filtersStore";
import { KpiCard } from "@/components/KpiCard";

export default function Dashboard() {
  const filters = useFilterKey();
  const { data, isLoading } = useSummary(filters);

  const s = data?.summary;
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-4 gap-3">
        <KpiCard label="API Keys" loading={isLoading} value={s ? "—" : undefined} sub="—" />
        <KpiCard label="Accounts" loading={isLoading} value={s ? "—" : undefined} sub="—" />
        <KpiCard label="Today Requests" loading={isLoading} value={s?.requests} sub={`${s?.failed ?? 0} 失败`} />
        <KpiCard label="Active Keys" loading={isLoading} value="—" sub="—" />
      </div>
      <div className="grid grid-cols-4 gap-3">
        <KpiCard label="Today Tokens" loading={isLoading} value={s?.total_tokens} sub="..." />
        <KpiCard label="Total Tokens" loading={isLoading} value="—" sub="—" />
        <KpiCard label="Performance" loading={isLoading} value={s ? `${((s.success_requests / Math.max(s.requests,1)) * 100).toFixed(1)}%` : undefined} sub="—" />
        <KpiCard label="Avg Response" loading={isLoading} value={s ? `${Math.round((s.success_latency_ms / Math.max(s.success_requests,1)))} ms` : undefined} sub="—" />
      </div>
    </div>
  );
}
```

(The exact KPI values for "API Keys", "Accounts", "Total Tokens", "Active Keys"
require extra endpoints; in this ticket they remain `—` placeholders. They
are filled in a later sub-ticket or via the existing `/api/v1/accounts` route.)

**Verify green**:
```bash
pnpm test
pnpm build
```

Commit: `feat(dashboard): 8 KPI cards from useSummary — green`

### Step 4 — Refactor: extract number formatting

```ts
// frontend/src/lib/format.ts
export function formatTokens(n?: number): string {
  if (n === undefined) return "—";
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return String(n);
}

export function formatMs(ms?: number): string {
  if (ms === undefined) return "—";
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.round(ms)} ms`;
}
```

Use everywhere.

Commit: `refactor(format): extract number formatters`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test` (KpiCard + Dashboard.kpis) |
| 5 | Integration Tests | Vite dev → real back end → cards render real values |
| 6 | Functional Tests | Back end returns 0 events → cards show "—" not NaN |
| 7 | Contract Tests | Type of `useSummary` return matches `paths["/api/v1/summary"]` |
| 8 | E2E | N/A (composed in 3.8) |
| 9 | Code Review | No `any`; formatters reused; loading state covered |

All green → Tickets 3.5, 3.6.
