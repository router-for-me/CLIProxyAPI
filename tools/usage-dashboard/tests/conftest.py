"""Shared fixtures for read-only API router tests."""

import pytest
from fastapi.testclient import TestClient

from usage_dashboard import collector as col
from usage_dashboard import query as qy
from usage_dashboard import storage as st
from usage_dashboard.api import app


@pytest.fixture
def cfg_with_data(tmp_path):
    """Create a fixture DB with sample usage events."""
    cfg = {
        "data_dir": str(tmp_path),
        "cliproxy_base_url": "http://fake",
        "management_key": "k",
        "default_limit": 100,
        "max_limit": 500,
    }
    st.init_schema(cfg)
    events = [
        {
            "request_id": f"r{i}",
            "timestamp": f"2026-01-01T0{i}:00:00Z",
            "model": f"m{i % 3}",
            "provider": "p",
            "endpoint": "e",
            "total_tokens": 100 + i,
            "input_tokens": 60,
            "output_tokens": 40,
            "failed": 0,
            "latency_ms": 100,
        }
        for i in range(10)
    ]
    col.insert_usage(cfg, events)
    return cfg


@pytest.fixture
def api_client(cfg_with_data):
    """FastAPI TestClient with cfg populated and data loaded."""
    app.state.cfg = cfg_with_data
    with TestClient(app) as client:
        yield client
    app.state.cfg = None


@pytest.fixture
def legacy_response():
    """Helper: call legacy query function directly."""

    def _call(cfg, fn_name, qs):
        return getattr(qy, fn_name)(cfg, qs)

    return _call
