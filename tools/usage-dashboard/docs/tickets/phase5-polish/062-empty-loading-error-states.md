# Ticket 5.2 — Empty / loading / error states for every panel

**Phase**: 5 — Polish
**Blocks**: 5.4
**Blocked by**: Phase 4 complete
**Files touched**:
- `tools/usage-dashboard/frontend/src/components/StatePanel.tsx` (new — shared)
- `tools/usage-dashboard/frontend/src/components/ErrorRetry.tsx` (new)
- All chart/table components updated to use `StatePanel`
- `tools/usage-dashboard/frontend/src/components/__tests__/StatePanel.test.tsx` (new)

---

## 🎯 Goal

Every panel (KPI cards, charts, tables) handles three states consistently:

1. **Empty**: data loaded but has zero rows/items → "暂无数据" with an icon.
2. **Loading**: first load in progress → skeleton (pulsing rectangle).
3. **Error**: query failed → "加载失败" + Retry button that invalidates the query.

No component silently shows `—` on error.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: StatePanel test

```tsx
// frontend/src/components/__tests__/StatePanel.test.tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import { StatePanel } from "../StatePanel";

describe("StatePanel", () => {
  it("shows children when loaded with data", () => {
    render(<StatePanel loading={false} error={null} isEmpty={false}>content</StatePanel>);
    expect(screen.getByText("content")).toBeInTheDocument();
  });

  it("shows skeleton when loading", () => {
    render(<StatePanel loading={true} error={null} isEmpty={false}>content</StatePanel>);
    expect(document.querySelector("[aria-busy='true']")).toBeInTheDocument();
    expect(screen.queryByText("content")).not.toBeInTheDocument();
  });

  it("shows error + Retry when errored", () => {
    render(<StatePanel loading={false} error={new Error("oops")} isEmpty={false}
                       onRetry={() => {}}>content</StatePanel>);
    expect(screen.getByText(/加载失败/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry|重试/i })).toBeInTheDocument();
  });

  it("shows empty state when isEmpty", () => {
    render(<StatePanel loading={false} error={null} isEmpty={true}>content</StatePanel>);
    expect(screen.getByText(/暂无数据/i)).toBeInTheDocument();
  });

  it("Retry click calls onRetry", async () => {
    const onRetry = vi.fn();
    render(<StatePanel loading={false} error={new Error("x")} isEmpty={false}
                       onRetry={onRetry}>x</StatePanel>);
    await userEvent.click(screen.getByRole("button", { name: /retry|重试/i }));
    expect(onRetry).toHaveBeenCalled();
  });
});
```

Commit: `test(state-panel): red — three states + retry`

### Step 2 — Green: implement StatePanel

```tsx
// frontend/src/components/StatePanel.tsx
import { AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";

interface Props {
  loading: boolean;
  error: Error | null;
  isEmpty: boolean;
  onRetry?: () => void;
  children: React.ReactNode;
  skeletonClassName?: string;
}

export function StatePanel({ loading, error, isEmpty, onRetry, children, skeletonClassName }: Props) {
  if (loading) {
    return <div aria-busy="true"
                className={`animate-pulse rounded bg-muted ${skeletonClassName ?? "h-32 w-full"}"} />;
  }
  if (error) {
    return (
      <div className="flex flex-col items-center gap-2 p-4 text-muted-foreground">
        <AlertCircle className="h-8 w-8 text-red-500" />
        <div>加载失败</div>
        {onRetry && <Button size="sm" variant="ghost" onClick={onRetry}>重试</Button>}
      </div>
    );
  }
  if (isEmpty) {
    return <div className="p-4 text-center text-muted-foreground">暂无数据</div>;
  }
  return <>{children}</>;
}
```

Wrap every chart and table:
```tsx
<StatePanel loading={isLoading} error={error} isEmpty={!data || rows.length === 0}
            onRetry={() => refetch()}>
  <Bar data={...} options={...} />
</StatePanel>
```

For tables, the empty state replaces the "加载中…" row in `<tbody>`.

**Verify green**:
```bash
pnpm test
```

Commit: `feat(state-panel): shared empty/loading/error states — green`

### Step 3 — Refactor: error boundary at route level

Add a React Error Boundary around each route so an uncaught render error
does not blank the whole app:

```tsx
// frontend/src/components/RouteErrorBoundary.tsx
import { Component, ReactNode } from "react";

export class RouteErrorBoundary extends Component<{children: ReactNode}, {error: Error | null}> {
  state = { error: null };
  static getDerivedStateFromError(error: Error) { return { error }; }
  render() {
    if (this.state.error) {
      return <div className="p-4 text-red-500">页面加载失败: {this.state.error.message}</div>;
    }
    return this.props.children;
  }
}
```

Wrap each `<Route element={<X />}/>` with the boundary.

Commit: `feat(state-panel): route-level error boundary`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/StatePanel.test.tsx` |
| 5 | Integration Tests | Stop back end mid-session → panels show error + retry |
| 6 | Functional Tests | Empty fixture DB → all panels show "暂无数据" |
| 7 | Contract Tests | N/A |
| 8 | E2E | N/A (composed in 5.4) |
| 9 | Code Review | Every panel uses StatePanel; no raw `if (loading) return null` |

All green → Ticket 5.4.
