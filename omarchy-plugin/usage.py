#!/usr/bin/env python3
import argparse
import concurrent.futures
import datetime as dt
from email.utils import parsedate_to_datetime
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

MAX_BYTES = 1 << 20
MAX_ACCOUNTS = 16
REFRESH_INTERVAL = 15 * 60
OMARCHY = Path(os.environ.get("OMARCHY_PATH") or "/usr/share/omarchy")
PROVIDERS = {
    "claude": ("api.anthropic.com", "/api/oauth/usage", {
        "User-Agent": "claude-cli/2.1.280 (external, cli)",
        "anthropic-beta": "oauth-2025-04-20",
    }),
    "codex": ("chatgpt.com", "/backend-api/wham/usage", {
        "User-Agent": "codex_cli_rs/0.134.0",
    }),
}


def read_json(stream):
    body = stream.read(MAX_BYTES + 1)
    if len(body) > MAX_BYTES:
        raise ValueError("usage record too large")
    return json.loads(body)


def epoch(value):
    if isinstance(value, str) and len(value) <= 40:
        try:
            value = dt.datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
        except ValueError:
            return None
    if type(value) in (float, int) and 0 < value < 4102444800:
        return round(value)
    return None


def window(label, percent, reset, custom=False, five_hour=False):
    if type(percent) not in (float, int) or not 0 <= percent <= 100:
        return None
    return {"label": label, "percent": round(percent, 1), "reset": epoch(reset),
            "custom": custom, "five_hour": five_hour or label == "session"}


def claude_windows(data):
    for key, label in (
        ("five_hour", "session"),
        ("seven_day", "weekly"),
        ("seven_day_sonnet", "sonnet"),
        ("seven_day_opus", "opus"),
        ("seven_day_oauth_apps", "oauthApps"),
        ("seven_day_cowork", "cowork"),
    ):
        row = data.get(key)
        if isinstance(row, dict):
            yield window(label, row.get("utilization"), row.get("resets_at"))

    limits = data.get("limits")
    fable = None
    for item in limits[:32] if isinstance(limits, list) else []:
        if not isinstance(item, dict) or item.get("kind") != "weekly_scoped":
            continue
        scope = item.get("scope") or {}
        model = scope.get("model") or {}
        if str(model.get("display_name", "")).strip().lower() not in ("fable", "fable 5"):
            continue
        parsed = window("fable", item.get("percent"), item.get("resets_at"))
        if parsed:
            fable = parsed
            if item.get("is_active"):
                break
    legacy = data.get("iguana_necktie")
    if isinstance(legacy, dict):
        is_credit_pool = any(legacy.get(k) is not None for k in (
            "limit_dollars", "used_dollars", "remaining_dollars",
        ))
        if not fable and not is_credit_pool:
            fable = window("fable", legacy.get("utilization"), legacy.get("resets_at"))
    if fable:
        yield fable


def codex_windows(data):
    groups = [(data.get("rate_limit"), "window", False),
              (data.get("code_review_rate_limit"), "codeReview", False)]
    extra = data.get("additional_rate_limits")
    for entry in extra[:8] if isinstance(extra, list) else []:
        if not isinstance(entry, dict):
            continue
        name = entry.get("limit_name") or entry.get("metered_feature")
        if isinstance(name, str) and re.fullmatch(r"[\w .()+/-]{1,48}", name):
            name = re.sub(r"^GPT-[\d.]+-Codex-", "", name)
            groups.append((entry.get("rate_limit"), name, True))
    for limit, fallback, custom in groups:
        if not isinstance(limit, dict):
            continue
        multiple = bool(limit.get("primary_window") and limit.get("secondary_window"))
        for key in ("primary_window", "secondary_window"):
            row = limit.get(key)
            if isinstance(row, dict):
                label = fallback
                seconds = row.get("limit_window_seconds")
                if fallback == "window" and not custom:
                    label = {18000: "session", 604800: "weekly", 2592000: "monthly"}.get(
                        seconds, fallback)
                elif custom and multiple:
                    period = {18000: "5h", 604800: "7d", 2592000: "30d"}.get(seconds)
                    if period:
                        label = f"{label} {period}"
                yield window(label, row.get("used_percent"), row.get("reset_at"),
                             custom, seconds == 18000)


def proxy_quota(provider, auth, row):
    host, path, headers = PROVIDERS[provider]
    conn = http.client.HTTPSConnection(host, timeout=6)
    try:
        token = auth["access_token"]
        headers = {**headers, "Authorization": "Bearer " + token, "Accept": "application/json"}
        if provider == "codex" and auth.get("account_id"):
            headers["ChatGPT-Account-Id"] = auth["account_id"]
        conn.request("GET", path, headers=headers)
        with conn.getresponse() as response:
            if response.status == 429:
                now = time.time()
                retry = response.getheader("Retry-After") or ""
                try:
                    until = now + int(retry) if retry.isdecimal() else (
                        parsedate_to_datetime(retry).timestamp())
                except (ValueError, OverflowError):
                    until = now + REFRESH_INTERVAL
                return row, max(now + 60, until)
            if response.status != 200:
                return row, 0
            data = read_json(response)
        get_windows = claude_windows if provider == "claude" else codex_windows
        row["windows"] = [entry for entry in get_windows(data) if entry]
        row["status"] = "ok" if row["windows"] else "noWindows"
    except (OSError, ValueError, KeyError, TypeError, AttributeError, http.client.HTTPException):
        pass
    finally:
        conn.close()
    return row, 0


def local_usage(agent, name):
    row = {"id": agent, "name": name, "detail": "", "status": "unavailable", "windows": []}
    # These native collectors already handle the signed-in profile and cache.
    # Isolate them from the stock widget's account switching and other collectors.
    env = {**os.environ, "OMARCHY_PATH": str(OMARCHY)}
    env.pop("CLOUD_CODE_URL", None)
    command = [OMARCHY / "bin" / ("omarchy-agent-usage-" + agent)]
    if agent == "agy":
        command = [sys.executable, Path(__file__).with_name("agy.py")]
    try:
        done = subprocess.run(
            command,
            env=env, capture_output=True, timeout=30, check=True,
        )
        if len(done.stdout) > MAX_BYTES:
            return row
        data = json.loads(done.stdout)
        plan = data.get("tierLabel", "")
        if isinstance(plan, str) and re.fullmatch(r"[\w .()+-]{1,48}", plan):
            row["detail"] = plan
        for entry in data.get("limits", [])[:8]:
            label = entry.get("title") or entry.get("label")
            if isinstance(label, str) and re.fullmatch(r"[\w .()+/-]{1,40}", label):
                five_hour = entry.get("label") == "Session (5-hour)"
                if label == "Session (5-hour)":
                    label = "Session"
                if label == "Weekly (7-day)":
                    label = "Weekly"
                # Omarchy's collectors report a fraction; the panel shows percent.
                percent = entry.get("percent")
                if type(percent) in (float, int):
                    percent *= 100
                parsed = window(label, percent, entry.get("resetsAt"),
                                True, five_hour)
                if parsed:
                    row["windows"].append(parsed)
        if data.get("hasLocalStats"):
            today = data.get("todayTotalTokens")
            daily = [d.get("messageCount") for d in data.get("recentDays", [])[-7:]]
            if all(type(n) is int and 0 <= n < 10**13 for n in [today, *daily]):
                row["tokens"] = [today, sum(daily)]
        if row["windows"]:
            row["status"] = "stale" if data.get("limitsStale") else "ok"
        elif data.get("ready"):
            row["status"] = "noWindows"
    except (OSError, ValueError, TypeError, AttributeError, subprocess.SubprocessError):
        pass
    return row


def proxy_accounts(auth_dir):
    accounts = {provider: {} for provider in PROVIDERS}
    count = 0
    for path in sorted(auth_dir.rglob("*.json", case_sensitive=False)):
        if count == MAX_ACCOUNTS:
            break
        try:
            with path.open("rb") as stream:
                auth = read_json(stream)
            provider = auth.get("type")
            if provider not in PROVIDERS or auth.get("disabled"):
                continue
            identity = (auth.get("account_uuid") or auth.get("account_id")
                        or auth.get("email") or auth.get("access_token", ""))
            file_id = path.relative_to(auth_dir)
            key = hashlib.sha256(f"{file_id}\0{identity}".encode()).hexdigest()
            email = auth.get("email") or path.stem.split("-", 2)[-1].split("-pro")[0]
            alias = email.split("@", 1)[0]
            detail = alias if re.fullmatch(r"[\w.+-]{1,28}", alias) else f"Account {count + 1}"
            row = {"id": provider, "name": "Claude" if provider == "claude" else "Codex",
                   "detail": detail, "status": "unavailable", "windows": []}
            accounts[provider][key] = (auth, row)
            count += 1
        except (OSError, ValueError, TypeError, AttributeError):
            continue
    return accounts


def refresh_proxy(provider, accounts, previous):
    rows = {}
    checked = previous.get("checked", 0)
    blocked = previous.get("blocked", 0)
    for key, (auth, row) in accounts.items():
        old = previous.get("rows", {}).get(key, {})
        now = time.time()
        if old.get("next_check", 0) > now:
            rows[key] = old
            continue
        next_check = blocked
        if blocked <= now:
            row, blocked = proxy_quota(provider, auth, row)
            checked = int(time.time())
            next_check = blocked or checked + REFRESH_INTERVAL
        if row["status"] == "unavailable":
            row["windows"] = old.get("row", {}).get("windows", [])
            if blocked > now:
                row["status"] = "rateLimitedStale" if row["windows"] else "rateLimited"
            elif row["windows"]:
                row["status"] = "stale"
        rows[key] = {"next_check": next_check, "row": row}
    return {"checked": checked, "blocked": blocked, "rows": rows}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--auth-dir", default="", help="Papa Proxy OAuth credential directory")
    auth_dir = parser.parse_args().auth_dir
    accounts = proxy_accounts(Path(auth_dir).expanduser() if auth_dir else Path.home() / ".cli-proxy-api")
    root = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache")
    directory = root / "omarchy" / "proxy-usage"
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = directory / "snapshot.json"
    with (directory / "refresh.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            with path.open("rb") as stream:
                previous = read_json(stream)
        except (OSError, ValueError):
            previous = {}
        if not isinstance(previous, dict):
            previous = {}

        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            jobs = {p: pool.submit(refresh_proxy, p, a, previous.get(p, {}))
                    for p, a in accounts.items()}
            local = previous.get("local", {})
            if local.get("next_check", 0) <= time.time():
                local_jobs = [pool.submit(local_usage, agent, name)
                              for agent, name in (("agy", "Antigravity"), ("muse", "Meta / Muse"))]
                rows = [job.result() for job in local_jobs]
                checked = int(time.time())
                local = {"checked": checked, "next_check": checked + REFRESH_INTERVAL, "rows": rows}
            snapshot = {**{p: job.result() for p, job in jobs.items()}, "local": local}

        if snapshot != previous:
            with tempfile.NamedTemporaryFile(mode="w", dir=directory, delete_on_close=False) as temp:
                json.dump(snapshot, temp, allow_nan=False)
                temp.close()
                os.replace(temp.name, path)
        entries = [entry for p in PROVIDERS for entry in snapshot[p]["rows"].values()]
        deadline = min([local["next_check"], *(entry["next_check"] for entry in entries)])
        print(json.dumps({"proxy": [entry["row"] for entry in entries],
                          "local": local["rows"], "next_check": deadline,
                          "checked": max(s["checked"] for s in snapshot.values())}, allow_nan=False))


if __name__ == "__main__":
    main()
