# NixLLM Dashboard

Management UI for the CLIProxyAPI PostgreSQL-backed features: per-key API
policies (RPM, hourly rate, budget caps, model allow/deny lists), usage
statistics persistence, and the PG-mirrored model catalog + pricing.

Built with **React 18 + Vite + React Router**.

## Quickstart — development

```bash
# Terminal 1: start the Go API server (with PG backend enabled)
PGSTORE_DSN=postgresql://user:pass@localhost:5432/cliproxy \
MANAGEMENT_PASSWORD=change-me \
go run ./cmd/server

# Terminal 2: start the dashboard dev server (port 9173)
cd web/dashboard
npm install
npm run dev
```

Open http://127.0.0.1:9173/ and sign in with the value of `MANAGEMENT_PASSWORD`.
Vite proxies `/v0/*` requests to the Go server on `127.0.0.1:8317`, so no CORS
configuration is needed in development.

Override the upstream API host if your server runs on a different port:

```bash
VITE_API_HOST=http://127.0.0.1:9000 npm run dev
```

## Quickstart — production build (embedded)

Build the bundle, then rebuild the Go binary — the dashboard is embedded
into the server via `//go:embed` and served at `/dashboard`:

```bash
cd web/dashboard
npm install
npm run build              # produces web/dashboard/dist/

cd ../..
go build -o bin/cli-proxy-api ./cmd/server
./bin/cli-proxy-api
```

Open http://127.0.0.1:8317/dashboard/. The SPA is served unauthenticated
so the browser can load the bundle; all data calls go through the protected
`/v0/management/*` routes and require the master password.

When no bundle is embedded (e.g. running `go run` without first building the
SPA), the `/dashboard` route returns a short instruction page telling the
operator to run `npm run dev` instead.

## Authentication

The dashboard does not maintain its own user store. It reuses the
existing `/v0/management` auth scheme:

- The user enters the server's `MANAGEMENT_PASSWORD` env var value (or the
  auto-printed localPassword when running in TUI mode) on the login screen.
- The value is stored in `localStorage` under the key
  `nixllm.dashboard.token` and sent as `Authorization: Bearer <password>`
  on every management API call.
- A 401/403 from any route clears the stored token and bounces back to the
  login screen automatically.
- Brute-force protection is enforced server-side (5 failed attempts →
  30-minute IP ban), so the dashboard does not need its own rate limit.

## Environment variables

| Env var                     | Required | Description                                                 |
|-----------------------------|----------|-------------------------------------------------------------|
| `MANAGEMENT_PASSWORD`       | yes*     | Master password used to log in. Acts on the Go server.     |
| `PGSTORE_DSN`               | yes*     | PostgreSQL connection string; enables all PG features.     |
| `VITE_API_HOST`             | no       | Dev mode only: upstream API host (default `http://127.0.0.1:8317`). |

\* Required only when running the dashboard against PG-backed routes.
Without `PGSTORE_DSN`, the dashboard still loads but returns 503 on every
PG route (kv store not configured). Without `MANAGEMENT_PASSWORD`, login
fails.

## Pages

- **API Keys** (`/`) — list, create, delete, and navigate to per-key detail.
- **API Key Detail** (`/api-keys/:id`) — view/edit policy (RPM, hourly rate,
  hourly/weekly/monthly budget caps, model whitelist/blacklist), regenerate
  secret, change status, view budget windows.
- **Usage Stats** (`/usage`) — aggregate query with grouping by model /
  provider / API key / day / hour, totals cards, and per-bucket cost.
- **Models Catalog** (`/models`) — browse the PG-mirrored model catalog,
  edit per-model pricing (input / output / cached input / reasoning, all
  USD per 1M tokens).
- **Settings** (`/settings`) — session info, copy/clear auth token,
  server reachability check.

## Scripts

| Command          | Description                                  |
|------------------|----------------------------------------------|
| `npm run dev`    | Start Vite dev server on port 9173.         |
| `npm run build`  | Build production bundle into `dist/`.        |
| `npm run preview`| Preview the production build on port 9173.    |
| `npm run lint`   | (Optional) run ESLint.                       |

## Layout

```
web/dashboard/
├─ index.html                # HTML entry
├─ vite.config.js            # dev server + /v0 proxy, port 9173
├─ package.json
├─ public/
│  └─ favicon.svg
└─ src/
   ├─ main.jsx              # React root + BrowserRouter
   ├─ App.jsx               # Auth gating + layout shell
   ├─ api/client.js         # /v0/management client (Bearer auth)
   ├─ hooks/useAsync.js     # data fetching helper
   ├─ components/Primitives.jsx   # Spinner, Modal, Badge, Stat, ...
   ├─ styles/global.css     # Dark-tech design system
   └─ pages/
      ├─ LoginPage.jsx
      ├─ ApiKeysPage.jsx
      ├─ ApiKeyDetailPage.jsx
      ├─ UsageStatsPage.jsx
      ├─ ModelsCatalogPage.jsx
      └─ SettingsPage.jsx
```
