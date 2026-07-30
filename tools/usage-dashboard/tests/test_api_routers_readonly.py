"""Tests for read-only API routers — parity vs legacy query.py functions.

TDD: Red → Green → Refactor.
Each parametrized test asserts the FastAPI response equals the legacy
query function output on the same fixture DB.
"""

import pytest

ENDPOINTS = [
    ("/api/v1/summary", "query_summary", {"range": ["24h"]}),
    ("/api/v1/timeseries", "query_timeseries", {"range": ["24h"], "group_by": ["model"]}),
    ("/api/v1/models", "query_models", {"range": ["24h"]}),
    ("/api/v1/accounts", "query_accounts", {"range": ["24h"]}),
    ("/api/v1/errors", "query_errors", {"range": ["24h"]}),
    ("/api/v1/providers", "query_providers", {"range": ["24h"]}),
    ("/api/v1/endpoints", "query_endpoints", {"range": ["24h"]}),
]


@pytest.mark.parametrize("path,fn,qs", ENDPOINTS)
def test_endpoint_matches_legacy(api_client, cfg_with_data, legacy_response, path, fn, qs):
    """Each v1 endpoint returns the same JSON as the legacy query function."""
    resp = api_client.get(path, params={k: v[0] for k, v in qs.items()})
    assert resp.status_code == 200
    fastapi_body = resp.json()
    legacy_body = legacy_response(cfg_with_data, fn, qs)
    assert fastapi_body == legacy_body, f"{path} mismatch"


def test_prices_endpoint(api_client):
    """Prices endpoint returns currency info."""
    resp = api_client.get("/api/v1/prices")
    assert resp.status_code == 200
    assert "currency" in resp.json()


def test_requests_pagination(api_client):
    """Requests endpoint returns paginated results with cursor."""
    resp = api_client.get("/api/v1/requests", params={"range": "24h", "limit": 5})
    assert resp.status_code == 200
    body = resp.json()
    assert "requests" in body
    assert "next_cursor" in body


def test_auth_check_no_token(api_client):
    """When no dashboard_token is configured, auth check reports not required."""
    resp = api_client.get("/api/v1/auth/check")
    assert resp.status_code == 200
    body = resp.json()
    assert body["auth_required"] is False
    assert body["valid"] is True


def test_invalid_range_returns_400(api_client):
    """Invalid range parameter returns 400."""
    resp = api_client.get("/api/v1/summary", params={"range": "bogus"})
    assert resp.status_code == 400
    assert "detail" in resp.json()


def test_legacy_health_returns_db_path(api_client, cfg_with_data):
    """Legacy /api/health returns ok and db_path."""
    resp = api_client.get("/api/health")
    assert resp.status_code == 200
    assert resp.json()["ok"] is True
    assert resp.json()["db_path"] is not None


def test_legacy_summary_endpoint(api_client):
    """Legacy /api/summary works."""
    resp = api_client.get("/api/summary", params={"range": "24h"})
    assert resp.status_code == 200
    assert "summary" in resp.json()


def test_legacy_requests_endpoint(api_client):
    """Legacy /api/requests works."""
    resp = api_client.get("/api/requests", params={"range": "24h", "limit": 5})
    assert resp.status_code == 200
    assert "requests" in resp.json()


def test_timeseries_group_by(api_client):
    """Timeseries works with different group_by values."""
    for group in ("model", "provider", "day", "hour"):
        resp = api_client.get("/api/v1/timeseries", params={"range": "24h", "group_by": group})
        assert resp.status_code == 200
        body = resp.json()
        assert body["group_by"] == group


def test_requests_no_limit(api_client):
    """Requests works without a limit parameter."""
    resp = api_client.get("/api/v1/requests", params={"range": "24h"})
    assert resp.status_code == 200
    assert "requests" in resp.json()


def test_models_returns_priced_flag(api_client):
    """Models endpoint returns priced flag per model."""
    resp = api_client.get("/api/v1/models", params={"range": "24h"})
    assert resp.status_code == 200
    for model in resp.json()["models"]:
        assert "priced" in model
