"""Collect CLIProxyAPI usage and quota readings for InterChat's Fork database.

The management API key and Fork token stay on disk. SQLite is a local outbox so
temporary Fork outages do not discard usage after the proxy queue is drained.
"""

from __future__ import annotations

import hashlib
import json
import math
import os
import re
import sqlite3
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[1]
INTERCHAT = Path(os.environ.get("INTERCHAT_SOURCE", r"E:\Projects\InterChat"))
sys.path.insert(0, str(INTERCHAT))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import wsbus  # noqa: E402
from quota_forecast import Account, forecast  # noqa: E402

PROXY = "http://127.0.0.1:8317/v8/management"
FORK_HOST = "127.0.0.1"
FORK_PORT = 3212
FORK_DB = os.environ.get("FORK_QUOTA_DB", "interchat-bus")
CLAUDE_USAGE = "https://api.anthropic.com/api/oauth/usage"
CODEX_USAGE = "https://chatgpt.com/backend-api/wham/usage"
OUTBOX = Path(os.environ.get("QUOTA_OUTBOX_PATH", str(ROOT / "bin" / "quota-outbox.sqlite3")))
STREAM_PORT = int(os.environ.get("QUOTA_STREAM_PORT", "8318"))
MAX_WHAT_IF_AGENTS = 10_000


def account_id(auth_index: str, provider: str = "claude") -> str:
    return hashlib.sha256((provider + ":" + auth_index).encode()).hexdigest()


def unix_seconds(value: object) -> int:
    if not isinstance(value, str) or not value:
        return 0
    try:
        return int(datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp())
    except ValueError:
        return 0


def ensure_schema(db: sqlite3.Connection) -> None:
    db.executescript("""
        CREATE TABLE IF NOT EXISTS usage_event (
          id TEXT PRIMARY KEY, account TEXT NOT NULL, model TEXT NOT NULL,
          minute INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL
        );
        CREATE TABLE IF NOT EXISTS observation (
          id TEXT PRIMARY KEY, account TEXT NOT NULL, window TEXT NOT NULL,
          used_basis_points INTEGER NOT NULL, reset_at INTEGER NOT NULL,
          observed_at INTEGER NOT NULL, sent INTEGER NOT NULL DEFAULT 0
        );
        CREATE TABLE IF NOT EXISTS sent_usage (
          id TEXT PRIMARY KEY, digest TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS credit_balance (
          account TEXT PRIMARY KEY, balance TEXT, has_credits INTEGER NOT NULL,
          unlimited INTEGER NOT NULL, overage_limit_reached INTEGER NOT NULL,
          observed_at INTEGER NOT NULL, sent INTEGER NOT NULL DEFAULT 0
        );
    """)
    # Sessions identify agents; cache tokens let the forecast weigh cached input.
    columns = {row[1] for row in db.execute("PRAGMA table_info(usage_event)")}
    with db:
        for name, kind in (("session_id", "TEXT"), ("parent_session_id", "TEXT"),
                           ("cache_read_tokens", "INTEGER"), ("cache_creation_tokens", "INTEGER")):
            if name not in columns:
                db.execute(f"ALTER TABLE usage_event ADD COLUMN {name} {kind}")
        db.execute("CREATE INDEX IF NOT EXISTS usage_event_account_minute ON usage_event (account, minute)")
        db.execute("CREATE INDEX IF NOT EXISTS observation_account_time ON observation (account, observed_at)")


def session_text(value: object) -> str | None:
    """A session ID as the proxy normalised it, or None for anything else."""
    if not isinstance(value, str):
        return None
    value = value.strip()
    return value if 0 < len(value) <= 128 and value.isprintable() else None


def parse_forecast_query(query: str) -> tuple[list[Account], dict[str, int]]:
    """Accounts to forecast and optional what-if agent counts per provider; raises ValueError on bad input."""
    params = parse_qs(query)
    items = json.loads(params.get("accounts", ["[]"])[0])
    if not isinstance(items, list) or not 0 < len(items) <= 256:
        raise ValueError("invalid accounts")
    accounts = []
    for item in items:
        if not isinstance(item, dict):
            raise ValueError("invalid account")
        provider, auth_index = item.get("provider"), item.get("auth_index")
        label, plan = item.get("label", ""), item.get("plan") or ""
        if (provider not in ("claude", "codex") or not isinstance(auth_index, str) or not 0 < len(auth_index) <= 128
                or not isinstance(label, str) or len(label) > 256 or not isinstance(plan, str) or len(plan) > 32):
            raise ValueError("invalid account")
        accounts.append(Account(account_id(auth_index, provider), provider, label or auth_index, plan))
    agents: dict[str, int] = {}
    for provider in ("claude", "codex"):
        if f"agents.{provider}" in params:
            agents[provider] = int(params[f"agents.{provider}"][0])
            # Input sanity, not a work limit: the simulation cost does not depend on the count.
            if not 0 <= agents[provider] <= MAX_WHAT_IF_AGENTS:
                raise ValueError("invalid agents")
    return accounts, agents


def claude_quota_available(payload: dict) -> bool:
    # Null windows are inactive, but an omitted window is not evidence of recovery.
    if not all(window in payload for window in ("five_hour", "seven_day")):
        return False
    observed = False
    for name, window in payload.items():
        if name != "five_hour" and not name.startswith("seven_day"):
            continue
        if window is None:
            continue
        if not isinstance(window, dict):
            return False
        used = window.get("utilization")
        if (isinstance(used, bool) or not isinstance(used, (int, float))
                or not math.isfinite(used) or not 0 <= used < 100):
            return False
        observed = True
    limits = payload.get("limits", [])
    if not isinstance(limits, list):
        return False
    for limit in limits:
        if not isinstance(limit, dict):
            return False
        used = limit.get("percent")
        if (isinstance(used, bool) or not isinstance(used, (int, float))
                or not math.isfinite(used) or not 0 <= used < 100):
            return False
    return observed


def claude_quota_resettable(file: dict) -> bool:
    if file.get("provider") != "claude" or file.get("disabled") or not file.get("unavailable"):
        return False
    cooldowns = file.get("cooldowns")
    if not isinstance(cooldowns, list) or not cooldowns or not all(isinstance(c, dict) for c in cooldowns):
        return False
    shared = [c for c in cooldowns if c.get("scope") == "credential" and c.get("reason") == "credential_quota"]
    if len(shared) != 1 or not shared[0].get("retry_at"):
        return False
    # ResetQuota resets every model. Do not use it when a separate failure or
    # a model-specific timer is visible alongside the shared quota restriction.
    return all(c.get("reason") in ("quota", "credential_quota")
               and c.get("retry_at") == shared[0]["retry_at"]
               and c.get("http_status", 429) == 429 for c in cooldowns)


class Bridge:
    def __init__(self, database: Path = OUTBOX):
        keys = json.loads((ROOT / "bin" / "local-access.json").read_text(encoding="utf-8"))
        self.management_key = keys["management-key"]
        token = Path(os.environ["LOCALAPPDATA"]) / "InterChat" / "tokens" / "cliproxy-quota.token"
        self.fork_token = token.read_text(encoding="utf-8").strip()
        # Fork requires the database owner for private-table subscriptions.
        owner_token = Path(os.environ["LOCALAPPDATA"]) / "InterChat" / "tokens" / "operator.token"
        self.owner_token = owner_token.read_text(encoding="utf-8").strip()
        self.signal_condition = threading.Condition()
        self.signal_version = 0
        self.subscription = wsbus.Subscription(
            FORK_HOST, FORK_PORT, FORK_DB, self.owner_token,
            ["SELECT * FROM quota_observation", "SELECT * FROM quota_usage", "SELECT * FROM quota_credit_balance"],
            columns={
                "quota_observation": ["id", "account", "window", "used_basis_points", "reset_at", "observed_at"],
                "quota_usage": ["id", "account", "model", "minute", "input_tokens", "output_tokens", "requests"],
                "quota_credit_balance": ["account", "balance", "has_credits", "unlimited", "overage_limit_reached", "observed_at"],
            },
            keys={"quota_observation": "id", "quota_usage": "id", "quota_credit_balance": "account"},
            on_change=self.signal_changed,
            log=lambda message: self.log(message),
        )
        self.db = sqlite3.connect(database)
        self.db.execute("PRAGMA journal_mode=WAL")
        ensure_schema(self.db)
        self.database = database

    def log(self, message: str) -> None:
        with (ROOT / "bin" / "quota-bridge.log").open("a", encoding="utf-8") as output:
            output.write(f"{datetime.now().astimezone().isoformat(timespec='seconds')} {message}\n")

    def signal_changed(self, tables: set[str], initial: bool) -> None:
        if tables & {"quota_observation", "quota_usage", "quota_credit_balance"}:
            with self.signal_condition:
                self.signal_version += 1
                self.signal_condition.notify_all()

    def management(self, path: str, body: dict | None = None) -> object:
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(
            PROXY + path, data=data,
            headers={"Authorization": "Bearer " + self.management_key, "Content-Type": "application/json"},
            method="POST" if data is not None else "GET",
        )
        with urllib.request.urlopen(request, timeout=75 if data else 15) as response:
            return json.load(response)

    def collect_usage(self) -> None:
        records = self.management("/observability/usage/queue?count=500")
        if not isinstance(records, list):
            return
        with self.db:
            for row in records:
                if not isinstance(row, dict) or row.get("provider") not in ("claude", "codex") or row.get("failed"):
                    continue
                auth_index = row.get("auth_index")
                event_id = row.get("execution_id")
                if not isinstance(auth_index, str) or not isinstance(event_id, str) or not event_id:
                    continue
                try:
                    stamp = datetime.fromisoformat(row["timestamp"].replace("Z", "+00:00"))
                    minute = int(stamp.timestamp()) // 60 * 60
                    tokens = row["tokens"]
                    self.db.execute("""
                        INSERT OR IGNORE INTO usage_event (id, account, model, minute, input_tokens,
                          output_tokens, session_id, parent_session_id, cache_read_tokens, cache_creation_tokens)
                        VALUES (?,?,?,?,?,?,?,?,?,?)""", (
                        event_id, account_id(auth_index, row["provider"]), str(row.get("model") or "unknown"), minute,
                        max(0, int(tokens.get("input_tokens") or 0)),
                        max(0, int(tokens.get("output_tokens") or 0)),
                        session_text(row.get("session_id")), session_text(row.get("parent_session_id")),
                        max(0, int(tokens.get("cache_read_tokens") or tokens.get("cached_tokens") or 0)),
                        max(0, int(tokens.get("cache_creation_tokens") or 0)),
                    ))
                except (KeyError, TypeError, ValueError, AttributeError):
                    continue

    def collect_quota(self) -> bool:
        files = self.management("/credentials")
        if not isinstance(files, dict):
            return False
        limited = False
        for file in files.get("files", []):
            if not isinstance(file, dict):
                continue
            provider = str(file.get("provider") or file.get("type")).lower()
            if provider == "codex":
                limited = self.collect_codex_quota(file) or limited
                continue
            if provider != "claude":
                continue
            auth_index = file.get("auth_index")
            if not isinstance(auth_index, str) or not auth_index:
                continue
            result = self.management("/requests/api-call", {
                "auth_index": auth_index, "method": "GET", "url": CLAUDE_USAGE,
                "header": {"User-Agent": "claude-cli/2.1.309 (external, cli)",
                           "Authorization": "Bearer $TOKEN$", "Content-Type": "application/json",
                           "anthropic-beta": "oauth-2025-04-20", "Accept": "application/json"},
            })
            if not isinstance(result, dict):
                continue
            if result.get("status_code") == 429:
                limited = True
                continue
            if result.get("status_code") != 200:
                continue
            try:
                payload = json.loads(result["body"])
            except (KeyError, TypeError, ValueError):
                continue
            if not isinstance(payload, dict):
                continue
            self.release_restored_claude_quota(file, payload)
            observed_at = int(time.time())
            with self.db:
                for window in ("five_hour", "seven_day", "seven_day_oauth_apps", "seven_day_opus"):
                    value = payload.get(window)
                    if not isinstance(value, dict):
                        continue
                    utilization = value.get("utilization")
                    if (isinstance(utilization, bool) or not isinstance(utilization, (float, int))
                            or not math.isfinite(utilization)):
                        continue
                    used = round(utilization * 100)
                    if used < 0 or used > 10000:
                        continue
                    account = account_id(auth_index)
                    ident = hashlib.sha256(f"{account}:{window}:{observed_at}".encode()).hexdigest()
                    self.db.execute("INSERT OR IGNORE INTO observation VALUES (?,?,?,?,?,?,0)", (
                        ident, account, window, used, unix_seconds(value.get("resets_at")), observed_at,
                    ))
                for limit in payload.get("limits", []):
                    if not isinstance(limit, dict) or limit.get("kind") != "weekly_scoped":
                        continue
                    model = ((limit.get("scope") or {}).get("model") or {}).get("display_name", "")
                    if str(model).strip().lower() not in ("fable", "fable 5"):
                        continue
                    percent = limit.get("percent")
                    if not isinstance(percent, (float, int)) or not 0 <= percent <= 100:
                        continue
                    account = account_id(auth_index)
                    ident = hashlib.sha256(f"{account}:seven_day_fable:{observed_at}".encode()).hexdigest()
                    self.db.execute("INSERT OR IGNORE INTO observation VALUES (?,?,?,?,?,?,0)", (
                        ident, account, "seven_day_fable", round(percent * 100),
                        unix_seconds(limit.get("resets_at")), observed_at,
                    ))
        return limited

    def release_restored_claude_quota(self, file: dict, payload: dict) -> None:
        if not claude_quota_resettable(file) or not claude_quota_available(payload):
            return
        auth_index = file.get("auth_index")
        if not isinstance(auth_index, str) or not auth_index:
            return
        # A request or credential update during the probe can establish a newer
        # restriction. Re-read routing state before releasing the old snapshot.
        current = self.management("/credentials")
        if not isinstance(current, dict) or not isinstance(current.get("files"), list):
            return
        for candidate in current["files"]:
            if not isinstance(candidate, dict) or candidate.get("auth_index") != auth_index:
                continue
            if not claude_quota_resettable(candidate):
                return
            if any(candidate.get(key) != file.get(key) for key in ("updated_at", "cooldowns", "status_message")):
                return
            result = self.management("/routing/cooldown/reset", {"auth_index": auth_index})
            if not isinstance(result, dict) or result.get("status") != "ok":
                raise RuntimeError("proxy refused restored quota cooldown reset")
            self.log(f"released restored Claude quota cooldown: {auth_index}")
            return

    def collect_codex_quota(self, file: dict) -> bool:
        auth_index = file.get("auth_index")
        if not isinstance(auth_index, str) or not auth_index:
            return False
        headers = {"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json"}
        account = file.get("id_token")
        if isinstance(account, dict) and account.get("chatgpt_account_id"):
            headers["Chatgpt-Account-Id"] = account["chatgpt_account_id"]
        result = self.management("/requests/api-call", {
            "auth_index": auth_index, "method": "GET", "url": CODEX_USAGE, "header": headers,
        })
        if not isinstance(result, dict):
            return False
        if result.get("status_code") == 429:
            return True
        if result.get("status_code") != 200:
            return False
        try:
            payload = json.loads(result["body"])
        except (KeyError, TypeError, ValueError):
            return False
        if not isinstance(payload, dict):
            return False
        observed_at = int(time.time())
        self.record_codex_credits(auth_index, payload.get("credits"), observed_at)
        rate_limit = payload.get("rate_limit")
        if not isinstance(rate_limit, dict):
            return False
        with self.db:
            for key in ("primary_window", "secondary_window"):
                window = rate_limit.get(key)
                if not isinstance(window, dict):
                    continue
                duration = window.get("limit_window_seconds")
                used = window.get("used_percent")
                if duration == 18000:
                    name = "five_hour"
                elif duration == 604800:
                    name = "weekly"
                else:
                    continue
                if not isinstance(used, (float, int)) or not 0 <= used <= 100:
                    continue
                reset_at = window.get("reset_at")
                if not isinstance(reset_at, (float, int)) or reset_at <= 0:
                    reset_after = window.get("reset_after_seconds")
                    reset_at = observed_at + reset_after if isinstance(reset_after, (float, int)) else 0
                account = account_id(auth_index, "codex")
                ident = hashlib.sha256(f"{account}:{name}:{observed_at}".encode()).hexdigest()
                self.db.execute("INSERT OR IGNORE INTO observation VALUES (?,?,?,?,?,?,0)", (
                    ident, account, name, round(used * 100), int(reset_at), observed_at,
                ))
        return False

    def record_codex_credits(self, auth_index: str, credits: object, observed_at: int) -> None:
        if not isinstance(credits, dict):
            return
        raw = credits.get("balance")
        balance = str(raw).strip() if isinstance(raw, (str, int, float)) and not isinstance(raw, bool) else None
        unlimited = credits.get("unlimited") is True
        if balance is not None and (len(balance) > 64 or re.fullmatch(r"\d+(?:\.\d+)?", balance, re.ASCII) is None):
            return
        if balance is None and not unlimited:
            return
        with self.db:
            self.db.execute("""
                INSERT INTO credit_balance VALUES (?,?,?,?,?,?,0)
                ON CONFLICT(account) DO UPDATE SET balance=excluded.balance,
                  has_credits=excluded.has_credits, unlimited=excluded.unlimited,
                  overage_limit_reached=excluded.overage_limit_reached,
                  observed_at=excluded.observed_at, sent=0
                WHERE excluded.observed_at >= credit_balance.observed_at
            """, (account_id(auth_index, "codex"), balance, credits.get("has_credits") is True,
                  unlimited, credits.get("overage_limit_reached") is True, observed_at))

    def publish(self) -> None:
        session = wsbus.WsSession(FORK_HOST, FORK_PORT, FORK_DB, self.fork_token)
        try:
            session.connect()
            groups = self.db.execute("""
                SELECT account, model, minute, SUM(input_tokens), SUM(output_tokens), COUNT(*)
                FROM usage_event GROUP BY account, model, minute
            """).fetchall()
            for account, model, minute, inputs, outputs, requests in groups:
                ident = hashlib.sha256(f"{account}:{model}:{minute}".encode()).hexdigest()
                digest = hashlib.sha256(f"{inputs}:{outputs}:{requests}".encode()).hexdigest()
                previous = self.db.execute("SELECT digest FROM sent_usage WHERE id=?", (ident,)).fetchone()
                if previous and previous[0] == digest:
                    continue
                result = session.call("quota_usage_put", [
                    ident, account, model, minute, inputs, outputs, requests,
                ])
                if not result.get("ok"):
                    raise RuntimeError("Fork refused quota usage row")
                with self.db:
                    self.db.execute("INSERT OR REPLACE INTO sent_usage VALUES (?,?)", (ident, digest))
            rows = self.db.execute("""
                SELECT id,account,window,used_basis_points,reset_at,observed_at
                FROM observation WHERE sent=0 ORDER BY observed_at
            """).fetchall()
            for row in rows:
                result = session.call("quota_observe", list(row))
                if not result.get("ok"):
                    raise RuntimeError("Fork refused quota observation")
                with self.db:
                    self.db.execute("UPDATE observation SET sent=1 WHERE id=?", (row[0],))
            for row in self.db.execute("""
                SELECT account,balance,has_credits,unlimited,overage_limit_reached,observed_at
                FROM credit_balance WHERE sent=0
            """).fetchall():
                account, balance, has_credits, unlimited, overage, observed_at = row
                result = session.call("quota_credits_put", [account, balance or "", bool(has_credits),
                    bool(unlimited), bool(overage), observed_at])
                if not result.get("ok"):
                    raise RuntimeError("Fork refused quota credit balance")
                with self.db:
                    self.db.execute("UPDATE credit_balance SET sent=1 WHERE account=? AND observed_at=?", (account, observed_at))
        finally:
            session.close()

    def run(self) -> None:
        self.subscription.start()
        server = self.serve()
        threading.Thread(target=server.serve_forever, daemon=True).start()
        next_quota = 0.0
        while True:
            try:
                self.collect_usage()
                if time.monotonic() >= next_quota:
                    limited = self.collect_quota()
                    next_quota = time.monotonic() + (3600 if limited else 900)
                self.publish()
            except (OSError, ValueError, KeyError, RuntimeError, sqlite3.Error,
                    wsbus.WsError, wsbus.WsProcedureError) as error:
                self.log(f"quota bridge: {type(error).__name__}: {error}")
            time.sleep(10)

    def serve(self) -> ThreadingHTTPServer:
        key = self.management_key
        fork_token = self.fork_token
        bridge = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: object) -> None:
                pass

            def do_OPTIONS(self) -> None:
                if self.headers.get("Origin") != "http://127.0.0.1:8317":
                    self.send_error(403)
                    return
                self.send_response(204)
                self.send_header("Access-Control-Allow-Origin", "http://127.0.0.1:8317")
                self.send_header("Access-Control-Allow-Headers", "Authorization")
                self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")
                self.end_headers()

            def do_GET(self) -> None:
                if self.headers.get("Authorization") != "Bearer " + key:
                    self.send_error(401)
                    return
                path = urlsplit(self.path)
                if path.path == "/cards/stream":
                    self.stream_cards(path.query)
                    return
                if path.path == "/forecast":
                    self.send_forecast(path.query)
                    return
                if path.path not in ("/history", "/stream"):
                    self.send_error(404)
                    return
                auth_index = parse_qs(path.query).get("auth_index", [""])[0]
                if not auth_index or len(auth_index) > 128:
                    self.send_error(400)
                    return
                if path.path == "/stream" and not bridge.subscription.live.is_set():
                    self.send_error(503, "quota subscription unavailable")
                    return
                provider = parse_qs(path.query).get("provider", ["claude"])[0]
                if provider not in ("claude", "codex"):
                    self.send_error(400)
                    return
                account = account_id(auth_index, provider)
                self.send_response(200)
                self.send_header("Access-Control-Allow-Origin", "http://127.0.0.1:8317")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Content-Type", "text/event-stream" if path.path == "/stream" else "application/json")
                self.end_headers()
                session = wsbus.WsSession(FORK_HOST, FORK_PORT, FORK_DB, fork_token)
                try:
                    session.connect()
                    previous = b""
                    while True:
                        with bridge.signal_condition:
                            seen_version = bridge.signal_version
                        history = session.call("quota_history", [account, int(time.time()) - 7 * 86400])
                        if not history.get("ok"):
                            raise RuntimeError("Fork refused quota history")
                        payload = json.dumps({
                            "comparison": history.get("comparison", []),
                            "observations": history.get("observations", []),
                            "credits": history.get("credits"),
                        }).encode()
                        if path.path == "/history":
                            self.wfile.write(payload)
                            break
                        if payload != previous:
                            self.wfile.write(b"data: " + payload + b"\n\n")
                            self.wfile.flush()
                            previous = payload
                        with bridge.signal_condition:
                            changed = bridge.signal_condition.wait_for(
                                lambda: bridge.signal_version != seen_version or not bridge.subscription.live.is_set(),
                                timeout=30,
                            )
                        if not bridge.subscription.live.is_set():
                            break
                        if not changed:
                            self.wfile.write(b": keepalive\n\n")
                            self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError, OSError, wsbus.WsError):
                    pass
                finally:
                    session.close()

            def send_forecast(self, query: str) -> None:
                try:
                    accounts, agents = parse_forecast_query(query)
                except (ValueError, TypeError):
                    self.send_error(400)
                    return
                # The collector owns bridge.db on the main thread; read through our own connection.
                db = sqlite3.connect(f"file:{bridge.database}?mode=ro", uri=True)
                try:
                    payload = json.dumps(forecast(db, accounts, int(time.time()), agents)).encode()
                finally:
                    db.close()
                self.send_response(200)
                self.send_header("Access-Control-Allow-Origin", "http://127.0.0.1:8317")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def stream_cards(self, query: str) -> None:
                try:
                    accounts = json.loads(parse_qs(query).get("accounts", ["[]"])[0])
                    if not isinstance(accounts, list) or not 0 < len(accounts) <= 256:
                        raise ValueError("invalid accounts")
                    requested = {}
                    for item in accounts:
                        provider, auth_index = item["provider"], item["auth_index"]
                        if provider not in ("claude", "codex") or not isinstance(auth_index, str) or not 0 < len(auth_index) <= 128:
                            raise ValueError("invalid account")
                        requested[account_id(auth_index, provider)] = item
                except (ValueError, TypeError, KeyError):
                    self.send_error(400)
                    return
                self.send_response(200)
                self.send_header("Access-Control-Allow-Origin", "http://127.0.0.1:8317")
                self.send_header("Cache-Control", "no-store")
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                previous = b""
                session = wsbus.WsSession(FORK_HOST, FORK_PORT, FORK_DB, fork_token)
                try:
                    session.connect()
                    while True:
                        with bridge.signal_condition:
                            version = bridge.signal_version
                        latest = {}
                        for row in bridge.subscription.rows("quota_observation"):
                            if row["account"] not in requested:
                                continue
                            ident = (row["account"], row["window"])
                            if row["observed_at"] > latest.get(ident, {}).get("observed_at", 0):
                                latest[ident] = row
                        snapshots = []
                        for account, item in requested.items():
                            history = session.call("quota_history", [account, int(time.time()) - 7 * 86400])
                            if not history.get("ok"):
                                raise RuntimeError("Fork refused quota history")
                            snapshots.append({**item, "comparison": history.get("comparison", []),
                                "credits": history.get("credits"),
                                "observations": [row for (owner, _), row in latest.items() if owner == account]})
                        payload = json.dumps({"accounts": snapshots}).encode()
                        if payload != previous:
                            self.wfile.write(b"data: " + payload + b"\n\n")
                            self.wfile.flush()
                            previous = payload
                        with bridge.signal_condition:
                            changed = bridge.signal_condition.wait_for(
                                lambda: bridge.signal_version != version, timeout=30)
                        if not changed:
                            self.wfile.write(b": keepalive\n\n")
                            self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError, OSError, RuntimeError, wsbus.WsError):
                    pass
                finally:
                    session.close()

        return ThreadingHTTPServer(("127.0.0.1", STREAM_PORT), Handler)


if __name__ == "__main__":
    Bridge().run()
