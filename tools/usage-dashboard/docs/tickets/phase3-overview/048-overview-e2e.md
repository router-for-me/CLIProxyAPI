# Ticket 3.8 — Overview E2E (Playwright)

**Phase**: 3 — Overview
**Blocks**: Phase 4
**Blocked by**: 3.4, 3.5, 3.6, 3.7
**Files touched**:
- `tools/usage-dashboard/e2e/playwright.config.ts` (new)
- `tools/usage-dashboard/e2e/fixtures/seed.ts` (new — seeds a fixture DB)
- `tools/usage-dashboard/e2e/overview.spec.ts` (new)
- `tools/usage-dashboard/Makefile` (add `make e2e`)
- `tools/usage-dashboard/frontend/package.json` (add `@playwright/test` as devDependency in repo root or here)

---

## 🎯 Goal

Playwright drives the React `/` view against a seeded fixture DB. Asserts:

1. All 8 KPI cards render with non-empty values.
2. Both charts render canvas elements.
3. Recent Usage table has 12 rows.
4. Clicking a row opens the detail drawer with the right content.
5. Health badge shows a status (not "—").
6. Changing the range selector refetches data and updates at least one KPI.

This is the **acceptance gate** for Phase 3.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. Write the spec first, watch it fail, then make it
pass by fixing the implementation (or the test if it was wrong).

---

## 🪜 Steps

### Step 1 — Red: install Playwright + write spec

```bash
cd tools/usage-dashboard
pnpm dlx playwright install --with-deps chromium
pnpm add -D @playwright/test
```

`e2e/playwright.config.ts`:
```ts
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  use: {
    baseURL: "http://127.0.0.1:8320",
    trace: "retain-on-failure",
    video: "retain-on-failure",
  },
  webServer: {
    command: "uv run python usage_dashboard.py run",
    port: 8320,
    timeout: 30_000,
    reuseExistingServer: !process.env.CI,
  },
});
```

`e2e/fixtures/seed.ts` — seeds the fixture DB before tests run. Uses a
dedicated `USAGE_DASHBOARD_DATA_DIR` env var pointing at a temp dir.

`e2e/overview.spec.ts`:
```ts
import { test, expect } from "@playwright/test";

test.beforeAll(async () => {
  // Seed via /api/v1/internal/seed (test-only endpoint) or via direct SQLite.
});

test("overview shows 8 KPI cards", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("API Keys")).toBeVisible();
  await expect(page.getByText("Accounts")).toBeVisible();
  await expect(page.getByText("Today Requests")).toBeVisible();
  await expect(page.getByText("Active Keys")).toBeVisible();
  await expect(page.getByText("Today Tokens")).toBeVisible();
  await expect(page.getByText("Total Tokens")).toBeVisible();
  await expect(page.getByText("Performance")).toBeVisible();
  await expect(page.getByText("Avg Response")).toBeVisible();
});

test("overview shows both charts", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("canvas").first()).toBeVisible();
  await expect(page.locator("canvas").nth(1)).toBeVisible();
});

test("recent usage table has 12 rows and clicking opens drawer", async ({ page }) => {
  await page.goto("/");
  const rows = page.locator("main table tbody tr");
  await expect(rows).toHaveCount(12);
  await rows.first().click();
  await expect(page.getByText(/请求详情|Request Detail/i)).toBeVisible();
});

test("changing range updates KPIs", async ({ page }) => {
  await page.goto("/");
  const reqsBefore = await page.getByText("Today Requests").locator("..").locator(".text-xl").textContent();
  await page.getByRole("combobox").first().selectOption("1h");
  // Wait for refetch
  await page.waitForTimeout(1000);
  const reqsAfter = await page.getByText("Today Requests").locator("..").locator(".text-xl").textContent();
  // Value should change (1h typically smaller than 24h)
  expect(reqsBefore).not.toEqual(reqsAfter);
});
```

**Verify red**:
```bash
cd tools/usage-dashboard && pnpm exec playwright test
```

Commit: `test(e2e): red — overview spec`

### Step 2 — Green: fix any implementation gaps

The spec exercises Tickets 3.4–3.7. If any step fails because of an
implementation bug, fix the implementation. If a test is wrong (e.g., a
selector mismatch), fix the test.

**Verify green**:
```bash
make e2e   # new Makefile target
```

Commit: `test(e2e): overview spec — green`

### Step 3 — Refactor: seed helper + parallelize

Factor the seed logic into `e2e/fixtures/seed.ts` so Phase 4 can reuse it.
Configure Playwright workers:
```ts
export default defineConfig({
  workers: process.env.CI ? 1 : 2,
  // ...
});
```

Commit: `refactor(e2e): seed helper + worker config`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` (Playwright specs are TS) |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test` (existing component tests) |
| 5 | Integration Tests | `pnpm exec playwright test` — all overview specs pass |
| 6 | Functional Tests | Manual click-through matches E2E assertions |
| 7 | Contract Tests | N/A (E2E covers the full contract) |
| 8 | E2E | `make e2e` green against a seeded fixture DB |
| 9 | Code Review | Specs are deterministic; no flakiness from polling |

All green → **Phase 3 complete**. Move to Phase 4.
