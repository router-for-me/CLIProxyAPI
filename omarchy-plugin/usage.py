#!/usr/bin/env python3
import concurrent.futures
import datetime as dt
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import time

MAX_BYTES = 1 << 20
MAX_ACCOUNTS = 16
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


def window(label, percent, reset, custom=False):
    if type(percent) not in (float, int) or not 0 <= percent <= 100:
        return None
    return {"label": label, "percent": round(percent, 1), "reset": epoch(reset),
            "custom": custom}


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
    for group, fallback in (("rate_limit", "window"), ("code_review_rate_limit", "codeReview")):
        limit = data.get(group)
        if not isinstance(limit, dict):
            continue
        for key in ("primary_window", "secondary_window"):
            row = limit.get(key)
            if isinstance(row, dict):
                label = fallback
                if group == "rate_limit":
                    label = {18000: "session", 604800: "weekly", 2592000: "monthly"}.get(
                        row.get("limit_window_seconds"), fallback)
                yield window(label, row.get("used_percent"), row.get("reset_at"))


def proxy_quota(provider, auth, fallback):
    name = "Claude" if provider == "claude" else "Codex"
    row = {"id": provider, "name": name, "detail": fallback,
           "status": "unavailable", "windows": []}
    host, path, headers = PROVIDERS[provider]
    conn = http.client.HTTPSConnection(host, timeout=6)
    try:
        token = auth["access_token"]
        headers = {**headers, "Authorization": "Bearer " + token, "Accept": "application/json"}
        if provider == "codex" and auth.get("account_id"):
            headers["ChatGPT-Account-Id"] = auth["account_id"]
        conn.request("GET", path, headers=headers)
        with conn.getresponse() as response:
            if response.status != 200:
                return row
            data = read_json(response)
        get_windows = claude_windows if provider == "claude" else codex_windows
        row["windows"] = [entry for entry in get_windows(data) if entry]
        row["status"] = "ok" if row["windows"] else "noWindows"
    except (OSError, ValueError, KeyError, TypeError, AttributeError, http.client.HTTPException):
        pass
    finally:
        conn.close()
    return row


def local_usage(agent, name):
    row = {"id": agent, "name": name, "detail": "", "status": "unavailable", "windows": []}
    # These native collectors already handle the signed-in profile and cache.
    # Isolate them from the stock widget's account switching and other collectors.
    env = {**os.environ, "OMARCHY_PATH": str(OMARCHY)}
    env.pop("CLOUD_CODE_URL", None)
    try:
        done = subprocess.run(
            [OMARCHY / "bin" / ("omarchy-agent-usage-" + agent)],
            env=env, capture_output=True, timeout=22, check=True,
        )
        if len(done.stdout) > MAX_BYTES:
            return row
        data = json.loads(done.stdout)
        plan = data.get("tierLabel", "")
        if isinstance(plan, str) and re.fullmatch(r"[\w .()+-]{1,48}", plan):
            row["detail"] = plan
        for entry in data.get("limits", [])[:8]:
            label = entry.get("title")
            if isinstance(label, str) and re.fullmatch(r"[\w .()+/-]{1,40}", label):
                parsed = window(label, entry.get("percent"), entry.get("resetsAt"), True)
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


def main():
    proxy = []
    auth_dir = Path.home() / ".cli-proxy-api"
    with concurrent.futures.ThreadPoolExecutor(max_workers=MAX_ACCOUNTS + 2) as pool:
        local = [pool.submit(local_usage, agent, name)
                 for agent, name in (("agy", "Antigravity"), ("muse", "Meta / Muse"))]
        for path in sorted(auth_dir.glob("*.json")):
            if len(proxy) == MAX_ACCOUNTS:
                break
            try:
                with path.open("rb") as stream:
                    auth = read_json(stream)
                provider = auth.get("type")
                if provider not in PROVIDERS or auth.get("disabled"):
                    continue
                email = auth.get("email") or path.stem.split("-", 2)[-1].split("-pro")[0]
                alias = email.split("@", 1)[0]
                detail = alias if re.fullmatch(r"[\w.+-]{1,28}", alias) else f"Account {len(proxy) + 1}"
                proxy.append(pool.submit(proxy_quota, provider, auth, detail))
            except (OSError, ValueError, TypeError, AttributeError):
                continue
        print(json.dumps({"proxy": [job.result() for job in proxy],
                          "local": [job.result() for job in local], "checked": round(time.time())},
                         allow_nan=False))


if __name__ == "__main__":
    main()
