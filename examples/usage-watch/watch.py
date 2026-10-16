#!/usr/bin/env python3
"""Print the account, model, and observed quota for each CLIProxyAPI request."""

import argparse
import json
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def get_json(base_url, key, path):
    request = Request(
        base_url.rstrip("/") + "/v8/management" + path,
        headers={"Authorization": "Bearer " + key},
    )
    with urlopen(request, timeout=10) as response:
        return json.load(response)


def remaining_percent(signals, used_key=None, utilization_key=None):
    try:
        if used_key and used_key in signals:
            used = float(signals[used_key])
        elif utilization_key and utilization_key in signals:
            used = float(signals[utilization_key]) * 100
        else:
            return None
    except (TypeError, ValueError):
        return None
    return f"{max(0, min(100, 100 - used)):.0f}% left"


def quota_summary(credential, model, now=None):
    now = now or datetime.now(timezone.utc)
    model_quota = (credential.get("model_quotas") or {}).get(model) or {}
    quota = model_quota if model_quota.get("signals") else credential.get("quota") or {}
    signals = quota.get("signals") or {}
    if not signals:
        return "quota unavailable"

    provider = credential.get("provider")
    if provider == "codex":
        specs = (
            ("primary", "X-Codex-Primary-Used-Percent", None),
            ("secondary", "X-Codex-Secondary-Used-Percent", None),
        )
    elif provider == "claude":
        specs = (
            ("5h", None, "Anthropic-Ratelimit-Unified-5h-Utilization"),
            ("7d", None, "Anthropic-Ratelimit-Unified-7d-Utilization"),
        )
    else:
        return "quota unavailable"

    parts = []
    for label, used_key, utilization_key in specs:
        remaining = remaining_percent(signals, used_key, utilization_key)
        if remaining is not None:
            parts.append(f"{label} {remaining}")
    if not parts:
        return "quota unavailable"

    observed = quota.get("observed_at")
    if observed:
        try:
            age = max(0, int((now - datetime.fromisoformat(observed.replace("Z", "+00:00"))).total_seconds()))
            parts.append(f"observed {age}s ago")
        except (ValueError, TypeError):
            pass
    return ", ".join(parts)


def credential_for_record(record, credentials):
    auth_index = str(record.get("auth_index") or "")
    source = str(record.get("source") or "")
    for credential in credentials:
        if auth_index and auth_index == str(credential.get("auth_index") or ""):
            return credential
    for credential in credentials:
        if source and source in (
            credential.get("email"), credential.get("name"), credential.get("id")
        ):
            return credential
    return {}


def format_record(record, credentials):
    credential = credential_for_record(record, credentials)
    account = credential.get("email") or record.get("source") or "unknown account"
    model = record.get("model") or "unknown model"
    provider = record.get("provider") or "unknown provider"
    tokens = (record.get("tokens") or {}).get("total_tokens")
    token_text = f" | {tokens} tokens" if isinstance(tokens, int) else ""
    status = "failed" if record.get("failed") else "ok"
    quota = quota_summary(credential, model) if credential else "quota unavailable"
    return f"{provider} | {model} | {account} | {status}{token_text} | {quota}"


def account_change(record, credentials, previous_by_provider):
    provider = record.get("provider") or "unknown provider"
    credential = credential_for_record(record, credentials)
    account = credential.get("email") or record.get("source") or "unknown account"
    previous = previous_by_provider.get(provider)
    previous_by_provider[provider] = account
    if previous and previous != account:
        return f"{provider} account changed: {previous} -> {account}"
    return None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8317")
    parser.add_argument("--key-file", type=Path, help="file containing the management key")
    parser.add_argument("--interval", type=float, default=1, help="poll interval in seconds")
    parser.add_argument("--provider", choices=("claude", "codex"))
    parser.add_argument("--once", action="store_true", help="read the current queue once and exit")
    args = parser.parse_args(argv)
    if args.interval <= 0:
        parser.error("--interval must be positive")
    key = args.key_file.read_text().strip() if args.key_file else os.environ.get("CLIPROXY_MANAGEMENT_KEY", "").strip()
    if not key:
        parser.error("set CLIPROXY_MANAGEMENT_KEY or provide --key-file")

    previous_by_provider = {}
    if not args.once:
        print("Watching completed API requests (Ctrl-C to stop)...", flush=True)
    while True:
        try:
            records = get_json(args.base_url, key, "/observability/usage/queue?" + urlencode({"count": 100}))
            if records:
                credentials = get_json(args.base_url, key, "/credentials").get("files", [])
                for record in records:
                    if args.provider and record.get("provider") != args.provider:
                        continue
                    change = account_change(record, credentials, previous_by_provider)
                    if change:
                        print(change, flush=True)
                    print(format_record(record, credentials), flush=True)
        except (HTTPError, URLError, TimeoutError, ValueError) as exc:
            print(f"usage-watch: {exc}", file=sys.stderr, flush=True)
            if args.once:
                return 1
        if args.once:
            return 0
        try:
            time.sleep(args.interval)
        except KeyboardInterrupt:
            return 0


if __name__ == "__main__":
    raise SystemExit(main())
