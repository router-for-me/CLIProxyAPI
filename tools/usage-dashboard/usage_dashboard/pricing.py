"""Effective-dated pricing: load, validate, and estimate cost."""
import datetime as dt
import json

from . import config as cfg_mod

_RATE_KEYS = ("input_per_million", "output_per_million",
              "cached_input_per_million", "reasoning_per_million")
_UTC = dt.timezone.utc


def load_pricing(cfg):
    """Load pricing.json; return a normalized dict. Missing file -> empty."""
    path = cfg_mod.pricing_path_for(cfg)
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, json.JSONDecodeError):
        return {"currency": "USD", "models": {}}
    if not isinstance(data, dict):
        return {"currency": "USD", "models": {}}
    data.setdefault("currency", "USD")
    data.setdefault("models", {})
    return data


def _parse_iso(value):
    """Parse RFC 3339 strictly; raise ValueError on failure."""
    text = str(value).replace("Z", "+00:00")
    parsed = dt.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=_UTC)
    return parsed.astimezone(_UTC)


def validate(pricing):
    """Validate pricing data. Raise ValueError with a combined message if invalid."""
    if not isinstance(pricing, dict):
        raise ValueError("pricing root must be an object")
    currency = pricing.get("currency", "USD")
    if not isinstance(currency, str) or not currency:
        raise ValueError("pricing 'currency' must be a non-empty string")
    models = pricing.get("models")
    if not isinstance(models, dict):
        raise ValueError("pricing 'models' must be an object")
    errors = []
    for model, intervals in models.items():
        if not isinstance(intervals, list) or not intervals:
            errors.append(f"{model}: intervals must be a non-empty list")
            continue
        for i, iv in enumerate(intervals):
            if not isinstance(iv, dict):
                errors.append(f"{model}[{i}]: not an object")
                continue
            if not iv.get("effective_from"):
                errors.append(f"{model}[{i}]: missing effective_from")
            else:
                try:
                    _parse_iso(iv["effective_from"])
                except ValueError:
                    errors.append(f"{model}[{i}]: effective_from is not RFC 3339")
            if iv.get("effective_to"):
                try:
                    _parse_iso(iv["effective_to"])
                except ValueError:
                    errors.append(f"{model}[{i}]: effective_to is not RFC 3339")
            for rate_key in _RATE_KEYS:
                if rate_key in iv:
                    try:
                        v = float(iv[rate_key])
                        if v < 0:
                            errors.append(f"{model}[{i}]: {rate_key} is negative")
                    except (TypeError, ValueError):
                        errors.append(f"{model}[{i}]: {rate_key} is not a number")
        try:
            spans = []
            for iv in intervals:
                start = _parse_iso(iv["effective_from"])
                end = _parse_iso(iv["effective_to"]) if iv.get("effective_to") else None
                spans.append((start, end))
            spans.sort(key=lambda s: s[0])
            for (s1, e1), (s2, _) in zip(spans, spans[1:]):
                if e1 is None or e1 > s2:
                    errors.append(f"{model}: overlapping price intervals")
                    break
        except ValueError:
            pass
    if errors:
        raise ValueError("; ".join(errors))


def price_for(pricing, model, ts_epoch):
    """Return the price interval active at ts_epoch, or None.
    No fallback to the latest interval for pre-dating records."""
    intervals = (pricing.get("models") or {}).get(model)
    if not intervals or not ts_epoch:
        return None
    ts = dt.datetime.fromtimestamp(ts_epoch, _UTC)
    for iv in intervals:
        eff = _parse_iso(iv.get("effective_from"))
        end_iv = iv.get("effective_to")
        end_dt = _parse_iso(end_iv) if end_iv else None
        if eff <= ts and (end_dt is None or ts < end_dt):
            return iv
    return None


def _row_get(row, key, default=0):
    if isinstance(row, dict):
        return row.get(key, default)
    try:
        return row[key]
    except (IndexError, KeyError):
        return default


def estimate_cost(cfg, records):
    """Compute estimated cost per-record using each record's own timestamp,
    then aggregate by model. Invalid pricing raises ValueError (fail-fast)."""
    pricing = load_pricing(cfg)
    validate(pricing)
    currency = pricing.get("currency") or "USD"
    total_cost = 0.0
    by_model = {}
    for rec in records:
        model = _row_get(rec, "model", "unknown") or "unknown"
        ts = _row_get(rec, "ts_epoch", 0.0)
        iv = price_for(pricing, model, float(ts) if ts else 0.0)
        agg = by_model.setdefault(model, {
            "cost": 0.0, "priced": True, "requests": 0, "total_tokens": 0,
            "unpriced_requests": 0, "input_tokens": 0, "output_tokens": 0,
            "reasoning_tokens": 0, "cached_tokens": 0,
        })
        agg["requests"] += 1
        agg["total_tokens"] += int(_row_get(rec, "total_tokens", 0))
        agg["input_tokens"] += int(_row_get(rec, "input_tokens", 0))
        agg["output_tokens"] += int(_row_get(rec, "output_tokens", 0))
        agg["reasoning_tokens"] += int(_row_get(rec, "reasoning_tokens", 0))
        agg["cached_tokens"] += int(_row_get(rec, "cached_tokens", 0))
        if not iv:
            agg["priced"] = False
            agg["unpriced_requests"] += 1
            continue
        cost = (
            int(_row_get(rec, "input_tokens", 0)) * float(iv.get("input_per_million", 0)) / 1_000_000
            + int(_row_get(rec, "output_tokens", 0)) * float(iv.get("output_per_million", 0)) / 1_000_000
            + int(_row_get(rec, "cached_tokens", 0)) * float(iv.get("cached_input_per_million", 0)) / 1_000_000
            + int(_row_get(rec, "reasoning_tokens", 0)) * float(iv.get("reasoning_per_million", 0)) / 1_000_000
        )
        agg["cost"] += cost
        total_cost += cost
    if not by_model:
        coverage = "empty"
    elif any(not m["priced"] for m in by_model.values()):
        coverage = "partial"
    else:
        coverage = "complete"
    model_rows = [{
        "model": model,
        "requests": agg["requests"],
        "total_tokens": agg["total_tokens"],
        "input_tokens": agg["input_tokens"],
        "output_tokens": agg["output_tokens"],
        "reasoning_tokens": agg["reasoning_tokens"],
        "cached_tokens": agg["cached_tokens"],
        "estimated_cost": round(agg["cost"], 6),
        "priced": agg["priced"],
        "unpriced_requests": agg["unpriced_requests"],
    } for model, agg in sorted(by_model.items(), key=lambda x: x[1]["total_tokens"], reverse=True)]
    return {
        "total": {"cost": round(total_cost, 6), "currency": currency},
        "by_model_rows": model_rows,
        "coverage": coverage,
        "currency": currency,
    }
