#!/usr/bin/env python3
"""Opt-in management-only reconciliation. No inference, token files or reset credits."""
import argparse
import email.utils
import fcntl
import json
import os
from pathlib import Path
import stat
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class ManagementError(Exception):
    def __init__(self, message, retry_after=0):
        super().__init__(message)
        self.retry_after = retry_after


class UnsupportedBackend(ManagementError):
    pass


def retry_seconds(value):
    if not value:
        return 0
    try:
        if value.strip().isdigit():
            return int(value)
        return max(0, int(email.utils.parsedate_to_datetime(value).timestamp() - time.time()) + 1)
    except (ValueError, TypeError, OverflowError):
        return 0


class Client:
    def __init__(self, origin, key):
        parsed = urllib.parse.urlsplit(origin)
        if (parsed.username or parsed.password or parsed.query or parsed.fragment
                or parsed.path not in ("", "/") or not parsed.hostname):
            raise ValueError("management origin must contain only scheme, host and port")
        if parsed.scheme != "https" and not (
                parsed.scheme == "http" and parsed.hostname in ("127.0.0.1", "::1", "localhost")):
            raise ValueError("management requires HTTPS or loopback HTTP")
        self.origin, self.key = origin.rstrip("/"), key
        # Do not send management credentials through ambient proxies or redirects.
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect())

    def call(self, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(
            self.origin + "/v8/management/" + path, data=data,
            headers={"Authorization": "Bearer " + self.key, "Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=45) as response:
                raw = response.read(1024 * 1024 + 1)
                if len(raw) > 1024 * 1024:
                    raise ManagementError("oversized management response")
                return json.loads(raw)
        except urllib.error.HTTPError as exc:
            after = retry_seconds(exc.headers.get("Retry-After"))
            exc.close()
            # The safe endpoint must exist. Never fall back to unconditional reset.
            if path.endswith("/reconcile") and exc.code in (404, 405):
                raise UnsupportedBackend("backend lacks guarded reconciliation") from None
            if exc.code == 409:
                return {"status": "state_changed"}
            raise ManagementError("management request failed", retry_after=after) from None
        except (OSError, ValueError):
            raise ManagementError("management request failed") from None


def candidates(document):
    if not isinstance(document, dict) or not isinstance(document.get("files"), list):
        raise ManagementError("invalid credential inventory")
    seen, result = set(), []
    for row in document["files"]:
        if not isinstance(row, dict):
            raise ManagementError("invalid credential row")
        index = row.get("auth_index")
        if (row.get("provider") != "claude" or row.get("disabled") is not False
                or row.get("requires_sign_in") is True or row.get("quota_blocked") is not True
                or not isinstance(index, str) or not index or index in seen):
            continue
        cooldowns = row.get("cooldowns")
        if (not isinstance(cooldowns, list) or not cooldowns
                or not all(isinstance(c, dict) and c.get("reason") in ("quota", "credential_quota")
                           for c in cooldowns)
                or not any(c.get("scope") == "credential" and c.get("reason") == "credential_quota"
                           for c in cooldowns)):
            continue
        seen.add(index)
        result.append(index)
    return result


def run_once(client):
    counts = {"checked": 0, "reconciled": 0, "unchanged": 0, "state_changed": 0, "failed": 0,
              "retry_after_seconds": 0}
    # Serial calls: no overlap, no retry storm, and no healthy-account probing.
    for index in candidates(client.call("credentials")):
        counts["checked"] += 1
        try:
            result = client.call("routing/cooldown/reconcile", {"auth_index": index})
            status = result.get("status") if isinstance(result, dict) else None
            if status not in ("reconciled", "unchanged", "state_changed"):
                raise ManagementError("unrecognized reconciliation result")
            counts[status] += 1
        except UnsupportedBackend:
            raise
        except ManagementError as exc:
            counts["failed"] += 1
            counts["retry_after_seconds"] = max(counts["retry_after_seconds"], exc.retry_after)
    return counts


def read_key(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise ManagementError("management key must be an owner-only regular file")
        key = source.read(8193).strip()
        if not key or len(key) > 8192 or "\n" in key or "\r" in key:
            raise ManagementError("invalid management key file")
        return key


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--origin", default="http://127.0.0.1:8317")
    parser.add_argument("--key-file", type=Path, required=True)
    parser.add_argument("--lock-file", type=Path, required=True)
    parser.add_argument("--interval", type=int, default=300)
    parser.add_argument("--once", action="store_true")
    args = parser.parse_args()
    if args.interval < 300:
        parser.error("interval must be at least 300 seconds")
    # Supervisor owns the lifecycle. Multiple invocations cannot overlap.
    fd = os.open(args.lock_file, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return 0
        failures = 0
        while True:
            delay = args.interval
            try:
                client = Client(args.origin, read_key(args.key_file))
                counts = run_once(client)
                if counts["reconciled"] or counts["failed"] or args.once:
                    print(json.dumps(counts), flush=True)
                failures = min(failures + 1, 4) if counts["failed"] else 0
                delay = max(delay, counts["retry_after_seconds"])
            except UnsupportedBackend:
                print("guarded reconciliation unavailable; upgrade required", flush=True)
                return 2
            except (ManagementError, OSError, ValueError) as exc:
                print("reconciliation unavailable; no unconditional reset attempted", flush=True)
                if args.once:
                    return 1
                failures = min(failures + 1, 4)
                delay = max(delay, getattr(exc, "retry_after", 0))
            if args.once:
                return 0 if not counts["failed"] else 1
            delay = max(delay, min(3600, args.interval * (2 ** failures)))
            time.sleep(delay)


if __name__ == "__main__":
    raise SystemExit(main())
