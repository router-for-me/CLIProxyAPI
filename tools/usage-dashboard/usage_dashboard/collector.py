"""Async collector: httpx + asyncio.Task. Also exposes sync helpers
and sync insert_usage/fetch_usage_batch for backward compatibility."""
import asyncio
import contextlib
import datetime as dt
import fcntl
import hashlib
import json
import logging
import os
import sqlite3
import urllib.request
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
import httpx
from sqlmodel import Session, create_engine
from . import config as cfg_mod
from . import storage as st
from .models import UsageEvent

log = logging.getLogger(__name__)
_utc = timezone.utc
_UTC = dt.timezone.utc

INSERT_COLUMNS = (
    "event_key,timestamp,ts_epoch,utc_date,utc_hour,request_id,account_hash,"
    "provider,model,alias,endpoint,auth_type,executor_type,service_tier,reasoning_effort,"
    "failed,fail_status,latency_ms,ttft_ms,input_tokens,output_tokens,"
    "reasoning_tokens,cached_tokens,cache_read_tokens,cache_creation_tokens,total_tokens"
)


def parse_rfc3339(value):
    if not value:
        return dt.datetime.now(_UTC)
    text = str(value).replace("Z", "+00:00")
    parsed = dt.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=_UTC)
    return parsed.astimezone(_UTC)


def _event_key(payload):
    rid = payload.get("request_id")
    if rid:
        return str(rid)
    raw = payload.get("_raw_text") or json.dumps(payload, sort_keys=True)
    return "sha256:" + hashlib.sha256(raw.encode()).hexdigest()


def _account_hash(payload):
    for key in ("api_key", "source", "auth_index"):
        val = payload.get(key)
        if val is None:
            continue
        text = str(val).strip()
        if text:
            return hashlib.sha256(text.encode()).hexdigest()[:12]
    return None


def _safe_int(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def normalize_record(payload):
    raw_text = payload.get("_raw_text")
    if raw_text is None:
        raw_text = json.dumps(payload, sort_keys=True, ensure_ascii=False)
    ts_utc = parse_rfc3339(payload.get("timestamp"))
    tokens = payload.get("tokens") or {}
    fail = payload.get("fail") or {}
    cached_tokens = _safe_int(tokens.get("cached_tokens"))
    cache_read = _safe_int(tokens.get("cache_read_tokens"))
    cache_creation = _safe_int(tokens.get("cache_creation_tokens"))
    if cache_read == 0 and cached_tokens:
        cache_read = cached_tokens
    return {
        "event_key": _event_key(payload),
        "timestamp": ts_utc.isoformat(),
        "ts_epoch": ts_utc.timestamp(),
        "utc_date": ts_utc.strftime("%Y-%m-%d"),
        "utc_hour": ts_utc.strftime("%Y-%m-%d %H:00"),
        "request_id": payload.get("request_id"),
        "account_hash": _account_hash(payload),
        "provider": payload.get("provider"),
        "model": payload.get("model"),
        "alias": payload.get("alias"),
        "endpoint": payload.get("endpoint"),
        "auth_type": payload.get("auth_type"),
        "executor_type": payload.get("executor_type"),
        "service_tier": payload.get("service_tier"),
        "reasoning_effort": payload.get("reasoning_effort"),
        "failed": 1 if payload.get("failed") else 0,
        "fail_status": _safe_int(fail.get("status_code")),
        "latency_ms": _safe_int(payload.get("latency_ms")),
        "ttft_ms": _safe_int(payload.get("ttft_ms")),
        "input_tokens": _safe_int(tokens.get("input_tokens")),
        "output_tokens": _safe_int(tokens.get("output_tokens")),
        "reasoning_tokens": _safe_int(tokens.get("reasoning_tokens")),
        "cached_tokens": cached_tokens,
        "cache_read_tokens": cache_read,
        "cache_creation_tokens": cache_creation,
        "total_tokens": _safe_int(tokens.get("total_tokens")),
    }


def _iso(epoch):
    if not epoch:
        return None
    return dt.datetime.fromtimestamp(epoch, _UTC).isoformat()


def _redact_error(text):
    if not text:
        return ""
    redacted = text
    idx = redacted.find("Bearer ")
    if idx >= 0:
        redacted = redacted[: idx + len("Bearer ")] + "***"
    return redacted[:300]


def fetch_usage_batch(cfg, count):
    """Sync fetch — kept for backward compatibility with tests."""
    base = (cfg.get("cliproxy_base_url") or "").rstrip("/")
    url = f"{base}/v0/management/usage-queue?count={int(count)}"
    req = urllib.request.Request(url)
    key = cfg.get("management_key") or ""
    if not key:
        raise RuntimeError("management_key is required")
    req.add_header("Authorization", f"Bearer {key}")
    req.add_header("Accept", "application/json")
    with urllib.request.urlopen(req, timeout=20) as resp:
        data = json.loads(resp.read().decode("utf-8", "replace"))
    if not isinstance(data, list):
        return []
    return [item for item in data if item]


def insert_usage(cfg, items):
    """Sync insert — kept for backward compatibility with tests."""
    inserted = 0
    duplicates = 0
    errors = 0
    with st.db_connect(cfg) as conn:
        for raw in items:
            try:
                if isinstance(raw, (bytes, bytearray)):
                    raw_text = raw.decode("utf-8", "replace")
                    payload = json.loads(raw_text)
                elif isinstance(raw, str):
                    raw_text = raw
                    payload = json.loads(raw_text)
                elif isinstance(raw, dict):
                    raw_text = json.dumps(raw, ensure_ascii=False)
                    payload = dict(raw)
                else:
                    errors += 1
                    continue
                payload["_raw_text"] = raw_text
                values = normalize_record(payload)
            except (json.JSONDecodeError, TypeError, ValueError):
                errors += 1
                continue
            placeholders = ":" + ",:".join(INSERT_COLUMNS.split(","))
            try:
                conn.execute(
                    f"INSERT INTO usage_events ({INSERT_COLUMNS}) VALUES ({placeholders})",
                    values,
                )
                inserted += 1
            except sqlite3.IntegrityError:
                duplicates += 1
    return inserted, duplicates, errors


@dataclass
class CollectorHealth:
    last_poll_at: str | None = None
    last_poll_ok: bool = True
    last_poll_error: str | None = None
    inserted: int = 0
    duplicates: int = 0
    errors: int = 0
    dropped: int = 0

    def to_state_dict(self) -> dict:
        d = asdict(self)
        d["last_poll_at"] = d["last_poll_at"] or ""
        d["last_poll_ok"] = "1" if d["last_poll_ok"] else "0"
        d["last_poll_error"] = d["last_poll_error"] or ""
        return {k: str(v) for k, v in d.items()}


async def async_fetch_usage_batch(cfg, count):
    url = f"{cfg['cliproxy_base_url']}/v0/management/usage-queue"
    headers = {"Authorization": f"Bearer {cfg['management_key']}"}
    async with httpx.AsyncClient(timeout=10) as client:
        resp = await client.get(url, headers=headers, params={"count": count})
        resp.raise_for_status()
        data = resp.json()
    # Upstream may return a bare list of records or {"items": [...]}.
    if isinstance(data, dict):
        items = data.get("items") or []
    elif isinstance(data, list):
        items = data
    else:
        items = []
    return [i for i in items if i]


async def async_insert_usage(cfg, items):
    inserted = duplicates = errors = 0
    engine = create_engine(f"sqlite:///{cfg_mod.db_path_for(cfg)}")
    with Session(engine) as session:
        for payload in items:
            try:
                if isinstance(payload, dict):
                    normalized = normalize_record(payload)
                    event = UsageEvent(**normalized)
                    session.add(event)
                    session.commit()
                    inserted += 1
                else:
                    errors += 1
            except Exception as exc:
                session.rollback()
                msg = str(exc).lower()
                if "unique" in msg:
                    duplicates += 1
                else:
                    errors += 1
                    log.warning("skipping malformed record: %s", exc)
    engine.dispose()
    return inserted, duplicates, errors


async def collect_once(cfg) -> CollectorHealth:
    health = CollectorHealth()
    try:
        items = await async_fetch_usage_batch(cfg, int(cfg.get("batch_size") or 100))
        inserted, duplicates, errors = await async_insert_usage(cfg, items)
        health.inserted = inserted
        health.duplicates = duplicates
        health.errors = errors
        health.last_poll_ok = errors == 0
        health.last_poll_at = datetime.now(_utc).isoformat()
    except Exception as exc:
        health.last_poll_ok = False
        health.last_poll_error = _redact_error(str(exc))
        health.last_poll_at = datetime.now(_utc).isoformat()
        log.error("collector poll failed: %s", exc)
        raise
    finally:
        st.save_state(cfg, health.to_state_dict())
    return health


async def collect_forever(cfg, stop_event=None):
    interval = max(1.0, float(cfg.get("poll_interval_seconds") or 2))
    while True:
        if stop_event and stop_event.is_set():
            return
        try:
            await collect_once(cfg)
        except Exception:
            pass  # already persisted in finally
        if stop_event:
            try:
                await asyncio.wait_for(stop_event.wait(), timeout=interval)
            except asyncio.TimeoutError:
                pass
        else:
            await asyncio.sleep(interval)


class AsyncCollectorLock:
    """fcntl-based exclusive lock — same lockfile as legacy CollectorLock."""

    def __init__(self, cfg):
        self.path = os.path.join(cfg_mod.data_dir_for(cfg), "collector.lock")
        self.fd = None

    @contextlib.asynccontextmanager
    async def __aenter__(self):
        cfg_mod.ensure_dirs({"data_dir": os.path.dirname(self.path)})
        self.fd = open(self.path, "w")
        try:
            fcntl.flock(self.fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self.fd.close()
            self.fd = None
            raise RuntimeError("another collector already holds the lock") from None
        yield self

    async def __aexit__(self, exc_type, exc, tb):
        if self.fd is None:
            return
        try:
            fcntl.flock(self.fd.fileno(), fcntl.LOCK_UN)
        finally:
            self.fd.close()
            self.fd = None
