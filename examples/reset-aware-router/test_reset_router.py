import datetime as dt
import importlib.util
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("reset_router", Path(__file__).with_name("reset_router.py"))
router = importlib.util.module_from_spec(spec)
spec.loader.exec_module(router)


class PolicyTests(unittest.TestCase):
    def setUp(self):
        self.now = dt.datetime(2026, 10, 16, tzinfo=dt.timezone.utc)
        self.expiry = int((self.now + dt.timedelta(hours=20)).timestamp())
        self.weekly_reset = int((self.now + dt.timedelta(days=5)).timestamp())

    def limits(self, used=92, expiry=None, reset=None):
        return {
            "rateLimits": {"primary": {"usedPercent": 20, "windowDurationMins": 300}},
            "rateLimitsByLimitId": {"codex": {
                "primary": {"usedPercent": 20, "windowDurationMins": 300},
                "secondary": {"usedPercent": used, "windowDurationMins": 10080,
                              "resetsAt": reset or self.weekly_reset},
            }},
            "rateLimitResetCredits": {"availableCount": 1, "credits": [
                {"id": "earned-1", "status": "available", "expiresAt": expiry or self.expiry}
            ]},
        }

    def test_redeems_near_expiry_with_high_weekly_usage(self):
        self.assertEqual(router.should_redeem(self.limits(), self.now)["id"], "earned-1")

    def test_does_not_redeem_when_weekly_usage_low_or_window_renews_first(self):
        self.assertIsNone(router.should_redeem(self.limits(used=20), self.now))
        self.assertIsNone(router.should_redeem(
            self.limits(reset=int((self.now + dt.timedelta(hours=3)).timestamp())), self.now))

    def test_final_hour_uses_partly_consumed_window(self):
        result = self.limits(used=12, expiry=int((self.now + dt.timedelta(minutes=40)).timestamp()))
        self.assertIsNotNone(router.should_redeem(result, self.now))

    def test_weekly_renewal_can_rank_without_credit_details(self):
        result = self.limits()
        result["rateLimitResetCredits"] = {"availableCount": 1, "credits": None}
        self.assertIsNotNone(router.account_rank(result, self.now))
        self.assertIsNone(router.should_redeem(result, self.now))

    def test_earlier_usable_weekly_renewal_beats_earlier_credit_expiry(self):
        soon = self.limits(used=2, expiry=int((self.now + dt.timedelta(days=3)).timestamp()),
                           reset=int((self.now + dt.timedelta(days=1)).timestamp()))
        later = self.limits(used=10, expiry=self.expiry)
        self.assertLess(router.account_rank(soon, self.now), router.account_rank(later, self.now))

    def test_credit_expiry_breaks_same_weekly_renewal_tie(self):
        earlier = self.limits(used=20, expiry=int((self.now + dt.timedelta(days=1)).timestamp()))
        later = self.limits(used=5, expiry=int((self.now + dt.timedelta(days=2)).timestamp()))
        self.assertLess(router.account_rank(earlier, self.now), router.account_rank(later, self.now))

    def test_exhausted_weekly_quota_is_not_promoted(self):
        self.assertIsNone(router.account_rank(self.limits(used=100), self.now))

    def test_missing_weekly_and_credit_details_has_no_rank(self):
        result = {"rateLimitResetCredits": {"availableCount": 1, "credits": None}}
        self.assertIsNone(router.account_rank(result, self.now))

    def test_redemption_key_is_stable_for_retries(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            first = router.redemption_key(path, "account", "earned-1")
            self.assertEqual(first, router.redemption_key(path, "account", "earned-1"))
            self.assertNotEqual(first, router.redemption_key(path, "account", "earned-2"))
            router.clear_redemption_key(path, "account", "earned-1")
            self.assertNotEqual(first, router.redemption_key(path, "account", "earned-1"))

    def test_claude_date_only_offer_expires_after_its_calendar_day(self):
        self.assertTrue(router.claude_offer_active("2026-10-22", self.now))
        self.assertFalse(router.claude_offer_active("2026-10-15", self.now))
        self.assertFalse(router.claude_offer_active("invalid", self.now))

    def test_claude_uses_nearest_observed_weekly_renewal(self):
        def entry(name, days, status="active", priority=0):
            return {
                "name": name, "type": "claude", "status": status,
                "priority": priority, "quota": {
                    "observed_at": self.now.isoformat(), "signals": {
                        "Anthropic-Ratelimit-Unified-7d-Reset": str(
                            int((self.now + dt.timedelta(days=days)).timestamp())),
                        "Anthropic-Ratelimit-Unified-7d-Utilization": "0.2",
                    },
                },
            }
        auths = [entry("soon.json", 1), entry("later.json", 2),
                 entry("invalid.json", 0.5, status="error", priority=5)]
        with tempfile.TemporaryDirectory() as directory:
            args = SimpleNamespace(claude_offers_file=str(Path(directory) / "offers.json"),
                                   auth_dir=directory, dry_run=False)
            writes = []
            with patch.object(router, "management", side_effect=lambda *a: writes.append(a)):
                router.apply_claude_priorities(args, "unused", auths, self.now)
        self.assertEqual([(call[3]["name"], call[3]["priority"]) for call in writes],
                         [("soon.json", 2), ("later.json", 1), ("invalid.json", 0)])

    def test_stale_claude_observation_is_not_ranked(self):
        entry = {"quota": {"observed_at": (self.now - dt.timedelta(days=4)).isoformat(),
                           "signals": {
                               "Anthropic-Ratelimit-Unified-7d-Reset": str(self.weekly_reset),
                               "Anthropic-Ratelimit-Unified-7d-Utilization": "0.2",
                           }}}
        self.assertIsNone(router.claude_weekly_rank(entry, self.now))

    def test_partial_codex_read_preserves_all_priorities(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "management-key").write_text("unused")
            for name in ("first.json", "second.json"):
                (root / name).write_text('{"account_id":"test"}')
            auths = [{"name": name, "type": "codex", "status": "active",
                      "priority": priority}
                     for name, priority in (("first.json", 2), ("second.json", 1))]
            args = SimpleNamespace(auth_dir=directory,
                                   management_key=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   dry_run=False, priority_only=True)
            calls = []
            def management(method, path, key, payload=None):
                calls.append((method, path))
                return {"files": auths} if method == "GET" else {}
            with patch.object(router, "management", side_effect=management), \
                 patch.object(router, "app_server", side_effect=[(self.limits(), None), RuntimeError("offline")]):
                router.run_once(args, self.now)
            self.assertEqual(calls, [("GET", "/credentials")])


if __name__ == "__main__":
    unittest.main()
