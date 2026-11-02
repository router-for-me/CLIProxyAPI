import json
import sqlite3
import unittest
from copy import deepcopy

from tools.quota_bridge import Bridge, account_id, ensure_schema, parse_forecast_query


class ClaudeQuotaRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.bridge = Bridge.__new__(Bridge)
        self.bridge.db = sqlite3.connect(":memory:")
        self.bridge.db.execute("""CREATE TABLE observation (
            id TEXT PRIMARY KEY, account TEXT, window TEXT,
            used_basis_points INTEGER, reset_at INTEGER,
            observed_at INTEGER, sent INTEGER DEFAULT 0)""")
        self.addCleanup(self.bridge.db.close)
        self.file = {
            "auth_index": "restored-account", "provider": "claude", "disabled": False,
            "updated_at": "2026-11-01T15:53:17Z", "unavailable": True,
            "cooldowns": [
                {"scope": "credential", "reason": "credential_quota", "retry_at": "2026-11-01T17:00:26Z"},
                {"scope": "model", "model_key": "claude-opus-5-5", "reason": "quota",
                 "retry_at": "2026-11-01T17:00:26Z", "http_status": 429},
            ],
        }
        self.current = deepcopy(self.file)
        self.payload = {"five_hour": {"utilization": 0, "resets_at": None},
                        "seven_day": {"utilization": 0, "resets_at": "2026-11-04T17:00:00Z"}}
        self.requests = []
        self.status = 200
        self.credential_reads = 0
        self.bridge.log = lambda message: None

        def management(path, body=None):
            self.requests.append((path, body))
            if path == "/credentials":
                self.credential_reads += 1
                return {"files": [self.file if self.credential_reads == 1 else self.current]}
            if path == "/requests/api-call":
                return {"status_code": self.status, "body": json.dumps(self.payload)}
            if path == "/routing/cooldown/reset":
                return {"status": "ok"}
            self.fail(f"unexpected management request: {path}")

        self.bridge.management = management

    def resets(self):
        return [body for path, body in self.requests if path == "/routing/cooldown/reset"]

    def test_collection_releases_restored_credential_and_records_observation(self):
        self.assertFalse(self.bridge.collect_quota())
        self.assertEqual(self.resets(), [{"auth_index": "restored-account"}])
        self.assertEqual(self.bridge.db.execute("SELECT COUNT(*) FROM observation").fetchone()[0], 2)

    def test_exhausted_shared_or_scoped_limit_keeps_cooldown(self):
        for field in ("five_hour", "seven_day", "seven_day_opus"):
            with self.subTest(field=field):
                self.payload[field] = {"utilization": 100}
                self.bridge.collect_quota()
                self.assertEqual(self.resets(), [])
                self.payload[field] = {"utilization": 0}
        self.payload["limits"] = [{"kind": "weekly_scoped", "percent": 100}]
        self.bridge.collect_quota()
        self.assertEqual(self.resets(), [])

    def test_missing_malformed_or_nonfinite_usage_keeps_cooldown(self):
        for value in ({}, [], None, {"five_hour": {"utilization": 0}},
                      {"five_hour": None, "seven_day": None},
                      *({"five_hour": {"utilization": u}, "seven_day": {"utilization": 0}}
                        for u in (-1, 100, True, "0", float("nan"), float("inf")))):
            with self.subTest(value=value):
                self.payload = value
                self.bridge.collect_quota()
                self.assertEqual(self.resets(), [])

    def test_inactive_window_can_be_null(self):
        self.payload["five_hour"] = None
        self.bridge.collect_quota()
        self.assertEqual(self.resets(), [{"auth_index": "restored-account"}])

    def test_unrelated_or_independent_model_cooldown_is_preserved(self):
        for reason in ("unauthorized", "cloudflare_challenge", "transient_error", "unknown"):
            with self.subTest(reason=reason):
                self.file["cooldowns"][1]["reason"] = reason
                self.current = deepcopy(self.file)
                self.bridge.collect_quota()
                self.assertEqual(self.resets(), [])
        self.file["cooldowns"][1]["reason"] = "quota"
        self.file["cooldowns"][1]["retry_at"] = "2026-11-02T17:00:26Z"
        self.current = deepcopy(self.file)
        self.bridge.collect_quota()
        self.assertEqual(self.resets(), [])

    def test_newer_routing_state_or_disabled_account_is_preserved(self):
        self.current["updated_at"] = "2026-11-01T16:05:00Z"
        self.bridge.collect_quota()
        self.assertEqual(self.resets(), [])
        self.current = deepcopy(self.file)
        self.current["disabled"] = True
        self.bridge.collect_quota()
        self.assertEqual(self.resets(), [])

    def test_failed_usage_probe_keeps_cooldown(self):
        for status in (401, 429, 500):
            with self.subTest(status=status):
                self.status = status
                self.bridge.collect_quota()
                self.assertEqual(self.resets(), [])


class CodexQuotaCollectionTest(unittest.TestCase):
    def test_records_account_windows_under_codex_identity(self):
        bridge = Bridge.__new__(Bridge)
        bridge.db = sqlite3.connect(":memory:")
        bridge.db.execute("""
            CREATE TABLE observation (
                id TEXT PRIMARY KEY, account TEXT, window TEXT,
                used_basis_points INTEGER, reset_at INTEGER,
                observed_at INTEGER, sent INTEGER DEFAULT 0
            )
        """)
        bridge.management = lambda path, body: {
            "status_code": 200,
            "body": json.dumps({"rate_limit": {
                "primary_window": {
                    "used_percent": 45,
                    "limit_window_seconds": 604800,
                    "reset_at": 1793774152,
                },
                "secondary_window": {
                    "used_percent": 12.5,
                    "limit_window_seconds": 18000,
                    "reset_at": 1793274152,
                },
            }}),
        }

        self.assertFalse(bridge.collect_codex_quota({
            "auth_index": "test-auth-index",
            "id_token": {"chatgpt_account_id": "test-account"},
        }))
        rows = bridge.db.execute(
            "SELECT account, window, used_basis_points, reset_at FROM observation ORDER BY window"
        ).fetchall()
        self.assertEqual(rows, [
            (account_id("test-auth-index", "codex"), "five_hour", 1250, 1793274152),
            (account_id("test-auth-index", "codex"), "weekly", 4500, 1793774152),
        ])
        self.assertNotEqual(account_id("test-auth-index"), rows[0][0])


class CodexCreditBalanceTest(unittest.TestCase):
    def setUp(self):
        self.bridge = Bridge.__new__(Bridge)
        self.bridge.db = sqlite3.connect(":memory:")
        self.bridge.db.execute("""CREATE TABLE credit_balance (
            account TEXT PRIMARY KEY, balance TEXT, has_credits INTEGER,
            unlimited INTEGER, overage_limit_reached INTEGER,
            observed_at INTEGER, sent INTEGER DEFAULT 0)""")

    def tearDown(self):
        self.bridge.db.close()

    def test_preserves_exact_decimal_and_zero_without_unit_conversion(self):
        self.bridge.record_codex_credits("account", {"balance": "45696.5189625000", "has_credits": True}, 100)
        self.assertEqual(self.bridge.db.execute("SELECT balance FROM credit_balance").fetchone()[0], "45696.5189625000")
        self.bridge.record_codex_credits("account", {"balance": "0", "has_credits": False}, 200)
        self.assertEqual(self.bridge.db.execute("SELECT balance,has_credits FROM credit_balance").fetchone(), ("0", 0))

    def test_missing_invalid_and_older_reads_keep_last_known_balance(self):
        self.bridge.record_codex_credits("account", {"balance": "12.50"}, 200)
        for payload in (None, {}, {"balance": "NaN"}, {"balance": -1}, {"balance": True}, {"balance": "1e3"}):
            self.bridge.record_codex_credits("account", payload, 300)
        self.bridge.record_codex_credits("account", {"balance": "99"}, 100)
        self.assertEqual(self.bridge.db.execute("SELECT balance,observed_at FROM credit_balance").fetchone(), ("12.50", 200))

    def test_collects_credit_balance_even_without_a_rate_limit_window(self):
        self.bridge.management = lambda path, body: {"status_code": 200,
            "body": json.dumps({"credits": {"balance": "45696.5189625000", "has_credits": True}})}
        self.assertFalse(self.bridge.collect_codex_quota({"auth_index": "account"}))
        self.assertEqual(self.bridge.db.execute("SELECT balance FROM credit_balance").fetchone()[0], "45696.5189625000")

    def test_accepts_unlimited_without_inventing_a_balance(self):
        self.bridge.record_codex_credits("account", {"balance": None, "unlimited": True}, 100)
        self.assertEqual(self.bridge.db.execute("SELECT balance,unlimited FROM credit_balance").fetchone(), (None, 1))


class UsageSessionTest(unittest.TestCase):
    def setUp(self):
        self.bridge = Bridge.__new__(Bridge)
        self.bridge.db = sqlite3.connect(":memory:")
        self.addCleanup(self.bridge.db.close)
        ensure_schema(self.bridge.db)

    def record(self, **extra):
        row = {"provider": "claude", "auth_index": "acc", "execution_id": "e1", "model": "claude-opus-5-5",
               "timestamp": "2026-11-02T08:00:30Z",
               "tokens": {"input_tokens": 10, "output_tokens": 5, "cache_read_tokens": 900, "cache_creation_tokens": 40}}
        row.update(extra)
        self.bridge.management = lambda path, body=None: [row]
        self.bridge.collect_usage()
        return self.bridge.db.execute(
            "SELECT session_id,parent_session_id,cache_read_tokens,cache_creation_tokens,minute FROM usage_event").fetchone()

    def test_saves_sessions_and_cache_tokens(self):
        self.assertEqual(self.record(session_id="child", parent_session_id="lead"),
                         ("child", "lead", 900, 40, 1793606400))

    def test_codex_cached_tokens_count_as_cache_reads(self):
        row = self.record(tokens={"input_tokens": 10, "output_tokens": 5, "cached_tokens": 700})
        self.assertEqual(row[2], 700)

    def test_malformed_record_is_skipped_not_fatal(self):
        self.assertIsNone(self.record(tokens="not an object"))
        self.assertIsNone(self.record(timestamp=12345))

    def test_unusable_session_ids_are_dropped_not_guessed(self):
        self.assertEqual(self.record(session_id="x" * 200, parent_session_id=5)[:2], (None, None))

    def test_existing_outbox_gains_the_new_columns(self):
        db = sqlite3.connect(":memory:")
        self.addCleanup(db.close)
        db.execute("""CREATE TABLE usage_event (id TEXT PRIMARY KEY, account TEXT NOT NULL, model TEXT NOT NULL,
            minute INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL)""")
        db.execute("INSERT INTO usage_event VALUES ('old','a','m',60,1,2)")
        ensure_schema(db)
        ensure_schema(db)  # idempotent on the next start
        columns = [row[1] for row in db.execute("PRAGMA table_info(usage_event)")]
        self.assertEqual(columns[-4:], ["session_id", "parent_session_id", "cache_read_tokens", "cache_creation_tokens"])
        self.assertEqual(db.execute("SELECT COUNT(*) FROM usage_event").fetchone()[0], 1)


class ForecastQueryParsingTest(unittest.TestCase):
    def test_accepts_accounts_plan_and_agents(self):
        accounts, agents = parse_forecast_query("accounts=" + json.dumps(
            [{"provider": "claude", "auth_index": "abc", "label": "Main", "plan": "plan_ultra"}]) + "&agents.claude=3")
        self.assertEqual((accounts[0].account, accounts[0].label, accounts[0].plan, agents),
                         (account_id("abc"), "Main", "plan_ultra", {"claude": 3}))

    def test_refuses_malformed_input(self):
        for query in ("", "accounts=[]", "accounts=nope", "accounts=" + json.dumps([{"provider": "gemini", "auth_index": "a"}]),
                      "accounts=" + json.dumps([{"provider": "claude", "auth_index": ""}]),
                      "accounts=" + json.dumps([{"provider": "claude", "auth_index": "a"}]) + "&agents.codex=-1",
                      "accounts=" + json.dumps([{"provider": "claude", "auth_index": "a"}]) + "&agents.claude=many",
                      "accounts=" + json.dumps([{"provider": "claude", "auth_index": "a"}]) + "&agents.claude=1" + "0" * 400):
            with self.subTest(query=query):
                with self.assertRaises((ValueError, TypeError)):
                    parse_forecast_query(query)


if __name__ == "__main__":
    unittest.main()
