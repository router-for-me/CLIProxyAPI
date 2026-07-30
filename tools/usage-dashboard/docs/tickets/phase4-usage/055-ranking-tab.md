# Ticket 4.5 — Ranking tab

**Phase**: 4 — Usage detail
**Blocks**: 4.7
**Blocked by**: 4.1
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/RankingTable.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/RankingTable.test.tsx` (new)

---

## 🎯 Goal

The Ranking tab shows accounts ranked by token volume: Account, Requests,
Tokens, Cost. Uses `useSummary`'s `accounts` array (already aliased).

Clicking a row sets the `selectedAccounts` filter and switches to Usage tab.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: RankingTable test

```tsx
// frontend/src/components/__tests__/RankingTable.test.tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import RankingTable from "../RankingTable";

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200,
    json: async () => ({
      range: "24h", models_filter: [], accounts_filter: [],
      summary: { requests: 100, total_tokens: 50000 },
      accounts: [
        { account: "Alice", requests: 60, total_tokens: 30000, input_tokens: 18000, output_tokens: 12000, failed: 1 },
        { account: "Bob", requests: 40, total_tokens: 20000, input_tokens: 12000, output_tokens: 8000, failed: 0 },
      ],
      models: [], hours: [], price_coverage: "empty",
    }),
  })) as any;
});

describe("RankingTable", () => {
  it("renders accounts sorted by token volume", async () => {
    render(<QueryClientProvider client={new QueryClient()}><RankingTable /></QueryClientProvider>);
    await waitFor(() => expect(screen.getByText("Alice")).toBeInTheDocument());
    const rows = screen.getAllByRole("row");
    expect(rows[1]).toHaveTextContent("Alice");
    expect(rows[2]).toHaveTextContent("Bob");
  });

  it("clicking a row filters by that account", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><RankingTable onSelect={onSelect} /></QueryClientProvider>);
    await waitFor(() => expect(screen.getByText("Alice")).toBeInTheDocument());
    await user.click(screen.getByText("Alice").closest("tr")!);
    expect(onSelect).toHaveBeenCalledWith("acc-alice-hash");
  });
});
```

Commit: `test(ranking-tab): red — sorted + clickable`

### Step 2 — Green: implement

```tsx
// frontend/src/components/RankingTable.tsx
import { useSummary } from "@/api/hooks/useSummary";
import { useFilterKey } from "@/stores/filtersStore";
import { formatTokens } from "@/lib/format";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

interface Props { onSelect?: (accountHash: string) => void; }

export default function RankingTable({ onSelect }: Props) {
  const filters = useFilterKey();
  const { data } = useSummary(filters);
  const rows = data?.accounts ?? [];

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Account</TableHead>
          <TableHead className="text-right">Requests</TableHead>
          <TableHead className="text-right">Tokens</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((a, i) => (
          <TableRow key={i} className="cursor-pointer"
                    onClick={() => onSelect?.(a.account)}>
            <TableCell>{a.account}</TableCell>
            <TableCell className="text-right">{a.requests}</TableCell>
            <TableCell className="text-right">{formatTokens(a.total_tokens)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
```

Wire `onSelect` in the Usage page: call `useFiltersStore.getState().toggleAccount(hash)`
then switch to Usage tab.

**Verify green**:
```bash
pnpm test src/components/__tests__/RankingTable.test.tsx
```

Commit: `feat(ranking-tab): account ranking — green`

### Step 3 — Refactor: empty state + column "Cost"

If `price_coverage !== "empty"`, add a Cost column. Add a test for the
Cost column appearing.

Commit: `feat(ranking-tab): cost column when pricing configured`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/RankingTable.test.tsx` |
| 5 | Integration Tests | Vite dev → real accounts → ranking renders |
| 6 | Functional Tests | Click row → switches to Usage tab with that account filtered |
| 7 | Contract Tests | Reuses `useSummary` cache; no extra request |
| 8 | E2E | N/A (composed in 4.7) |
| 9 | Code Review | No N+1 queries; single summary fetch |

All green → Ticket 4.7.
