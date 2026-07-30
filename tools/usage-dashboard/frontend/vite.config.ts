import path from "node:path";

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  plugins: [react(), tailwindcss()],
  server: {
    host: true,
    port: 8320,
    proxy: {
      "/api": "http://127.0.0.1:8321",
      "/static": "http://127.0.0.1:8321",
    },
  },
  // @ts-expect-error - test config is a vitest extension
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.tsx"],
  },
});