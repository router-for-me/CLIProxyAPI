// Cloudflare Worker — reverse proxy for the NixLLM dashboard SPA.
//
// The dashboard frontend (web/dashboard) is a static React SPA that talks to
// the CLIProxyAPI Go server using same-origin relative URLs. The API client
// hardcodes `API_BASE = '/v0/management'` (see src/api/client.js), and the
// SPA also reaches /v1, /v1beta, /openai, /backend-api (WebSocket), and
// /healthz. When the frontend is hosted on Cloudflare, those same-origin
// paths would 404 against the static-asset origin — this worker intercepts
// them and forwards them to the Go backend so no CORS setup is needed.
//
// Routing summary (run_worker_first = true guarantees this worker runs first):
//   1. API requests  → forwarded to BACKEND_ORIGIN (method, headers, body,
//                       query string, and WebSocket upgrade preserved).
//   2. Everything else → served from the Static Assets binding, which falls
//                       back to /index.html for unknown routes (SPA mode).
//
// Deploy:
//   wrangler deploy
// Override the backend origin per environment (Cloudflare dashboard →
// Workers → Settings → Variables, or `wrangler secret` / `.dev.vars`).

// Path prefixes that belong to the Go backend, not the static SPA.
const API_PREFIXES = [
  '/v0/',
  '/v1/',
  '/v1beta/',
  '/openai/',
  '/backend-api/',
];

// Exact-match API paths with no trailing slash.
const API_EXACT = new Set(['/healthz']);

function isApiRequest(pathname) {
  if (API_EXACT.has(pathname)) return true;
  return API_PREFIXES.some((p) => pathname.startsWith(p));
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const backend = env.BACKEND_ORIGIN;

    if (isApiRequest(url.pathname)) {
      if (!backend) {
        // Misconfiguration: the worker is deployed but BACKEND_ORIGIN is unset.
        // Surface a clear error so the operator fixes the binding rather than
        // seeing a confusing 404 / SPA fallback for an API route.
        return new Response(
          JSON.stringify({
            error: {
              message:
                'BACKEND_ORIGIN is not configured. Set it to the CLIProxyAPI Go server origin (e.g. https://api.example.com) in the worker variables.',
              type: 'proxy_misconfigured',
            },
          }),
          {
            status: 502,
            headers: { 'content-type': 'application/json' },
          },
        );
      }

      // Build the upstream URL preserving the original path + query string.
      const target = new URL(url.pathname + url.search, backend);

      // Using the original request as the init copies method, headers, body,
      // redirect mode, and WebSocket upgrade semantics. This is what lets
      // /backend-api/* (WebSocket) and multipart uploads pass through intact.
      const proxyReq = new Request(target, request);

      // Forward to the backend. fetch() transparently proxies WebSocket
      // upgrades when the request carries the `Upgrade: websocket` header.
      return fetch(proxyReq);
    }

    // Non-API request → serve the static SPA bundle. The Static Assets
    // binding is configured with not_found_handling = "single-page-application"
    // so unknown routes (e.g. /usage, /api-keys/:id) fall back to
    // /index.html and react-router resolves them client-side.
    return env.ASSETS.fetch(request);
  },
};
