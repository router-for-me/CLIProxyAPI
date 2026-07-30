"""Router for /api/v1/timeseries endpoint."""

from urllib.parse import parse_qs

from fastapi import APIRouter, Request

import usage_dashboard.query as qy

from ._errors import map_query_errors

router = APIRouter()


@router.get("/api/v1/timeseries")
@map_query_errors
def timeseries(request: Request):
    """Time-bucketed usage series grouped by model, provider, day, or hour."""
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    return qy.query_timeseries(cfg, qs)
