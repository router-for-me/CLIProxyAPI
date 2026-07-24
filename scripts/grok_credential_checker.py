#!/usr/bin/env python3
"""Grok free credential inspector and active-pool manager.

Operations script for CLIProxyAPI Management API. Python standard library only.
Default mode is dry-run; mutating status changes require --apply.

See scripts/README-grok-credential-checker.md for operator documentation.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import errno
import fcntl
import hashlib
import json
import logging
import os
import re
import sys
import tempfile
import threading
import time
import traceback
import uuid
from dataclasses import asdict, dataclass, field
from datetime import datetime, timedelta, timezone
from http.client import HTTPResponse
from typing import Any, Callable, Dict, Iterable, List, Optional, Sequence, Tuple
from urllib.error import HTTPError, URLError
from urllib.parse import urljoin
from urllib.request import Request, urlopen

__version__ = "1.0.0"

LOG = logging.getLogger("grok_credential_checker")

DEFAULT_TARGET_ACTIVE = 500
DEFAULT_QUOTA_LIMIT_TOKENS = 1_000_000
DEFAULT_RESET_WINDOW_HOURS = 24
DEFAULT_CONCURRENCY = 32
DEFAULT_BATCH_SIZE = 100
DEFAULT_MAX_PROBES_PER_CYCLE = 50
DEFAULT_TIMEOUT_SECONDS = 30
DEFAULT_INTERVAL_SECONDS = 300
DEFAULT_PROBE_RATE_PER_SECOND = 1.0
DEFAULT_STATE_FILE = "grok_credential_checker_state.json"
STATE_VERSION = 1

XAI_BILLING_URL = "https://cli-chat-proxy.grok.com/v1/billing"
XAI_PROBE_URL = "https://cli-chat-proxy.grok.com/v1/models"
XAI_REQUEST_HEADERS = {
    "Authorization": "Bearer $TOKEN$",
    "x-xai-token-auth": "xai-grok-cli",
    "x-grok-client-version": "0.2.91",
    "accept": "*/*",
    "user-agent": "grok-pager/0.2.91 grok-shell/0.2.91 (linux; x86_64)",
}

OWNERSHIP_EXTERNAL = "external"
OWNERSHIP_MANAGED = "managed"
OWNERSHIP_MANUAL_OVERRIDE = "manual_override"

CLASS_HEALTHY = "healthy"
CLASS_EXHAUSTED = "exhausted"
CLASS_COOLDOWN = "cooldown"
CLASS_AUTH_INVALID = "auth_invalid"
CLASS_VERIFICATION = "verification_required"
CLASS_UPSTREAM_ERROR = "upstream_error"
CLASS_UNKNOWN = "unknown"

REASON_QUOTA_EXHAUSTED = "quota_exhausted"
REASON_AUTH_INVALID = "auth_invalid"
REASON_VERIFICATION = "verification_required"
REASON_POOL_STANDBY = "pool_standby"
REASON_POOL_REFILL = "pool_refill"
REASON_ROLLBACK = "rollback"

SECRET_PATTERNS = (
    re.compile(r"(?i)(authorization\s*[:=]\s*bearer\s+)(\S+)"),
    re.compile(r"(?i)(x-management-key\s*[:=]\s*)(\S+)"),
    re.compile(r"(?i)(access_token|refresh_token|id_token|api[_-]?key|cookie)\s*[\"']?\s*[:=]\s*[\"']?([^\s\"',}]+)"),
    re.compile(r"(?i)(Bearer\s+)([A-Za-z0-9\-._~+/]+=*)"),
)
TOKEN_USAGE_RE = re.compile(
    r"tokens\s*\(\s*actual\s*/\s*limit\s*\)\s*:\s*(\d+)\s*/\s*(\d+)",
    re.IGNORECASE,
)


# ---------------------------------------------------------------------------
# Time helpers
# ---------------------------------------------------------------------------


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def to_iso(dt: Optional[datetime]) -> Optional[str]:
    if dt is None:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def parse_iso(value: Any) -> Optional[datetime]:
    if value is None or value == "":
        return None
    if isinstance(value, (int, float)):
        # treat large values as ms
        ts = float(value)
        if ts > 1e12:
            ts /= 1000.0
        return datetime.fromtimestamp(ts, tz=timezone.utc)
    if isinstance(value, datetime):
        return value if value.tzinfo else value.replace(tzinfo=timezone.utc)
    text = str(value).strip()
    if not text:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(text)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


# ---------------------------------------------------------------------------
# Secret redaction
# ---------------------------------------------------------------------------


def redact_text(text: str) -> str:
    if not text:
        return text
    out = text
    for pattern in SECRET_PATTERNS:
        out = pattern.sub(lambda m: m.group(1) + "[REDACTED]", out)
    return out


def redact_obj(obj: Any) -> Any:
    if obj is None:
        return None
    if isinstance(obj, str):
        return redact_text(obj)
    if isinstance(obj, dict):
        redacted: Dict[str, Any] = {}
        for key, value in obj.items():
            key_l = str(key).lower()
            if any(
                s in key_l
                for s in (
                    "token",
                    "secret",
                    "password",
                    "authorization",
                    "cookie",
                    "api_key",
                    "apikey",
                    "management_key",
                    "management-key",
                )
            ):
                redacted[key] = "[REDACTED]"
            else:
                redacted[key] = redact_obj(value)
        return redacted
    if isinstance(obj, list):
        return [redact_obj(v) for v in obj]
    return obj


class RedactingFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        if isinstance(record.msg, str):
            record.msg = redact_text(record.msg)
        if record.args:
            if isinstance(record.args, dict):
                record.args = redact_obj(record.args)
            elif isinstance(record.args, tuple):
                record.args = tuple(redact_obj(a) if isinstance(a, str) else a for a in record.args)
        return True


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------


@dataclass
class Config:
    management_url: str
    management_key: str
    once: bool = True
    daemon: bool = False
    interval_seconds: int = DEFAULT_INTERVAL_SECONDS
    round_robin: bool = False
    isolate: Optional[str] = None
    json_output: bool = False
    dry_run: bool = True
    apply: bool = False
    target_active: int = DEFAULT_TARGET_ACTIVE
    quota_limit_tokens: int = DEFAULT_QUOTA_LIMIT_TOKENS
    reset_window_hours: int = DEFAULT_RESET_WINDOW_HOURS
    state_file: str = DEFAULT_STATE_FILE
    concurrency: int = DEFAULT_CONCURRENCY
    batch_size: int = DEFAULT_BATCH_SIZE
    max_probes_per_cycle: int = DEFAULT_MAX_PROBES_PER_CYCLE
    timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS
    probe_rate_per_second: float = DEFAULT_PROBE_RATE_PER_SECOND
    rollback_run: Optional[str] = None
    log_level: str = "INFO"
    management_key_env: str = "CLIPROXY_MANAGEMENT_KEY"

    def validate(self) -> None:
        errors: List[str] = []
        if not self.management_url:
            errors.append("--management-url or CLIPROXY_MANAGEMENT_URL is required")
        if not self.management_key:
            errors.append(
                f"--management-key or env {self.management_key_env} is required "
                "(or CLIPROXY_MANAGEMENT_KEY)"
            )
        if self.daemon and self.once and not self.rollback_run:
            # once defaults True; daemon overrides to loop
            pass
        if self.target_active < 0:
            errors.append("--target-active must be >= 0")
        if self.quota_limit_tokens < 0:
            errors.append("--quota-limit-tokens must be >= 0")
        if self.reset_window_hours <= 0:
            errors.append("--reset-window-hours must be > 0")
        if self.concurrency < 1:
            errors.append("--concurrency must be >= 1")
        if self.concurrency > 32:
            errors.append("--concurrency must be <= 32")
        if self.batch_size < 1:
            errors.append("--batch-size must be >= 1")
        if self.max_probes_per_cycle < 0:
            errors.append("--max-probes-per-cycle must be >= 0")
        if self.timeout_seconds <= 0:
            errors.append("--timeout-seconds must be > 0")
        if self.interval_seconds < 1:
            errors.append("--interval-seconds must be >= 1")
        if self.apply and self.dry_run:
            # apply wins: mutating allowed
            self.dry_run = False
        if not self.apply:
            self.dry_run = True
        if errors:
            raise SystemExit("argument validation failed:\n  - " + "\n  - ".join(errors))


def build_arg_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="grok_credential_checker.py",
        description=(
            "Inspect Grok/XAI free-tier OAuth credentials via the Management API, "
            "disable exhausted accounts, and keep a configurable active pool."
        ),
    )
    p.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    mode = p.add_mutually_exclusive_group()
    mode.add_argument(
        "--once",
        action="store_true",
        default=True,
        help="Run a single cycle and exit (default).",
    )
    mode.add_argument(
        "--daemon",
        action="store_true",
        help="Run continuously with --interval-seconds between cycles.",
    )
    p.add_argument("--interval-seconds", type=int, default=DEFAULT_INTERVAL_SECONDS)
    p.add_argument(
        "--round-robin",
        action="store_true",
        help="Prefer stable round-robin ranking when usage metrics are unavailable.",
    )
    p.add_argument(
        "--isolate",
        metavar="NAME_OR_INDEX",
        help="Check only one credential (name or auth_index) and report.",
    )
    p.add_argument("--json", dest="json_output", action="store_true", help="Emit JSON report.")
    p.add_argument(
        "--dry-run",
        action="store_true",
        default=True,
        help="Report planned actions without PATCH (default).",
    )
    p.add_argument(
        "--apply",
        action="store_true",
        help="Apply status mutations. Required for any enable/disable changes.",
    )
    p.add_argument(
        "--management-url",
        default=os.environ.get("CLIPROXY_MANAGEMENT_URL", ""),
        help="Base Management API URL (env CLIPROXY_MANAGEMENT_URL).",
    )
    p.add_argument(
        "--management-key",
        default="",
        help="Management key (prefer env; do not commit secrets).",
    )
    p.add_argument(
        "--management-key-env",
        default="CLIPROXY_MANAGEMENT_KEY",
        help="Environment variable holding the management key (default CLIPROXY_MANAGEMENT_KEY).",
    )
    p.add_argument("--target-active", type=int, default=DEFAULT_TARGET_ACTIVE)
    p.add_argument("--quota-limit-tokens", type=int, default=DEFAULT_QUOTA_LIMIT_TOKENS)
    p.add_argument("--reset-window-hours", type=int, default=DEFAULT_RESET_WINDOW_HOURS)
    p.add_argument("--state-file", default=DEFAULT_STATE_FILE)
    p.add_argument("--concurrency", type=int, default=DEFAULT_CONCURRENCY)
    p.add_argument("--batch-size", type=int, default=DEFAULT_BATCH_SIZE)
    p.add_argument("--max-probes-per-cycle", type=int, default=DEFAULT_MAX_PROBES_PER_CYCLE)
    p.add_argument("--timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    p.add_argument(
        "--probe-rate-per-second",
        type=float,
        default=DEFAULT_PROBE_RATE_PER_SECOND,
        help="Max isolated fallback probes per second (default 1.0).",
    )
    p.add_argument(
        "--rollback-run",
        metavar="RUN_ID",
        help="Restore only that run's script-owned mutations that were not changed later.",
    )
    p.add_argument("--log-level", default="INFO", choices=["DEBUG", "INFO", "WARNING", "ERROR"])
    return p


def config_from_args(args: argparse.Namespace) -> Config:
    key = (args.management_key or "").strip()
    env_name = (args.management_key_env or "CLIPROXY_MANAGEMENT_KEY").strip()
    if not key:
        key = (os.environ.get(env_name) or os.environ.get("CLIPROXY_MANAGEMENT_KEY") or "").strip()
    cfg = Config(
        management_url=(args.management_url or "").strip().rstrip("/"),
        management_key=key,
        once=bool(args.once) and not bool(args.daemon),
        daemon=bool(args.daemon),
        interval_seconds=int(args.interval_seconds),
        round_robin=bool(args.round_robin),
        isolate=(args.isolate or None),
        json_output=bool(args.json_output),
        dry_run=not bool(args.apply),
        apply=bool(args.apply),
        target_active=int(args.target_active),
        quota_limit_tokens=int(args.quota_limit_tokens),
        reset_window_hours=int(args.reset_window_hours),
        state_file=str(args.state_file),
        concurrency=int(args.concurrency),
        batch_size=int(args.batch_size),
        max_probes_per_cycle=int(args.max_probes_per_cycle),
        timeout_seconds=int(args.timeout_seconds),
        probe_rate_per_second=float(args.probe_rate_per_second),
        rollback_run=(args.rollback_run or None),
        log_level=str(args.log_level),
        management_key_env=env_name,
    )
    cfg.validate()
    return cfg


# ---------------------------------------------------------------------------
# Domain models
# ---------------------------------------------------------------------------


@dataclass
class Credential:
    name: str
    auth_index: str
    provider: str
    disabled: bool
    unavailable: bool = False
    status: str = ""
    status_message: str = ""
    next_retry_after: Optional[datetime] = None
    runtime_only: bool = False
    raw: Dict[str, Any] = field(default_factory=dict, repr=False)

    @property
    def identity(self) -> str:
        return self.name or self.auth_index


@dataclass
class Classification:
    classification: str
    reason: str = ""
    source: str = ""
    reset_at: Optional[datetime] = None
    usage_tokens: Optional[int] = None
    usage_limit_tokens: Optional[int] = None
    units: str = ""
    message: str = ""
    retry_after_seconds: Optional[float] = None


@dataclass
class CredentialState:
    name: str
    auth_index: str = ""
    ownership: str = OWNERSHIP_EXTERNAL
    last_script_disabled: Optional[bool] = None
    last_observed_disabled: Optional[bool] = None
    disable_reason: Optional[str] = None
    quota_source: Optional[str] = None
    usage_tokens: Optional[int] = None
    usage_limit_tokens: Optional[int] = None
    reset_at: Optional[str] = None
    last_check: Optional[str] = None
    last_error_class: Optional[str] = None
    consecutive_failures: int = 0
    last_action: Optional[str] = None
    last_action_run_id: Optional[str] = None
    classification: Optional[str] = None
    rr_cursor: int = 0

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)

    @staticmethod
    def from_dict(data: Dict[str, Any]) -> "CredentialState":
        return CredentialState(
            name=str(data.get("name") or ""),
            auth_index=str(data.get("auth_index") or ""),
            ownership=str(data.get("ownership") or OWNERSHIP_EXTERNAL),
            last_script_disabled=data.get("last_script_disabled"),
            last_observed_disabled=data.get("last_observed_disabled"),
            disable_reason=data.get("disable_reason"),
            quota_source=data.get("quota_source"),
            usage_tokens=data.get("usage_tokens"),
            usage_limit_tokens=data.get("usage_limit_tokens"),
            reset_at=data.get("reset_at"),
            last_check=data.get("last_check"),
            last_error_class=data.get("last_error_class"),
            consecutive_failures=int(data.get("consecutive_failures") or 0),
            last_action=data.get("last_action"),
            last_action_run_id=data.get("last_action_run_id"),
            classification=data.get("classification"),
            rr_cursor=int(data.get("rr_cursor") or 0),
        )


@dataclass
class PlannedAction:
    name: str
    auth_index: str
    old_disabled: bool
    new_disabled: bool
    reason: str
    classification: str = ""
    ownership: str = ""
    reset_at: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        return {
            "name": self.name,
            "auth_index": self.auth_index,
            "old_disabled": self.old_disabled,
            "new_disabled": self.new_disabled,
            "reason": self.reason,
            "classification": self.classification,
            "ownership": self.ownership,
            "reset_at": self.reset_at,
        }


@dataclass
class CycleReport:
    run_id: str
    started_at: str
    finished_at: str = ""
    dry_run: bool = True
    safe_mode: bool = False
    inventory_total: int = 0
    xai_total: int = 0
    active_healthy: int = 0
    active_target: int = DEFAULT_TARGET_ACTIVE
    active_shortfall: int = 0
    probes_used: int = 0
    max_probes: int = DEFAULT_MAX_PROBES_PER_CYCLE
    concurrency_used: int = 0
    batch_size: int = DEFAULT_BATCH_SIZE
    classifications: Dict[str, int] = field(default_factory=dict)
    planned_actions: List[Dict[str, Any]] = field(default_factory=list)
    applied_actions: List[Dict[str, Any]] = field(default_factory=list)
    skipped: List[Dict[str, Any]] = field(default_factory=list)
    errors: List[str] = field(default_factory=list)
    notes: List[str] = field(default_factory=list)

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)


# ---------------------------------------------------------------------------
# Management API client
# ---------------------------------------------------------------------------


class ManagementAPIError(Exception):
    def __init__(self, message: str, status: Optional[int] = None, body: str = "") -> None:
        super().__init__(redact_text(message))
        self.status = status
        self.body = redact_text(body)


class ManagementClient:
    def __init__(
        self,
        base_url: str,
        management_key: str,
        timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS,
        opener: Optional[Callable[..., HTTPResponse]] = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self._key = management_key
        self.timeout_seconds = timeout_seconds
        self._opener = opener or urlopen
        self.request_log: List[Dict[str, Any]] = []
        self._lock = threading.Lock()
        self.peak_in_flight = 0
        self._in_flight = 0

    def _headers(self) -> Dict[str, str]:
        return {
            "Authorization": f"Bearer {self._key}",
            "X-Management-Key": self._key,
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": f"grok-credential-checker/{__version__}",
        }

    def _url(self, path: str) -> str:
        if path.startswith("http://") or path.startswith("https://"):
            return path
        base = self.base_url
        if not base.endswith("/v0/management") and "/v0/management" not in base:
            base = urljoin(base + "/", "v0/management")
        return urljoin(base.rstrip("/") + "/", path.lstrip("/"))

    def request(
        self,
        method: str,
        path: str,
        payload: Optional[Dict[str, Any]] = None,
    ) -> Tuple[int, Any, str]:
        url = self._url(path)
        data = None
        if payload is not None:
            data = json.dumps(payload).encode("utf-8")
        req = Request(url, data=data, headers=self._headers(), method=method.upper())
        with self._lock:
            self._in_flight += 1
            self.peak_in_flight = max(self.peak_in_flight, self._in_flight)
            self.request_log.append(
                {
                    "method": method.upper(),
                    "path": path,
                    "payload": redact_obj(payload) if payload else None,
                }
            )
        try:
            with self._opener(req, timeout=self.timeout_seconds) as resp:
                raw = resp.read().decode("utf-8", errors="replace")
                status = getattr(resp, "status", None) or resp.getcode()
        except HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace") if exc.fp else ""
            raise ManagementAPIError(
                f"Management API {method} {path} failed: HTTP {exc.code}",
                status=exc.code,
                body=raw,
            ) from None
        except URLError as exc:
            raise ManagementAPIError(
                f"Management API {method} {path} transport error: {redact_text(str(exc.reason))}"
            ) from None
        finally:
            with self._lock:
                self._in_flight = max(0, self._in_flight - 1)

        body: Any = None
        if raw.strip():
            try:
                body = json.loads(raw)
            except json.JSONDecodeError:
                body = raw
        if status >= 400:
            raise ManagementAPIError(
                f"Management API {method} {path} failed: HTTP {status}",
                status=status,
                body=raw,
            )
        return status, body, raw

    def list_auth_files(self) -> List[Dict[str, Any]]:
        _, body, _ = self.request("GET", "auth-files")
        if isinstance(body, dict):
            files = body.get("files") or []
            if isinstance(files, list):
                return [f for f in files if isinstance(f, dict)]
        if isinstance(body, list):
            return [f for f in body if isinstance(f, dict)]
        return []

    def set_status(self, name: str, disabled: bool) -> Dict[str, Any]:
        _, body, _ = self.request(
            "PATCH",
            "auth-files/status",
            {"name": name, "disabled": bool(disabled)},
        )
        return body if isinstance(body, dict) else {"status": "ok", "disabled": disabled}

    def api_call(
        self,
        auth_index: str,
        method: str,
        url: str,
        header: Optional[Dict[str, str]] = None,
        data: str = "",
    ) -> Dict[str, Any]:
        payload: Dict[str, Any] = {
            "auth_index": auth_index,
            "method": method,
            "url": url,
            "header": header or dict(XAI_REQUEST_HEADERS),
        }
        if data:
            payload["data"] = data
        _, body, _ = self.request("POST", "api-call", payload)
        if not isinstance(body, dict):
            return {"status_code": 0, "header": {}, "body": str(body)}
        return body


# ---------------------------------------------------------------------------
# Inventory filtering
# ---------------------------------------------------------------------------


def _as_bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return False


def normalize_provider(value: Any) -> str:
    key = str(value or "").strip().lower().replace("_", "-")
    if key in {"x-ai", "grok", "xai"}:
        return "xai"
    return key


def is_xai_oauth_record(item: Dict[str, Any]) -> bool:
    provider = normalize_provider(item.get("provider") or item.get("type"))
    if provider != "xai":
        return False
    # Ignore plugin virtual children / pure runtime-only without file identity when disabled.
    if _as_bool(item.get("runtime_only")) and _as_bool(item.get("disabled")):
        return False
    name = str(item.get("name") or item.get("id") or "").strip()
    auth_index = item.get("auth_index")
    if auth_index is None:
        auth_index = item.get("authIndex")
    auth_index_s = str(auth_index).strip() if auth_index is not None else ""
    if not name or not auth_index_s:
        return False
    # Config API keys sometimes share provider; prefer file-like names for OAuth.
    source = str(item.get("source") or "").strip().lower()
    if source == "config":
        return False
    return True


def normalize_credential(item: Dict[str, Any]) -> Optional[Credential]:
    if not is_xai_oauth_record(item):
        return None
    name = str(item.get("name") or item.get("id") or "").strip()
    auth_index_raw = item.get("auth_index")
    if auth_index_raw is None:
        auth_index_raw = item.get("authIndex")
    auth_index = str(auth_index_raw).strip()
    next_retry = parse_iso(item.get("next_retry_after") or item.get("nextRetryAfter"))
    return Credential(
        name=name,
        auth_index=auth_index,
        provider=normalize_provider(item.get("provider") or item.get("type")),
        disabled=_as_bool(item.get("disabled")),
        unavailable=_as_bool(item.get("unavailable")),
        status=str(item.get("status") or "").strip(),
        status_message=str(item.get("status_message") or item.get("statusMessage") or "").strip(),
        next_retry_after=next_retry,
        runtime_only=_as_bool(item.get("runtime_only") or item.get("runtimeOnly")),
        raw=item,
    )


def discover_xai_credentials(files: Sequence[Dict[str, Any]]) -> List[Credential]:
    out: List[Credential] = []
    seen: set[str] = set()
    for item in files:
        cred = normalize_credential(item)
        if cred is None:
            continue
        key = cred.name.lower()
        if key in seen:
            continue
        seen.add(key)
        out.append(cred)
    out.sort(key=lambda c: c.name.lower())
    return out


# ---------------------------------------------------------------------------
# State store
# ---------------------------------------------------------------------------


class ProcessLock:
    def __init__(self, path: str) -> None:
        self.path = path
        self._fh: Optional[Any] = None

    def acquire(self) -> None:
        directory = os.path.dirname(os.path.abspath(self.path)) or "."
        os.makedirs(directory, exist_ok=True)
        self._fh = open(self.path, "a+", encoding="utf-8")
        try:
            fcntl.flock(self._fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            try:
                self._fh.close()
            except OSError:
                pass
            self._fh = None
            if exc.errno in (errno.EACCES, errno.EAGAIN):
                raise SystemExit(
                    f"another grok_credential_checker instance holds the lock: {self.path}"
                ) from None
            raise
        self._fh.seek(0)
        self._fh.truncate()
        self._fh.write(f"pid={os.getpid()} time={to_iso(utcnow())}\n")
        self._fh.flush()

    def release(self) -> None:
        if self._fh is None:
            return
        try:
            fcntl.flock(self._fh.fileno(), fcntl.LOCK_UN)
        finally:
            self._fh.close()
            self._fh = None

    def __enter__(self) -> "ProcessLock":
        self.acquire()
        return self

    def __exit__(self, *args: Any) -> None:
        self.release()


class StateStore:
    def __init__(self, path: str, now_fn: Callable[[], datetime] = utcnow) -> None:
        self.path = path
        self.now_fn = now_fn
        self.safe_mode = False
        self.safe_mode_reason = ""
        self.credentials: Dict[str, CredentialState] = {}
        self.runs: Dict[str, Dict[str, Any]] = {}
        self.meta: Dict[str, Any] = {"version": STATE_VERSION}
        self.rr_global = 0

    def load(self) -> None:
        if not os.path.exists(self.path):
            self.safe_mode = True
            self.safe_mode_reason = "state file absent"
            return
        try:
            with open(self.path, "r", encoding="utf-8") as fh:
                data = json.load(fh)
        except (OSError, json.JSONDecodeError) as exc:
            self.safe_mode = True
            self.safe_mode_reason = f"state file invalid: {exc}"
            LOG.warning("entering safe mode: %s", self.safe_mode_reason)
            return
        if not isinstance(data, dict) or int(data.get("version") or 0) != STATE_VERSION:
            self.safe_mode = True
            self.safe_mode_reason = "state version missing or unsupported"
            LOG.warning("entering safe mode: %s", self.safe_mode_reason)
            return
        creds = data.get("credentials") or {}
        if not isinstance(creds, dict):
            self.safe_mode = True
            self.safe_mode_reason = "credentials map corrupt"
            return
        for key, value in creds.items():
            if isinstance(value, dict):
                st = CredentialState.from_dict(value)
                if not st.name:
                    st.name = str(key)
                self.credentials[st.name] = st
        runs = data.get("runs") or {}
        if isinstance(runs, dict):
            self.runs = runs
        self.rr_global = int(data.get("rr_global") or 0)
        self.meta = {k: v for k, v in data.items() if k not in {"credentials", "runs"}}
        self.meta["version"] = STATE_VERSION

    def save(self) -> None:
        payload = {
            "version": STATE_VERSION,
            "updated_at": to_iso(self.now_fn()),
            "rr_global": self.rr_global,
            "credentials": {name: st.to_dict() for name, st in sorted(self.credentials.items())},
            "runs": self.runs,
        }
        # never persist secrets
        text = json.dumps(redact_obj(payload), indent=2, sort_keys=True) + "\n"
        directory = os.path.dirname(os.path.abspath(self.path)) or "."
        os.makedirs(directory, exist_ok=True)
        fd, tmp_path = tempfile.mkstemp(prefix=".grok-checker-", suffix=".tmp", dir=directory)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                fh.write(text)
                fh.flush()
                os.fsync(fh.fileno())
            os.replace(tmp_path, self.path)
            # best-effort fsync directory
            try:
                dir_fd = os.open(directory, os.O_DIRECTORY)
                try:
                    os.fsync(dir_fd)
                finally:
                    os.close(dir_fd)
            except OSError:
                pass
        except Exception:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
            raise

    def get(self, name: str) -> CredentialState:
        st = self.credentials.get(name)
        if st is None:
            st = CredentialState(name=name)
            self.credentials[name] = st
        return st

    def record_run_actions(self, run_id: str, actions: List[Dict[str, Any]]) -> None:
        self.runs[run_id] = {
            "started_at": to_iso(self.now_fn()),
            "actions": actions,
        }
        # keep last 50 runs
        if len(self.runs) > 50:
            ordered = sorted(self.runs.items(), key=lambda kv: str(kv[1].get("started_at") or ""))
            for old_id, _ in ordered[: len(self.runs) - 50]:
                self.runs.pop(old_id, None)


def sync_ownership(state: CredentialState, live_disabled: bool) -> CredentialState:
    """Update ownership from live disabled vs last script mutation."""
    state.last_observed_disabled = live_disabled
    if state.last_script_disabled is not None and live_disabled != state.last_script_disabled:
        state.ownership = OWNERSHIP_MANUAL_OVERRIDE
    elif state.ownership == OWNERSHIP_MANUAL_OVERRIDE:
        pass
    elif state.last_script_disabled is None:
        # Never mutated by script.
        if live_disabled:
            state.ownership = OWNERSHIP_EXTERNAL
        # enabled + external stays external until we mutate it
    return state


# ---------------------------------------------------------------------------
# Quota / error adapter
# ---------------------------------------------------------------------------


def _contains_any(text: str, needles: Sequence[str]) -> bool:
    low = text.lower()
    return any(n in low for n in needles)


def parse_token_usage_from_text(text: str) -> Tuple[Optional[int], Optional[int]]:
    match = TOKEN_USAGE_RE.search(text or "")
    if not match:
        return None, None
    return int(match.group(1)), int(match.group(2))


def parse_retry_after_header(headers: Any) -> Optional[float]:
    if not headers:
        return None
    values: List[str] = []
    if isinstance(headers, dict):
        for key, val in headers.items():
            if str(key).lower() == "retry-after":
                if isinstance(val, list):
                    values.extend(str(v) for v in val)
                else:
                    values.append(str(val))
    for raw in values:
        text = raw.strip()
        if not text:
            continue
        if text.isdigit():
            return float(text)
        dt = parse_iso(text)
        if dt is not None:
            return max(0.0, (dt - utcnow()).total_seconds())
    return None


def classify_from_runtime(
    cred: Credential,
    now: datetime,
    reset_window_hours: int,
) -> Optional[Classification]:
    msg = cred.status_message or ""
    status = (cred.status or "").lower()
    combined = f"{status} {msg}".strip()

    if _contains_any(combined, ("verification", "verify your", "phone verification", "captcha")):
        return Classification(
            CLASS_VERIFICATION,
            reason=REASON_VERIFICATION,
            source="runtime.status_message",
            message=msg,
        )
    if _contains_any(
        combined,
        ("unauthorized", "unauthenticated", "invalid_grant", "token expired", "revoked", "login"),
    ) or status in {"error", "invalid"}:
        # only if message hints auth; bare error is weak
        if _contains_any(
            combined,
            (
                "unauthorized",
                "unauthenticated",
                "invalid",
                "expired",
                "revoked",
                "re-login",
                "relogin",
                "login",
            ),
        ):
            return Classification(
                CLASS_AUTH_INVALID,
                reason=REASON_AUTH_INVALID,
                source="runtime.status_message",
                message=msg,
            )

    if _contains_any(combined, ("free-usage-exhausted", "included free usage")):
        usage, limit = parse_token_usage_from_text(msg)
        reset_at = cred.next_retry_after or (now + timedelta(hours=reset_window_hours))
        return Classification(
            CLASS_EXHAUSTED,
            reason=REASON_QUOTA_EXHAUSTED,
            source="runtime.status_message",
            reset_at=reset_at,
            usage_tokens=usage,
            usage_limit_tokens=limit,
            units="tokens" if usage is not None else "",
            message=msg,
        )

    if cred.next_retry_after and cred.next_retry_after > now:
        # cooldown from backend without explicit free-usage text
        cls = CLASS_COOLDOWN
        if cred.unavailable or status in {"cooldown", "rate_limited", "quota"}:
            cls = CLASS_COOLDOWN
        return Classification(
            cls,
            reason="runtime_cooldown",
            source="runtime.next_retry_after",
            reset_at=cred.next_retry_after,
            message=msg,
        )

    if cred.unavailable and not cred.disabled:
        return Classification(
            CLASS_UNKNOWN,
            reason="unavailable",
            source="runtime.unavailable",
            message=msg or "unavailable",
        )
    return None


def classify_upstream_response(
    status_code: int,
    body_text: str,
    headers: Any,
    now: datetime,
    reset_window_hours: int,
    source: str,
) -> Classification:
    text = body_text or ""
    low = text.lower()
    usage, limit = parse_token_usage_from_text(text)
    retry_after = parse_retry_after_header(headers)

    if status_code in (401, 403):
        if _contains_any(low, ("verification", "verify", "captcha")):
            return Classification(
                CLASS_VERIFICATION,
                reason=REASON_VERIFICATION,
                source=source,
                message=text[:500],
            )
        return Classification(
            CLASS_AUTH_INVALID,
            reason=REASON_AUTH_INVALID,
            source=source,
            message=text[:500],
        )

    if status_code == 429 or _contains_any(low, ("free-usage-exhausted", "included free usage")):
        if _contains_any(low, ("free-usage-exhausted", "included free usage")):
            reset_at = None
            # Prefer explicit provider reset if present as ISO in body (rare)
            for key in ("reset_at", "resetAt", "resets_at"):
                try:
                    parsed_body = json.loads(text) if text.strip().startswith("{") else {}
                except json.JSONDecodeError:
                    parsed_body = {}
                if isinstance(parsed_body, dict) and parsed_body.get(key):
                    reset_at = parse_iso(parsed_body.get(key))
                    if reset_at:
                        break
            if reset_at is None and retry_after is not None:
                reset_at = now + timedelta(seconds=retry_after)
            if reset_at is None:
                reset_at = now + timedelta(hours=reset_window_hours)
            return Classification(
                CLASS_EXHAUSTED,
                reason=REASON_QUOTA_EXHAUSTED,
                source=source,
                reset_at=reset_at,
                usage_tokens=usage,
                usage_limit_tokens=limit,
                units="tokens" if usage is not None else "",
                message=text[:500],
                retry_after_seconds=retry_after,
            )
        # generic 429: not quota exhaustion
        return Classification(
            CLASS_UPSTREAM_ERROR,
            reason="generic_429",
            source=source,
            message=text[:500],
            retry_after_seconds=retry_after,
        )

    if status_code >= 500 or status_code == 0:
        return Classification(
            CLASS_UPSTREAM_ERROR,
            reason=f"http_{status_code or 'transport'}",
            source=source,
            message=text[:500],
        )

    if 200 <= status_code < 300:
        # successful billing/probe — treat as healthy unless body says otherwise
        if _contains_any(low, ("free-usage-exhausted", "included free usage")):
            reset_at = now + timedelta(hours=reset_window_hours)
            return Classification(
                CLASS_EXHAUSTED,
                reason=REASON_QUOTA_EXHAUSTED,
                source=source,
                reset_at=reset_at,
                usage_tokens=usage,
                usage_limit_tokens=limit,
                units="tokens" if usage is not None else "",
                message=text[:500],
            )
        return Classification(
            CLASS_HEALTHY,
            reason="ok",
            source=source,
            usage_tokens=usage,
            usage_limit_tokens=limit,
            units="tokens" if usage is not None else "",
            message="ok",
        )

    return Classification(
        CLASS_UNKNOWN,
        reason=f"http_{status_code}",
        source=source,
        message=text[:500],
    )


def extract_api_call_body(result: Dict[str, Any]) -> str:
    body = result.get("body")
    if body is None:
        return ""
    if isinstance(body, (dict, list)):
        return json.dumps(body)
    return str(body)


class RateLimiter:
    def __init__(self, rate_per_second: float) -> None:
        self.interval = 1.0 / rate_per_second if rate_per_second > 0 else 0.0
        self._lock = threading.Lock()
        self._next = 0.0

    def wait(self) -> None:
        if self.interval <= 0:
            return
        with self._lock:
            now = time.monotonic()
            if now < self._next:
                delay = self._next - now
            else:
                delay = 0.0
            self._next = max(now, self._next) + self.interval
        if delay > 0:
            time.sleep(delay)


class GrokQuotaAdapter:
    def __init__(
        self,
        client: ManagementClient,
        reset_window_hours: int = DEFAULT_RESET_WINDOW_HOURS,
        probe_rate: float = DEFAULT_PROBE_RATE_PER_SECOND,
        now_fn: Callable[[], datetime] = utcnow,
    ) -> None:
        self.client = client
        self.reset_window_hours = reset_window_hours
        self.now_fn = now_fn
        self.probe_limiter = RateLimiter(probe_rate)
        self.probes_used = 0
        self._probe_lock = threading.Lock()

    def classify_runtime(self, cred: Credential) -> Optional[Classification]:
        return classify_from_runtime(cred, self.now_fn(), self.reset_window_hours)

    def classify_billing(self, cred: Credential) -> Classification:
        try:
            result = self.client.api_call(
                cred.auth_index,
                "GET",
                XAI_BILLING_URL,
                header=dict(XAI_REQUEST_HEADERS),
            )
        except ManagementAPIError as exc:
            return Classification(
                CLASS_UPSTREAM_ERROR,
                reason="management_api_error",
                source="api-call.billing",
                message=str(exc),
            )
        status = int(result.get("status_code") or 0)
        body = extract_api_call_body(result)
        headers = result.get("header") or {}
        return classify_upstream_response(
            status,
            body,
            headers,
            self.now_fn(),
            self.reset_window_hours,
            source="api-call.billing",
        )

    def classify_probe(self, cred: Credential) -> Classification:
        with self._probe_lock:
            self.probes_used += 1
        self.probe_limiter.wait()
        try:
            result = self.client.api_call(
                cred.auth_index,
                "GET",
                XAI_PROBE_URL,
                header=dict(XAI_REQUEST_HEADERS),
            )
        except ManagementAPIError as exc:
            return Classification(
                CLASS_UPSTREAM_ERROR,
                reason="management_api_error",
                source="api-call.probe",
                message=str(exc),
            )
        status = int(result.get("status_code") or 0)
        body = extract_api_call_body(result)
        headers = result.get("header") or {}
        return classify_upstream_response(
            status,
            body,
            headers,
            self.now_fn(),
            self.reset_window_hours,
            source="api-call.probe",
        )


# ---------------------------------------------------------------------------
# Policy + pool reconciliation
# ---------------------------------------------------------------------------


def remaining_quota_ratio(state: CredentialState, quota_limit: int) -> Optional[float]:
    if state.usage_tokens is None:
        return None
    limit = state.usage_limit_tokens if state.usage_limit_tokens else quota_limit
    if not limit or limit <= 0:
        return None
    used = max(0, int(state.usage_tokens))
    return max(0.0, min(1.0, 1.0 - (used / float(limit))))


def is_reset_due(state: CredentialState, now: datetime) -> bool:
    if state.disable_reason != REASON_QUOTA_EXHAUSTED:
        return False
    if state.ownership != OWNERSHIP_MANAGED:
        return False
    if not state.reset_at:
        return False
    reset_at = parse_iso(state.reset_at)
    if reset_at is None:
        return False
    return reset_at <= now


def apply_classification_to_state(
    state: CredentialState,
    cls: Classification,
    now: datetime,
) -> None:
    state.classification = cls.classification
    state.last_check = to_iso(now)
    state.quota_source = cls.source or state.quota_source
    if cls.usage_tokens is not None:
        state.usage_tokens = cls.usage_tokens
    if cls.usage_limit_tokens is not None:
        state.usage_limit_tokens = cls.usage_limit_tokens
    if cls.reset_at is not None:
        state.reset_at = to_iso(cls.reset_at)
    if cls.classification in {
        CLASS_UPSTREAM_ERROR,
        CLASS_UNKNOWN,
    }:
        state.consecutive_failures += 1
        state.last_error_class = cls.classification
    else:
        state.consecutive_failures = 0
        if cls.classification == CLASS_HEALTHY:
            state.last_error_class = None


def desired_disable_for_active(
    cred: Credential,
    state: CredentialState,
    cls: Classification,
) -> Optional[PlannedAction]:
    """Return a disable action for active (enabled) credentials when policy requires it."""
    if cred.disabled:
        return None
    if state.ownership == OWNERSHIP_MANUAL_OVERRIDE:
        return None
    if cls.classification == CLASS_EXHAUSTED:
        return PlannedAction(
            name=cred.name,
            auth_index=cred.auth_index,
            old_disabled=False,
            new_disabled=True,
            reason=REASON_QUOTA_EXHAUSTED,
            classification=cls.classification,
            ownership=OWNERSHIP_MANAGED,
            reset_at=to_iso(cls.reset_at),
        )
    if cls.classification == CLASS_AUTH_INVALID:
        return PlannedAction(
            name=cred.name,
            auth_index=cred.auth_index,
            old_disabled=False,
            new_disabled=True,
            reason=REASON_AUTH_INVALID,
            classification=cls.classification,
            ownership=OWNERSHIP_MANAGED,
        )
    if cls.classification == CLASS_VERIFICATION:
        return PlannedAction(
            name=cred.name,
            auth_index=cred.auth_index,
            old_disabled=False,
            new_disabled=True,
            reason=REASON_VERIFICATION,
            classification=cls.classification,
            ownership=OWNERSHIP_MANAGED,
        )
    # 5xx / unknown / cooldown without explicit exhaustion: do not mutate
    return None


def rank_key(
    cred: Credential,
    state: CredentialState,
    quota_limit: int,
    round_robin: bool,
    rr_global: int,
) -> Tuple[Any, ...]:
    # Lower is better for enable priority / higher priority to keep active.
    healthy_rank = 0 if state.classification == CLASS_HEALTHY else 1
    ratio = remaining_quota_ratio(state, quota_limit)
    # higher remaining quota first => use negative ratio
    ratio_key = -ratio if ratio is not None else 0.0
    reset_bonus = 0 if (state.reset_at and is_reset_due(state, utcnow())) else 1
    failures = state.consecutive_failures
    if round_robin or ratio is None:
        # stable round-robin using deterministic name digest + global cursor
        digest = int(hashlib.sha1(cred.name.encode("utf-8")).hexdigest()[:8], 16)
        rr = (digest + rr_global) % 10_000_000
        return (healthy_rank, failures, rr, cred.name.lower())
    return (healthy_rank, failures, ratio_key, reset_bonus, cred.name.lower())


def reconcile_pool(
    credentials: Sequence[Credential],
    states: Dict[str, CredentialState],
    classifications: Dict[str, Classification],
    target_active: int,
    quota_limit: int,
    round_robin: bool,
    rr_global: int,
    safe_mode: bool,
    now: datetime,
) -> Tuple[List[PlannedAction], int, List[Dict[str, Any]]]:
    """Compute enable/disable actions to approach target_active.

    Returns (actions, active_shortfall, skipped).
    """
    actions: List[PlannedAction] = []
    skipped: List[Dict[str, Any]] = []
    by_name = {c.name: c for c in credentials}

    # Start from live disabled flags, apply pending policy disables virtually.
    virtual_disabled: Dict[str, bool] = {c.name: c.disabled for c in credentials}
    virtual_class: Dict[str, str] = {
        name: (classifications[name].classification if name in classifications else (states[name].classification or CLASS_UNKNOWN))
        for name in by_name
    }

    # 1) Policy disables for enabled accounts.
    for cred in credentials:
        state = states[cred.name]
        cls = classifications.get(cred.name)
        if cls is None:
            continue
        action = desired_disable_for_active(cred, state, cls)
        if action:
            actions.append(action)
            virtual_disabled[cred.name] = True
            virtual_class[cred.name] = cls.classification

    # 2) Eligible healthy active set after policy disables.
    def is_healthy_active(name: str) -> bool:
        if virtual_disabled.get(name, True):
            return False
        cls_name = virtual_class.get(name, CLASS_UNKNOWN)
        if cls_name in {CLASS_EXHAUSTED, CLASS_AUTH_INVALID, CLASS_VERIFICATION}:
            return False
        # cooldown without disable still counts as not fully healthy for pool
        if cls_name == CLASS_COOLDOWN:
            return False
        if cls_name in {CLASS_UPSTREAM_ERROR, CLASS_UNKNOWN}:
            # keep currently-enabled unknowns as active occupancy but do not promote
            return not virtual_disabled.get(name, True)
        return cls_name == CLASS_HEALTHY or (
            cls_name not in {CLASS_EXHAUSTED, CLASS_AUTH_INVALID, CLASS_VERIFICATION, CLASS_COOLDOWN}
            and not virtual_disabled.get(name, True)
        )

    healthy_active = [n for n in by_name if is_healthy_active(n)]
    # Prefer ranking: worst candidates demoted first when surplus.
    healthy_active_sorted = sorted(
        healthy_active,
        key=lambda n: rank_key(by_name[n], states[n], quota_limit, round_robin, rr_global),
    )

    # 3) Surplus: demote lowest-ranked *managed* actives to standby.
    if len(healthy_active_sorted) > target_active:
        surplus = len(healthy_active_sorted) - target_active
        # demote from end (worst rank)
        demote_candidates = list(reversed(healthy_active_sorted))
        demoted = 0
        for name in demote_candidates:
            if demoted >= surplus:
                break
            state = states[name]
            if state.ownership not in {OWNERSHIP_MANAGED}:
                # only demote script-managed; if never managed, claim management by demoting
                # only if last_script_disabled is not None or we previously managed
                if state.last_script_disabled is None and state.ownership == OWNERSHIP_EXTERNAL:
                    # Taking ownership to enforce pool size on previously untouched enabled accounts
                    pass
            if state.ownership == OWNERSHIP_MANUAL_OVERRIDE:
                skipped.append({"name": name, "reason": "manual_override_not_demoted"})
                continue
            actions.append(
                PlannedAction(
                    name=name,
                    auth_index=by_name[name].auth_index,
                    old_disabled=False,
                    new_disabled=True,
                    reason=REASON_POOL_STANDBY,
                    classification=virtual_class.get(name, ""),
                    ownership=OWNERSHIP_MANAGED,
                )
            )
            virtual_disabled[name] = True
            demoted += 1
        healthy_active_sorted = [n for n in healthy_active_sorted if not virtual_disabled[n]]

    # 4) Shortage: enable eligible managed standby after reset.
    current_active = sum(1 for n in by_name if not virtual_disabled.get(n, True) and is_healthy_active(n) or (
        not virtual_disabled.get(n, True) and virtual_class.get(n) == CLASS_HEALTHY
    ))
    # recompute simpler: enabled and not exhausted/invalid/verification/cooldown
    def counts_as_active(name: str) -> bool:
        if virtual_disabled.get(name, True):
            return False
        cls_name = virtual_class.get(name, CLASS_UNKNOWN)
        return cls_name not in {
            CLASS_EXHAUSTED,
            CLASS_AUTH_INVALID,
            CLASS_VERIFICATION,
            CLASS_COOLDOWN,
        }

    current_active = sum(1 for n in by_name if counts_as_active(n))
    shortfall = max(0, target_active - current_active)

    if shortfall > 0 and safe_mode:
        skipped.append(
            {
                "reason": "safe_mode_no_enable",
                "shortfall": shortfall,
            }
        )
        return actions, shortfall, skipped

    if shortfall > 0:
        candidates: List[str] = []
        for name, cred in by_name.items():
            if not virtual_disabled.get(name, True):
                continue
            state = states[name]
            if state.ownership == OWNERSHIP_MANUAL_OVERRIDE:
                continue
            if state.ownership == OWNERSHIP_EXTERNAL:
                continue
            if state.ownership != OWNERSHIP_MANAGED:
                continue
            # only auto-enable quota-disabled after reset; not invalid/verification
            if state.disable_reason not in {REASON_QUOTA_EXHAUSTED, REASON_POOL_STANDBY, None}:
                if state.disable_reason in {REASON_AUTH_INVALID, REASON_VERIFICATION}:
                    continue
            if state.disable_reason in {REASON_AUTH_INVALID, REASON_VERIFICATION}:
                continue
            cls = classifications.get(name)
            cls_name = cls.classification if cls else (state.classification or CLASS_UNKNOWN)
            if state.disable_reason == REASON_QUOTA_EXHAUSTED and not is_reset_due(state, now):
                continue
            if cls_name in {CLASS_AUTH_INVALID, CLASS_VERIFICATION, CLASS_EXHAUSTED, CLASS_COOLDOWN}:
                # allow reset-due exhausted to become candidate only if reset due already handled
                if not (state.disable_reason == REASON_QUOTA_EXHAUSTED and is_reset_due(state, now)):
                    if cls_name != CLASS_HEALTHY and state.disable_reason != REASON_POOL_STANDBY:
                        continue
            if state.disable_reason == REASON_POOL_STANDBY:
                if cls_name not in {CLASS_HEALTHY, CLASS_UNKNOWN, None, ""}:
                    if cls_name in {CLASS_AUTH_INVALID, CLASS_VERIFICATION, CLASS_EXHAUSTED}:
                        continue
            candidates.append(name)

        candidates_sorted = sorted(
            candidates,
            key=lambda n: rank_key(by_name[n], states[n], quota_limit, round_robin, rr_global),
        )
        enabled = 0
        for name in candidates_sorted:
            if enabled >= shortfall:
                break
            # mark healthy for refill
            actions.append(
                PlannedAction(
                    name=name,
                    auth_index=by_name[name].auth_index,
                    old_disabled=True,
                    new_disabled=False,
                    reason=REASON_POOL_REFILL,
                    classification=virtual_class.get(name, CLASS_HEALTHY),
                    ownership=OWNERSHIP_MANAGED,
                )
            )
            virtual_disabled[name] = False
            virtual_class[name] = CLASS_HEALTHY
            enabled += 1
        shortfall = max(0, shortfall - enabled)

    # Deduplicate actions per name (last wins by priority: policy disable > demote > enable)
    merged: Dict[str, PlannedAction] = {}
    for action in actions:
        prev = merged.get(action.name)
        if prev is None:
            merged[action.name] = action
            continue
        # Prefer disable for exhaustion over later enable, etc.
        priority = {
            REASON_QUOTA_EXHAUSTED: 100,
            REASON_AUTH_INVALID: 100,
            REASON_VERIFICATION: 100,
            REASON_POOL_STANDBY: 50,
            REASON_POOL_REFILL: 10,
        }
        if priority.get(action.reason, 0) >= priority.get(prev.reason, 0):
            merged[action.name] = action

    # Drop no-ops where old == new
    final = [a for a in merged.values() if a.old_disabled != a.new_disabled]
    final.sort(key=lambda a: a.name.lower())

    # virtual_disabled already reflects policy disables, demotions, and refills.
    current_active = sum(1 for n in by_name if counts_as_active(n))
    shortfall = max(0, target_active - current_active)

    return final, shortfall, skipped


# ---------------------------------------------------------------------------
# Cycle execution
# ---------------------------------------------------------------------------


def batched(items: Sequence[Any], size: int) -> Iterable[Sequence[Any]]:
    for i in range(0, len(items), size):
        yield items[i : i + size]


class Checker:
    def __init__(
        self,
        config: Config,
        client: Optional[ManagementClient] = None,
        store: Optional[StateStore] = None,
        now_fn: Callable[[], datetime] = utcnow,
    ) -> None:
        self.config = config
        self.now_fn = now_fn
        self.client = client or ManagementClient(
            config.management_url,
            config.management_key,
            timeout_seconds=config.timeout_seconds,
        )
        self.store = store or StateStore(config.state_file, now_fn=now_fn)
        self.adapter = GrokQuotaAdapter(
            self.client,
            reset_window_hours=config.reset_window_hours,
            probe_rate=config.probe_rate_per_second,
            now_fn=now_fn,
        )

    def run_once(self) -> CycleReport:
        run_id = uuid.uuid4().hex[:12]
        started = self.now_fn()
        report = CycleReport(
            run_id=run_id,
            started_at=to_iso(started) or "",
            dry_run=self.config.dry_run,
            active_target=self.config.target_active,
            max_probes=self.config.max_probes_per_cycle,
            batch_size=self.config.batch_size,
        )

        if not self.store.credentials and not os.path.exists(self.config.state_file):
            self.store.load()
        elif not self.store.credentials and os.path.exists(self.config.state_file):
            self.store.load()
        # If already loaded empty with safe_mode, keep it.
        if not hasattr(self.store, "_loaded_flag"):
            if not self.store.credentials and not self.store.safe_mode:
                self.store.load()
            self.store._loaded_flag = True  # type: ignore[attr-defined]

        report.safe_mode = self.store.safe_mode
        if self.store.safe_mode:
            report.notes.append(f"safe_mode: {self.store.safe_mode_reason}")

        if self.config.rollback_run:
            return self._rollback(report)

        try:
            files = self.client.list_auth_files()
        except ManagementAPIError as exc:
            report.errors.append(str(exc))
            report.finished_at = to_iso(self.now_fn()) or ""
            return report

        report.inventory_total = len(files)
        credentials = discover_xai_credentials(files)
        report.xai_total = len(credentials)

        if self.config.isolate:
            key = self.config.isolate.strip().lower()
            credentials = [
                c
                for c in credentials
                if c.name.lower() == key or c.auth_index.lower() == key
            ]
            if not credentials:
                report.errors.append(f"isolate target not found: {self.config.isolate}")
                report.finished_at = to_iso(self.now_fn()) or ""
                return report

        # Sync ownership state from live inventory.
        for cred in credentials:
            st = self.store.get(cred.name)
            st.auth_index = cred.auth_index
            sync_ownership(st, cred.disabled)

        # Decide who to inspect this cycle.
        to_check = self._select_check_targets(credentials)
        classifications: Dict[str, Classification] = {}
        probes_budget = self.config.max_probes_per_cycle
        peak_workers = 0

        for batch in batched(to_check, self.config.batch_size):
            workers = min(self.config.concurrency, len(batch))
            peak_workers = max(peak_workers, workers)
            results: Dict[str, Classification] = {}
            if workers <= 1:
                for cred in batch:
                    results[cred.name] = self._classify_one(cred, probes_budget)
                    if results[cred.name].source.endswith("probe"):
                        probes_budget = max(0, probes_budget - 1)
            else:
                with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
                    fut_map = {
                        pool.submit(self._classify_one, cred, probes_budget): cred for cred in batch
                    }
                    for fut in concurrent.futures.as_completed(fut_map):
                        cred = fut_map[fut]
                        try:
                            cls = fut.result()
                        except Exception as exc:  # pragma: no cover - defensive
                            cls = Classification(
                                CLASS_UNKNOWN,
                                reason="checker_exception",
                                source="local",
                                message=redact_text(str(exc)),
                            )
                        results[cred.name] = cls
            for name, cls in results.items():
                classifications[name] = cls
                apply_classification_to_state(self.store.get(name), cls, self.now_fn())
                report.classifications[cls.classification] = (
                    report.classifications.get(cls.classification, 0) + 1
                )
                if cls.source.endswith("probe"):
                    probes_budget = max(0, probes_budget - 1)

        # For credentials not checked this cycle, use stored classification / runtime snapshot.
        for cred in credentials:
            if cred.name in classifications:
                continue
            runtime_cls = self.adapter.classify_runtime(cred)
            if runtime_cls is not None:
                classifications[cred.name] = runtime_cls
                apply_classification_to_state(self.store.get(cred.name), runtime_cls, self.now_fn())
            else:
                st = self.store.get(cred.name)
                assumed = CLASS_HEALTHY if not cred.disabled else (st.classification or CLASS_UNKNOWN)
                classifications[cred.name] = Classification(
                    assumed,
                    reason="assumed_from_state",
                    source="state",
                )
                if not st.classification:
                    st.classification = assumed

        report.probes_used = self.adapter.probes_used
        report.concurrency_used = peak_workers or min(self.config.concurrency, 1)

        states = {c.name: self.store.get(c.name) for c in credentials}
        actions, shortfall, skipped = reconcile_pool(
            credentials,
            states,
            classifications,
            target_active=self.config.target_active,
            quota_limit=self.config.quota_limit_tokens,
            round_robin=self.config.round_robin,
            rr_global=self.store.rr_global,
            safe_mode=self.store.safe_mode,
            now=self.now_fn(),
        )
        report.active_shortfall = shortfall
        report.skipped = skipped
        report.planned_actions = [a.to_dict() for a in actions]
        report.active_healthy = sum(
            1
            for c in credentials
            if not c.disabled
            and classifications.get(c.name)
            and classifications[c.name].classification
            not in {CLASS_EXHAUSTED, CLASS_AUTH_INVALID, CLASS_VERIFICATION, CLASS_COOLDOWN}
        )

        applied: List[Dict[str, Any]] = []
        for action in actions:
            entry = action.to_dict()
            entry["time"] = to_iso(self.now_fn())
            entry["run_id"] = run_id
            if self.config.dry_run:
                entry["applied"] = False
                applied.append(entry)
                continue
            try:
                self.client.set_status(action.name, action.new_disabled)
                entry["applied"] = True
                st = self.store.get(action.name)
                st.ownership = OWNERSHIP_MANAGED
                st.last_script_disabled = action.new_disabled
                st.last_observed_disabled = action.new_disabled
                st.last_action = action.reason
                st.last_action_run_id = run_id
                if action.new_disabled:
                    st.disable_reason = action.reason
                    if action.reset_at:
                        st.reset_at = action.reset_at
                else:
                    # enabled
                    if st.disable_reason == REASON_QUOTA_EXHAUSTED:
                        st.disable_reason = None
                        st.reset_at = None
                    elif action.reason == REASON_POOL_REFILL:
                        st.disable_reason = None
                applied.append(entry)
            except ManagementAPIError as exc:
                entry["applied"] = False
                entry["error"] = str(exc)
                report.errors.append(f"{action.name}: {exc}")
                applied.append(entry)

        report.applied_actions = applied
        if not self.config.dry_run:
            journal = [
                {
                    "name": a["name"],
                    "old_disabled": a["old_disabled"],
                    "new_disabled": a["new_disabled"],
                    "reason": a["reason"],
                    "time": a.get("time"),
                    "applied": a.get("applied"),
                }
                for a in applied
                if a.get("applied")
            ]
            if journal:
                self.store.record_run_actions(run_id, journal)

        self.store.rr_global = (self.store.rr_global + 1) % 10_000_000
        # After first successful load cycle, leave safe mode only when state was absent?
        # Plan: missing/invalid => safe mode report-only no enable. Keep safe_mode sticky for this process
        # until valid state exists. After we save a new state file from absent, subsequent cycles can manage.
        try:
            self.store.save()
            # First successful write establishes a baseline. Keep safe_mode for the
            # cycle that discovered a missing/invalid file (already enforced above),
            # then allow subsequent daemon cycles to manage enables normally.
            if self.store.safe_mode and self.store.safe_mode_reason in {
                "state file absent",
                "state version missing or unsupported",
                "state file invalid",
            } or (
                self.store.safe_mode
                and str(self.store.safe_mode_reason).startswith("state file invalid")
            ):
                self.store.safe_mode = False
                self.store.safe_mode_reason = ""
        except OSError as exc:
            report.errors.append(f"state save failed: {exc}")

        report.finished_at = to_iso(self.now_fn()) or ""
        return report

    def _select_check_targets(self, credentials: Sequence[Credential]) -> List[Credential]:
        if self.config.isolate:
            return list(credentials)
        now = self.now_fn()
        active: List[Credential] = []
        reset_due: List[Credential] = []
        standby: List[Credential] = []
        for cred in credentials:
            st = self.store.get(cred.name)
            if not cred.disabled:
                active.append(cred)
            elif is_reset_due(st, now):
                reset_due.append(cred)
            else:
                standby.append(cred)
        # Estimate shortfall for refill probes
        active_count = len(active)
        need = max(0, self.config.target_active - active_count)
        # Probe budget for standby candidates only when needed
        refill_candidates = []
        if need > 0:
            managed_standby = [
                c
                for c in standby
                if self.store.get(c.name).ownership == OWNERSHIP_MANAGED
                and self.store.get(c.name).disable_reason
                not in {REASON_AUTH_INVALID, REASON_VERIFICATION}
            ]
            managed_standby.sort(key=lambda c: c.name.lower())
            refill_candidates = managed_standby[: max(need, self.config.max_probes_per_cycle)]
        # Bound: never return entire inventory for probing path — selection is intentional.
        selected = active + reset_due + refill_candidates
        # stable unique
        seen = set()
        out: List[Credential] = []
        for c in selected:
            if c.name in seen:
                continue
            seen.add(c.name)
            out.append(c)
        return out

    def _classify_one(self, cred: Credential, probes_budget: int) -> Classification:
        runtime = self.adapter.classify_runtime(cred)
        if runtime is not None and runtime.classification in {
            CLASS_EXHAUSTED,
            CLASS_AUTH_INVALID,
            CLASS_VERIFICATION,
            CLASS_COOLDOWN,
        }:
            return runtime

        # Active accounts: prefer runtime healthy if nothing pending; still try billing lightly.
        if not cred.disabled:
            if runtime is not None and runtime.classification == CLASS_HEALTHY:
                return runtime
            # billing is read-only observation
            billing = self.adapter.classify_billing(cred)
            if billing.classification != CLASS_UNKNOWN:
                # generic billing success without free-usage text => healthy
                if billing.classification == CLASS_UPSTREAM_ERROR and runtime is not None:
                    return runtime
                return billing
            if runtime is not None:
                return runtime
            return Classification(CLASS_HEALTHY, reason="active_default", source="runtime")

        # Standby / reset-due: may need probe if runtime/billing inconclusive and budget allows
        if runtime is not None:
            return runtime
        billing = self.adapter.classify_billing(cred)
        if billing.classification in {
            CLASS_HEALTHY,
            CLASS_EXHAUSTED,
            CLASS_AUTH_INVALID,
            CLASS_VERIFICATION,
        }:
            return billing
        if probes_budget > 0 and self.adapter.probes_used < self.config.max_probes_per_cycle:
            return self.adapter.classify_probe(cred)
        return billing if billing else Classification(CLASS_UNKNOWN, reason="budget", source="local")

    def _rollback(self, report: CycleReport) -> CycleReport:
        run_id = self.config.rollback_run or ""
        run = self.store.runs.get(run_id)
        if not run:
            report.errors.append(f"run not found in state journal: {run_id}")
            report.finished_at = to_iso(self.now_fn()) or ""
            return report
        actions = run.get("actions") or []
        # refresh live inventory for drift detection
        try:
            files = self.client.list_auth_files()
        except ManagementAPIError as exc:
            report.errors.append(str(exc))
            report.finished_at = to_iso(self.now_fn()) or ""
            return report
        live = {c.name: c for c in discover_xai_credentials(files)}
        restored: List[Dict[str, Any]] = []
        for action in actions:
            if not isinstance(action, dict):
                continue
            name = str(action.get("name") or "")
            if not name or name not in live:
                report.skipped.append({"name": name, "reason": "missing"})
                continue
            cred = live[name]
            st = self.store.get(name)
            sync_ownership(st, cred.disabled)
            if st.ownership == OWNERSHIP_MANUAL_OVERRIDE:
                report.skipped.append({"name": name, "reason": "manual_override"})
                continue
            # Only restore if live still matches the script's new value from that run
            new_disabled = bool(action.get("new_disabled"))
            old_disabled = bool(action.get("old_disabled"))
            if cred.disabled != new_disabled:
                report.skipped.append({"name": name, "reason": "state_changed_since_run"})
                continue
            entry = {
                "name": name,
                "old_disabled": cred.disabled,
                "new_disabled": old_disabled,
                "reason": REASON_ROLLBACK,
                "run_id": run_id,
            }
            report.planned_actions.append(entry)
            if self.config.dry_run:
                entry["applied"] = False
                restored.append(entry)
                continue
            try:
                self.client.set_status(name, old_disabled)
                entry["applied"] = True
                st.last_script_disabled = old_disabled
                st.last_observed_disabled = old_disabled
                st.ownership = OWNERSHIP_MANAGED
                st.last_action = REASON_ROLLBACK
                st.last_action_run_id = report.run_id
                restored.append(entry)
            except ManagementAPIError as exc:
                entry["applied"] = False
                entry["error"] = str(exc)
                report.errors.append(str(exc))
                restored.append(entry)
        report.applied_actions = restored
        if not self.config.dry_run:
            self.store.record_run_actions(
                report.run_id,
                [a for a in restored if a.get("applied")],
            )
            self.store.save()
        report.finished_at = to_iso(self.now_fn()) or ""
        return report


def emit_report(report: CycleReport, json_output: bool) -> None:
    data = report.to_dict()
    data = redact_obj(data)
    if json_output:
        print(json.dumps(data, indent=2, sort_keys=True))
        return
    LOG.info(
        "run=%s dry_run=%s xai=%s/%s active_healthy~=%s target=%s shortfall=%s actions=%s errors=%s",
        report.run_id,
        report.dry_run,
        report.xai_total,
        report.inventory_total,
        report.active_healthy,
        report.active_target,
        report.active_shortfall,
        len(report.planned_actions),
        len(report.errors),
    )
    if report.safe_mode:
        LOG.warning("safe_mode enabled: %s", "; ".join(report.notes) or "yes")
    for action in report.planned_actions:
        LOG.info(
            "plan %s disabled %s -> %s reason=%s",
            action.get("name"),
            action.get("old_disabled"),
            action.get("new_disabled"),
            action.get("reason"),
        )
    for err in report.errors:
        LOG.error("%s", err)


def setup_logging(level: str, json_output: bool) -> None:
    root = logging.getLogger()
    root.handlers.clear()
    handler = logging.StreamHandler(sys.stderr if json_output else sys.stdout)
    handler.setFormatter(
        logging.Formatter("%(asctime)s %(levelname)s %(name)s: %(message)s")
    )
    handler.addFilter(RedactingFilter())
    root.addHandler(handler)
    root.setLevel(getattr(logging, level.upper(), logging.INFO))
    LOG.addFilter(RedactingFilter())


def run_with_config(config: Config, client: Optional[ManagementClient] = None) -> int:
    setup_logging(config.log_level, config.json_output)
    lock_path = config.state_file + ".lock"
    with ProcessLock(lock_path):
        store = StateStore(config.state_file)
        store.load()
        checker = Checker(config, client=client, store=store)
        if config.daemon:
            exit_code = 0
            while True:
                report = checker.run_once()
                emit_report(report, config.json_output)
                if report.errors:
                    exit_code = 2
                time.sleep(config.interval_seconds)
            return exit_code  # pragma: no cover
        report = checker.run_once()
        emit_report(report, config.json_output)
        return 2 if report.errors else 0


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_arg_parser()
    args = parser.parse_args(list(argv) if argv is not None else None)
    try:
        config = config_from_args(args)
    except SystemExit as exc:
        # argparse also uses SystemExit; re-raise codes, format our validation
        if isinstance(exc.code, str):
            print(exc.code, file=sys.stderr)
            return 2
        raise
    try:
        return run_with_config(config)
    except KeyboardInterrupt:
        LOG.info("interrupted")
        return 130
    except SystemExit:
        raise
    except Exception as exc:  # pragma: no cover
        print(redact_text(f"fatal: {exc}"), file=sys.stderr)
        print(redact_text(traceback.format_exc()), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
