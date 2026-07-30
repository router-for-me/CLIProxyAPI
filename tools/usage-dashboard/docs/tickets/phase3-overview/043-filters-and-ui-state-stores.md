# Ticket 3.3 — Filters + UI state stores (Zustand)

**Phase**: 3 — Overview
**Blocks**: 3.4, 3.5
**Blocked by**: 3.2
**Files touched**:
- `tools/usage-dashboard/frontend/src/stores/filtersStore.ts` (new)
- `tools/usage-dashboard/frontend/src/stores/uiStore.ts` (new)
- `tools/usage-dashboard/frontend/src/stores/__tests__/filtersStore.test.ts` (new)
- `tools/usage-dashboard/frontend/src/components/RangeSelector.tsx` (new)

---

## 🎯 Goal

A Zustand store owns **shared filter state** (`range`, `from`, `to`,
`granularity`, `selectedModels`, `selectedAccounts`) and another owns
**UI state** (drawer visibility, refresh interval). TanStack Query cache
keys include the filter state, so changing a filter invalidates and
refetches.

This is the **core fix for the multi-view interference pain** (ADR 0003):
filters are not in component state, they are in a store that any view
subscribes to via selectors.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: filtersStore test

```ts
// frontend/src/stores/__tests__/filtersStore.test.ts
import { describe, it, expect, beforeEach } from "vitest";
import { useFiltersStore } from "../filtersStore";

describe("filtersStore", () => {
  beforeEach(() => useFiltersStore.setState({
    range: "24h", from: undefined, to: undefined,
    granularity: "hour", selectedModels: [], selectedAccounts: [],
  }));

  it("updates range and clears explicit from/to", () => {
    useFiltersStore.getState().setRange("7d");
    expect(useFiltersStore.getState().range).toBe("7d");
    expect(useFiltersStore.getState().from).toBeUndefined();
  });

  it("sets explicit from/to and clears range preset", () => {
    useFiltersStore.getState().setExplicitRange("2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z");
    expect(useFiltersStore.getState().range).toBe("explicit");
    expect(useFiltersStore.getState().from).toBe("2026-01-01T00:00:00Z");
  });

  it("toggles a model selection", () => {
    useFiltersStore.getState().toggleModel("gpt-4");
    expect(useFiltersStore.getState().selectedModels).toEqual(["gpt-4"]);
    useFiltersStore.getState().toggleModel("gpt-4");
    expect(useFiltersStore.getState().selectedModels).toEqual([]);
  });

  it("preserves object identity for unchanged slices (avoids re-renders)", () => {
    const before = useFiltersStore.getState().selectedAccounts;
    useFiltersStore.getState().setRange("1h");
    expect(useFiltersStore.getState().selectedAccounts).toBe(before);
  });
});
```

**Verify red**: `pnpm test` fails — store doesn't exist.

Commit: `test(stores): red — filtersStore behavior`

### Step 2 — Green: install Zustand + write stores

```bash
pnpm add zustand
```

```ts
// frontend/src/stores/filtersStore.ts
import { create } from "zustand";

export type RangePreset = "today" | "1h" | "5h" | "24h" | "7d" | "30d" | "explicit";
export type Granularity = "hour" | "day";

export interface FiltersState {
  range: RangePreset;
  from?: string;
  to?: string;
  granularity: Granularity;
  selectedModels: string[];
  selectedAccounts: string[];

  setRange: (r: RangePreset) => void;
  setExplicitRange: (from: string, to: string) => void;
  setGranularity: (g: Granularity) => void;
  toggleModel: (m: string) => void;
  toggleAccount: (a: string) => void;
  clearModels: () => void;
  clearAccounts: () => void;
}

export const useFiltersStore = create<FiltersState>((set) => ({
  range: "24h",
  from: undefined,
  to: undefined,
  granularity: "hour",
  selectedModels: [],
  selectedAccounts: [],

  setRange: (r) => set({ range: r, from: undefined, to: undefined }),
  setExplicitRange: (from, to) => set({ range: "explicit", from, to }),
  setGranularity: (g) => set({ granularity: g }),
  toggleModel: (m) => set((s) => ({
    selectedModels: s.selectedModels.includes(m)
      ? s.selectedModels.filter((x) => x !== m)
      : [...s.selectedModels, m],
  })),
  toggleAccount: (a) => set((s) => ({
    selectedAccounts: s.selectedAccounts.includes(a)
      ? s.selectedAccounts.filter((x) => x !== a)
      : [...s.selectedAccounts, a],
  })),
  clearModels: () => set({ selectedModels: [] }),
  clearAccounts: () => set({ selectedAccounts: [] }),
}));

// Selector hook: returns the slice of filters relevant to TanStack Query keys.
export function useFilterKey() {
  return useFiltersStore((s) => ({
    range: s.range, from: s.from, to: s.to,
    models: s.selectedModels, accounts: s.selectedAccounts,
  }));
}
```

```ts
// frontend/src/stores/uiStore.ts
import { create } from "zustand";

export interface UIState {
  aliasDrawerOpen: boolean;
  pricingDrawerOpen: boolean;
  detailDrawerRequestId: string | null;
  toggleAliasDrawer: () => void;
  togglePricingDrawer: () => void;
  openDetailDrawer: (id: string | null) => void;
}

export const useUIStore = create<UIState>((set) => ({
  aliasDrawerOpen: false,
  pricingDrawerOpen: false,
  detailDrawerRequestId: null,
  toggleAliasDrawer: () => set((s) => ({ aliasDrawerOpen: !s.aliasDrawerOpen })),
  togglePricingDrawer: () => set((s) => ({ pricingDrawerOpen: !s.pricingDrawerOpen })),
  openDetailDrawer: (id) => set({ detailDrawerRequestId: id }),
}));
```

`RangeSelector.tsx` (used in Header):
```tsx
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useFiltersStore } from "@/stores/filtersStore";

export function RangeSelector() {
  const range = useFiltersStore((s) => s.range);
  const setRange = useFiltersStore((s) => s.setRange);
  return (
    <Select value={range} onValueChange={(v) => setRange(v as any)}>
      <SelectTrigger className="w-32"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value="1h">近 1 小时</SelectItem>
        <SelectItem value="5h">近 5 小时</SelectItem>
        <SelectItem value="24h">近 24 小时</SelectItem>
        <SelectItem value="7d">近 7 天</SelectItem>
        <SelectItem value="30d">近 30 天</SelectItem>
      </SelectContent>
    </Select>
  );
}
```

**Verify green**:
```bash
pnpm test src/stores
```

Commit: `feat(stores): filters + ui Zustand stores — green`

### Step 3 — Refactor: hook the RangeSelector into Header

Update `Header.tsx` to render `<RangeSelector />` and a Refresh button.
The Refresh button calls `queryClient.invalidateQueries()` — pass it via
prop or use `useQueryClient()`.

Add a test that changing `RangeSelector` updates the store:

```tsx
it("changing RangeSelector updates the store", () => {
  render(<RangeSelector />, { wrapper: TestProvider });
  // ... simulate user selecting "7d"
  expect(useFiltersStore.getState().range).toBe("7d");
});
```

Commit: `feat(header): wire RangeSelector to filtersStore`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/stores` |
| 5 | Integration Tests | Manual: change RangeSelector → observe TanStack Query refetch (via React DevTools) |
| 6 | Functional Tests | `useFilterKey` returns a stable reference when unrelated fields change |
| 7 | Contract Tests | TanStack Query `queryKey` includes every field in `useFilterKey` |
| 8 | E2E | N/A (no full page yet) |
| 9 | Code Review | No component holds filter state locally; all flows through the store |

All green → Tickets 3.4, 3.5.
