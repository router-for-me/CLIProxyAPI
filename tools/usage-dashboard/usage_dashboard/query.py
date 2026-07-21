"""Read-only historical queries against usage_events."""
import base64
import datetime as dt

from . import config as cfg_mod
from . import storage as st
from . import pricing as pr
from .collector import parse_rfc3339

_UTC = dt.timezone.utc
PRESETS = {"today", "1h", "5h", "24h", "7d", "30d"}
ALLOWED_GROUPINGS = {"model", "provider", "day", "hour"}


def _first(qs, key):
    vals = qs.get(key)
    return vals[0] if vals else None


def _strict_rfc3339(value):
    """Strict RFC 3339 parse; raise ValueError on invalid input."""
    text = str(value).replace("Z", "+00:00")
    parsed = dt.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=_UTC)
    return parsed.astimezone(_UTC)


def resolve_range(qs):
    """Return (start_epoch, end_epoch, error). Explicit from/to win over preset."""
    now = dt.datetime.now(_UTC)
    from_val = _first(qs, "from")
    to_val = _first(qs, "to")
    if from_val or to_val:
        try:
            start = _strict_rfc3339(from_val).timestamp() if from_val else 0.0
            end = _strict_rfc3339(to_val).timestamp() if to_val else now.timestamp()
        except ValueError:
            return None, None, "invalid from/to timestamp (use RFC 3339, e.g. 2026-07-17T00:00:00Z)"
        if start >= end:
            return None, None, "from must be earlier than to"
        return start, end, None
    name = _first(qs, "range") or "today"
    if name not in PRESETS:
        return None, None, f"unsupported range preset: {name}"
    local_now = dt.datetime.now(cfg_mod.LOCAL_TZ)
    n = name
    if n == "1h":
        start_dt = now - dt.timedelta(hours=1)
    elif n == "5h":
        start_dt = now - dt.timedelta(hours=5)
    elif n == "24h":
        start_dt = now - dt.timedelta(hours=24)
    elif n == "7d":
        start_dt = now - dt.timedelta(days=7)
    elif n == "30d":
        start_dt = now - dt.timedelta(days=30)
    else:  # today — local midnight to now, in UTC
        start_dt = local_now.replace(hour=0, minute=0, second=0, microsecond=0).astimezone(_UTC)
    return start_dt.astimezone(_UTC).timestamp(), now.timestamp(), None


def resolve_models(qs):
    return [v for v in (qs.get("model") or []) if v]


def resolve_accounts(qs):
    """Accept repeated account= params (account_hash values)."""
    return [v for v in (qs.get("account") or []) if v]


def resolve_group_by(qs):
    val = _first(qs, "group_by") or "model"
    if val not in ALLOWED_GROUPINGS:
        return None, f"unsupported group_by: {val} (allowed: {sorted(ALLOWED_GROUPINGS)})"
    return val, None


def model_clause(models):
    if not models:
        return "", []
    placeholders = ",".join("?" for _ in models)
    return f" AND model IN ({placeholders})", list(models)


def account_clause(accounts):
    if not accounts:
        return "", []
    placeholders = ",".join("?" for _ in accounts)
    return f" AND COALESCE(account_hash,'unknown') IN ({placeholders})", list(accounts)


def _filters(qs):
    """Shared range + model + account filters for analytics queries.

    Returns (start, end, models, accounts, where, params, err).
    On error, where/params are None and err is a message.
    """
    start, end, err = resolve_range(qs)
    if err:
        return None, None, [], [], None, None, err
    models = resolve_models(qs)
    accounts = resolve_accounts(qs)
    mclause, mparams = model_clause(models)
    aclause, aparams = account_clause(accounts)
    where = f"WHERE ts_epoch BETWEEN ? AND ?{mclause}{aclause}"
    params = [start, end] + mparams + aparams
    return start, end, models, accounts, where, params, None


def _raise(err):
    raise ValueError(err)


def query_summary(cfg, qs):
    start, end, models, accounts, where, params, err = _filters(qs)
    if err:
        _raise(err)
    with st.db_connect(cfg) as conn:
        total = conn.execute(
            f"""SELECT COUNT(*) requests,
                       COALESCE(SUM(total_tokens),0) total_tokens,
                       COALESCE(SUM(input_tokens),0) input_tokens,
                       COALESCE(SUM(output_tokens),0) output_tokens,
                       COALESCE(SUM(reasoning_tokens),0) reasoning_tokens,
                       COALESCE(SUM(cached_tokens),0) cached_tokens,
                       COALESCE(SUM(cache_read_tokens),0) cache_read_tokens,
                       COALESCE(SUM(cache_creation_tokens),0) cache_creation_tokens,
                       COALESCE(SUM(failed),0) failed,
                       COALESCE(SUM(CASE WHEN failed=0 THEN latency_ms END),0) success_latency_ms,
                       COUNT(CASE WHEN failed=0 THEN 1 END) success_requests,
                       COALESCE(SUM(CASE WHEN failed=0 THEN ttft_ms END),0) success_ttft_ms
                FROM usage_events {where}""",
            params,
        ).fetchone()
        account_rows = conn.execute(
            f"""SELECT COALESCE(account_hash,'unknown') account, COUNT(*) requests,
                       COALESCE(SUM(total_tokens),0) total_tokens,
                       COALESCE(SUM(input_tokens),0) input_tokens,
                       COALESCE(SUM(output_tokens),0) output_tokens,
                       COALESCE(SUM(reasoning_tokens),0) reasoning_tokens,
                       COALESCE(SUM(failed),0) failed
                FROM usage_events {where}
                GROUP BY account ORDER BY total_tokens DESC""",
            params,
        ).fetchall()
        model_rows = conn.execute(
            f"""SELECT id, ts_epoch, model, input_tokens, output_tokens,
                       reasoning_tokens, cached_tokens, total_tokens
                FROM usage_events {where}""",
            params,
        ).fetchall()
        hours = conn.execute(
            f"""SELECT utc_hour hour, COUNT(*) requests,
                       COALESCE(SUM(total_tokens),0) total_tokens,
                       COALESCE(SUM(failed),0) failed
                FROM usage_events {where}
                GROUP BY utc_hour ORDER BY utc_hour""",
            params,
        ).fetchall()
    cost = pr.estimate_cost(cfg, model_rows)
    summary = dict(total)
    summary["estimated_cost"] = cost["total"]["cost"]
    summary["estimated_cost_currency"] = cost["total"]["currency"]
    return {
        "range": _first(qs, "range") or ("explicit" if (_first(qs, "from") or _first(qs, "to")) else "today"),
        "models_filter": models,
        "accounts_filter": accounts,
        "summary": summary,
        "accounts": [dict(x) for x in account_rows],
        "models": cost["by_model_rows"],
        "hours": [dict(x) for x in hours],
        "price_coverage": cost["coverage"],
    }


def query_timeseries(cfg, qs):
    start, end, models, accounts, where, params, err = _filters(qs)
    if err:
        _raise(err)
    group_by, gerr = resolve_group_by(qs)
    if gerr:
        _raise(gerr)
    select_key = {
        "model": "COALESCE(model, 'unknown')",
        "provider": "COALESCE(provider, 'unknown')",
        "day": "utc_date",
        "hour": "utc_hour",
    }[group_by]
    sql = f"""SELECT {select_key} AS bucket, COUNT(*) requests,
                     COALESCE(SUM(total_tokens),0) total_tokens,
                     COALESCE(SUM(input_tokens),0) input_tokens,
                     COALESCE(SUM(output_tokens),0) output_tokens,
                     COALESCE(SUM(reasoning_tokens),0) reasoning_tokens,
                     COALESCE(SUM(cached_tokens),0) cached_tokens,
                     COALESCE(SUM(cache_read_tokens),0) cache_read_tokens,
                     COALESCE(SUM(failed),0) failed,
                     COALESCE(AVG(CASE WHEN failed=0 THEN latency_ms END),0) avg_latency_ms,
                     COALESCE(AVG(CASE WHEN failed=0 THEN ttft_ms END),0) avg_ttft_ms
              FROM usage_events {where}
              GROUP BY bucket ORDER BY bucket"""
    with st.db_connect(cfg) as conn:
        rows = conn.execute(sql, params).fetchall()
    return {
        "group_by": group_by,
        "models_filter": models,
        "accounts_filter": accounts,
        "series": [dict(r) for r in rows],
    }


def query_models(cfg, qs):
    start, end, models, accounts, where, params, err = _filters(qs)
    if err:
        _raise(err)
    with st.db_connect(cfg) as conn:
        rows = conn.execute(
            f"""SELECT COALESCE(model,'unknown') model, MAX(alias) alias,
                       MAX(provider) provider, COUNT(*) requests,
                       COALESCE(SUM(total_tokens),0) total_tokens
                FROM usage_events {where}
                GROUP BY model ORDER BY total_tokens DESC""",
            params,
        ).fetchall()
    pricing = pr.load_pricing(cfg)
    known = set((pricing.get("models") or {}).keys())
    out = []
    for row in rows:
        d = dict(row)
        d["priced"] = d["model"] in known
        out.append(d)
    return {"models": out, "accounts_filter": accounts}


def query_accounts(cfg, qs):
    """Distinct account hashes in range (for filter UI), ordered by volume."""
    start, end, models, accounts, where, params, err = _filters(qs)
    if err:
        _raise(err)
    # Account picker should list all accounts in the time/model scope, not only
    # currently selected accounts — so drop account filter for discovery.
    mclause, mparams = model_clause(models)
    discover_where = f"WHERE ts_epoch BETWEEN ? AND ?{mclause}"
    discover_params = [start, end] + mparams
    with st.db_connect(cfg) as conn:
        rows = conn.execute(
            f"""SELECT COALESCE(account_hash,'unknown') account, COUNT(*) requests,
                       COALESCE(SUM(total_tokens),0) total_tokens
                FROM usage_events {discover_where}
                GROUP BY account ORDER BY total_tokens DESC""",
            discover_params,
        ).fetchall()
    return {"accounts": [dict(r) for r in rows], "accounts_filter": accounts}


def query_errors(cfg, qs):
    """Aggregate failed requests by fail_status x model.

    Drives the Errors tab. Honors range + model + account filters so the
    aggregation stays consistent with every other panel under the same toolbar.
    """
    start, end, models, accounts, where, params, err = _filters(qs)
    if err:
        _raise(err)
    sql = f"""SELECT fail_status, COALESCE(model, 'unknown') model,
                     COUNT(*) count, MAX(timestamp) last_seen
              FROM usage_events {where} AND failed = 1
              GROUP BY fail_status, model
              ORDER BY count DESC, fail_status ASC"""
    total_failed_sql = f"SELECT COUNT(*) FROM usage_events {where} AND failed = 1"
    total_sql = f"SELECT COUNT(*) FROM usage_events {where}"
    with st.db_connect(cfg) as conn:
        rows = conn.execute(sql, params).fetchall()
        total_failed = conn.execute(total_failed_sql, params).fetchone()[0]
        total_requests = conn.execute(total_sql, params).fetchone()[0]
    denom = total_requests or 1
    errors = []
    for r in rows:
        d = dict(r)
        d["percent"] = round(d["count"] / denom * 100, 1)
        errors.append(d)
    return {
        "errors": errors,
        "total_failed": total_failed,
        "total_requests": total_requests,
        "models_filter": models,
        "accounts_filter": accounts,
    }


def max_limit(cfg, requested):
    default = int(cfg.get("default_limit") or 100)
    maximum = int(cfg.get("max_limit") or 500)
    try:
        n = int(requested) if requested else default
    except (TypeError, ValueError):
        n = default
    return max(1, min(n, maximum))


def query_requests(cfg, qs):
    start, end, models, accounts, where, params, err = _filters(qs)
    if err:
        _raise(err)
    limit = max_limit(cfg, _first(qs, "limit"))
    cursor = _first(qs, "cursor")
    if cursor:
        try:
            decoded = base64.urlsafe_b64decode(cursor.encode()).decode()
            cur_ts, cur_id = decoded.rsplit(",", 1)
            where += " AND (ts_epoch, id) < (?, ?)"
            params = list(params) + [float(cur_ts), int(cur_id)]
        except Exception:
            _raise("invalid cursor")
    where += " ORDER BY ts_epoch DESC, id DESC LIMIT ?"
    params = list(params) + [limit + 1]
    with st.db_connect(cfg) as conn:
        rows = conn.execute(
            f"""SELECT id, timestamp, account_hash, model, alias, provider, endpoint,
                       failed, fail_status, latency_ms, ttft_ms,
                       input_tokens, output_tokens, reasoning_tokens, cached_tokens,
                       cache_read_tokens, cache_creation_tokens, total_tokens, request_id
                FROM usage_events {where}""",
            params,
        ).fetchall()
    items = [dict(r) for r in rows[:limit]]
    for item in items:
        ts = parse_rfc3339(item["timestamp"])
        item["local_time"] = ts.astimezone(cfg_mod.LOCAL_TZ).strftime("%Y-%m-%d %H:%M:%S")
        item["account"] = item.get("account_hash") or "unknown"
        item.pop("account_hash", None)
    next_cursor = None
    if len(rows) > limit and items:
        last = items[-1]
        raw = f"{parse_rfc3339(last['timestamp']).timestamp()},{last['id']}"
        next_cursor = base64.urlsafe_b64encode(raw.encode()).decode()
    for item in items:
        item.pop("id", None)
    return {
        "requests": items,
        "next_cursor": next_cursor,
        "limit": limit,
        "models_filter": models,
        "accounts_filter": accounts,
    }