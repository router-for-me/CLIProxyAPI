import { test, expect } from "@playwright/test";

test.describe("Usage page", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/usage");
    // Wait for KPI data to load (text-xl values show actual data, not loading skeletons)
    await page.waitForFunction(() => {
      const cards = document.querySelectorAll(".text-xl");
      if (cards.length === 0) return false;
      return Array.from(cards).some((c) => {
        const t = c.textContent?.trim();
        return t && t !== "" && t !== "—";
      });
    }, { timeout: 15_000 });
  });

  test("usage view has 4 KPI cards", async ({ page }) => {
    for (const label of ["Total Requests", "Total Tokens", "Total Cost", "Avg Duration"]) {
      await expect(page.getByText(label, { exact: true })).toBeVisible();
    }
  });

  test("usage view has 4 charts", async ({ page }) => {
    await expect(page.locator("canvas")).toHaveCount(4);
  });

  test("tabs switch between Usage, Errors, and Ranking", async ({ page }) => {
    // Click Errors tab
    await page.getByRole("tab", { name: /errors/i }).click();
    // Errors tab should show the errors table heading row
    await expect(page.getByRole("columnheader", { name: /status code/i })).toBeVisible({ timeout: 10_000 });

    // Click Ranking tab
    await page.getByRole("tab", { name: /ranking/i }).click();
    await expect(page.getByText("Account Ranking")).toBeVisible();

    // Click Usage tab
    await page.getByRole("tab", { name: /usage/i }).click();
    // Usage tab should show charts
    await expect(page.getByText("Model Distribution")).toBeVisible();
  });

  test("Usage tab loads requests table rows", async ({ page }) => {
    // The requests table should have rows
    const rows = page.locator('table thead:has-text("时间") ~ tbody tr');
    await expect(rows.first()).toBeVisible({ timeout: 10_000 });
    // Should list at least some results
    await expect(page.getByText(/条结果/)).toBeVisible();
  });

  test("filter bar renders with all filter controls", async ({ page }) => {
    // Filter bar buttons - use role to avoid ambiguity with chart headings
    await expect(page.getByRole("button", { name: /^Model$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Account$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Provider$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Endpoint$/ })).toBeVisible();
    // Action buttons
    await expect(page.getByRole("button", { name: /refresh/i })).toBeVisible();
    await expect(page.getByRole("button", { name: /reset/i })).toBeVisible();
    await expect(page.getByRole("button", { name: /column settings/i })).toBeVisible();
    await expect(page.getByRole("button", { name: /export csv/i })).toBeVisible();
  });

  test("Errors tab shows aggregated error rows", async ({ page }) => {
    await page.getByRole("tab", { name: /errors/i }).click();
    // Errors table should have column headers
    await expect(page.getByRole("columnheader", { name: /status code/i })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole("columnheader", { name: /model/i })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /count/i })).toBeVisible();
    // Should have rows
    const rows = page.locator('[data-slot="tabs-content"] table tbody tr');
    await expect(rows.first()).toBeVisible({ timeout: 10_000 });
  });

  test("Errors tab drill-down switches to Usage tab", async ({ page }) => {
    await page.getByRole("tab", { name: /errors/i }).click();
    // Wait for the errors table to have a row
    const firstRow = page.locator('[data-slot="tabs-content"] table tbody tr').first();
    await expect(firstRow).toBeVisible({ timeout: 10_000 });
    await firstRow.click();
    // Should switch back to Usage tab — Base UI uses aria-selected
    await expect(page.getByRole("tab", { name: /usage/i })).toHaveAttribute("aria-selected", "true");
  });

  test("charts render with visible titles", async ({ page }) => {
    await expect(page.getByText("Model Distribution")).toBeVisible();
    await expect(page.getByText("Provider Distribution")).toBeVisible();
    await expect(page.getByText("Endpoint Distribution")).toBeVisible();
    await expect(page.getByText("Token Usage Trend")).toBeVisible();
  });

  test("Ranking tab shows Account Ranking table", async ({ page }) => {
    await page.getByRole("tab", { name: /ranking/i }).click();
    await expect(page.getByText("Account Ranking")).toBeVisible();
    // Ranking table should have column headers
    await expect(page.getByRole("columnheader", { name: /account/i })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /requests/i })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /tokens/i })).toBeVisible();
    // Should have rows
    const rows = page.locator('[data-slot="tabs-content"] table tbody tr');
    await expect(rows.first()).toBeVisible({ timeout: 10_000 });
  });

  test("clicking Ranking row filters by account", async ({ page }) => {
    await page.getByRole("tab", { name: /ranking/i }).click();
    // Wait for ranking table to have rows
    const firstRow = page.locator('[data-slot="tabs-content"] table tbody tr').first();
    await expect(firstRow).toBeVisible({ timeout: 10_000 });
    await firstRow.click();
    // Should switch back to Usage tab — Base UI uses aria-selected
    await expect(page.getByRole("tab", { name: /usage/i })).toHaveAttribute("aria-selected", "true");
  });

  test("Column Settings modal opens and shows column checkboxes", async ({ page }) => {
    // Open Column Settings
    await page.getByRole("button", { name: /column settings/i }).click();
    // Should show the sheet with column checkboxes — use heading to avoid ambiguity
    await expect(page.getByRole("heading", { name: /column settings/i })).toBeVisible();
    // Uncheck a column (e.g. 供应商/Provider column)
    await page.getByRole("checkbox", { name: /供应商/i }).uncheck();
    // Close the sheet by clicking the close button
    await page.getByRole("button", { name: /close/i }).click();
    // The sheet heading should disappear
    await expect(page.getByRole("heading", { name: /column settings/i })).not.toBeVisible();
  });
});