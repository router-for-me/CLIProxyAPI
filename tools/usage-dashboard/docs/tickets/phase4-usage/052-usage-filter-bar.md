# Ticket 4.2 — Usage filter bar (multi-selects)

**Phase**: 4 — Usage detail
**Blocks**: 4.3, 4.6
**Blocked by**: 4.1
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/FilterMultiSelect.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/UsageFilterBar.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/UsageFilterBar.test.tsx` (new)

---

## 🎯 Goal

A filter bar with four multi-select dropdowns (Model, Account, Provider,
Endpoint) + Refresh / Reset / Column Settings / Export buttons.

Selections write to the `filtersStore`. Each multi-select loads its options
from the relevant API endpoint (`/api/v1/models`, `/api/v1/accounts`,
`/api/v1/providers`, `/api/v1/endpoints`) using TanStack Query.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: filter bar test

```tsx
// frontend/src/components/__tests__/UsageFilterBar.test.tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import UsageFilterBar from "../UsageFilterBar";

beforeAll(() => {
  global.fetch = vi.fn(async (url: any) => {
    const path = typeof url === "string" ? url : url.toString();
    let body: any = {};
    if (path.includes("/models")) body = { models: [{ model: "gpt-4" }, { model: "claude" }], accounts_filter: [] };
    if (path.includes("/accounts")) body = { accounts: [{ account: "acc1" }], accounts_filter: [] };
    if (path.includes("/providers")) body = { providers: [{ provider: "openai" }], accounts_filter: [] };
    if (path.includes("/endpoints")) body = { endpoints: [{ endpoint: "/v1/chat" }], accounts_filter: [] };
    return { ok: true, status: 200, json: async () => body };
  }) as any;
});

describe("UsageFilterBar", () => {
  it("renders 4 multi-select triggers", async () => {
    render(<QueryClientProvider client={new QueryClient()}><UsageFilterBar /></QueryClientProvider>);
    expect(screen.getByText("Model")).toBeInTheDocument();
    expect(screen.getByText("Account")).toBeInTheDocument();
    expect(screen.getByText("Provider")).toBeInTheDocument();
    expect(screen.getByText("Endpoint")).toBeInTheDocument();
  });

  it("clicking Refresh triggers a refetch", async () => {
    const invalidate = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><UsageFilterBar onRefresh={invalidate} /></QueryClientProvider>);
    await userEvent.click(screen.getByRole("button", { name: /refresh/i }));
    expect(invalidate).toHaveBeenCalled();
  });

  it("Reset clears filtersStore", async () => {
    render(<QueryClientProvider client={new QueryClient()}><UsageFilterBar /></QueryClientProvider>);
    await userEvent.click(screen.getByRole("button", { name: /reset/i }));
    // Verify store is cleared via direct import
    const { useFiltersStore } = await import("@/stores/filtersStore");
    expect(useFiltersStore.getState().selectedModels).toEqual([]);
  });
});
```

Commit: `test(usage-filters): red — multi-select + buttons`

### Step 2 — Green: implement FilterMultiSelect + UsageFilterBar

```tsx
// frontend/src/components/FilterMultiSelect.tsx
import { useState, useRef, useEffect } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";

interface Option { label: string; value: string; }
interface Props {
  label: string;
  options: Option[];
  selected: string[];
  onToggle: (v: string) => void;
}

export function FilterMultiSelect({ label, options, selected, onToggle }: Props) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    function onDocClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, []);

  return (
    <div className="relative" ref={ref}>
      <button type="button"
              className="border border-border px-3 py-1 text-sm rounded bg-card"
              onClick={() => setOpen(!open)}>
        {label}{selected.length > 0 && ` (${selected.length})`}
      </button>
      {open && (
        <div className="absolute z-10 mt-1 max-h-64 overflow-y-auto bg-card border border-border rounded shadow-lg p-2">
          {options.map((o) => (
            <label key={o.value} className="flex items-center gap-2 px-2 py-1 text-sm">
              <Checkbox checked={selected.includes(o.value)} onCheckedChange={() => onToggle(o.value)} />
              {o.label}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
```

```tsx
// frontend/src/components/UsageFilterBar.tsx
import { useModels } from "@/api/hooks/useModels";
import { useAccounts } from "@/api/hooks/useAccounts";
import { useProviders } from "@/api/hooks/useProviders";
import { useEndpoints } from "@/api/hooks/useEndpoints";
import { useFiltersStore } from "@/stores/filtersStore";
import { useQueryClient } from "@tanstack/react-query";
import { FilterMultiSelect } from "./FilterMultiSelect";
import { Button } from "@/components/ui/button";

interface Props { onRefresh?: () => void; onExport?: () => void; onColumnSettings?: () => void; }

export default function UsageFilterBar({ onRefresh, onExport, onColumnSettings }: Props) {
  const filterKey = useFiltersStore((s) => ({ range: s.range, from: s.from, to: s.to }));
  const { data: modelData } = useModels(filterKey);
  const { data: accountData } = useAccounts(filterKey);
  const { data: providerData } = useProviders(filterKey);
  const { data: endpointData } = useEndpoints(filterKey);

  const selectedModels = useFiltersStore((s) => s.selectedModels);
  const selectedAccounts = useFiltersStore((s) => s.selectedAccounts);
  const toggleModel = useFiltersStore((s) => s.toggleModel);
  const toggleAccount = useFiltersStore((s) => s.toggleAccount);
  const clearModels = useFiltersStore((s) => s.clearModels);
  const clearAccounts = useFiltersStore((s) => s.clearAccounts);

  const qc = useQueryClient();

  return (
    <div className="flex flex-wrap items-center gap-2">
      <FilterMultiSelect label="Model"
                         options={(modelData?.models ?? []).map((m) => ({ label: m.model, value: m.model }))}
                         selected={selectedModels} onToggle={toggleModel} />
      <FilterMultiSelect label="Account"
                         options={(accountData?.accounts ?? []).map((a) => ({ label: a.account, value: a.account }))}
                         selected={selectedAccounts} onToggle={toggleAccount} />
      <FilterMultiSelect label="Provider"
                         options={(providerData?.providers ?? []).map((p) => ({ label: p.provider, value: p.provider }))}
                         selected={[]} onToggle={() => {}} />
      <FilterMultiSelect label="Endpoint"
                         options={(endpointData?.endpoints ?? []).map((e) => ({ label: e.endpoint, value: e.endpoint }))}
                         selected={[]} onToggle={() => {}} />
      <Button size="sm" onClick={() => { onRefresh?.(); qc.invalidateQueries(); }}>Refresh</Button>
      <Button size="sm" variant="ghost" onClick={() => { clearModels(); clearAccounts(); }}>Reset</Button>
      {onColumnSettings && <Button size="sm" variant="ghost" onClick={onColumnSettings}>Column Settings</Button>}
      {onExport && <Button size="sm" onClick={onExport}>Export CSV</Button>}
    </div>
  );
}
```

**Verify green**:
```bash
pnpm test src/components/__tests__/UsageFilterBar.test.tsx
```

Commit: `feat(usage-filters): multi-select + Refresh/Reset — green`

### Step 3 — Refactor: keyboard accessibility

Each dropdown trigger has `aria-expanded`, `aria-haspopup="listbox"`, and
the option list has `role="listbox"` with `role="option"` children. Add a
test that asserts these attributes.

Commit: `a11y(usage-filters): aria attributes on dropdown`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/UsageFilterBar.test.tsx` |
| 5 | Integration Tests | Vite dev → click Model → dropdown populates from real API |
| 6 | Functional Tests | Select 2 models → KPIs/charts refetch with new filter |
| 7 | Contract Tests | Options come from typed endpoints, not hardcoded |
| 8 | E2E | N/A (composed in 4.7) |
| 9 | Code Review | Provider/Endpoint toggles wired in Ticket 4.6 |

All green → Tickets 4.3, 4.6.
