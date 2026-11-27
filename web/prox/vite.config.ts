import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The bundle is embedded in the Go plugin (internal/prox/mgmt/dist) and served from
// /prox/ui/, so asset URLs must be relative.
export default defineConfig({
  base: "./",
  plugins: [react()],
  build: {
    outDir: "../../internal/prox/mgmt/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
    modulePreload: { polyfill: false },
    target: "es2022",
  },
  server: {
    // `bun run dev` talks to a local prox instance.
    proxy: { "/prox/api": "http://127.0.0.1:8317", "/v0": "http://127.0.0.1:8317" },
  },
  test: { environment: "jsdom" },
} as never);
