#!/usr/bin/env python3
"""Seed a temp SQLite DB with sample usage data for E2E tests.

Usage:
    USAGE_DASHBOARD_DATA_DIR=$(python e2e/fixtures/seed.py)
    uv run python usage_dashboard.py serve

This prints the temp dir path so the caller can set the env var.
"""

import json
import os
import sys
import tempfile
from datetime import datetime, timezone


def _main():
    data_dir = tempfile.mkdtemp(prefix="e2e-seed-")
    cfg = {
        "data_dir": data_dir,
        "cliproxy_base_url": "http://127.0.0.1:8317",
        "management_key": "e2e-test-key",
        "dashboard_host": "127.0.0.1",
        "dashboard_port": 8321,
        "default_limit": 100,
        "max_limit": 500,
    }
    # Write config file so load_config() doesn't fail
    config_path = os.path.join(data_dir, "config.json")
    with open(config_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, indent=2)

    from usage_dashboard import storage as st
    from usage_dashboard.collector import insert_usage

    st.init_schema(cfg)

    now = datetime.now(timezone.utc)

    # Generate 120 events across 4 models, 3 providers, 3 accounts, 2 endpoints,
    # spread over the last 12h. ~5 accounts so Ranking table has data.
    events = []
    models = ["gpt-4o", "gpt-4", "claude-3-opus", "gemini-pro"]
    providers = ["openai", "anthropic", "google"]
    accounts = ["acct_alpha", "acct_beta", "acct_gamma", "acct_delta", "acct_epsilon"]
    endpoints = ["/v1/chat/completions", "/v1/embeddings"]

    for i in range(120):
        hour_offset = (119 - i) // 10  # ~12 hours spread
        ts = now.timestamp() - hour_offset * 3600
        dt = datetime.fromtimestamp(ts, timezone.utc)
        model_idx = i % 4
        provider_idx = (i // 4) % 3
        account_idx = (i // 2) % 5
        endpoint_idx = i % 2
        failed = 1 if i % 7 <= 1 else 0  # ~28% failure rate for errors tab
        fail_status = 500 if i % 14 == 0 else (429 if i % 14 == 7 else None)
        tokens_total = 500 + i * 50
        tokens_input = int(tokens_total * 0.6)
        tokens_output = tokens_total - tokens_input
        events.append({
            "request_id": f"e2e-req-{i:04d}",
            "timestamp": dt.isoformat(),
            "model": models[model_idx],
            "provider": providers[provider_idx],
            "account_hash": accounts[account_idx],
            "endpoint": endpoints[endpoint_idx],
            "tokens": {
                "total_tokens": tokens_total,
                "input_tokens": tokens_input,
                "output_tokens": tokens_output,
                "cached_tokens": tokens_input // 2 if i % 2 == 0 else 0,
                "cache_read_tokens": tokens_input // 2 if i % 2 == 0 else 0,
            },
            "failed": failed,
            "fail_status": fail_status,
            "latency_ms": 200 + i * 10,
            "ttft_ms": 50 + i * 2,
        })

    inserted, dupes, errors = insert_usage(cfg, events)
    print(data_dir, end="")
    return 0 if inserted > 0 else 1


if __name__ == "__main__":
    sys.exit(_main())
