"""FastAPI app for the usage dashboard.

This module is the single source of truth for the app instance and its
lifespan. Routers are registered here; route handlers live in per-resource
modules (added in subsequent tickets).
"""
import asyncio
import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from .. import collector as ca

log = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Start the usage collector for every serve path.

    Both `serve` and `run` boot uvicorn against this same `app`, so wiring the
    collector here is the single source of truth — historically only `run`
    started it, which meant `serve` (the Makefile dev-backend and e2e default)
    silently collected nothing and the dashboard showed stale data forever.
    """
    cfg = getattr(app.state, "cfg", None)
    task: asyncio.Task | None = None
    if cfg is not None:
        task = asyncio.create_task(ca.collect_forever(cfg), name="collector")
        app.state.collector_task = task
        log.info("collector task started")
    else:
        log.warning("app.state.cfg unset; collector not started")
    try:
        yield
    finally:
        if task and not task.done():
            task.cancel()
            try:
                await task
            except asyncio.CancelledError:
                pass
        log.info("usage-dashboard FastAPI stopped")


app = FastAPI(
    title="CLIProxyAPI Usage Dashboard",
    version="0.1.0",
    lifespan=lifespan,
)

# ---------------------------------------------------------------------------
# Auth gate middleware (mirrors legacy server.py _gate)
# ---------------------------------------------------------------------------

_PUBLIC_PATHS = {"/", "/index.html", "/login",
                 "/api/v1/auth/check", "/api/health", "/static/chart.js"}
_PUBLIC_PREFIXES = {"/assets/", "/favicon"}  # Vite-built frontend assets


def _is_loopback(host: str) -> bool:
    return host in ("127.0.0.1", "::1", "localhost")


@app.middleware("http")
async def auth_gate(request: Request, call_next):
    cfg = request.app.state.cfg  # populated in Ticket 1.8 CLI cutover
    if cfg is None:  # test mode — allow
        return await call_next(request)
    path = request.url.path
    if path in _PUBLIC_PATHS or any(path.startswith(p) for p in _PUBLIC_PREFIXES):
        return await call_next(request)
    # Everything under /api/ requires auth.
    # All other paths are frontend (SPA, assets, deep links) — public.
    if not path.startswith("/api/"):
        return await call_next(request)
    host = cfg.get("dashboard_host", "127.0.0.1")
    if not _is_loopback(host) and not cfg.get("dashboard_token"):
        return JSONResponse({"error": "non-loopback bind requires dashboard_token"}, 503)
    token = cfg.get("dashboard_token") or ""
    if token and request.headers.get("X-Dashboard-Token", "").strip() != token:
        return JSONResponse({"error": "unauthorized"}, 401)
    return await call_next(request)

# ---------------------------------------------------------------------------
# Request validation → 400 (legacy parity: FastAPI default is 422)
# ---------------------------------------------------------------------------


@app.exception_handler(RequestValidationError)
async def validation_exception_handler(request, exc):
    """Return 400 instead of 422 for invalid JSON / missing fields."""
    return JSONResponse(status_code=400, content={"detail": "invalid JSON"})

# ---------------------------------------------------------------------------
# Routers
# ---------------------------------------------------------------------------

from . import health  # noqa: E402
from . import aliases  # noqa: E402
from . import export  # noqa: E402
from .routers import (  # noqa: E402
    accounts,
    endpoints,
    errors,
    models,
    prices,
    providers,
    requests,
    summary,
    timeseries,
)

app.include_router(aliases.router)
app.include_router(health.router)
app.include_router(summary.router)
app.include_router(timeseries.router)
app.include_router(models.router)
app.include_router(accounts.router)
app.include_router(requests.router)
app.include_router(errors.router)
app.include_router(providers.router)
app.include_router(endpoints.router)
app.include_router(prices.router)
app.include_router(export.router)

# ---------------------------------------------------------------------------
# StaticFiles mount — serve Vite-built frontend dist/
# ---------------------------------------------------------------------------

import os

from starlette.exceptions import HTTPException

from fastapi.staticfiles import StaticFiles

_HERE = os.path.dirname(os.path.abspath(__file__))
_FRONTEND_DIST = os.path.normpath(
    os.path.join(_HERE, "..", "..", "frontend", "dist")
)


class _ImmutableCacheStaticFiles(StaticFiles):
    """StaticFiles mount with immutable cache for hashed assets and SPA fallback."""

    async def get_response(self, path: str, scope):
        try:
            resp = await super().get_response(path, scope)
        except HTTPException as exc:
            if exc.status_code == 404 and path != "index.html":
                # SPA fallback: serve index.html for unknown paths (no-cache)
                resp = await super().get_response("index.html", scope)
                resp.headers["Cache-Control"] = "no-cache"
                return resp
            raise
        if resp.status_code == 404 and path != "index.html":
            # SPA fallback: serve index.html for unknown paths (no-cache)
            resp = await super().get_response("index.html", scope)
            resp.headers["Cache-Control"] = "no-cache"
            return resp
        self._set_cache_headers(resp, path)
        return resp

    @staticmethod
    def _set_cache_headers(resp, path):
        """Set Cache-Control headers based on the path."""
        if path not in ("", ".", "index.html"):
            resp.headers["Cache-Control"] = "public, max-age=31536000, immutable"
        else:
            resp.headers["Cache-Control"] = "no-cache"

if os.path.isdir(_FRONTEND_DIST):
    app.mount("/", _ImmutableCacheStaticFiles(directory=_FRONTEND_DIST, html=True),
              name="frontend")
else:
    log.warning("frontend/dist not built; serving API only. Run `pnpm build`.")
