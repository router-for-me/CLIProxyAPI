"""Tests for StaticFiles mount of the Vite frontend dist/ directory."""

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from usage_dashboard.api import app


def _set_test_mode():
    """Set app.state.cfg to None so auth_gate middleware passes through."""
    app.state.cfg = None


def test_root_serves_index_html():
    """When frontend/dist exists, GET / returns the built index.html."""
    _set_test_mode()
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist"
    if not dist.exists():
        pytest.skip("frontend/dist not built yet")

    with TestClient(app) as client:
        resp = client.get("/")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
        assert "<div id=\"root\">" in resp.text or "root" in resp.text


def test_hashed_assets_have_immutable_cache_header():
    """Hashed JS/CSS assets get immutable cache headers."""
    _set_test_mode()
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist" / "assets"
    if not dist.exists():
        pytest.skip("no built assets")

    asset = next(dist.glob("*.js"))
    with TestClient(app) as client:
        resp = client.get(f"/assets/{asset.name}")
        assert resp.status_code == 200
        assert "max-age=31536000" in resp.headers.get("cache-control", "")
        assert "immutable" in resp.headers.get("cache-control", "")


def test_index_html_is_no_cache():
    """index.html has no-cache headers for fresh SPA loads."""
    _set_test_mode()
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist"
    if not dist.exists():
        pytest.skip("frontend/dist not built yet")

    with TestClient(app) as client:
        resp = client.get("/")
        cc = resp.headers.get("cache-control", "")
        assert "no-cache" in cc or "max-age=0" in cc


def test_usage_deep_link_serves_index():
    """SPA deep links like /usage serve index.html (not 404)."""
    _set_test_mode()
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist"
    if not dist.exists():
        pytest.skip("frontend/dist not built yet")

    with TestClient(app) as client:
        resp = client.get("/usage")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
