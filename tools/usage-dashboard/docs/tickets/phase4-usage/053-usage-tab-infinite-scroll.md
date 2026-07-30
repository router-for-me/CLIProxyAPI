# Ticket 4.3 — Usage tab: infinite-scroll requests table

**Phase**: 4 — Usage detail
**Blocks**: 4.7
**Blocked by**: 4.1, 4.2
**Files touched**:
- `tools/usage-dashboard/frontend/src/api/hooks/useRequestsInfinite.ts` (new)
- `tools/usage-dashboard/frontend/src/components/UsageTable.tsx` (new)
- `tools/usage-dashboard/frontend/src/stores/settingsStore.ts` (new — column visibility)
- `tools/usage-dashboard/frontend/src/components/__tests__/UsageTable.test.tsx` (new)

---

## 🎯 Goal

The Usage tab shows the requests table with columns (Time, Account, Model,
Endpoint, Provider, Tokens, Cost, Latency, Status) and infinite-scroll
pagination via `useInfiniteQuery` keyed on the cursor returned by
`/api/v1/requests`.

Column visibility persists to a Zustand `settingsStore`.

Row click opens the `DetailDrawer` (shared with Dashboard).

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: infinite query + table test

```tsx
// frontend/src/components/__tests__/UsageTable.test.tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import UsageTable from "../UsageTable";

beforeAll(() => {
  let page = 0;
  global.fetch = vi.fn(async () => {
    page++;
    const items = Array.from({length: page === 1 ? 50 : 30}, (_, i) => ({
      id: (page - 1) * 50 + i, request_id: `r${(page-1)*50+i}`,
      timestamp: "2026-01-01T00:00:00Z", account_hash: "acc", model: "m",
      endpoint: "e", provider: "p", total_tokens: 100, latency_ms: 100,
      failed: 0, fail_status: 0,
    }));
    return {
      ok: true, status: 200,
      json: async () => ({ requests: items, next_cursor: page < 3 ? String(page * 50) : null,
                            range: "24h", models_filter: [], accounts_filter: [] }),
    };
  }) as any;
});

describe("UsageTable infinite scroll", () => {
  it("renders first page of 50 rows", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={qc}><UsageTable /></QueryClientProvider>);
    await waitFor(() => expect(screen.getAllByRole("row").length).toBe(51));
  });

  it("loads next page when Load More clicked", async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={qc}><UsageTable /></QueryClientProvider>);
    await waitFor(() => expect(screen.getAllByRole("row").length).toBe(51));
    const btn = screen.getByRole("button", { name: /load more/i });
    await user.click(btn);
    await waitFor(() => expect(screen.getAllByRole("row").length).toBeGreaterThan(51));
  });
});
```

Commit: `test(usage-table): red — infinite scroll`

### Step 2 — Green: implement

```ts
// frontend/src/api/hooks/useRequestsInfinite.ts
import { useInfiniteQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { useFilterKey } from "@/stores/filtersStore";

export function useRequestsInfinite(limit = 50, token?: string) {
  const filters = useFilterKey();
  return useInfiniteQuery({
    queryKey: ["requests-infinite", filters, limit],
    queryFn: ({ pageParam }) => apiGet("/api/v1/requests",
      { ...filters, limit, cursor: pageParam }, token),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}
```

```ts
// frontend/src/stores/settingsStore.ts
import { create } from "zustand";

export type UsageColumn = "time" | "account" | "model" | "endpoint" | "provider"
                        | "tokens" | "cost" | "latency" | "status";

interface SettingsState {
  visibleColumns: Record<UsageColumn, boolean>;
  toggleColumn: (c: UsageColumn) => void;
}

export const useSettingsStore = create<SettingsState>((set) => ({
  visibleColumns: { time: true, account: true, model: true, endpoint: true,
                    provider: true, tokens: true, cost: true, latency: true, status: true },
  toggleColumn: (c) => set((s) => ({ visibleColumns: { ...s.visibleColumns, [c]: !s.visibleColumns[c] } })),
}));
```

```tsx
// frontend/src/components/UsageTable.tsx
import { useRequestsInfinite } from "@/api/hooks/useRequestsInfinite";
import { useSettingsStore, UsageColumn } from "@/stores/settingsStore";
import { useUIStore } from "@/stores/uiStore";
import { formatTokens, formatMs } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const HEADERS: Record<UsageColumn, string> = {
  time: "Time", account: "Account", model: "Model", endpoint: "Endpoint",
  provider: "Provider", tokens: "Tokens", cost: "Cost", latency: "Latency", status: "Status",
};

export default function UsageTable() {
  const { data, fetchNextPage, hasNextPage, isFetchingNextPage } = useRequestsInfinite();
  const visible = useSettingsStore((s) => s.visibleColumns);
  const openDetail = useUIStore((s) => s.openDetailDrawer);
  const rows = data?.pages.flatMap((p) => p.requests) ?? [];

  return (
    <div>
      <Table>
        <TableHeader>
          <TableRow>
            {(Object.keys(HEADERS) as UsageColumn[]).filter((c) => visible[c]).map((c) => (
              <TableHead key={c} className={c === "tokens" || c === "cost" || c === "latency" ? "text-right" : ""}>
                {HEADERS[c]}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => (
            <TableRow key={r.id} className="cursor-pointer" onClick={() => openDetail(String(r.id))}>
              {visible.time && <TableCell>{r.timestamp}</TableCell>}
              {visible.account && <TableCell>{r.alias ?? r.account_hash?.slice(0,12)}</TableCell>}
              {visible.model && <TableCell>{r.model}</TableCell>}
              {visible.endpoint && <TableCell>{r.endpoint}</TableCell>}
              {visible.provider && <TableCell>{r.provider}</TableCell>}
              {visible.tokens && <TableCell className="text-right">{formatTokens(r.total_tokens)}</TableCell>}
              {visible.cost && <TableCell className="text-right">{(r as any).estimated_cost?.toFixed(4) ?? "—"}</TableCell>}
              {visible.latency && <TableCell className="text-right">{formatMs(r.latency_ms ?? 0)}</TableCell>}
              {visible.status && <TableCell>{r.failed ? `✗ ${r.fail_status}` : "✓"}</TableCell>}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {hasNextPage && (
        <Button variant="ghost" disabled={isFetchingNextPage} onClick={() => fetchNextPage()}>
          {isFetchingNextPage ? "加载中…" : "Load More"}
        </Button>
      )}
    </div>
  );
}
```

**Verify green**:
```bash
pnpm test src/components/__tests__/UsageTable.test.tsx
```

Commit: `feat(usage-table): infinite scroll + column visibility — green`

### Step 3 — Refactor: column settings modal

Wire the Column Settings button in the filter bar to a modal that toggles
`visibleColumns` in the store. Add a test:

```tsx
it("Column Settings modal toggles column visibility", async () => {
  // ...open modal, uncheck 'endpoint'...
  // Assert endpoint column is no longer rendered.
});
```

Commit: `feat(usage-table): column settings modal`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/UsageTable.test.tsx` |
| 5 | Integration Tests | Vite dev → real back end → scroll, Load More works |
| 6 | Functional Tests | Column Settings modal hides/shows columns |
| 7 | Contract Tests | `next_cursor` flow matches `/api/v1/requests` schema |
| 8 | E2E | N/A (composed in 4.7) |
| 9 | Code Review | `useInfiniteQuery` correctly passes cursor; no double-fetch loops |

All green → Ticket 4.7.
