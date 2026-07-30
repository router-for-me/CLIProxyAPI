"""Health-check and auth-check endpoints for the usage dashboard FastAPI app."""

import os

from fastapi import APIRouter, Request

from .. import storage as st

router = APIRouter()


def _is_authorized(request: Request, cfg: dict) -> bool:
    """Check if the request's X-Dashboard-Token matches the configured token."""
    token = cfg.get("dashboard_token") or ""
    if not token:
        return True
    return request.headers.get("X-Dashboard-Token", "").strip() == token


@router.get("/api/v1/health")
def health(request: Request):
    """Health check — returns live collector state from storage."""
    cfg = getattr(request.app.state, "cfg", None) or {}
    if not cfg:
        return {"last_poll_at": None, "last_poll_ok": True}
    state = st.load_state(cfg)
    return {
        "last_poll_at": state.get("last_poll_at") or None,
        "last_poll_ok": bool(state.get("last_poll_ok")),
        "last_poll_error": state.get("last_poll_error") or None,
    }


@router.get("/api/v1/auth/check")
def auth_check(request: Request):
    """Check whether authentication is required and whether the current request is valid."""
    cfg = request.app.state.cfg or {}
    required = bool(cfg.get("dashboard_token"))
    valid = (not required) or _is_authorized(request, cfg)
    return {"auth_required": required, "valid": valid}


@router.get("/api/health")
def legacy_health(request: Request):
    """Legacy health endpoint — returns ok and db_path."""
    cfg = request.app.state.cfg or {}
    data_dir = cfg.get("data_dir")
    db_path = os.path.join(data_dir, "usage.sqlite") if data_dir else None
    return {"ok": True, "db_path": db_path}
