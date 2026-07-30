# Ticket 3.6 — Overview Recent Usage table + detail drawer

**Phase**: 3 — Overview
**Blocks**: 3.8
**Blocked by**: 3.4
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/RecentUsageTable.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/DetailDrawer.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/RecentUsageTable.test.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/DetailDrawer.test.tsx` (new)

---

## 🎯 Goal

The Recent Usage table shows the 12 most recent events (Time, Account,
Model, Token, Latency, Status). Clicking a row opens the `DetailDrawer`
showing the full event details.

The drawer is a shadcn `Sheet` component. Its open state and the selected
row id live in the `uiStore` from Ticket 3.3.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: RecentUsageTable test

```tsx
// frontend/src/components/__tests__/RecentUsageTable.test.tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import RecentUsageTable from "../RecentUsageTable";

const mockRequests = {
  requests: Array.from({length: 12}, (_, i) => ({
    id: i, request_id: `r${i}`, timestamp: "2026-01-01T00:00:00Z",
    account_hash: `acc${i}`, model: `m${i % 3}`, endpoint: "e", provider: "p",
    total_tokens: 100 * i, latency_ms: 100, failed: 0, fail_status: 0,
    input_tokens: 60, output_tokens: 40, reasoning_tokens: 0, cached_tokens: 0,
    cache_read_tokens: 0, cache_creation_tokens: 0, ttft_ms: 50, alias: null,
  })),
  next_cursor: null,
  range: "24h", models_filter: [], accounts_filter: [],
};

beforeEach(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200, json: async () => mockRequests,
  })) as unknown as typeof fetch;
});

describe("RecentUsageTable", () => {
  it("renders 12 rows", async () => {
    render(<QueryClientProvider client={new QueryClient()}><RecentUsageTable /></QueryClientProvider>);
    expect(await screen.findAllByRole("row")).toHaveLength(13); // header + 12
  });

  it("clicking a row opens the detail drawer", async () => {
    const user = userEvent.setup();
    render(<QueryClientProvider client={new QueryClient()}><RecentUsageTable /></QueryClientProvider>);
    const rows = await screen.findAllByRole("row");
    await user.click(rows[1]);
    // Drawer body shows the request_id
    expect(await screen.findByText(/r0/)).toBeInTheDocument();
  });
});
```

Commit: `test(recent-usage): red — table + drawer interaction`

### Step 2 — Green: implement

```tsx
// frontend/src/components/RecentUsageTable.tsx
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useRequests } from "@/api/hooks/useRequests";
import { useFilterKey } from "@/stores/filtersStore";
import { useUIStore } from "@/stores/uiStore";
import { formatTokens, formatMs } from "@/lib/format";

export default function RecentUsageTable() {
  const filters = useFilterKey();
  const { data } = useRequests({ ...filters, limit: 12 });
  const openDetail = useUIStore((s) => s.openDetailDrawer);

  const rows = data?.requests ?? [];

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>时间</TableHead>
          <TableHead>账号</TableHead>
          <TableHead>模型</TableHead>
          <TableHead className="text-right">Token</TableHead>
          <TableHead className="text-right">延迟</TableHead>
          <TableHead>状态</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((r) => (
          <TableRow key={r.id} className="cursor-pointer" onClick={() => openDetail(String(r.id))}>
            <TableCell>{r.timestamp}</TableCell>
            <TableCell>{r.alias ?? r.account_hash?.slice(0, 12)}</TableCell>
            <TableCell>{r.model}</TableCell>
            <TableCell className="text-right">{formatTokens(r.total_tokens)}</TableCell>
            <TableCell className="text-right">{formatMs(r.latency_ms ?? 0)}</TableCell>
            <TableCell>{r.failed ? `✗ ${r.fail_status}` : "✓"}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
```

`DetailDrawer.tsx`:
```tsx
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useRequests } from "@/api/hooks/useRequests";
import { useFilterKey } from "@/stores/filtersStore";
import { useUIStore } from "@/stores/uiStore";

export function DetailDrawer() {
  const id = useUIStore((s) => s.detailDrawerRequestId);
  const close = useUIStore((s) => s.openDetailDrawer);
  const filters = useFilterKey();
  const { data } = useRequests({ ...filters, limit: 100 });
  const row = data?.requests?.find((r) => String(r.id) === id);

  return (
    <Sheet open={id !== null} onOpenChange={(o) => !o && close(null)}>
      <SheetContent>
        <SheetHeader><SheetTitle>请求详情</SheetTitle></SheetHeader>
        {row && <pre className="text-xs">{JSON.stringify(row, null, 2)}</pre>}
      </SheetContent>
    </Sheet>
  );
}
```

Add `<DetailDrawer />` to `Layout.tsx` (renders once, portal-mounted).

**Verify green**:
```bash
pnpm test
pnpm build
```

Commit: `feat(recent-usage): table + detail drawer — green`

### Step 3 — Refactor: memoize row rendering

Wrap `TableRow` rows in `React.memo` so a 12-row table does not re-render
on every refetch when the data has not changed (compare by `id`).

Commit: `perf(recent-usage): memoize rows`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/RecentUsageTable.test.tsx` |
| 5 | Integration Tests | Vite dev → click rows → drawer content matches clicked row |
| 6 | Functional Tests | Empty data → "暂无数据" placeholder |
| 7 | Contract Tests | `useRequests` return type from `types.ts` |
| 8 | E2E | N/A (composed in 3.8) |
| 9 | Code Review | No row holds local state; selection flows through uiStore |

All green → Ticket 3.8.
