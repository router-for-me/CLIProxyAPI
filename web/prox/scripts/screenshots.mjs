// Captures dashboard screenshots from a running demo stack (make demo).
// Usage: node scripts/screenshots.mjs [outDir] [pages...]
import { chromium } from "playwright";
import { mkdirSync } from "node:fs";

const base = process.env.QR_URL ?? "http://127.0.0.1:18318/v0/resource/plugins/kpatdev-quota-router/index.html";
const key = process.env.QR_KEY ?? "demo-management-key";
const out = process.argv[2] ?? "../docs/screenshots";
const pages = process.argv.slice(3).length ? process.argv.slice(3) : ["overview", "accounts", "sessions", "activity", "usage", "settings"];
const viewports = {
  desktop: { viewport: { width: 1440, height: 960 }, deviceScaleFactor: 2 },
  tablet: { viewport: { width: 900, height: 1180 }, deviceScaleFactor: 2 },
  mobile: { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true },
};
const only = (process.env.QR_VIEWPORTS ?? "desktop,mobile").split(",");
const themes = (process.env.QR_THEMES ?? "dark,light").split(",");

mkdirSync(out, { recursive: true });
const browser = await chromium.launch();
for (const [vpName, vp] of Object.entries(viewports)) {
  if (!only.includes(vpName)) continue;
  for (const theme of themes) {
    const ctx = await browser.newContext({ ...vp, colorScheme: theme });
    await ctx.addInitScript((t) => localStorage.setItem("qr-theme", t), theme);
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
    await page.goto(base);
    await page.fill("#key", key);
    await page.click("button[type=submit]");
    await page.waitForSelector(".sidebar");
    for (const p of pages) {
      await page.evaluate((r) => (location.hash = `/${r}`), p);
      await page.waitForTimeout(1800);
      const file = `${out}/${p}-${vpName}-${theme}.png`;
      await page.screenshot({ path: file, fullPage: process.env.QR_FULL === "1" });
      console.log(file);
    }
    if (errors.length) console.error(`${vpName}/${theme} console errors:\n  ${errors.join("\n  ")}`);
    await ctx.close();
  }
}
await browser.close();
