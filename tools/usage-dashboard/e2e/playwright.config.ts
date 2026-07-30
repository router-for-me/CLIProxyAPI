import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  timeout: 30_000,
  workers: process.env.CI ? 1 : 2,
  use: {
    baseURL: "http://127.0.0.1:5174",
    trace: "retain-on-failure",
    video: "retain-on-failure",
  },
  webServer: [
    {
      command:
        'USAGE_DASHBOARD_DATA_DIR=$(uv run python e2e/fixtures/seed.py) uv run python usage_dashboard.py serve',
      cwd: __dirname + "/..",
      port: 8321,
      timeout: 30_000,
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      stderr: "pipe",
    },
    {
      command: "pnpm dev --port 5174",
      cwd: __dirname + "/../frontend",
      port: 5174,
      timeout: 30_000,
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      stderr: "pipe",
    },
  ],
});