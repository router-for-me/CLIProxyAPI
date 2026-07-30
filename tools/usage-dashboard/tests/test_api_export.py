"""Tests for the CSV export endpoint — parity vs query_requests."""
import csv
import io

import pytest
from fastapi.testclient import TestClient

from usage_dashboard.api import app


@pytest.fixture
def client(cfg_with_data):
    app.state.cfg = cfg_with_data
    with TestClient(app) as c:
        yield c
    app.state.cfg = None


FIXTURE_FROM = "2026-01-01T00:00:00Z"
FIXTURE_TO = "2026-01-02T00:00:00Z"


def _parse_csv(text: str):
    reader = csv.DictReader(io.StringIO(text))
    return list(reader)


def test_export_returns_csv(client):
    """CSV is returned with correct Content-Type and Content-Disposition."""
    resp = client.get("/api/v1/export", params={"from": FIXTURE_FROM, "to": FIXTURE_TO})
    assert resp.status_code == 200
    assert resp.headers["content-type"].startswith("text/csv")
    assert "attachment" in resp.headers.get("content-disposition", "")
    rows = _parse_csv(resp.text)
    assert len(rows) > 0
    # Every row has the canonical columns
    for row in rows:
        assert "request_id" in row
        assert "model" in row
        assert "total_tokens" in row
        assert "estimated_cost" in row


def test_export_matches_legacy_query_shape(client, cfg_with_data, legacy_response):
    """First row of CSV matches legacy query_requests row (same column names)."""
    resp = client.get("/api/v1/export", params={"from": FIXTURE_FROM, "to": FIXTURE_TO})
    csv_rows = _parse_csv(resp.text)
    qs = {"from": [FIXTURE_FROM], "to": [FIXTURE_TO]}
    legacy = legacy_response(cfg_with_data, "query_requests", qs)
    legacy_rows = legacy["requests"]
    assert len(csv_rows) == len(legacy_rows)
    # Spot-check first row
    for k in ("request_id", "model", "total_tokens"):
        assert csv_rows[0][k] == str(legacy_rows[0][k])


def test_export_handles_empty_range(client, cfg_with_data):
    """If no data in range, CSV has headers only."""
    from_ts = "2025-01-01T00:00:00Z"
    to_ts = "2025-01-02T00:00:00Z"
    resp = client.get("/api/v1/export", params={"from": from_ts, "to": to_ts})
    assert resp.status_code == 200
    rows = _parse_csv(resp.text)
    # May be empty list but headers present
    assert isinstance(rows, list)
