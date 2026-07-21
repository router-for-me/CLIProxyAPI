"""Tests for the usage dashboard package.

Run: python3 -m unittest test_usage_dashboard -v
Or:  python3 test_usage_dashboard.py
"""
import json
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer, ThreadingHTTPServer
from urllib.parse import parse_qs

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from usage_dashboard import config as cfg_mod
from usage_dashboard import storage as st
from usage_dashboard import collector as col
from usage_dashboard import pricing as pr
from usage_dashboard import query as qy
from usage_dashboard import server as srv

# ── helpers ────────────────────────────────────────────────────────────

RECORD_BASE = {
    "timestamp": "2026-07-17T10:00:00Z",
    "request_id": "r",
    "source": "a",
    "provider": "t",
    "model": "ts-gpt-56",
    "endpoint": "/v1",
    "tokens": {"input_tokens": 1000, "output_tokens": 500, "total_tokens": 1500},
    "failed": False,
}


def nrec(**o):
    r = dict(RECORD_BASE)
    r.update(o)
    return json.dumps(r)


class BaseTempData(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.mkdtemp(prefix="ud-test-")
        os.environ["USAGE_DASHBOARD_DATA_DIR"] = self.tmpdir
        self.cfg = cfg_mod.load_config()
        st.init_schema(self.cfg)

    def tearDown(self):
        os.environ.pop("USAGE_DASHBOARD_DATA_DIR", None)
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def fresh(self):
        shutil.rmtree(self.tmpdir, ignore_errors=True)
        os.makedirs(self.tmpdir, exist_ok=True)
        self.cfg = cfg_mod.load_config()
        st.init_schema(self.cfg)


# ── Storage tests ──────────────────────────────────────────────────────


class TestStorage(BaseTempData):
    def test_init_creates_latest_schema(self):
        self.assertEqual(st.current_version(self.cfg), st.SCHEMA_VERSION)

    def test_extended_indexes_exist(self):
        with st.db_connect(self.cfg) as conn:
            names = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='index'")}
        for idx in ("idx_usage_ts", "idx_usage_model_ts", "idx_usage_provider_ts"):
            self.assertIn(idx, names)

    def test_migration_failure_leaves_prior_version_intact(self):
        with st.db_connect(self.cfg) as conn:
            conn.execute("DELETE FROM schema_meta")
        original_migs = st.MIGRATIONS[:]
        original_sv = st.SCHEMA_VERSION
        st.SCHEMA_VERSION = 4

        def _failing(conn):
            raise Exception("migration failed")

        st.MIGRATIONS.append(_failing)
        try:
            with self.assertRaises(Exception):
                st.run_migrations(self.cfg)
            self.assertEqual(st.current_version(self.cfg), 3)
        finally:
            st.MIGRATIONS[:] = original_migs
            st.SCHEMA_VERSION = original_sv

    def test_db_connect_closes_connection(self):
        with st.db_connect(self.cfg) as conn:
            self.assertIsNotNone(conn.execute("SELECT 1").fetchone())
        with self.assertRaises(Exception):
            conn.execute("SELECT 1")

    def test_save_and_load_state(self):
        st.save_state(self.cfg, {"last_poll_ok": True, "total_inserted": 42})
        state = st.load_state(self.cfg)
        self.assertTrue(state["last_poll_ok"])
        self.assertEqual(state["total_inserted"], 42)


# ── Insertion tests ────────────────────────────────────────────────────


class TestInsertion(BaseTempData):
    def test_insert_idempotent(self):
        col.insert_usage(self.cfg, [nrec(request_id="d")])
        col.insert_usage(self.cfg, [nrec(request_id="d")])
        with st.db_connect(self.cfg) as c:
            self.assertEqual(c.execute("SELECT COUNT(*) FROM usage_events").fetchone()[0], 1)

    def test_sensitive_fields_dropped(self):
        col.insert_usage(self.cfg, [nrec(api_key="sk-SECRET")])
        with st.db_connect(self.cfg) as c:
            blob = json.dumps(dict(c.execute("SELECT * FROM usage_events").fetchone()))
        self.assertNotIn("sk-SECRET", blob)

    def test_source_hash_preserved(self):
        col.insert_usage(self.cfg, [nrec(source="sk-LiveKey")])
        with st.db_connect(self.cfg) as c:
            row = dict(c.execute("SELECT * FROM usage_events").fetchone())
        self.assertNotIn("sk-LiveKey", json.dumps(row))
        self.assertTrue(row["account_hash"])

    def test_account_hash_prefers_client_api_key(self):
        """Account grouping must key off the client CPA API key when present."""
        import hashlib
        want = hashlib.sha256(b"client-proxy-key").hexdigest()[:12]
        col.insert_usage(
            self.cfg,
            [
                nrec(
                    request_id="acc-1",
                    api_key="client-proxy-key",
                    source="upstream-provider-key",
                    auth_index="auth-xyz",
                )
            ],
        )
        with st.db_connect(self.cfg) as c:
            row = dict(c.execute("SELECT account_hash FROM usage_events").fetchone())
        self.assertEqual(row["account_hash"], want)
        self.assertNotEqual(
            row["account_hash"],
            hashlib.sha256(b"upstream-provider-key").hexdigest()[:12],
        )

    def test_account_filter_limits_summary(self):
        import hashlib
        a = hashlib.sha256(b"key-a").hexdigest()[:12]
        b = hashlib.sha256(b"key-b").hexdigest()[:12]
        col.insert_usage(
            self.cfg,
            [
                nrec(
                    request_id="fa",
                    api_key="key-a",
                    tokens={"input_tokens": 10, "output_tokens": 0, "total_tokens": 10},
                ),
                nrec(
                    request_id="fb",
                    api_key="key-b",
                    tokens={"input_tokens": 90, "output_tokens": 0, "total_tokens": 90},
                ),
            ],
        )
        all_sum = qy.query_summary(self.cfg, {"range": ["30d"]})
        self.assertEqual(all_sum["summary"]["requests"], 2)
        filtered = qy.query_summary(self.cfg, {"range": ["30d"], "account": [a]})
        self.assertEqual(filtered["summary"]["requests"], 1)
        self.assertEqual(filtered["accounts_filter"], [a])
        self.assertEqual(filtered["accounts"][0]["account"], a)
        accounts = qy.query_accounts(self.cfg, {"range": ["30d"]})
        ids = {x["account"] for x in accounts["accounts"]}
        self.assertEqual(ids, {a, b})

    def test_malformed_record_does_not_lose_batch(self):
        inserted, dup, err = col.insert_usage(self.cfg, [nrec(request_id="g1"), "{not-json", nrec(request_id="g2")])
        self.assertEqual(inserted, 2)
        self.assertEqual(err, 1)

    def test_parse_rfc3339_strict_raises_on_invalid(self):
        with self.assertRaises(ValueError):
            col.parse_rfc3339("not-a-time")

    def test_parse_rfc3339_empty_returns_now(self):
        now = time.time()
        result = col.parse_rfc3339(None)
        self.assertAlmostEqual(result.timestamp(), now, delta=1)

    def test_collect_once_tracks_dropped_records(self):
        original_fetch = col.fetch_usage_batch
        try:
            called = []

            def mock_fetch(cfg, count):
                if called:
                    return []
                called.append(1)
                return [nrec(request_id="ok"), "{bogus"]

            col.fetch_usage_batch = mock_fetch
            col.collect_once(self.cfg)
            snap = col.COLLECTOR_STATE.snapshot(self.cfg)
            self.assertFalse(snap["last_poll_ok"])
            self.assertGreater(snap["dropped_count"], 0)
        finally:
            col.fetch_usage_batch = original_fetch

    def test_collect_forever_survives_poll_errors(self):
        """Poll errors must be logged and retried; never kill the collector thread."""
        original_once = col.collect_once
        calls = {"n": 0}
        stop = threading.Event()

        def boom(cfg):
            calls["n"] += 1
            if calls["n"] >= 2:
                stop.set()
            raise RuntimeError("simulated poll failure Bearer secret-token")

        col.collect_once = boom
        try:
            t = threading.Thread(
                target=col.collect_forever,
                args=(self.cfg, stop),
                daemon=True,
            )
            t.start()
            t.join(timeout=5)
            self.assertFalse(t.is_alive(), "collector thread should exit via stop_event")
            self.assertGreaterEqual(calls["n"], 2)
        finally:
            col.collect_once = original_once


# ── Collector lock tests ───────────────────────────────────────────────


class TestCollectorLock(BaseTempData):
    def test_second_lock_rejected(self):
        l1 = col.CollectorLock(self.cfg)
        l1.acquire()
        with self.assertRaises(SystemExit):
            (col.CollectorLock(self.cfg)).acquire()
        l1.release()


# ── Pricing tests ──────────────────────────────────────────────────────


class TestPricing(BaseTempData):
    def setUp(self):
        super().setUp()
        with open(cfg_mod.pricing_path_for(self.cfg), "w") as f:
            json.dump({
                "currency": "USD",
                "models": {
                    "m": [{"effective_from": "2026-01-01T00:00:00Z",
                           "input_per_million": 2, "output_per_million": 4}],
                },
            }, f)

    def test_load_pricing_missing_returns_empty(self):
        p = pr.load_pricing({"data_dir": self.tmpdir})
        self.assertEqual(p["currency"], "USD")

    def test_valid_pricing(self):
        pr.validate(pr.load_pricing(self.cfg))

    def test_negative_rate_rejected(self):
        with open(cfg_mod.pricing_path_for(self.cfg), "w") as f:
            json.dump({"currency": "USD", "models": {"m": [{"effective_from": "2026-01-01T00:00:00Z", "input_per_million": -2}]}}, f)
        with self.assertRaises(ValueError):
            pr.validate(pr.load_pricing(self.cfg))

    def test_overlap_rejected(self):
        with open(cfg_mod.pricing_path_for(self.cfg), "w") as f:
            json.dump({"currency": "USD", "models": {"m": [
                {"effective_from": "2026-01-01T00:00:00Z", "effective_to": "2026-07-01T00:00:00Z", "input_per_million": 1},
                {"effective_from": "2026-06-01T00:00:00Z", "input_per_million": 2},
            ]}}, f)
        with self.assertRaises(ValueError):
            pr.validate(pr.load_pricing(self.cfg))

    def test_price_for_before_all_is_none(self):
        self.assertIsNone(pr.price_for(pr.load_pricing(self.cfg), "m", 0))

    def test_price_for_unknown_model_is_none(self):
        self.assertIsNone(pr.price_for(pr.load_pricing(self.cfg), "nosuch", 5000000000))

    def test_estimate_cost_empty(self):
        cost = pr.estimate_cost(self.cfg, [])
        self.assertEqual(cost["coverage"], "empty")

    def test_estimate_cost_valid(self):
        records = [{"model": "m", "ts_epoch": 5000000000, "input_tokens": 1000, "output_tokens": 500,
                     "reasoning_tokens": 0, "cached_tokens": 0, "total_tokens": 1500}]
        cost = pr.estimate_cost(self.cfg, records)
        self.assertAlmostEqual(cost["total"]["cost"], 1000 * 2 / 1_000_000 + 500 * 4 / 1_000_000)
        self.assertEqual(cost["coverage"], "complete")


# ── Query tests ────────────────────────────────────────────────────────


class TestQuery(BaseTempData):
    def setUp(self):
        super().setUp()
        with open(cfg_mod.pricing_path_for(self.cfg), "w") as f:
            json.dump({
                "currency": "USD",
                "models": {"ts-gpt-56": [{"effective_from": "2026-01-01T00:00:00Z",
                                          "input_per_million": 2, "output_per_million": 4}]},
            }, f)
        col.insert_usage(self.cfg, [nrec(request_id="r1", timestamp="2026-07-17T10:00:00Z")])
        col.insert_usage(self.cfg, [nrec(request_id="r2", timestamp="2026-07-17T11:00:00Z", model="ts-opus-48")])

    def q(self, s):
        return parse_qs(s, keep_blank_values=True)

    def test_summary_explicit_range(self):
        s = qy.query_summary(self.cfg, self.q("from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z"))
        self.assertEqual(s["summary"]["requests"], 2)

    def test_summary_model_filter(self):
        s = qy.query_summary(self.cfg, self.q("from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z&model=ts-opus-48"))
        self.assertEqual(s["summary"]["requests"], 1)

    def test_summary_empty_result(self):
        s = qy.query_summary(self.cfg, self.q("from=2030-01-01T00:00:00Z&to=2030-12-31T00:00:00Z"))
        self.assertEqual(s["summary"]["requests"], 0)

    def test_invalid_from_to_rejected(self):
        with self.assertRaises(ValueError):
            qy.query_summary(self.cfg, self.q("from=x&to=y"))

    def test_reversed_range_rejected(self):
        with self.assertRaises(ValueError):
            qy.query_summary(self.cfg, self.q("from=2026-07-18T00:00:00Z&to=2026-07-17T00:00:00Z"))

    def test_bogus_preset_rejected(self):
        with self.assertRaises(ValueError):
            qy.query_summary(self.cfg, self.q("range=bogus"))

    def test_group_by_allowlist(self):
        for g in ("model", "provider", "day", "hour"):
            qy.query_timeseries(self.cfg, self.q(f"from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z&group_by={g}"))

    def test_pagination_strict_lt(self):
        col.insert_usage(self.cfg, [
            nrec(request_id="d1", timestamp="2026-07-17T12:00:00Z"),
            nrec(request_id="d2", timestamp="2026-07-17T12:00:00Z"),
            nrec(request_id="d3", timestamp="2026-07-17T12:00:00Z"),
        ])
        r1 = qy.query_requests(self.cfg, self.q("from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z&limit=2"))
        self.assertEqual(len(r1["requests"]), 2)
        r2 = qy.query_requests(self.cfg, self.q(f"from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z&limit=2&cursor={r1['next_cursor']}"))
        p1_ids = {x["request_id"] for x in r1["requests"]}
        p2_ids = {x["request_id"] for x in r2["requests"]}
        self.assertEqual(len(p1_ids & p2_ids), 0)
        self.assertTrue({"d1", "d2", "d3"}.issubset(p1_ids | p2_ids))

    def test_no_credentials_in_requests(self):
        r = qy.query_requests(self.cfg, self.q("from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z"))
        self.assertNotIn("sk-LiveKey", json.dumps(r))

    def test_limit_bounded(self):
        r = qy.query_requests(self.cfg, self.q("from=2026-07-01T00:00:00Z&to=2026-07-31T00:00:00Z&limit=99999"))
        self.assertLessEqual(r["limit"], 500)


# ── Server tests ───────────────────────────────────────────────────────


class TestServerHTTP(BaseTempData):
    def setUp(self):
        super().setUp()
        self._httpd = None
        # Reset global collector state so snapshot falls through to persisted state.
        col.COLLECTOR_STATE.last_poll_epoch = 0.0

    def _start(self, cfg):
        self._httpd = ThreadingHTTPServer(("127.0.0.1", 0), srv.make_handler(cfg))
        t = threading.Thread(target=self._httpd.serve_forever, daemon=True)
        t.start()
        return self._httpd.server_address[1]

    def _get(self, port, path, token=None):
        url = f"http://127.0.0.1:{port}{path}"
        req = urllib.request.Request(url)
        if token:
            req.add_header("Authorization", f"Bearer {token}")
        try:
            with urllib.request.urlopen(req) as resp:
                return resp.status, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def test_valid_summary_200(self):
        col.insert_usage(self.cfg, [nrec()])
        port = self._start(self.cfg)
        s, _ = self._get(port, "/api/v1/summary?range=24h")
        self.assertEqual(s, 200)

    def test_invalid_query_400(self):
        port = self._start(self.cfg)
        s, _ = self._get(port, "/api/v1/summary?range=bogus")
        self.assertEqual(s, 400)

    def test_not_found_404(self):
        port = self._start(self.cfg)
        s, _ = self._get(port, "/api/v1/nope")
        self.assertEqual(s, 404)

    def test_token_gate_401(self):
        cfg = dict(self.cfg)
        cfg["dashboard_token"] = "secret"
        port = self._start(cfg)
        s, _ = self._get(port, "/api/v1/summary?range=24h")
        self.assertEqual(s, 401)

    def test_token_gate_200_with_valid_token(self):
        cfg = dict(self.cfg)
        cfg["dashboard_token"] = "secret"
        port = self._start(cfg)
        s, _ = self._get(port, "/api/v1/summary?range=24h", token="secret")
        self.assertEqual(s, 200)

    def test_health_returns_persisted_state(self):
        st.save_state(self.cfg, {
            "last_poll_ok": True,
            "last_poll_epoch": time.time(),
            "last_commit_epoch": time.time(),
            "total_inserted": 5,
        })
        port = self._start(self.cfg)
        s, body = self._get(port, "/api/v1/health")
        self.assertEqual(s, 200)
        data = json.loads(body)
        self.assertTrue(data["last_poll_ok"])
        self.assertEqual(data["total_inserted"], 5)

    def test_config_failure_raises(self):
        path = cfg_mod.config_path_for(self.cfg)
        with open(path, "w") as f:
            f.write("{invalid json")
        with self.assertRaises(SystemExit):
            cfg_mod.load_config()

    def test_non_loopback_without_token_rejected(self):
        cfg = dict(self.cfg)
        cfg["dashboard_host"] = "0.0.0.0"
        cfg["dashboard_token"] = ""
        with self.assertRaises(SystemExit):
            srv.serve(cfg)

    def test_is_authorized_with_token(self):
        cfg = dict(self.cfg)
        cfg["dashboard_token"] = "tok"

        class FakeHdr:
            def __init__(self, d):
                self.d = d
            def get(self, k, default=None):
                return self.d.get(k, default)

        class FakeHandler:
            def __init__(self, hdrs):
                self.headers = FakeHdr(hdrs)

        self.assertFalse(srv.is_authorized(FakeHandler({}), cfg))
        self.assertTrue(srv.is_authorized(FakeHandler({"Authorization": "Bearer tok"}), cfg))
        self.assertTrue(srv.is_authorized(FakeHandler({"X-Dashboard-Token": "tok"}), cfg))
        self.assertFalse(srv.is_authorized(FakeHandler({"Authorization": "Bearer wrong"}), cfg))


# ── Static asset tests ────────────────────────────────────────────────


class TestStaticAssets(BaseTempData):
    def test_chart_js_served_with_js_mime(self):
        import http.client
        import socket
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
        sock.close()
        self.cfg["dashboard_port"] = port
        ready = threading.Event()
        t = threading.Thread(target=srv.serve, args=(self.cfg, ready), daemon=True)
        t.start()
        ready.wait(timeout=3)
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
        conn.request("GET", "/static/chart.js")
        resp = conn.getresponse()
        body = resp.read()
        self.assertEqual(resp.status, 200)
        self.assertIn("javascript", resp.getheader("Content-Type", ""))
        self.assertIn(b"Chart.js", body[:500])
        # immutable cache for vendored asset
        self.assertIn("max-age=", resp.getheader("Cache-Control", ""))

    def test_unknown_static_path_returns_404(self):
        import http.client, socket
        sock = socket.socket(); sock.bind(("127.0.0.1", 0)); port = sock.getsockname()[1]; sock.close()
        self.cfg["dashboard_port"] = port
        ready = threading.Event()
        t = threading.Thread(target=srv.serve, args=(self.cfg, ready), daemon=True); t.start(); ready.wait(timeout=3)
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
        conn.request("GET", "/static/does-not-exist.js")
        resp = conn.getresponse()
        self.assertEqual(resp.status, 404)


# ── Bootstrap ──────────────────────────────────────────────────────────

if __name__ == "__main__":
    unittest.main()