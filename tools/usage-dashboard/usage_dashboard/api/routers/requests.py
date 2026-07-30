"""Router for /api/v1/requests and legacy /api/requests endpoints."""

from urllib.parse import parse_qs

from fastapi import APIRouter, Request

import usage_dashboard.query as qy

from ._errors import map_query_errors

router = APIRouter()


@router.get("/api/v1/requests")
@map_query_errors
def requests(request: Request):
    """Paginated usage event list with cursor-based pagination."""
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    return qy.query_requests(cfg, qs)


@router.get("/api/requests")
@map_query_errors
def legacy_requests(request: Request):
    """Legacy endpoint — query_requests with clamped limit."""
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    limit = qy.max_limit(cfg, qy._first(qs, "limit"))
    legacy_qs = {"limit": [str(limit)]}
    legacy_qs.update({k: v for k, v in qs.items() if k in ("range", "from", "to", "model")})
    return qy.query_requests(cfg, legacy_qs)
