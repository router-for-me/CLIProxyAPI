# Ticket 4.7 — Usage view E2E (Playwright)

**Phase**: 4 — Usage detail
**Blocks**: Phase 5
**Blocked by**: 4.3, 4.4, 4.5, 4.6
**Files touched**:
- `tools/usage-dashboard/e2e/usage.spec.ts` (new)
- `tools/usage-dashboard/e2e/fixtures/seed.ts` (extend for failed events + multiple accounts/providers)

---

## 🎯 Goal

Playwright spec exercises every interactive element of `/usage`:

1. All 4 KPI cards render with values.
2. All 4 charts render canvas.
3. Filter bar: selecting a model filters the Usage table.
4. Tabs switch between Usage / Errors / Ranking.
5. Usage tab: 50 rows load, Load More fetches next page.
6. Errors tab: aggregated rows render; clicking drills to Usage with filter.
7. Ranking tab: rows sorted by tokens; clicking filters Usage.
8. Column Settings modal hides a column.

This is the **acceptance gate** for Phase 4.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: write the spec

`e2e/usage.spec.ts`:
```ts
import { test, expect } from "@playwright/test";

test("usage view has 4 KPI cards", async ({ page }) => {
  await page.goto("/usage");
  for (const label of ["Total Requests", "Total Tokens", "Total Cost", "Avg Duration"]) {
    await expect(page.getByText(label)).toBeVisible();
  }
});

test("usage view has 4 charts", async ({ page }) => {
  await page.goto("/usage");
  await expect(page.locator("canvas")).toHaveCount(4);
});

test("tabs switch", async ({ page }) => {
  await page.goto("/usage");
  await page.getByRole("tab", { name: /errors/i }).click();
  await expect(page.getByRole("heading", { name: /error/i })).toBeVisible();
  await page.getByRole("tab", { name: /ranking/i }).click();
  await expect(page.getByRole("heading", { name: /ranking/i })).toBeVisible();
});

test("Usage tab loads 50 rows and Load More works", async ({ page }) => {
  await page.goto("/usage");
  await expect(page.locator("main table tbody tr")).toHaveCount(50);
  await page.getByRole("button", { name: /load more/i }).click();
  await expect(page.locator("main table tbody tr")).toHaveCount(100);
});

test("model filter narrows the table", async ({ page }) => {
  await page.goto("/usage");
  await page.getByText("Model").click();
  await page.getByRole("option", { name: "gpt-4" }).click();
  // Every visible row mentions gpt-4 in the Model column
  const rows = page.locator("main table tbody tr");
  const count = await rows.count();
  for (let i = 0; i < count; i++) {
    const cells = rows.nth(i).locator("td");
    expect(await cells.nth(2).textContent()).toContain("gpt-4");
  }
});

test("Errors tab drill-down switches to Usage with filter", async ({ page }) => {
  await page.goto("/usage");
  await page.getByRole("tab", { name: /errors/i }).click();
  await page.locator("main table tbody tr").first().click();
  // Back on Usage tab
  await expect(page.getByRole("tab", { name: /usage/i })).toHaveAttribute("aria-selected", "true");
});

test("Ranking tab click filters Usage by account", async ({ page }) => {
  await page.goto("/usage");
  await page.getByRole("tab", { name: /ranking/i }).click();
  await page.locator("main table tbody tr").first().click();
  await expect(page.getByRole("tab", { name: /usage/i })).toHaveAttribute("aria-selected", "true");
});

test("Column Settings hides a column", async ({ page }) => {
  await page.goto("/usage");
  await page.getByRole("button", { name: /column settings/i }).click();
  await page.getByRole("checkbox", { name: /endpoint/i }).uncheck();
  await page.getByRole("button", { name: /close/i }).click();
  await expect(page.getByRole("columnheader", { name: /endpoint/i })).toHaveCount(0);
});
```

**Verify red**:
```bash
cd tools/usage-dashboard && pnpm exec playwright test e2e/usage.spec.ts
```

Commit: `test(e2e-usage): red — all interactions`

### Step 2 — Green: fix gaps

Spec failures reveal implementation gaps. Fix each one in the relevant
component. Re-run until green.

**Verify green**:
```bash
make e2e
```

Commit: `test(e2e-usage): usage view spec — green`

### Step 3 — Refactor: parallelize spec files

Configure Playwright to run `overview.spec.ts` and `usage.spec.ts` in
parallel (separate browser contexts). Confirm no shared DB state issues.

Commit: `perf(e2e): parallelize spec files`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test` |
| 5 | Integration Tests | `pnpm exec playwright test` — both overview and usage specs pass |
| 6 | Functional Tests | Manual click-through of every tab |
| 7 | Contract Tests | E2E spec is the contract for behavior parity with legacy |
| 8 | E2E | `make e2e` green against fixture DB |
| 9 | Code Review | Specs deterministic; seed produces reproducible data |

All green → **Phase 4 complete**. Move to Phase 5.
