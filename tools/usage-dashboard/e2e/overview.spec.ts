import { test, expect } from "@playwright/test";

test.describe("Overview page", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/");
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

  test("shows 8 KPI cards with labels", async ({ page }) => {
    const labels = [
      "API Keys",
      "Accounts",
      "Today Requests",
      "Active Keys",
      "Today Tokens",
      "Total Tokens",
      "Performance",
      "Avg Response",
    ];
    for (const label of labels) {
      await expect(page.getByText(label, { exact: true })).toBeVisible();
    }
  });

  test("KPI cards have 8 value elements", async ({ page }) => {
    const values = page.locator(".text-xl");
    await expect(values).toHaveCount(8);
  });

  test("shows both chart panels", async ({ page }) => {
    await expect(page.getByText("Model Distribution")).toBeVisible();
    await expect(page.getByText("Token Usage Trend")).toBeVisible();
  });

  test("charts render canvas elements", async ({ page }) => {
    const canvases = page.locator("canvas");
    await expect(canvases.first()).toBeVisible();
    await expect(canvases.nth(1)).toBeVisible();
  });

  test("health badge shows a status", async ({ page }) => {
    await expect(page.getByText("healthy")).toBeVisible();
  });

  test("recent usage table has 12 rows", async ({ page }) => {
    await expect(page.getByText("Recent Usage")).toBeVisible();
    const rows = page.locator("table tbody tr");
    await expect(rows).toHaveCount(12);
  });

  test("clicking a row opens the detail drawer", async ({ page }) => {
    // Click the first row in the table
    const firstRow = page.locator("table tbody tr").first();
    await firstRow.click();

    // Wait for the drawer to open
    await expect(page.getByText("请求详情")).toBeVisible();

    // Drawer should show request detail data
    await expect(page.locator('[role="dialog"] pre')).toBeVisible();
  });

  test("changing range updates KPIs", async ({ page }) => {
    // Get the initial value of Today Requests
    const card = page.getByText("Today Requests").locator("..");
    const initialText = await card.locator(".text-xl").textContent();

    // Open the custom combobox dropdown
    const combobox = page.getByRole("combobox").first();
    await combobox.click();

    // Click the "近 1 小时" option from the popup
    const option = page.getByRole("option", { name: "近 1 小时" });
    await expect(option).toBeVisible();
    await option.click();

    // Wait for refetch
    await page.waitForTimeout(1500);

    // Get the new value
    const newText = await card.locator(".text-xl").textContent();

    // Value should change (1h range has fewer events than 24h)
    expect(newText).not.toEqual(initialText);
  });
});