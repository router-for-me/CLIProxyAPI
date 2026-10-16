#!/usr/bin/env python3
"""Prefer Codex credentials with expiring earned resets; redeem only when useful.

This companion uses the supported Codex app-server account API and the
CLIProxyAPI management API. It never sends model requests to spend quota.
"""

import argparse
import datetime as dt
import glob
import json
import os
import select
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid
from pathlib import Path

UTC = dt.timezone.utc
EXPIRY_HORIZON = dt.timedelta(days=7)
REDEEM_HORIZON = dt.timedelta(hours=24)


def rpc(proc, method, request_id, params=None, timeout=20):
    request = {"method": method, "id": request_id}
    if params is not None:
        request["params"] = params
    proc.stdin.write(json.dumps(request) + "\n")
    proc.stdin.flush()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not select.select([proc.stdout], [], [], max(0, deadline - time.monotonic()))[0]:
            break
        line = proc.stdout.readline()
        if not line:
            break
        response = json.loads(line)
        if response.get("id") == request_id:
            if "error" in response:
                raise RuntimeError(f"{method}: {response['error']}")
            return response.get("result", {})
    raise TimeoutError(method)


def app_server(credential, consume=None):
    with tempfile.TemporaryDirectory(prefix="cliproxy-reset-") as codex_home:
        env = os.environ.copy()
        env["CODEX_HOME"] = codex_home
        proc = subprocess.Popen(
            ["codex", "app-server", "--stdio"], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, bufsize=1, env=env,
        )
        try:
            rpc(proc, "initialize", 1, {
                "clientInfo": {"name": "cliproxy_reset_router", "title": "CLIProxy Reset Router", "version": "0.1.0"},
                "capabilities": {"experimentalApi": True},
            })
            proc.stdin.write('{"method":"initialized","params":{}}\n')
            proc.stdin.flush()
            rpc(proc, "account/login/start", 2, {
                "type": "chatgptAuthTokens",
                "accessToken": credential["access_token"],
                "chatgptAccountId": credential["account_id"],
                "chatgptPlanType": credential.get("plan_type"),
            })
            result = rpc(proc, "account/rateLimits/read", 3)
            if consume is not None:
                outcome = rpc(proc, "account/rateLimitResetCredit/consume", 4, consume)
                result = rpc(proc, "account/rateLimits/read", 5)
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


def shortest_window(result):
    bucket = result.get("rateLimits") or {}
    options = [bucket.get(name) for name in ("primary", "secondary")]
    options = [x for x in options if isinstance(x, dict) and x.get("windowDurationMins")]
    return min(options, key=lambda x: x["windowDurationMins"], default=None)


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
    credits = available_credits(result, now)
    if not credits:
        return None
    expiry = dt.datetime.fromtimestamp(credits[0]["expiresAt"], UTC)
    if expiry - now > EXPIRY_HORIZON:
        return None
    weekly = weekly_window(result) or {}
    short = shortest_window(result) or {}
    return (credits[0]["expiresAt"], -(weekly.get("usedPercent") or 0), short.get("usedPercent") or 0)


def management(method, path, key, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(
        "http://127.0.0.1:8317/v0/management" + path, data=data, method=method,
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
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


def claude_offer_active(expires_on, now):
    """Claude's UI currently exposes only a calendar date for an offer."""
    try:
        expiry_date = dt.date.fromisoformat(expires_on)
    except (TypeError, ValueError):
        return False
    return now.astimezone().date() <= expiry_date


def apply_claude_offers(args, key, auths, now):
    path = Path(args.claude_offers_file)
    if not path.exists():
        return
    offers = json.loads(path.read_text()).get("offers", [])
    by_name = {item.get("name"): item for item in auths if item.get("type") == "claude"}
    by_email = {}
    for auth_path in glob.glob(str(Path(args.auth_dir) / "claude-*.json")):
        credential = json.loads(Path(auth_path).read_text())
        by_email[credential.get("email")] = Path(auth_path).name
    for offer in offers:
        name = by_email.get(offer.get("email"))
        entry = by_name.get(name)
        if not entry or entry.get("disabled"):
            continue
        desired = 100 if claude_offer_active(offer.get("expires_on"), now) else 0
        current = entry.get("priority") or 0
        print(f"{name}: Claude {offer.get('kind', 'limit')} reset expires "
              f"{offer.get('expires_on')}; priority {current} -> {desired}")
        if current != desired and not args.dry_run:
            management("PATCH", "/auth-files/fields", key, {"name": name, "priority": desired})


def run_once(args, now=None):
    now = now or dt.datetime.now(UTC)
    key = Path(args.management_key).read_text().strip()
    auths = management("GET", "/auth-files", key).get("files", [])
    apply_claude_offers(args, key, auths, now)
    by_name = {item.get("name"): item for item in auths if item.get("type") == "codex"}
    observations = []
    for path in sorted(glob.glob(str(Path(args.auth_dir) / "codex-*.json"))):
        name = Path(path).name
        if name not in by_name or by_name[name].get("disabled"):
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
    priorities = {name: max(10, 100 - index * 10) for index, (_, name) in enumerate(ranked)}
    for name, credential, result in observations:
        desired = priorities.get(name, 0)
        current = by_name[name].get("priority") or 0
        if current != desired:
            print(f"{name}: priority {current} -> {desired}")
            if not args.dry_run:
                management("PATCH", "/auth-files/fields", key, {"name": name, "priority": desired})
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
              f"{(updated.get('rateLimitResetCredits') or {}).get('availableCount', 'unknown')}")
        if outcome in ("reset", "alreadyRedeemed"):
            index = by_name[name].get("auth_index")
            if index:
                management("POST", "/reset-quota", key, {"auth_index": index})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    auth_dir = Path(os.environ.get("CLIPROXY_AUTH_DIR", Path.home() / ".cli-proxy-api"))
    state_root = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local" / "state"))
    parser.add_argument("--auth-dir", default=str(auth_dir))
    parser.add_argument("--management-key", default=str(auth_dir / "management-key"))
    parser.add_argument("--state-file", default=str(state_root / "cliproxy-reset-router" / "state.json"))
    parser.add_argument("--claude-offers-file", default=str(state_root / "cliproxy-reset-router" / "claude-offers.json"))
    parser.add_argument("--dry-run", action="store_true")
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
