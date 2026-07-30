"""Tests for the FastAPI app skeleton: health endpoint, auth gate."""

from fastapi.testclient import TestClient

from usage_dashboard.api import app


def test_health_returns_ok():
    with TestClient(app) as client:
        resp = client.get("/api/v1/health")
        assert resp.status_code == 200
        body = resp.json()
        assert "last_poll_at" in body
        assert "last_poll_ok" in body


def test_auth_gate_blocks_non_loopback_without_token():
    app.state.cfg = {"dashboard_host": "0.0.0.0", "dashboard_token": ""}
    try:
        with TestClient(app) as client:
            resp = client.get("/api/v1/health")
            assert resp.status_code == 503
    finally:
        app.state.cfg = None


def test_auth_check_returns_auth_status():
    with TestClient(app) as client:
        resp = client.get("/api/v1/auth/check")
        assert resp.status_code == 200
        body = resp.json()
        assert "auth_required" in body
        assert "valid" in body


def test_api_health_returns_ok():
    with TestClient(app) as client:
        resp = client.get("/api/health")
        assert resp.status_code == 200
        body = resp.json()
        assert body["ok"] is True
