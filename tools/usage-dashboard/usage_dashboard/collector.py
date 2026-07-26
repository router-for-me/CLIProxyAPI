"""CPA management-queue collector: HTTP poll, normalize, insert, health."""
import datetime as dt
import hashlib
import json
import os
import sqlite3
import sys
import threading
import time
import urllib.error
import urllib.request

from . import config as cfg_mod
from . import storage as st

_UTC = dt.timezone.utc

INSERT_COLUMNS = (
    "event_key,timestamp,ts_epoch,utc_date,utc_hour,request_id,account_hash,"
    "provider,model,alias,endpoint,auth_type,executor_type,service_tier,reasoning_effort,"
    "failed,fail_status,latency_ms,ttft_ms,input_tokens,output_tokens,"
    "reasoning_tokens,cached_tokens,cache_read_tokens,cache_creation_tokens,total_tokens"
)


def parse_rfc3339(value):
    """Parse RFC 3339 strictly. Empty/None -> now. Invalid -> ValueError."""
    if not value:
        return dt.datetime.now(_UTC)
    text = str(value).replace("Z", "+00:00")
    parsed = dt.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=_UTC)
    return parsed.astimezone(_UTC)


def fetch_usage_batch(cfg, count):
    """Fetch one batch of usage records from CPA over HTTP. Destructive pop."""
    base = (cfg.get("cliproxy_base_url") or "").rstrip("/")
    url = f"{base}/v0/management/usage-queue?count={int(count)}"
    req = urllib.request.Request(url)
    key = cfg.get("management_key") or ""
    if not key:
        raise RuntimeError("management_key is required (config or CLIPROXY_MANAGEMENT_KEY)")
    req.add_header("Authorization", f"Bearer {key}")
    req.add_header("Accept", "application/json")
    with urllib.request.urlopen(req, timeout=20) as resp:
        data = json.loads(resp.read().decode("utf-8", "replace"))
    if not isinstance(data, list):
        return []
    return [item for item in data if item]


def _event_key(payload):
    rid = payload.get("request_id")
    if rid:
        return str(rid)
    raw = payload.get("_raw_text") or json.dumps(payload, sort_keys=True)
    return "sha256:" + hashlib.sha256(raw.encode()).hexdigest()


def _account_hash(payload):
    """Stable non-reversible account id for dashboard grouping.

    Prefer the client-facing CPA proxy API key (`api_key` in the usage queue).
    Fall back to upstream credential source / auth_index only when api_key is
    missing (older records or non-key auth paths).
    """
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
    """Map a CPA usage record to analytics columns, dropping all sensitive fields."""
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


def insert_usage(cfg, items):
    """Insert normalized records, isolating malformed records per-item.
    Returns (inserted, duplicates, errors)."""
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


def _iso(epoch):
    if not epoch:
        return None
    return dt.datetime.fromtimestamp(epoch, _UTC).isoformat()


class CollectorState:
    """In-memory + persisted collector health, shared with the serve process."""

    def __init__(self):
        self.lock = threading.Lock()
        self.last_poll_ok = False
        self.last_poll_epoch = 0.0
        self.last_poll_error = ""
        self.last_commit_epoch = 0.0
        self.last_usage_ts = 0.0
        self.total_inserted = 0
        self.schema_version = 0
        self.dropped_count = 0

    def snapshot(self, cfg):
        with self.lock:
            stale_secs = float(cfg.get("health_stale_seconds") or 300)
            stale = self.last_poll_epoch and (time.time() - self.last_poll_epoch > stale_secs)
            return {
                "ok": bool(self.last_poll_ok and not stale),
                "degraded": bool(stale or (self.last_poll_epoch and not self.last_poll_ok)),
                "last_poll_ok": self.last_poll_ok,
                "last_poll_at": _iso(self.last_poll_epoch),
                "last_poll_error": self.last_poll_error or None,
                "last_commit_at": _iso(self.last_commit_epoch),
                "last_usage_timestamp": _iso(self.last_usage_ts),
                "total_inserted": self.total_inserted,
                "dropped_count": self.dropped_count,
                "schema_version": self.schema_version or st.SCHEMA_VERSION,
                "db_path": cfg_mod.db_path_for(cfg),
            }

    def persist(self, cfg):
        st.save_state(cfg, {
            "last_poll_ok": self.last_poll_ok,
            "last_poll_epoch": self.last_poll_epoch,
            "last_poll_error": self.last_poll_error,
            "last_commit_epoch": self.last_commit_epoch,
            "last_usage_ts": self.last_usage_ts,
            "total_inserted": self.total_inserted,
            "schema_version": self.schema_version or st.SCHEMA_VERSION,
        })

    def restore(self, cfg):
        loaded = st.load_state(cfg)
        with self.lock:
            for k, v in loaded.items():
                setattr(self, k, v)


COLLECTOR_STATE = CollectorState()


def snapshot(cfg):
    """Return health snapshot, merging in-memory with persisted state."""
    mem = COLLECTOR_STATE.snapshot(cfg)
    if COLLECTOR_STATE.last_poll_epoch == 0.0:
        persisted = st.load_state(cfg)
        stale_secs = float(cfg.get("health_stale_seconds") or 300)
        stale = persisted["last_poll_epoch"] and (time.time() - persisted["last_poll_epoch"] > stale_secs)
        return {
            "ok": bool(persisted["last_poll_ok"] and not stale),
            "degraded": bool(stale or (persisted["last_poll_epoch"] and not persisted["last_poll_ok"])),
            "last_poll_ok": persisted["last_poll_ok"],
            "last_poll_at": _iso(persisted["last_poll_epoch"]),
            "last_poll_error": persisted["last_poll_error"] or None,
            "last_commit_at": _iso(persisted["last_commit_epoch"]),
            "last_usage_timestamp": _iso(persisted["last_usage_ts"]),
            "total_inserted": persisted["total_inserted"],
            "dropped_count": persisted.get("dropped_count", 0),
            "schema_version": persisted["schema_version"] or st.SCHEMA_VERSION,
            "db_path": cfg_mod.db_path_for(cfg),
        }
    return mem


def _redact_error(text):
    if not text:
        return ""
    redacted = text
    idx = redacted.find("Bearer ")
    if idx >= 0:
        redacted = redacted[: idx + len("Bearer ")] + "***"
    return redacted[:300]


def collect_once(cfg):
    """Drain the queue until empty. Updates and persists collector state.
    If parsing errors occur, health is marked degraded."""
    batch = max(1, int(cfg.get("batch_size") or 100))
    total_inserted = 0
    total_errors = 0
    last_ts = 0.0
    try:
        while True:
            items = fetch_usage_batch(cfg, batch)
            if not items:
                break
            inserted, _dup, errs = insert_usage(cfg, items)
            total_inserted += inserted
            total_errors += errs
            for item in items:
                try:
                    if isinstance(item, (bytes, bytearray)):
                        parsed = json.loads(item.decode("utf-8", "replace"))
                    elif isinstance(item, str):
                        parsed = json.loads(item)
                    elif isinstance(item, dict):
                        parsed = item
                    else:
                        continue
                    ts = parse_rfc3339(parsed.get("timestamp") if isinstance(parsed, dict) else None)
                    last_ts = max(last_ts, ts.timestamp())
                except (json.JSONDecodeError, TypeError, ValueError):
                    continue
            if len(items) < batch:
                break
        now = time.time()
        ok = total_errors == 0
        with COLLECTOR_STATE.lock:
            COLLECTOR_STATE.last_poll_ok = ok
            COLLECTOR_STATE.last_poll_error = "" if ok else f"{total_errors} record(s) dropped during ingest"
            COLLECTOR_STATE.last_poll_epoch = now
            COLLECTOR_STATE.last_commit_epoch = now
            COLLECTOR_STATE.dropped_count = total_errors
            if last_ts:
                COLLECTOR_STATE.last_usage_ts = last_ts
            COLLECTOR_STATE.total_inserted += total_inserted
        COLLECTOR_STATE.persist(cfg)
        return total_inserted
    except (urllib.error.URLError, urllib.error.HTTPError, OSError, RuntimeError, ValueError) as exc:
        with COLLECTOR_STATE.lock:
            COLLECTOR_STATE.last_poll_ok = False
            COLLECTOR_STATE.last_poll_error = _redact_error(str(exc))
            COLLECTOR_STATE.last_poll_epoch = time.time()
            COLLECTOR_STATE.dropped_count = total_errors
        COLLECTOR_STATE.persist(cfg)
        raise


def collect_forever(cfg, stop_event=None):
    interval = max(1, float(cfg.get("poll_interval_seconds") or 2))
    while True:
        if stop_event is not None and stop_event.is_set():
            return
        try:
            inserted = collect_once(cfg)
            if inserted:
                print(f"inserted {inserted} usage events", flush=True)
        except Exception as exc:  # noqa: BLE001 - collector must stay alive
            print(f"collector error: {_redact_error(str(exc))}", file=sys.stderr, flush=True)
        if stop_event is not None:
            if stop_event.wait(interval):
                return
        else:
            time.sleep(interval)


class CollectorLock:
    """Exclusive fcntl lock to prevent two collectors draining one queue."""

    def __init__(self, cfg):
        self.path = os.path.join(cfg_mod.data_dir_for(cfg), "collector.lock")
        self._fd = None

    def acquire(self):
        import fcntl

        fd = os.open(self.path, os.O_CREAT | os.O_RDWR, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            os.close(fd)
            raise SystemExit(
                "Another collector already owns this database "
                f"({self.path}). Only one collector may run per data directory."
            )
        self._fd = fd

    def release(self):
        import fcntl

        if self._fd is not None:
            try:
                fcntl.flock(self._fd, fcntl.LOCK_UN)
            finally:
                os.close(self._fd)
                self._fd = None

    def __enter__(self):
        self.acquire()
        return self

    def __exit__(self, *exc):
        self.release()
