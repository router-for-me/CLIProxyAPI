import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// NixLLM Dashboard — Vite config.
//
// Dev server runs on port 9173. The /v0 API calls are proxied to the
// NixLLM Go server (default :8317) so the dashboard can authenticate
// against the existing /v0/management routes using MANAGEMENT_PASSWORD,
// avoiding CORS in development.
//
// Override the upstream port with `VITE_API_HOST` if your dev server
// does not run on 8317.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 9173,
    host: true,
    strictPort: true,
    proxy: {
      // Management API (used for API Keys, Usage Stats, Pricing CRUD).
      '/v0': {
        target: process.env.VITE_API_HOST || 'http://127.0.0.1:8317',
        changeOrigin: true,
        secure: false,
      },
      // Caller-facing API (used by the "Show available only" sync on the
      // Models Catalog page: GET /v1/models with a caller API key). Has to
      // hit the NixLLM Go server so /v1/models resolves through the
      // AuthMiddleware -> policyMiddleware -> openai handlers pipeline, not
      // the Vite dev server itself (which would 404 to SPA fallback).
      '/v1': {
        target: process.env.VITE_API_HOST || 'http://127.0.0.1:8317',
        changeOrigin: true,
        secure: false,
      },
      // Other caller-facing routes that the dashboard may exercise in the
      // future (Coder WebSocket, Gemini, etc.) — proxy them the same way so
      // dev mode never accidentally hits Vite's own asset tree.
      '/openai': {
        target: process.env.VITE_API_HOST || 'http://127.0.0.1:8317',
        changeOrigin: true,
        secure: false,
      },
      '/v1beta': {
        target: process.env.VITE_API_HOST || 'http://127.0.0.1:8317',
        changeOrigin: true,
        secure: false,
      },
      '/backend-api': {
        target: process.env.VITE_API_HOST || 'http://127.0.0.1:8317',
        changeOrigin: true,
        secure: false,
        ws: true,
      },
      '/healthz': {
        target: process.env.VITE_API_HOST || 'http://127.0.0.1:8317',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    chunkSizeWarningLimit: 1024,
  },
});
