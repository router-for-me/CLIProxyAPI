"""CSV export endpoint — parity with legacy server.py csv_response."""
import csv
import datetime as dt
import io
from urllib.parse import parse_qs

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import StreamingResponse

from .. import pricing as pr
from .. import query as qy

router = APIRouter()

CSV_COLUMNS = [
    "timestamp", "account_hash", "provider", "model", "endpoint",
    "request_id", "input_tokens", "output_tokens", "reasoning_tokens",
    "cached_tokens", "cache_read_tokens", "cache_creation_tokens",
    "total_tokens", "latency_ms", "ttft_ms", "failed", "fail_status",
    "estimated_cost",
]


def _compute_estimated_cost(cfg, row):
    ts_str = row.get("timestamp", "")
    ts = 0
    if ts_str:
        try:
            ts = dt.datetime.fromisoformat(ts_str.replace("Z", "+00:00")).timestamp()
        except Exception:
            ts = 0
    pricing_cfg = pr.load_pricing(cfg)
    iv = pr.price_for(pricing_cfg, row.get("model", ""), ts)
    if not iv:
        return 0
    return (
        (row.get("input_tokens", 0) or 0)
        * float(iv.get("input_per_million", 0)) / 1_000_000
        + (row.get("output_tokens", 0) or 0)
        * float(iv.get("output_per_million", 0)) / 1_000_000
        + (row.get("cached_tokens", 0) or 0)
        * float(iv.get("cached_input_per_million", 0)) / 1_000_000
        + (row.get("reasoning_tokens", 0) or 0)
        * float(iv.get("reasoning_per_million", 0)) / 1_000_000
    )


async def _row_generator(cfg, rows):
    """Stream CSV rows in chunks to avoid building the whole string in memory."""
    buf = io.StringIO()
    writer = csv.DictWriter(buf, fieldnames=CSV_COLUMNS, extrasaction="ignore")
    writer.writeheader()
    yield buf.getvalue()
    buf.seek(0)
    buf.truncate()
    for r in rows:
        r["estimated_cost"] = _compute_estimated_cost(cfg, r)
        writer.writerow(r)
        yield buf.getvalue()
        buf.seek(0)
        buf.truncate()


@router.get("/api/v1/export")
async def export(request: Request):
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    try:
        data = qy.query_requests(cfg, qs, no_limit=True)
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from None
    rows = data.get("requests", [])
    return StreamingResponse(
        _row_generator(cfg, rows),
        media_type="text/csv",
        headers={"Content-Disposition": "attachment; filename=usage_export.csv"},
    )
