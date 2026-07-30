# Ticket 4.4 — Errors tab

**Phase**: 4 — Usage detail
**Blocks**: 4.7
**Blocked by**: 4.1
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/ErrorsTable.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/ErrorsTable.test.tsx` (new)

---

## 🎯 Goal

The Errors tab shows an aggregated table of failed requests grouped by
`(fail_status, model)`: Status Code, Model, Count, Percentage, Last Seen.
Clicking a row deep-links to the Usage tab with a filter that shows those
specific failures.

Uses the `useErrors` hook from Phase 2.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: ErrorsTable test

```tsx
// frontend/src/components/__tests__/ErrorsTable.test.tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ErrorsTable from "../ErrorsTable";

const mockErrors = {
  range: "24h", models_filter: [], accounts_filter: [],
  errors: [
    { fail_status: 429, model: "gpt-4", count: 12, percentage: 60.0, last_seen: "2026-01-01T00:00:00Z" },
    { fail_status: 500, model: "claude", count: 8, percentage: 40.0, last_seen: "2026-01-01T01:00:00Z" },
  ],
};

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200, json: async () => mockErrors,
  })) as any;
});

describe("ErrorsTable", () => {
  it("renders aggregated error rows", async () => {
    render(<QueryClientProvider client={new QueryClient()}><ErrorsTable /></QueryClientProvider>);
    await waitFor(() => expect(screen.getAllByRole("row").length).toBe(3)); // header + 2
    expect(screen.getByText("429")).toBeInTheDocument();
    expect(screen.getByText(/60.0%/)).toBeInTheDocument();
  });

  it("clicking a row switches to Usage tab with filter", async () => {
    const user = userEvent.setup();
    const onDrillDown = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><ErrorsTable onDrillDown={onDrillDown} /></QueryClientProvider>);
    await waitFor(() => expect(screen.getByText("429")).toBeInTheDocument());
    await user.click(screen.getByText("429"));
    expect(onDrillDown).toHaveBeenCalledWith({ model: "gpt-4" });
  });
});
```

Commit: `test(errors-tab): red — aggregated errors + drill-down`

### Step 2 — Green: implement

```tsx
// frontend/src/components/ErrorsTable.tsx
import { useErrors } from "@/api/hooks/useErrors";
import { useFilterKey } from "@/stores/filtersStore";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

interface Props { onDrillDown?: (filter: { model?: string }) => void; }

export default function ErrorsTable({ onDrillDown }: Props) {
  const filters = useFilterKey();
  const { data } = useErrors(filters);
  const rows = data?.errors ?? [];

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Status Code</TableHead>
          <TableHead>Model</TableHead>
          <TableHead className="text-right">Count</TableHead>
          <TableHead className="text-right">Percentage</TableHead>
          <TableHead>Last Seen</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((e, i) => (
          <TableRow key={i} className="cursor-pointer"
                    onClick={() => onDrillDown?.({ model: e.model })}>
            <TableCell>{e.fail_status}</TableCell>
            <TableCell>{e.model}</TableCell>
            <TableCell className="text-right">{e.count}</TableCell>
            <TableCell className="text-right">{e.percentage.toFixed(1)}%</TableCell>
            <TableCell>{e.last_seen}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
```

Wire `onDrillDown` in the Usage page: set `selectedModels` in the filter
store, switch active tab to "usage".

**Verify green**:
```bash
pnpm test src/components/__tests__/ErrorsTable.test.tsx
```

Commit: `feat(errors-tab): aggregated table + drill-down — green`

### Step 3 — Refactor: empty state

When `rows.length === 0`, show "无错误请求" placeholder. Add a test.

Commit: `feat(errors-tab): empty state`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/ErrorsTable.test.tsx` |
| 5 | Integration Tests | Vite dev → real failed events → table populates |
| 6 | Functional Tests | Click row → switches to Usage tab with model filter applied |
| 7 | Contract Tests | `useErrors` return type matches `/api/v1/errors` schema |
| 8 | E2E | N/A (composed in 4.7) |
| 9 | Code Review | Drill-down does not duplicate the request; just sets filter + switches tab |

All green → Ticket 4.7.
