# Ticket 5.1 — CSV export button

**Phase**: 5 — Polish
**Blocks**: 5.4
**Blocked by**: Phase 4 complete
**Files touched**:
- `tools/usage-dashboard/frontend/src/api/hooks/useExport.ts` (new)
- `tools/usage-dashboard/frontend/src/components/UsageFilterBar.tsx` (wire onExport)
- `tools/usage-dashboard/frontend/src/components/__tests__/useExport.test.tsx` (new)

---

## 🎯 Goal

The Export CSV button in the Usage filter bar downloads a CSV from
`/api/v1/export` (FastAPI endpoint added in Ticket 1.6) using current
filters.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: useExport test

```tsx
// frontend/src/components/__tests__/useExport.test.tsx
import { renderHook, act } from "@testing-library/react";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useExport } from "@/api/hooks/useExport";

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true, status: 200,
    text: async () => "timestamp,model\n2026-01-01,gpt-4",
    headers: { get: (k: string) => k === "content-disposition" ? 'attachment; filename="usage_export.csv"' : null },
    blob: async () => new Blob(["x"], { type: "text/csv" }),
  })) as any;
});

describe("useExport", () => {
  it("downloads a CSV blob", async () => {
    const createObjectURL = vi.fn(() => "blob:fake");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });

    const qc = new QueryClient();
    const wrapper = ({ children }: any) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useExport(), { wrapper });
    await act(async () => { await result.current.export({ range: "24h" }); });
    expect(createObjectURL).toHaveBeenCalled();
  });
});
```

Commit: `test(export): red — useExport downloads CSV`

### Step 2 — Green: implement hook

```ts
// frontend/src/api/hooks/useExport.ts
import { useState } from "react";

export function useExport() {
  const [downloading, setDownloading] = useState(false);

  async function exportCsv(params: Record<string, string | string[] | undefined>, token?: string) {
    setDownloading(true);
    try {
      const url = new URL("/api/v1/export", window.location.origin);
      for (const [k, v] of Object.entries(params)) {
        if (v === undefined) continue;
        if (Array.isArray(v)) v.forEach((x) => url.searchParams.append(k, String(x)));
        else url.searchParams.set(k, String(v));
      }
      const headers: Record<string, string> = {};
      if (token) headers["X-Dashboard-Token"] = token;
      const resp = await fetch(url, { headers });
      if (!resp.ok) throw new Error(`${resp.status}`);
      const blob = await resp.blob();
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = "usage_export.csv";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(objUrl);
    } finally {
      setDownloading(false);
    }
  }

  return { export: exportCsv, downloading };
}
```

Wire into `UsageFilterBar`:
```tsx
const { export: exportCsv, downloading } = useExport();
// In render:
<Button size="sm" disabled={downloading} onClick={() => exportCsv(filterKey)}>
  {downloading ? "导出中…" : "Export CSV"}
</Button>
```

**Verify green**:
```bash
pnpm test src/components/__tests__/useExport.test.tsx
```

Commit: `feat(export): CSV download button — green`

### Step 3 — Refactor: shared query string builder

The export hook duplicates the query string building in `apiGet`. Extract
a helper:

```ts
// frontend/src/api/queryString.ts
export function buildQuery(params: Record<string, string | string[] | undefined>): URLSearchParams {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined) continue;
    if (Array.isArray(v)) v.forEach((x) => sp.append(k, String(x)));
    else sp.set(k, String(v));
  }
  return sp;
}
```

Use in both `client.ts` and `useExport.ts`.

Commit: `refactor(api): shared query string builder`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test src/components/__tests__/useExport.test.tsx` |
| 5 | Integration Tests | Vite dev → real back end → click Export → browser downloads CSV |
| 6 | Functional Tests | CSV opens in Excel/Numbers with correct headers |
| 7 | Contract Tests | URL matches `/api/v1/export` route; filter params serialized |
| 8 | E2E | (covered in 5.4 cutover E2E) |
| 9 | Code Review | Memory leak prevention: `revokeObjectURL` called |

All green → Ticket 5.4.
