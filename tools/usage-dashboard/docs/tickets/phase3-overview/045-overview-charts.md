# Ticket 3.5 — Overview charts (Model Distribution + Token Usage Trend)

**Phase**: 3 — Overview
**Blocks**: 3.8
**Blocked by**: 3.4
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/ChartPanel.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/charts/ModelDistributionChart.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/charts/TokenTrendChart.tsx` (new)
- `tools/usage-dashboard/frontend/src/lib/chartConfig.ts` (new)
- `tools/usage-dashboard/frontend/src/stores/chartToggleStore.ts` (new — Token/Cost toggle per chart)
- `tools/usage-dashboard/frontend/src/components/charts/__tests__/ModelDistributionChart.test.tsx` (new)

---

## 🎯 Goal

Two charts render in the overview, matching the legacy dashboard:

1. **Model Distribution** — bar chart of tokens per model, with a
   `Token`/`Cost` mode toggle.
2. **Token Usage Trend** — line chart of `total_tokens` over `utc_hour`.

Both use `react-chartjs-2`. Chart.js is already vendored; we install the
React wrapper.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: ModelDistributionChart test

```tsx
// frontend/src/components/charts/__tests__/ModelDistributionChart.test.tsx
import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ModelDistributionChart from "../ModelDistributionChart";

vi.mock("react-chartjs-2", () => ({
  Bar: (props: any) => <div data-testid="bar-mock" data-mode={props.data?.datasets?.[0]?.label} />,
}));

function render_() {
  const qc = new QueryClient();
  return render(<QueryClientProvider client={qc}><ModelDistributionChart /></QueryClientProvider>);
}

describe("ModelDistributionChart", () => {
  it("renders a Bar chart", () => {
    render_();
    expect(screen.getByTestId("bar-mock")).toBeInTheDocument();
  });

  it("shows Token/Cost toggle buttons", () => {
    render_();
    expect(screen.getByRole("button", { name: /token/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /cost/i })).toBeInTheDocument();
  });
});
```

Commit: `test(chart): red — ModelDistributionChart renders`

### Step 2 — Green: install chart.js + react-chartjs-2 + implement

```bash
pnpm add chart.js react-chartjs-2
```

`src/lib/chartConfig.ts`:
```ts
import {
  Chart, BarElement, CategoryScale, LinearScale, Tooltip, Legend, Title,
  PointElement, LineElement, Filler,
} from "chart.js";

Chart.register(
  BarElement, CategoryScale, LinearScale, Tooltip, Legend, Title,
  PointElement, LineElement, Filler,
);

export const DARK_AXIS_COLOR = "hsl(215 14% 60%)";
export const DARK_GRID_COLOR = "hsl(215 14% 22%)";

export const darkThemeOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { labels: { color: DARK_AXIS_COLOR } },
    tooltip: { mode: "index" as const, intersect: false },
  },
  scales: {
    x: { ticks: { color: DARK_AXIS_COLOR }, grid: { color: DARK_GRID_COLOR } },
    y: { ticks: { color: DARK_AXIS_COLOR }, grid: { color: DARK_GRID_COLOR } },
  },
};
```

`src/stores/chartToggleStore.ts`:
```ts
import { create } from "zustand";

interface ChartToggleState {
  modelChartMode: "tokens" | "cost";
  setModelChartMode: (m: "tokens" | "cost") => void;
}

export const useChartToggleStore = create<ChartToggleState>((set) => ({
  modelChartMode: "tokens",
  setModelChartMode: (m) => set({ modelChartMode: m }),
}));
```

`ModelDistributionChart.tsx`:
```tsx
import { Bar } from "react-chartjs-2";
import { useModels } from "@/api/hooks/useModels";
import { useFilterKey } from "@/stores/filtersStore";
import { useChartToggleStore } from "@/stores/chartToggleStore";
import { darkThemeOptions } from "@/lib/chartConfig";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export default function ModelDistributionChart() {
  const filters = useFilterKey();
  const { data } = useModels(filters);
  const mode = useChartToggleStore((s) => s.modelChartMode);
  const setMode = useChartToggleStore((s) => s.setModelChartMode);

  const models = data?.models ?? [];
  const labels = models.map((m) => m.model);
  const values = models.map((m) => mode === "tokens" ? m.total_tokens : (m as any).estimated_cost ?? 0);

  return (
    <div>
      <div className="mb-2 flex gap-1">
        <Button size="sm" variant={mode === "tokens" ? "default" : "ghost"} onClick={() => setMode("tokens")}>Token</Button>
        <Button size="sm" variant={mode === "cost" ? "default" : "ghost"} onClick={() => setMode("cost")}>Cost</Button>
      </div>
      <Bar data={{ labels, datasets: [{ label: mode, data: values, backgroundColor: "hsl(187 92% 58%)" }] }} options={darkThemeOptions} />
    </div>
  );
}
```

`TokenTrendChart.tsx`: similar pattern, using `useTimeseries`.

`ChartPanel.tsx`: wraps a chart with a title header.

**Verify green**:
```bash
pnpm test
pnpm build
```

Commit: `feat(charts): Model Distribution + Token Trend — green`

### Step 3 — Refactor: accessibility — chart fallback table

Per the legacy `cleanroom-design.md` a11y list, each canvas chart has a
`<table>` fallback for screen readers. Add a `ChartWithTable` wrapper that
renders the data in a visually-hidden `<table>` alongside the `<canvas>`.

Add a test that asserts the table exists with the right summary.

Commit: `a11y(charts): add screen-reader table fallback`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/charts` |
| 5 | Integration Tests | Vite dev → real data → chart bars render |
| 6 | Functional Tests | Click Token/Cost toggle → bars re-render with new values |
| 7 | Contract Tests | `useModels` return shape matches `/api/v1/models` schema |
| 8 | E2E | N/A (composed in 3.8) |
| 9 | Code Review | Chart.js imported via tree-shakeable `chart.js/auto` alternative (manual register) |

All green → Ticket 3.8.
