#!/usr/bin/env python3
# Run Omarchy's Antigravity collector, renewing a lapsed agy access token in
# memory first. The collector only reads the sign-in agy keeps, so its limits
# go stale an hour after agy last ran. The keyring is never written.
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import time
import urllib.parse
import urllib.request

# The public Google OAuth client agy signs in with.
CLIENT_ID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
CLIENT_SECRET = "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"

path = os.path.join(os.environ["OMARCHY_PATH"], "bin", "omarchy-agent-usage-agy")
loader = importlib.machinery.SourceFileLoader("omarchy_agy", path)
collector = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
loader.exec_module(collector)
stock_login = collector.login


def login():
    entry = stock_login()
    if not entry or not entry["refresh"] or collector.token_fresh(entry, time.time() + 60):
        return entry
    body = urllib.parse.urlencode({
        "client_id": CLIENT_ID, "client_secret": CLIENT_SECRET,
        "refresh_token": entry["refresh"], "grant_type": "refresh_token",
    }).encode()
    try:
        with urllib.request.urlopen("https://oauth2.googleapis.com/token", body, timeout=8) as r:
            data = json.load(r)
        return {**entry, "accessToken": data["access_token"],
                "expiresAt": time.time() + data["expires_in"]}
    except (OSError, ValueError, KeyError, TypeError):
        return entry


# With no Gemini group (Ultra meters Gemini nowhere), the collector reads the
# first group as the plan's own limits. Name the group those meters belong to.
stock_extract_limits = collector.extract_limits


def extract_limits(data):
    limits = stock_extract_limits(data)
    groups = [g for g in data.get("groups") or [] if isinstance(g, dict)] if limits else []
    names = [str(g.get("displayName") or "").lower() for g in groups]
    if names and not any("gemini" in name for name in names) and "claude" in names[0]:
        for limit in limits:
            if limit["title"] in ("Session", "Weekly"):
                limit["title"] = "Claude/GPT " + ("5h" if limit["title"] == "Session" else "7d")
    return limits


# T3 Code runs agy with a data directory of its own, so count its sessions
# too. One Stats object collects every root; harness rows are counted once.
T3_ROOTS = sorted(Path.home().glob(".t3/userdata/providers/antigravity/*/antigravity-acp"))
stock_local_stats = collector.local_stats


def local_stats(root, problems=None):
    stock = collector.Stats, collector.harness_rows
    shared = collector.Stats()
    collector.Stats = lambda *args, **kwargs: shared
    try:
        collector.harness_rows = lambda problems: []
        for extra in T3_ROOTS:
            stock_local_stats(extra, problems)
        collector.harness_rows = stock[1]
        return stock_local_stats(root, problems)
    finally:
        collector.Stats, collector.harness_rows = stock


def cached_local_stats(root, max_age):
    def scan():
        problems = []
        stats = local_stats(root, problems)
        return stats, not problems

    sources = [root, *T3_ROOTS, collector.opencode_db(), *collector.session_roots()]
    return collector.cached_scan(collector.AGENT_ID, sources, max_age, scan)


collector.login = login
collector.extract_limits = extract_limits
collector.local_stats = local_stats
collector.cached_local_stats = cached_local_stats
collector.main()
