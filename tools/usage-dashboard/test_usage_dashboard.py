"""Tests for the usage dashboard package.

Run: python3 -m unittest test_usage_dashboard -v
Or:  python3 test_usage_dashboard.py
"""
import json
import os
import shutil
import sys
import tempfile
import time
import unittest
from urllib.parse import parse_qs

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from usage_dashboard import collector as col
from usage_dashboard import config as cfg_mod
from usage_dashboard import pricing as pr
from usage_dashboard import query as qy
from usage_dashboard import storage as st

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
        for idx in ("idx_events_ts", "idx_events_hour_model", "idx_events_hour"):
            self.assertIn(idx, names)

    def test_migration_failure_leaves_prior_version_intact(self):
        with st.db_connect(self.cfg) as conn:
            conn.execute("DELETE FROM schema_meta")
        original_migs = st.MIGRATIONS[:]
        original_sv = st.SCHEMA_VERSION
        st.SCHEMA_VERSION = 5

        def _failing(conn):
            raise Exception("migration failed")

        st.MIGRATIONS.append(_failing)
        try:
            with self.assertRaises(Exception):
                st.run_migrations(self.cfg)
            self.assertEqual(st.current_version(self.cfg), 4)
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


# ── Errors tests ───────────────────────────────────────────────────────


class TestErrors(BaseTempData):
    def _seed(self):
        col.insert_usage(self.cfg, [
            nrec(request_id="e1", model="m1", failed=True, fail={"status_code": 429, "body": ""},
                 tokens={"input_tokens": 1, "output_tokens": 0, "total_tokens": 1}),
            nrec(request_id="e2", model="m1", failed=True, fail={"status_code": 429, "body": ""},
                 tokens={"input_tokens": 1, "output_tokens": 0, "total_tokens": 1}),
            nrec(request_id="e3", model="m2", failed=True, fail={"status_code": 500, "body": ""},
                 tokens={"input_tokens": 1, "output_tokens": 0, "total_tokens": 1}),
            nrec(request_id="ok1", model="m1", failed=False, fail={"status_code": 200, "body": ""},
                 tokens={"input_tokens": 1, "output_tokens": 0, "total_tokens": 1}),
        ])

    def test_errors_aggregated_by_status_and_model(self):
        self._seed()
        out = qy.query_errors(self.cfg, {"range": ["30d"]})
        by_key = {(e["fail_status"], e["model"]): e for e in out["errors"]}
        self.assertIn((429, "m1"), by_key)
        self.assertEqual(by_key[(429, "m1")]["count"], 2)
        self.assertEqual(by_key[(429, "m1")]["percentage"], 50.0)  # 2 of 4 total requests
        self.assertEqual(out["total_failed"], 3)
        self.assertEqual(out["total_requests"], 4)

    def test_errors_respect_model_filter(self):
        self._seed()
        out = qy.query_errors(self.cfg, {"range": ["30d"], "model": ["m2"]})
        self.assertEqual(len(out["errors"]), 1)
        self.assertEqual(out["errors"][0]["fail_status"], 500)

    def test_errors_empty_range_returns_zero_totals(self):
        out = qy.query_errors(self.cfg, {"range": ["30d"]})
        self.assertEqual(out["errors"], [])
        self.assertEqual(out["total_failed"], 0)
        self.assertEqual(out["total_requests"], 0)

    def test_errors_invalid_range_raises(self):
        with self.assertRaises(ValueError):
            qy.query_errors(self.cfg, {"range": ["bogus"]})


# ── Prices tests ──────────────────────────────────────────────────────


class TestPrices(BaseTempData):
    def test_prices_lists_currently_effective_intervals(self):
        pricing_path = cfg_mod.pricing_path_for(self.cfg)
        with open(pricing_path, "w") as f:
            json.dump({
                "currency": "USD",
                "models": {
                    "m1": [
                        {"effective_from": "2020-01-01T00:00:00Z",
                         "input_per_million": 1.0, "output_per_million": 2.0},
                    ],
                },
            }, f)
        out = qy.query_prices(self.cfg)
        self.assertEqual(out["currency"], "USD")
        self.assertEqual(len(out["models"]), 1)
        m = out["models"][0]
        self.assertEqual(m["model"], "m1")
        self.assertTrue(m["effective_now"])
        self.assertEqual(m["input_per_million"], 1.0)

    def test_prices_marks_future_interval_not_effective(self):
        pricing_path = cfg_mod.pricing_path_for(self.cfg)
        with open(pricing_path, "w") as f:
            json.dump({
                "currency": "USD",
                "models": {
                    "m2": [{"effective_from": "2999-01-01T00:00:00Z",
                            "input_per_million": 9.0}],
                },
            }, f)
        out = qy.query_prices(self.cfg)
        self.assertFalse(out["models"][0]["effective_now"])

    def test_prices_empty_when_no_file(self):
        out = qy.query_prices(self.cfg)
        self.assertEqual(out["models"], [])
        self.assertEqual(out["currency"], "USD")



# ── Key Aliases tests ──────────────────────────────────────────────────


class TestAliases(BaseTempData):
    def test_initial_empty(self):
        self.assertEqual(st.get_aliases(self.cfg), [])

    def test_upsert_and_get(self):
        st.upsert_alias(self.cfg, "hash1", "My Key")
        aliases = st.get_aliases(self.cfg)
        self.assertEqual(len(aliases), 1)
        self.assertEqual(aliases[0]["account_hash"], "hash1")
        self.assertEqual(aliases[0]["alias"], "My Key")

    def test_upsert_update(self):
        st.upsert_alias(self.cfg, "hash1", "Original")
        st.upsert_alias(self.cfg, "hash1", "Updated")
        aliases = st.get_aliases(self.cfg)
        self.assertEqual(aliases[0]["alias"], "Updated")

    def test_delete(self):
        st.upsert_alias(self.cfg, "hash1", "My Key")
        st.delete_alias(self.cfg, "hash1")
        self.assertEqual(st.get_aliases(self.cfg), [])

    def test_delete_nonexistent(self):
        st.delete_alias(self.cfg, "nonexistent")
        self.assertEqual(st.get_aliases(self.cfg), [])

    def test_query_summary_accounts_with_alias(self):
        col.insert_usage(self.cfg, [nrec(account_hash="hash1")])
        st.upsert_alias(self.cfg, "ca978112ca1b", "My Key")
        result = qy.query_summary(self.cfg, {"range": ["30d"]})
        accounts = result["accounts"]
        self.assertEqual(len(accounts), 1)
        self.assertEqual(accounts[0]["account"], "My Key")

    def test_query_accounts_with_alias(self):
        col.insert_usage(self.cfg, [nrec(account_hash="hash1")])
        st.upsert_alias(self.cfg, "ca978112ca1b", "My Key")
        result = qy.query_accounts(self.cfg, {"range": ["30d"]})
        self.assertEqual(result["accounts"][0]["account"], "My Key")

    def test_query_requests_with_alias(self):
        col.insert_usage(self.cfg, [nrec(account_hash="hash1")])
        st.upsert_alias(self.cfg, "ca978112ca1b", "My Key")
        result = qy.query_requests(self.cfg, {"range": ["30d"], "limit": ["10"]})
        self.assertEqual(result["requests"][0]["account"], "My Key")

    def test_query_accounts_fallback_to_hash(self):
        col.insert_usage(self.cfg, [nrec(account_hash="sha256:abcdef123456")])
        result = qy.query_accounts(self.cfg, {"range": ["30d"]})
        self.assertEqual(result["accounts"][0]["account"], "ca978112ca1b")


# ── Bootstrap ──────────────────────────────────────────────────────────

if __name__ == "__main__":
    unittest.main()
