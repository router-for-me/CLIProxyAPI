"""Tests for aliases mutation endpoints — GET/PUT/DELETE /api/v1/aliases.

TDD: RED → GREEN → REFACTOR.
"""
import pytest
from fastapi.testclient import TestClient

from usage_dashboard import storage as st
from usage_dashboard.api import app


@pytest.fixture
def client(cfg_with_data):
    app.state.cfg = cfg_with_data
    with TestClient(app) as c:
        yield c
    app.state.cfg = None


class TestGetAliases:
    def test_get_aliases_empty_initially(self, client):
        resp = client.get("/api/v1/aliases")
        assert resp.status_code == 200
        assert resp.json() == []

    def test_get_aliases_returns_created(self, client, cfg_with_data):
        st.upsert_alias(cfg_with_data, "abc123", "Alice")
        resp = client.get("/api/v1/aliases")
        assert resp.status_code == 200
        assert resp.json() == [{"account_hash": "abc123", "alias": "Alice"}]


class TestPutAlias:
    def test_put_alias_creates(self, client, cfg_with_data):
        resp = client.put("/api/v1/aliases", json={"account_hash": "abc123", "alias": "Alice"})
        assert resp.status_code == 200
        assert resp.json() == {"ok": True}
        aliases = st.get_aliases(cfg_with_data)
        assert aliases == [{"account_hash": "abc123", "alias": "Alice"}]

    def test_put_alias_updates_existing(self, client, cfg_with_data):
        client.put("/api/v1/aliases", json={"account_hash": "abc", "alias": "A"})
        client.put("/api/v1/aliases", json={"account_hash": "abc", "alias": "B"})
        aliases = st.get_aliases(cfg_with_data)
        assert len(aliases) == 1
        assert aliases[0]["alias"] == "B"

    def test_put_alias_missing_fields_returns_400(self, client):
        resp = client.put("/api/v1/aliases", json={"account_hash": "", "alias": ""})
        assert resp.status_code == 400
        assert resp.json() == {"detail": "account_hash and alias required"}

    def test_put_alias_invalid_json_returns_400(self, client):
        resp = client.put(
            "/api/v1/aliases",
            content=b"not json",
            headers={"Content-Type": "application/json"},
        )
        assert resp.status_code == 400

    def test_put_alias_too_long_returns_400(self, client):
        """account_hash > 128 or alias > 256."""
        resp = client.put(
            "/api/v1/aliases",
            json={"account_hash": "x" * 129, "alias": "ok"},
        )
        assert resp.status_code == 400
        assert resp.json() == {"detail": "account_hash or alias too long"}

        resp = client.put(
            "/api/v1/aliases",
            json={"account_hash": "ok", "alias": "x" * 257},
        )
        assert resp.status_code == 400
        assert resp.json() == {"detail": "account_hash or alias too long"}

    def test_put_alias_within_length_limit(self, client, cfg_with_data):
        """Boundary: 128-char hash and 256-char alias are accepted."""
        resp = client.put(
            "/api/v1/aliases",
            json={"account_hash": "x" * 128, "alias": "y" * 256},
        )
        assert resp.status_code == 200
        aliases = st.get_aliases(cfg_with_data)
        assert len(aliases) == 1


class TestDeleteAlias:
    def test_delete_alias(self, client, cfg_with_data):
        client.put("/api/v1/aliases", json={"account_hash": "abc", "alias": "A"})
        resp = client.delete("/api/v1/aliases/abc")
        assert resp.status_code == 200
        assert resp.json() == {"ok": True}
        assert st.get_aliases(cfg_with_data) == []

    def test_delete_nonexistent_alias_is_idempotent(self, client):
        resp = client.delete("/api/v1/aliases/nonexistent")
        assert resp.status_code == 200
        assert resp.json() == {"ok": True}
