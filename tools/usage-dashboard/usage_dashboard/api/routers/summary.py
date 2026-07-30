"""Router for /api/v1/summary and legacy /api/summary endpoints."""

from urllib.parse import parse_qs

from fastapi import APIRouter, Request

import usage_dashboard.query as qy

from ..schemas import SummaryResponse
from ._errors import map_query_errors

router = APIRouter()


@router.get("/api/v1/summary", response_model=SummaryResponse)
@map_query_errors
def summary(request: Request):
    """Aggregate usage summary: totals, accounts, models, hours, cost."""
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    return qy.query_summary(cfg, qs)


@router.get("/api/summary")
@map_query_errors
def legacy_summary(request: Request):
    """Legacy endpoint — query_summary with clamped limit."""
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    limit = qy.max_limit(cfg, qy._first(qs, "limit"))
    legacy_qs = {"limit": [str(limit)]}
    legacy_qs.update({k: v for k, v in qs.items() if k in ("range", "from", "to", "model")})
    return qy.query_summary(cfg, legacy_qs)