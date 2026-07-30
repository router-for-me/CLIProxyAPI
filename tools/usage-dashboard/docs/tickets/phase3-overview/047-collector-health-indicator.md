# Ticket 3.7 — Collector health indicator

**Phase**: 3 — Overview
**Blocks**: 3.8
**Blocked by**: 3.2
**Files touched**:
- `tools/usage-dashboard/frontend/src/api/hooks/useCollectorHealth.ts` (new)
- `tools/usage-dashboard/frontend/src/components/HealthBadge.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/__tests__/HealthBadge.test.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/Header.tsx` (add HealthBadge)

---

## 🎯 Goal

A small badge in the Header shows collector health: green dot +
"healthy" / red dot + "degraded" + last error. Polled every 5 seconds via
`useCollectorHealth`.

Maps the legacy `is_authorized`/`snapshot` shape to the React UI.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: HealthBadge test

```tsx
// frontend/src/components/__tests__/HealthBadge.test.tsx
import { render, screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import { HealthBadge } from "../HealthBadge";

describe("HealthBadge", () => {
  it("renders green dot when healthy", () => {
    render(<HealthBadge ok={true} lastPollAt="2026-01-01T00:00:00Z" />);
    expect(screen.getByText(/healthy|健康/i)).toBeInTheDocument();
    expect(document.querySelector(".bg-green-500")).toBeInTheDocument();
  });

  it("renders red dot when degraded", () => {
    render(<HealthBadge ok={false} lastPollAt="" error="connection refused" />);
    expect(screen.getByText(/degraded|异常/i)).toBeInTheDocument();
    expect(document.querySelector(".bg-red-500")).toBeInTheDocument();
  });

  it("renders unknown before first poll", () => {
    render(<HealthBadge ok={null} lastPollAt="" />);
    expect(screen.getByText(/—|unknown|未知/i)).toBeInTheDocument();
  });
});
```

Commit: `test(health): red — HealthBadge renders states`

### Step 2 — Green: implement hook + component

```ts
// frontend/src/api/hooks/useCollectorHealth.ts
import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";

export type Health = {
  last_poll_at?: string;
  last_poll_ok?: boolean;
  last_poll_error?: string;
  management_configured?: boolean;
};

export function useCollectorHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: () => apiGet("/api/v1/health"),
    refetchInterval: 5_000,
    staleTime: 5_000,
  });
}
```

```tsx
// frontend/src/components/HealthBadge.tsx
import { cn } from "@/lib/utils";
import { useCollectorHealth } from "@/api/hooks/useCollectorHealth";

interface Props { ok: boolean | null; lastPollAt: string; error?: string; }

export function HealthBadge(props: Props) {
  const dot = props.ok === null ? "bg-muted-foreground"
            : props.ok ? "bg-green-500" : "bg-red-500";
  const label = props.ok === null ? "—"
              : props.ok ? "healthy"
              : `degraded${props.error ? `: ${props.error}` : ""}`;
  return (
    <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
      <span className={cn("h-2 w-2 rounded-full", dot)} />
      {label}
    </span>
  );
}

export function HealthBadgeLive() {
  const { data } = useCollectorHealth();
  return <HealthBadge ok={data?.last_poll_ok ?? null}
                      lastPollAt={data?.last_poll_at ?? ""}
                      error={data?.last_poll_error} />;
}
```

Add `<HealthBadgeLive />` to the Header.

**Verify green**:
```bash
pnpm test
```

Commit: `feat(health): collector health badge — green`

### Step 3 — Refactor: aria-live for screen readers

```tsx
<span role="status" aria-live="polite" aria-atomic="true">
  <HealthBadgeLive />
</span>
```

Add a test that confirms the `aria-live` region.

Commit: `a11y(health): aria-live region for status updates`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/HealthBadge.test.tsx` |
| 5 | Integration Tests | Vite dev → back end reports degraded → badge turns red within 5s |
| 6 | Functional Tests | Stop collector → badge updates within poll interval |
| 7 | Contract Tests | `Health` type matches `/api/v1/health` response schema |
| 8 | E2E | N/A (composed in 3.8) |
| 9 | Code Review | Polling interval reasonable (5s); no thundering herd |

All green → Ticket 3.8.
