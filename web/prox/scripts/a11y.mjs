// Runs axe (WCAG 2.1 AA) on every dashboard page, in both themes, against a running demo stack (make demo).
// Usage: node scripts/a11y.mjs   — exits non-zero on any violation.
import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";

const base = process.env.QR_URL ?? "http://127.0.0.1:18318/v0/resource/plugins/kpatdev-quota-router/index.html";
const key = process.env.QR_KEY ?? "demo-management-key";
const pages = ["overview", "accounts", "sessions", "activity", "usage", "settings"];
const viewports = { desktop: { width: 1440, height: 960 }, mobile: { width: 390, height: 844 } };

const browser = await chromium.launch();
let failures = 0;
for (const theme of ["dark", "light"]) {
  for (const [vpName, viewport] of Object.entries(viewports)) {
    const ctx = await browser.newContext({ viewport, colorScheme: theme });
    await ctx.addInitScript((t) => localStorage.setItem("qr-theme", t), theme);
    const page = await ctx.newPage();
    await page.goto(base);
    const login = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
    failures += report(`login ${vpName}/${theme}`, login.violations);
    await page.fill("#key", key);
    await page.click("button[type=submit]");
    await page.waitForSelector(".sidebar");
    for (const p of pages) {
      await page.evaluate((r) => (location.hash = `/${r}`), p);
      await page.waitForTimeout(1500);
      const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
      failures += report(`${p} ${vpName}/${theme}`, r.violations);
    }
    await ctx.close();
  }
}
await browser.close();
console.log(failures ? `${failures} violation(s)` : "PASS: no axe WCAG 2.1 AA violations");
process.exit(failures ? 1 : 0);

function report(label, violations) {
  for (const v of violations) {
    console.log(`${label}: [${v.impact}] ${v.id} — ${v.help} (${v.nodes.length} node(s))`);
    for (const n of v.nodes.slice(0, 3)) console.log(`    ${n.target.join(" ")}  ${(n.failureSummary ?? "").split("\n").slice(1, 2).join("")}`);
  }
  return violations.length;
}
