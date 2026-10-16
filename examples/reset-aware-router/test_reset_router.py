import datetime as dt
import importlib.util
import tempfile
import unittest
from pathlib import Path


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

    def test_no_expiry_details_means_no_priority_or_spend(self):
        result = self.limits()
        result["rateLimitResetCredits"] = {"availableCount": 1, "credits": None}
        self.assertIsNone(router.account_rank(result, self.now))
        self.assertIsNone(router.should_redeem(result, self.now))

    def test_redemption_key_is_stable_for_retries(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            first = router.redemption_key(path, "account", "earned-1")
            self.assertEqual(first, router.redemption_key(path, "account", "earned-1"))
            self.assertNotEqual(first, router.redemption_key(path, "account", "earned-2"))


if __name__ == "__main__":
    unittest.main()
