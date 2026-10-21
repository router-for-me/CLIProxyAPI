#!/usr/bin/env python3
"""Prefer usable quota that renews soon, then expiring earned resets.

This companion uses the supported Codex app-server account API and the
CLIProxyAPI management API. It never sends model requests to spend quota.
"""

import argparse
import datetime as dt
import email.utils
import errno
import json
import os
import queue
import shutil
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
    codex_home = tempfile.mkdtemp(prefix="cliproxy-reset-")
    try:
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
                return result, outcome.get("outcome")
            return result, None
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
    finally:
        # Codex may finish writing cache files just after its parent exits.
        for attempt in range(5):
            try:
                shutil.rmtree(codex_home)
                break
            except OSError as exc:
                if exc.errno == errno.ENOENT:
                    break
                if exc.errno != errno.ENOTEMPTY:
                    raise
                if attempt == 4:
                    print(f"unable to remove temporary Codex home: {codex_home}", file=sys.stderr)
                else:
                    time.sleep(0.1)


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
    if result.get("ordinaryUsageAllowed") is not True:
        return None
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


def load_state(state_path):
    try:
        return json.loads(Path(state_path).read_text())
    except FileNotFoundError:
        return {}


def save_state(state_path, state):
    path = Path(state_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(".tmp")
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        json.dump(state, stream)
    os.replace(temporary, path)


def redemption_key(state_path, account_id, credit_id):
    """Reuse an attempt ID after a timeout, so retries cannot spend twice."""
    state = load_state(state_path)
    key = account_id + ":" + credit_id
    if key not in state:
        state[key] = str(uuid.uuid4())
        save_state(state_path, state)
    return state[key]


def clear_redemption_key(state_path, account_id, credit_id):
    state = load_state(state_path)
    if state.pop(account_id + ":" + credit_id, None) is None:
        return
    save_state(state_path, state)


def queue_cooldown_reset(state_path, auth_index):
    state = load_state(state_path)
    pending = state.setdefault("pending_cooldowns", [])
    if auth_index not in pending:
        pending.append(auth_index)
        save_state(state_path, state)


def retry_cooldown_resets(state_path, key):
    state = load_state(state_path)
    pending = state.get("pending_cooldowns", [])
    remaining = []
    for auth_index in pending:
        try:
            response = management("POST", "/routing/cooldown/reset", key,
                                  {"auth_index": auth_index})
            if response.get("status") != "ok":
                raise RuntimeError(f"unexpected response: {response}")
        except Exception as exc:
            print(f"{auth_index}: cooldown reset pending ({exc})", file=sys.stderr)
            remaining.append(auth_index)
    if remaining != pending:
        if remaining:
            state["pending_cooldowns"] = remaining
        else:
            state.pop("pending_cooldowns", None)
        save_state(state_path, state)


def read_management_key(args):
    key = os.environ.get("CLIPROXY_MANAGEMENT_KEY")
    if key:
        return key.strip()
    key_file = getattr(args, "management_key_file", None) or getattr(args, "management_key", None)
    return Path(key_file).read_text().strip()


def claude_offer_active(expires_on, now):
    """Claude's UI currently exposes only a calendar date for an offer."""
    try:
        expiry_date = dt.date.fromisoformat(expires_on)
    except (TypeError, ValueError):
        return False
    return now.astimezone().date() <= expiry_date


def parse_reset_timestamp(raw):
    if not isinstance(raw, str):
        raise ValueError("reset timestamp is not text")
    try:
        return float(raw)
    except ValueError:
        pass
    try:
        parsed = dt.datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        parsed = email.utils.parsedate_to_datetime(raw)
    if parsed.tzinfo is None:
        raise ValueError("reset timestamp has no time zone")
    return parsed.timestamp()


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
            reset_at = parse_reset_timestamp(signals["Anthropic-Ratelimit-Unified-7d-Reset"])
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
    by_name = {item.get("id") or item.get("name"): item
               for item in auths if item.get("type") == "claude"}
    by_email = {}
    for identity, entry in by_name.items():
        if entry.get("email"):
            by_email.setdefault(entry["email"], []).append(identity)
    offer_by_name = {}
    for offer in offers:
        name = offer.get("name")
        if name:
            matches = [identity for identity, entry in by_name.items()
                       if name in (identity, entry.get("name"))]
            if offer.get("email"):
                matches = [identity for identity in matches
                           if by_name[identity].get("email") == offer["email"]]
        else:
            matches = by_email.get(offer.get("email"), [])
        if len(matches) == 1:
            offer_by_name[matches[0]] = offer
        elif len(matches) > 1:
            print("Claude offer identity is ambiguous; add its credential name", file=sys.stderr)
    ranked = []
    for identity, entry in by_name.items():
        if entry.get("disabled") or entry.get("status") != "active":
            continue
        offer = offer_by_name.get(identity) or {}
        offer_expiry = (dt.date.fromisoformat(offer["expires_on"]).toordinal()
                        if claude_offer_active(offer.get("expires_on"), now) else float("inf"))
        weekly = claude_weekly_rank(entry, now)
        if weekly and weekly[1] < 1:
            reset_at, used = weekly
            ranked.append(((0, renewal_minute(reset_at), offer_expiry, used), identity))
        elif weekly is None and offer_expiry != float("inf"):
            ranked.append(((1, offer_expiry, float("inf"), 0), identity))
    ranked.sort()
    priorities = {identity: len(ranked) - index for index, (_, identity) in enumerate(ranked)}
    for identity, entry in by_name.items():
        desired = priorities.get(identity, 0)
        current = entry.get("priority") or 0
        if current != desired:
            print(f"{entry.get('name')}: priority {current} -> {desired}")
            if not args.dry_run:
                management("PATCH", "/credentials/fields", key,
                           {"name": identity, "priority": desired})


def run_once(args, now=None):
    now = now or dt.datetime.now(UTC)
    key = read_management_key(args)
    auths = management("GET", "/credentials", key).get("files", [])
    if not args.dry_run:
        retry_cooldown_resets(args.state_file, key)
    apply_claude_priorities(args, key, auths, now)
    by_identity = {item.get("id") or item.get("name"): item
                   for item in auths if item.get("type") == "codex"}
    observations = []
    for identity, entry in sorted(by_identity.items()):
        path = entry.get("path")
        quota_cooldown = (entry.get("status") == "error" and any(
            isinstance(cooldown, dict) and cooldown.get("reason") in ("quota", "credential_quota")
            for cooldown in (entry.get("cooldowns") or [])))
        if (entry.get("disabled") or not path
                or (entry.get("status") != "active" and not quota_cooldown)):
            continue
        try:
            credential = json.loads(Path(path).read_text())
            result, _ = app_server(credential)
        except Exception as exc:
            print(f"{entry.get('name')}: unable to read limits ({exc})", file=sys.stderr)
            continue
        credits = available_credits(result, now)
        weekly = weekly_window(result) or {}
        expiry = dt.datetime.fromtimestamp(credits[0]["expiresAt"], UTC).isoformat() if credits else "unknown"
        print(f"{entry.get('name')}: weekly={weekly.get('usedPercent', 'unknown')}% earned_resets="
              f"{(result.get('rateLimitResetCredits') or {}).get('availableCount', 'unknown')} next_expiry={expiry}")
        observations.append((identity, credential, result))

    ranked = []
    for identity, _, result in observations:
        if by_identity[identity].get("status") != "active":
            continue
        rank = account_rank(result, now)
        if rank is not None:
            ranked.append((rank, identity))
    ranked.sort(key=lambda item: item[0])
    priorities = {identity: len(ranked) - index for index, (_, identity) in enumerate(ranked)}
    active_names = {identity for identity, entry in by_identity.items()
                    if not entry.get("disabled") and entry.get("status") == "active"}
    observed_names = {identity for identity, _, _ in observations if identity in active_names}
    # Keep the last complete ordering when a transient account read fails.
    reprioritize = active_names == observed_names
    if not reprioritize:
        print("Codex limits incomplete; keeping existing priorities", file=sys.stderr)
    for identity, entry in by_identity.items():
        if entry.get("disabled"):
            continue
        desired = priorities.get(identity, 0)
        current = entry.get("priority") or 0
        if reprioritize and current != desired:
            print(f"{entry.get('name')}: priority {current} -> {desired}")
            if not args.dry_run:
                management("PATCH", "/credentials/fields", key,
                           {"name": identity, "priority": desired})
    for identity, credential, result in observations:
        entry = by_identity[identity]
        if args.priority_only:
            continue
        credit = should_redeem(result, now)
        if credit is None:
            continue
        print(f"{entry.get('name')}: earned reset {credit['id']} eligible for expiry check")
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
        _, outcome = app_server(credential, consume=attempt)
        print(f"{entry.get('name')}: reset outcome={outcome}")
        if outcome in ("reset", "alreadyRedeemed"):
            index = entry.get("auth_index")
            if index:
                queue_cooldown_reset(args.state_file, index)
                retry_cooldown_resets(args.state_file, key)
        elif outcome in ("nothingToReset", "noCredit"):
            clear_redemption_key(args.state_file, credential["account_id"], credit["id"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    auth_dir = Path(os.environ.get("CLIPROXY_AUTH_DIR", Path.home() / ".cli-proxy-api"))
    state_root = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local" / "state"))
    parser.add_argument("--auth-dir", default=str(auth_dir))
    parser.add_argument("--management-key-file", "--management-key",
                        dest="management_key_file", default=str(auth_dir / "management-key"),
                        help="File containing the management key (or set CLIPROXY_MANAGEMENT_KEY)")
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
