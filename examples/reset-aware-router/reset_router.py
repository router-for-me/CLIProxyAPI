#!/usr/bin/env python3
"""Prefer usable quota that renews soon, then expiring earned resets.

This companion uses the supported Codex app-server account API and the
CLIProxyAPI management API. It never sends model requests to spend quota.
"""

import argparse
import datetime as dt
import glob
import json
import os
import queue
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import uuid
from pathlib import Path

UTC = dt.timezone.utc
EXPIRY_HORIZON = dt.timedelta(days=7)
REDEEM_HORIZON = dt.timedelta(hours=24)


def renewal_minute(timestamp):
    """Ignore second-level jitter between otherwise equal provider windows."""
    return int((timestamp + 30) // 60)


def read_responses(proc, inbox):
    for line in proc.stdout:
        try:
            inbox.put(json.loads(line))
        except json.JSONDecodeError:
            continue
    inbox.put(None)


def rpc(proc, inbox, method, request_id, params=None, timeout=None):
    request = {"method": method, "id": request_id}
    if params is not None:
        request["params"] = params
    proc.stdin.write(json.dumps(request) + "\n")
    proc.stdin.flush()
    while True:
        try:
            response = inbox.get(timeout=timeout)
        except queue.Empty as exc:
            raise TimeoutError(method) from exc
        if response is None:
            raise RuntimeError(f"{method}: app-server closed its output")
        if response.get("id") == request_id:
            if "error" in response:
                raise RuntimeError(f"{method}: {response['error']}")
            return response.get("result", {})


def app_server(credential, consume=None):
    with tempfile.TemporaryDirectory(prefix="cliproxy-reset-") as codex_home:
        env = os.environ.copy()
        env["CODEX_HOME"] = codex_home
        proc = subprocess.Popen(
            ["codex", "app-server", "--stdio"], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, bufsize=1, env=env,
        )
        inbox = queue.Queue()
        threading.Thread(target=read_responses, args=(proc, inbox), daemon=True).start()
        try:
            rpc(proc, inbox, "initialize", 1, {
                "clientInfo": {"name": "cliproxy_reset_router", "title": "CLIProxy Reset Router", "version": "0.1.0"},
                "capabilities": {"experimentalApi": True},
            }, timeout=20)
            proc.stdin.write('{"method":"initialized","params":{}}\n')
            proc.stdin.flush()
            rpc(proc, inbox, "account/login/start", 2, {
                "type": "chatgptAuthTokens",
                "accessToken": credential["access_token"],
                "chatgptAccountId": credential["account_id"],
                "chatgptPlanType": credential.get("plan_type"),
            }, timeout=20)
            result = rpc(proc, inbox, "account/rateLimits/read", 3)
            if consume is not None:
                outcome = rpc(proc, inbox, "account/rateLimitResetCredit/consume", 4, consume)
                try:
                    result = rpc(proc, inbox, "account/rateLimits/read", 5)
                except Exception as exc:
                    print(f"rate limits refresh after redemption failed: {exc}", file=sys.stderr)
                    result = None
                return result, outcome.get("outcome")
            return result, None
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


def weekly_window(result):
    buckets = result.get("rateLimitsByLimitId") or {}
    if not buckets and result.get("rateLimits"):
        buckets = {"legacy": result["rateLimits"]}
    elif "codex" in buckets:
        buckets = {"codex": buckets["codex"]}
    windows = []
    for bucket in buckets.values():
        if not isinstance(bucket, dict):
            continue
        for name in ("primary", "secondary"):
            window = bucket.get(name)
            if isinstance(window, dict) and (window.get("windowDurationMins") or 0) >= 7 * 24 * 60:
                windows.append(window)
    return max(windows, key=lambda value: value.get("usedPercent") or 0, default=None)


def available_credits(result, now):
    reset = result.get("rateLimitResetCredits") or {}
    credits = reset.get("credits")
    if not isinstance(credits, list):
        return []  # Count alone is insufficient for expiry-aware decisions.
    return sorted(
        (item for item in credits if item.get("status") == "available"
         and isinstance(item.get("expiresAt"), (int, float))
         and item["expiresAt"] > now.timestamp()),
        key=lambda item: item["expiresAt"],
    )


def should_redeem(result, now):
    """Only try an expiring credit when the weekly window has useful usage."""
    credits = available_credits(result, now)
    weekly = weekly_window(result)
    if not credits or not weekly:
        return None
    expiry = dt.datetime.fromtimestamp(credits[0]["expiresAt"], UTC)
    if expiry - now > REDEEM_HORIZON:
        return None
    reset_at = weekly.get("resetsAt")
    if not isinstance(reset_at, (int, float)) or reset_at <= expiry.timestamp():
        return None  # Natural weekly renewal happens first.
    used = weekly.get("usedPercent") or 0
    # Earlier attempt only when nearly exhausted; in the final hour, a small
    # remaining balance is worth trying before the earned reset expires.
    if used >= 90 or (expiry - now <= dt.timedelta(hours=1) and used >= 10):
        return credits[0]
    return None


def account_rank(result, now):
    """Rank natural weekly renewal first; an expiring credit breaks ties.

    An exhausted weekly bucket cannot serve a request until a reset is redeemed,
    so do not promote it merely because its renewal is close.
    """
    credits = available_credits(result, now)
    credit_expiry = credits[0]["expiresAt"] if credits else float("inf")
    weekly = weekly_window(result) or {}
    reset_at = weekly.get("resetsAt")
    used = weekly.get("usedPercent")
    if (isinstance(reset_at, (int, float)) and reset_at > now.timestamp()
            and isinstance(used, (int, float)) and 0 <= used < 100):
        return (0, renewal_minute(reset_at), credit_expiry, used)
    if weekly:
        return None  # Known exhausted or malformed weekly quota is not promoted.
    if credit_expiry < now.timestamp() + EXPIRY_HORIZON.total_seconds():
        return (1, credit_expiry, float("inf"), 0)
    return None


def management(method, path, key, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(
        "http://127.0.0.1:8317/v8/management" + path, data=data, method=method,
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request) as response:
        return json.load(response)


def redemption_key(state_path, account_id, credit_id):
    """Reuse an attempt ID after a timeout, so retries cannot spend twice."""
    path = Path(state_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        state = json.loads(path.read_text())
    except FileNotFoundError:
        state = {}
    key = account_id + ":" + credit_id
    if key not in state:
        state[key] = str(uuid.uuid4())
        temporary = path.with_suffix(".tmp")
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(descriptor, "w") as stream:
            json.dump(state, stream)
        os.replace(temporary, path)
    return state[key]


def clear_redemption_key(state_path, account_id, credit_id):
    path = Path(state_path)
    try:
        state = json.loads(path.read_text())
    except FileNotFoundError:
        return
    if state.pop(account_id + ":" + credit_id, None) is None:
        return
    temporary = path.with_suffix(".tmp")
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        json.dump(state, stream)
    os.replace(temporary, path)


def claude_offer_active(expires_on, now):
    """Claude's UI currently exposes only a calendar date for an offer."""
    try:
        expiry_date = dt.date.fromisoformat(expires_on)
    except (TypeError, ValueError):
        return False
    return now.astimezone().date() <= expiry_date


def claude_weekly_rank(entry, now):
    """Use recent, observed Claude response headers; never infer a window."""
    observations = [entry.get("quota") or {}]
    model_quotas = entry.get("model_quotas")
    if isinstance(model_quotas, dict):
        observations.extend(model_quotas.values())
    candidates = []
    for observation in observations:
        if not isinstance(observation, dict):
            continue
        signals = observation.get("signals") or {}
        try:
            seen = dt.datetime.fromisoformat(observation["observed_at"].replace("Z", "+00:00"))
            reset_at = float(signals["Anthropic-Ratelimit-Unified-7d-Reset"])
            used = float(signals["Anthropic-Ratelimit-Unified-7d-Utilization"])
        except (KeyError, TypeError, ValueError):
            continue
        if (seen.tzinfo is not None and dt.timedelta(0) <= now - seen <= dt.timedelta(days=3)
                and now.timestamp() < reset_at and 0 <= used <= 1):
            candidates.append((seen, reset_at, used))
    if not candidates:
        return None
    _, reset_at, used = max(candidates, key=lambda item: item[0])
    return reset_at, used


def apply_claude_priorities(args, key, auths, now):
    path = Path(args.claude_offers_file)
    offers = json.loads(path.read_text()).get("offers", []) if path.exists() else []
    by_name = {item.get("name"): item for item in auths if item.get("type") == "claude"}
    by_email = {}
    for auth_path in glob.glob(str(Path(args.auth_dir) / "claude-*.json")):
        credential = json.loads(Path(auth_path).read_text())
        by_email[credential.get("email")] = Path(auth_path).name
    offer_by_name = {by_email.get(item.get("email")): item for item in offers}
    ranked = []
    for name, entry in by_name.items():
        if entry.get("disabled") or entry.get("status") != "active":
            continue
        offer = offer_by_name.get(name) or {}
        offer_expiry = (dt.date.fromisoformat(offer["expires_on"]).toordinal()
                        if claude_offer_active(offer.get("expires_on"), now) else float("inf"))
        weekly = claude_weekly_rank(entry, now)
        if weekly and weekly[1] < 1:
            reset_at, used = weekly
            ranked.append(((0, renewal_minute(reset_at), offer_expiry, used), name))
        elif weekly is None and offer_expiry != float("inf"):
            ranked.append(((1, offer_expiry, float("inf"), 0), name))
    ranked.sort()
    priorities = {name: len(ranked) - index for index, (_, name) in enumerate(ranked)}
    for name, entry in by_name.items():
        desired = priorities.get(name, 0)
        current = entry.get("priority") or 0
        if current != desired:
            print(f"{name}: priority {current} -> {desired}")
            if not args.dry_run:
                management("PATCH", "/credentials/fields", key, {"name": name, "priority": desired})


def run_once(args, now=None):
    now = now or dt.datetime.now(UTC)
    key = Path(args.management_key).read_text().strip()
    auths = management("GET", "/credentials", key).get("files", [])
    apply_claude_priorities(args, key, auths, now)
    by_name = {item.get("name"): item for item in auths if item.get("type") == "codex"}
    observations = []
    for path in sorted(glob.glob(str(Path(args.auth_dir) / "codex-*.json"))):
        name = Path(path).name
        if name not in by_name or by_name[name].get("disabled") or by_name[name].get("status") != "active":
            continue
        credential = json.loads(Path(path).read_text())
        try:
            result, _ = app_server(credential)
        except Exception as exc:
            print(f"{name}: unable to read limits ({exc})", file=sys.stderr)
            continue
        credits = available_credits(result, now)
        weekly = weekly_window(result) or {}
        expiry = dt.datetime.fromtimestamp(credits[0]["expiresAt"], UTC).isoformat() if credits else "unknown"
        print(f"{name}: weekly={weekly.get('usedPercent', 'unknown')}% earned_resets="
              f"{(result.get('rateLimitResetCredits') or {}).get('availableCount', 'unknown')} next_expiry={expiry}")
        observations.append((name, credential, result))

    ranked = []
    for name, _, result in observations:
        rank = account_rank(result, now)
        if rank is not None:
            ranked.append((rank, name))
    ranked.sort(key=lambda item: item[0])
    priorities = {name: len(ranked) - index for index, (_, name) in enumerate(ranked)}
    active_names = {name for name, entry in by_name.items()
                    if not entry.get("disabled") and entry.get("status") == "active"}
    observed_names = {name for name, _, _ in observations}
    # Keep the last complete ordering when a transient account read fails.
    reprioritize = active_names == observed_names
    if not reprioritize:
        print("Codex limits incomplete; keeping existing priorities", file=sys.stderr)
    for name, credential, result in observations:
        desired = priorities.get(name, 0)
        current = by_name[name].get("priority") or 0
        if reprioritize and current != desired:
            print(f"{name}: priority {current} -> {desired}")
            if not args.dry_run:
                management("PATCH", "/credentials/fields", key, {"name": name, "priority": desired})
        if args.priority_only:
            continue
        credit = should_redeem(result, now)
        if credit is None:
            continue
        print(f"{name}: earned reset {credit['id']} eligible for expiry check")
        if args.dry_run:
            continue
        # Re-read before spending; the provider may have changed since ranking.
        fresh, _ = app_server(credential)
        credit = should_redeem(fresh, dt.datetime.now(UTC))
        if credit is None:
            continue
        attempt = {
            "idempotencyKey": redemption_key(args.state_file, credential["account_id"], credit["id"]),
            "creditId": credit["id"],
        }
        updated, outcome = app_server(credential, consume=attempt)
        print(f"{name}: reset outcome={outcome}, remaining="
              f"{((updated or {}).get('rateLimitResetCredits') or {}).get('availableCount', 'unknown')}")
        if outcome in ("reset", "alreadyRedeemed"):
            index = by_name[name].get("auth_index")
            if index:
                management("POST", "/routing/cooldown/reset", key, {"auth_index": index})
        elif outcome in ("nothingToReset", "noCredit"):
            clear_redemption_key(args.state_file, credential["account_id"], credit["id"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    auth_dir = Path(os.environ.get("CLIPROXY_AUTH_DIR", Path.home() / ".cli-proxy-api"))
    state_root = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local" / "state"))
    parser.add_argument("--auth-dir", default=str(auth_dir))
    parser.add_argument("--management-key", default=str(auth_dir / "management-key"))
    parser.add_argument("--state-file", default=str(state_root / "cliproxy-reset-router" / "state.json"))
    parser.add_argument("--claude-offers-file", default=str(state_root / "cliproxy-reset-router" / "claude-offers.json"))
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--priority-only", action="store_true",
                        help="Update priorities without attempting a manual reset")
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--interval", type=int, default=900)
    args = parser.parse_args()
    if args.once:
        run_once(args)
        return
    while True:
        try:
            run_once(args)
        except Exception as exc:
            print(f"reset router: {exc}", file=sys.stderr)
        time.sleep(max(args.interval, 60))


if __name__ == "__main__":
    main()
