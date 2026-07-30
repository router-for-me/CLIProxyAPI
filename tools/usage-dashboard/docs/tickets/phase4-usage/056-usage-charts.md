# Ticket 4.6 — Usage charts (2 rows: Model+Provider, Endpoint+Trend)

**Phase**: 4 — Usage detail
**Blocks**: 4.7
**Blocked by**: 4.1, 4.2
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/charts/ProviderDistributionChart.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/charts/EndpointDistributionChart.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/charts/UsageTrendChart.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/UsageCharts.test.tsx` (new)

---

## 🎯 Goal

Four charts in two rows, matching the legacy `/usage` layout:

- Row 1: Model Distribution (with Token/Cost toggle — reuse 3.5 component) +
  Provider Distribution (Token/Cost toggle).
- Row 2: Endpoint Distribution + Token Usage Trend.

All charts subscribe to `useFilterKey` and refetch when filters change.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: charts render test

```tsx
// frontend/src/components/__tests__/UsageCharts.test.tsx
import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ProviderDistributionChart from "../charts/ProviderDistributionChart";
import EndpointDistributionChart from "../charts/EndpointDistributionChart";
import UsageTrendChart from "../charts/UsageTrendChart";

vi.mock("react-chartjs-2", () => ({
  Bar: () => <div data-testid="bar" />,
  Line: () => <div data-testid="line" />,
}));

describe("Usage charts", () => {
  it("Provider chart renders Bar", () => {
    render(<QueryClientProvider client={new QueryClient()}><ProviderDistributionChart /></QueryClientProvider>);
    expect(screen.getByTestId("bar")).toBeInTheDocument();
  });
  it("Endpoint chart renders Bar", () => {
    render(<QueryClientProvider client={new QueryClient()}><EndpointDistributionChart /></QueryClientProvider>);
    expect(screen.getByTestId("bar")).toBeInTheDocument();
  });
  it("Trend chart renders Line", () => {
    render(<QueryClientProvider client={new QueryClient()}><UsageTrendChart /></QueryClientProvider>);
    expect(screen.getByTestId("line")).toBeInTheDocument();
  });
});
```

Commit: `test(usage-charts): red — all 3 new charts render`

### Step 2 — Green: implement three charts

Follow the pattern from Ticket 3.5. Each chart:

1. Imports a `use*` hook (`useProviders`, `useEndpoints`, `useTimeseries`).
2. Subscribes to `useFilterKey` for filters.
3. Renders a `react-chartjs-2` `<Bar>` or `<Line>` with the dark theme options.

Example for Provider:
```tsx
// frontend/src/components/charts/ProviderDistributionChart.tsx
import { Bar } from "react-chartjs-2";
import { useProviders } from "@/api/hooks/useProviders";
import { useFilterKey } from "@/stores/filtersStore";
import { useChartToggleStore } from "@/stores/chartToggleStore";
import { darkThemeOptions } from "@/lib/chartConfig";

export default function ProviderDistributionChart() {
  const filters = useFilterKey();
  const { data } = useProviders(filters);
  const mode = useChartToggleStore((s) => s.modelChartMode); // or its own store

  const providers = data?.providers ?? [];
  const labels = providers.map((p) => p.provider);
  const values = providers.map((p) => mode === "tokens" ? p.total_tokens : (p as any).estimated_cost ?? 0);

  return <Bar data={{ labels, datasets: [{ label: mode, data: values, backgroundColor: "hsl(280 80% 60%)" }] }}
              options={darkThemeOptions} />;
}
```

Assemble the four charts in the Usage page (the Model Distribution chart
component from 3.5 is reused directly):

```tsx
// In Usage.tsx, after KPIs:
<div className="grid grid-cols-2 gap-4">
  <ChartPanel title="Model Distribution"><ModelDistributionChart /></ChartPanel>
  <ChartPanel title="Provider Distribution"><ProviderDistributionChart /></ChartPanel>
</div>
<div className="grid grid-cols-2 gap-4">
  <ChartPanel title="Endpoint Distribution"><EndpointDistributionChart /></ChartPanel>
  <ChartPanel title="Token Usage Trend"><UsageTrendChart /></ChartPanel>
</div>
```

**Verify green**:
```bash
pnpm test
pnpm build
```

Commit: `feat(usage-charts): provider + endpoint + trend — green`

### Step 3 — Refactor: dedicated toggle store per chart

The Token/Cost toggle from Ticket 3.5 was on `chartToggleStore.modelChartMode`.
Each Usage chart needs its own toggle. Refactor into an indexed store:

```ts
interface ChartToggleState {
  modes: Record<string, "tokens" | "cost">;
  getMode: (chartId: string) => "tokens" | "cost";
  setMode: (chartId: string, m: "tokens" | "cost") => void;
}
```

And each chart passes its unique `chartId`.

Commit: `refactor(charts): per-chart toggle store`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/UsageCharts.test.tsx` |
| 5 | Integration Tests | Vite dev → all 4 charts render with real data |
| 6 | Functional Tests | Toggle Token/Cost on each chart independently |
| 7 | Contract Tests | `useProviders`, `useEndpoints`, `useTimeseries` types match schemas |
| 8 | E2E | N/A (composed in 4.7) |
| 9 | Code Review | No duplicate chart options; `darkThemeOptions` reused |

All green → Ticket 4.7.
