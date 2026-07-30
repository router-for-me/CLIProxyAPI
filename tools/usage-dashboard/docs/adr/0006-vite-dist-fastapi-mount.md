# ADR 0006: Vite dist mounted by FastAPI StaticFiles

**Status**: Accepted
**Date**: 2026-07-27

## Context

A hard product constraint: in production, the dashboard must start with
`python3 usage_dashboard.py run` and have **no Node.js dependency at
runtime**. Air-gapped LANs, low-touch operator machines, and Docker
images built from `python:3.12-slim` are all first-class targets. CDN
dependencies are also rejected (the current Chart.js is vendored for this
reason).

During development, the front end is a separate Vite project that needs
HMR, a dev server on port 5173, and API proxying to the FastAPI back end
on port 8320.

The question is how the front end's production build is delivered by the
Python sidecar.

## Decision

- **Development**: `pnpm dev` runs Vite on `:5173` with
  `server.proxy['/api'] → http://127.0.0.1:8320` and
  `server.proxy['/static'] → http://127.0.0.1:8320`. The FastAPI server
  runs in parallel (`uvicorn usage_dashboard.api:app --reload --port 8320`).
  The developer opens `http://localhost:5173`.
- **Production**: `pnpm build` produces `frontend/dist/` containing
  `index.html` + hashed JS/CSS + vendored `chart.min.js`. The FastAPI
  app mounts this directory:
  ```python
  app.mount("/", StaticFiles(directory=frontend_dist, html=True),
            name="frontend")
  ```
  Mounted last, so `/api/*` and `/static/*` routes win over the catch-all.
- **Distribution**: `frontend/dist/` is committed to Git in the same
  release commit that bumps the version. A user running
  `git clone && python3 usage_dashboard.py run` gets a working dashboard
  with no build step. The `frontend/` directory is development-only and
  is listed in `.gitignore` for its `node_modules/`, `dist/` (during dev),
  and cache directories — but `frontend/dist/` is force-added on release.
- **Static caching**: hashed asset filenames get
  `Cache-Control: public, max-age=31536000, immutable`; `index.html` gets
  `Cache-Control: no-cache` so new releases are picked up on reload.

## Alternatives considered

- **Ship as separate static site (nginx / Vercel).** Rejected: destroys
  the sidecar property; operators now have two things to deploy.
- **Build in CI, do not commit `dist/`.** Rejected: a user `git clone`
  then has to `cd frontend && pnpm build` before the dashboard works.
  That violates the zero-runtime-Node constraint.
- **Inline the front end into a Python string (like the current
  `DASHBOARD_HTML = _load_dashboard_html()`).** Rejected: breaks
  hashed-asset caching, code-splitting, and any reasonable development
  workflow. Acceptable only for trivial single-file UIs; this dashboard
  is past that complexity.
- **Bundle the front end into a Python wheel as package data.** Equivalent
  to this decision; we commit `dist/` to the source tree instead of
  building a wheel because the dashboard is run from source, not
  installed from PyPI.

## Consequences

**Positive**

- Production has no Node runtime requirement.
- Development gets full Vite HMR + proxy.
- Hashed asset filenames give immutable caching.

**Negative**

- `frontend/dist/` is committed on every release, which bloats the Git
  history of the `tools/usage-dashboard/` subtree. Mitigated by squashing
  dev builds and committing only at release boundaries.
- A developer changing the front end must remember to rebuild and commit
  `dist/` for the change to appear to operators. Mitigated by a release
  checklist and CI check that fails if `dist/` is stale relative to
  `frontend/src/`.

**Neutral**

- `StaticFiles(html=True)` serves `index.html` for unknown paths under
  `/`, which is what a client-side router (React Router) needs.
