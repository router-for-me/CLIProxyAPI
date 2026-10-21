import datetime as dt
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, patch


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
            "ordinaryUsageAllowed": True,
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

    def test_one_second_renewal_jitter_allows_credit_expiry_tie_break(self):
        reset = int((self.now + dt.timedelta(days=5, seconds=31)).timestamp())
        with_credit = self.limits(used=0, reset=reset + 1)
        no_credit = self.limits(used=0, reset=reset)
        no_credit["rateLimitResetCredits"] = {"availableCount": 0, "credits": []}
        self.assertLess(router.account_rank(with_credit, self.now),
                        router.account_rank(no_credit, self.now))

    def test_exhausted_weekly_quota_is_not_promoted(self):
        self.assertIsNone(router.account_rank(self.limits(used=100), self.now))

    def test_ordinary_usage_must_be_explicitly_allowed(self):
        for allowed in (False, None):
            result = self.limits(used=10)
            result["ordinaryUsageAllowed"] = allowed
            self.assertIsNone(router.account_rank(result, self.now))

    def test_siwc_flag_matches_codex_boolean_forms(self):
        for enabled in (True, "true", "1", "T"):
            self.assertTrue(router.uses_siwc({"use_siwc": enabled}))
        for disabled in (False, "false", None):
            self.assertFalse(router.uses_siwc({"use_siwc": disabled}))

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

    def test_confirmed_redemption_does_not_wait_for_a_refresh(self):
        proc = MagicMock()
        with patch.object(router.subprocess, "Popen", return_value=proc), \
             patch.object(router.threading, "Thread"), \
             patch.object(router.tempfile, "mkdtemp", return_value="temporary-home"), \
             patch.object(router.shutil, "rmtree"), \
             patch.object(router, "rpc", side_effect=[{}, {}, self.limits(), {"outcome": "reset"}]) as rpc:
            _, outcome = router.app_server({"access_token": "test", "account_id": "test"},
                                           consume={"creditId": "earned-1"})
        self.assertEqual(outcome, "reset")
        self.assertEqual([call.args[2] for call in rpc.call_args_list],
                         ["initialize", "account/login/start", "account/rateLimits/read",
                          "account/rateLimitResetCredit/consume"])

    def test_cooldown_clear_is_retried_after_transient_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            state_file = Path(directory) / "state.json"
            router.queue_cooldown_reset(state_file, "index-1")
            with patch.object(router, "management", side_effect=RuntimeError("offline")):
                router.retry_cooldown_resets(state_file, "unused")
            self.assertEqual(router.load_state(state_file)["pending_cooldowns"], ["index-1"])
            with patch.object(router, "management", return_value={"status": "ok"}) as send:
                router.retry_cooldown_resets(state_file, "unused")
            self.assertNotIn("pending_cooldowns", router.load_state(state_file))
            self.assertEqual(send.call_args.args[:3],
                             ("POST", "/routing/cooldown/reset", "unused"))

    def test_management_key_can_come_from_environment(self):
        with patch.dict(router.os.environ, {"CLIPROXY_MANAGEMENT_KEY": "test-key"}):
            self.assertEqual(router.read_management_key(SimpleNamespace()), "test-key")

    def test_claude_date_only_offer_expires_after_its_calendar_day(self):
        self.assertTrue(router.claude_offer_active("2026-10-22", self.now))
        self.assertFalse(router.claude_offer_active("2026-10-15", self.now))
        self.assertFalse(router.claude_offer_active("invalid", self.now))

    def test_claude_reset_timestamp_formats(self):
        for raw in (str(self.weekly_reset), "2026-10-21T00:00:00Z",
                    "Wed, 21 Oct 2026 00:00:00 GMT"):
            self.assertEqual(router.parse_reset_timestamp(raw), self.weekly_reset)

    def test_ambiguous_claude_offer_requires_credential_name(self):
        auths = [{"id": identity, "name": name, "type": "claude", "email": "shared@example.com",
                  "status": "active", "priority": 0}
                 for identity, name in (("one", "renamed.json"), ("two", "nested.json"))]
        with tempfile.TemporaryDirectory() as directory:
            offer_file = Path(directory) / "offers.json"
            args = SimpleNamespace(claude_offers_file=str(offer_file), dry_run=False)
            offer_file.write_text(json.dumps({"offers": [
                {"email": "shared@example.com", "expires_on": "2026-10-22"}]}))
            with patch.object(router, "management") as send:
                router.apply_claude_priorities(args, "unused", auths, self.now)
                send.assert_not_called()
            offer_file.write_text(json.dumps({"offers": [
                {"name": "renamed.json", "expires_on": "2026-10-22"}]}))
            with patch.object(router, "management") as send:
                router.apply_claude_priorities(args, "unused", auths, self.now)
            self.assertEqual(send.call_args.args[3], {"name": "one", "priority": 1})

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
                      "priority": priority, "path": str(root / name)}
                     for name, priority in (("first.json", 2), ("second.json", 1))]
            args = SimpleNamespace(auth_dir=directory,
                                   management_key=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   state_file=str(root / "state.json"),
                                   dry_run=False, priority_only=True)
            calls = []
            def management(method, path, key, payload=None):
                calls.append((method, path))
                return {"files": auths} if method == "GET" else {}
            with patch.object(router, "management", side_effect=management), \
                 patch.object(router, "app_server", side_effect=[(self.limits(), None), RuntimeError("offline")]):
                router.run_once(args, self.now)
            self.assertEqual(calls, [("GET", "/credentials")])

    def test_incomplete_codex_quota_preserves_priority(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credential = root / "oauth.json"
            credential.write_text('{"account_id":"test"}')
            (root / "management-key").write_text("unused")
            entry = {"id": "unique-id", "name": "oauth.json", "path": str(credential),
                     "type": "codex", "status": "active", "priority": 3}
            args = SimpleNamespace(management_key_file=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   state_file=str(root / "state.json"),
                                   dry_run=False, priority_only=True)
            result = {"ordinaryUsageAllowed": True,
                      "rateLimitResetCredits": {"availableCount": 1, "credits": None}}
            calls = []
            def management(method, path, key, payload=None):
                calls.append(method)
                return {"files": [entry]}
            with patch.object(router, "management", side_effect=management), \
                 patch.object(router, "app_server", return_value=(result, None)):
                router.run_once(args, self.now)
            self.assertEqual(calls, ["GET"])

    def test_nonstandard_codex_path_uses_management_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credential = root / "nested" / "oauth.json"
            credential.parent.mkdir()
            credential.write_text('{"account_id":"test"}')
            (root / "management-key").write_text("unused")
            entry = {"id": "unique-id", "name": "oauth.json", "path": str(credential),
                     "type": "codex", "status": "active", "priority": 0}
            args = SimpleNamespace(management_key_file=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   state_file=str(root / "state.json"),
                                   dry_run=False, priority_only=True)
            def management(method, path, key, payload=None):
                if method == "GET":
                    return {"files": [entry]}
                writes.append(payload)
                return {"status": "ok"}
            writes = []
            with patch.object(router, "management", side_effect=management), \
                 patch.object(router, "app_server", return_value=(self.limits(used=10), None)):
                router.run_once(args, self.now)
            self.assertEqual(writes, [{"name": "unique-id", "priority": 1}])

    def test_siwc_account_is_never_read_or_reprioritized(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credential = root / "oauth.json"
            credential.write_text('{"use_siwc":true,"access_token":"outer"}')
            (root / "management-key").write_text("unused")
            entry = {"id": "siwc-id", "name": "oauth.json", "path": str(credential),
                     "type": "codex", "status": "active", "priority": 7}
            args = SimpleNamespace(management_key_file=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   state_file=str(root / "state.json"),
                                   dry_run=False, priority_only=False)
            with patch.object(router, "management", return_value={"files": [entry]}) as send, \
                 patch.object(router, "app_server") as read:
                router.run_once(args, self.now)
            read.assert_not_called()
            self.assertEqual([call.args[0] for call in send.call_args_list], ["GET"])

    def test_redemption_failure_does_not_skip_later_account(self):
        now = dt.datetime.now(dt.timezone.utc)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "management-key").write_text("unused")
            entries = []
            for identity in ("first", "second"):
                credential = root / (identity + ".json")
                credential.write_text(json.dumps({"account_id": identity}))
                entries.append({"id": identity, "name": credential.name, "path": str(credential),
                                "type": "codex", "status": "active", "priority": 0,
                                "auth_index": identity + "-index"})
            result = self.limits(used=95,
                                 expiry=int((now + dt.timedelta(hours=12)).timestamp()),
                                 reset=int((now + dt.timedelta(days=2)).timestamp()))
            args = SimpleNamespace(management_key_file=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   state_file=str(root / "state.json"),
                                   dry_run=False, priority_only=False)
            def management(method, path, key, payload=None):
                return {"files": entries} if method == "GET" else {"status": "ok"}
            with patch.object(router, "management", side_effect=management), \
                 patch.object(router, "app_server", side_effect=[
                     (result, None), (result, None), (result, None),
                     RuntimeError("first consume failed"), (result, None), (result, "reset")]) as read:
                router.run_once(args, now)
            self.assertEqual(read.call_count, 6)
            self.assertIn("first:earned-1", router.load_state(args.state_file))
            self.assertNotIn("pending_cooldowns", router.load_state(args.state_file))

    def test_cooldowned_codex_account_is_still_checked_for_redemption(self):
        now = dt.datetime.now(dt.timezone.utc)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credential = root / "oauth.json"
            credential.write_text('{"account_id":"test"}')
            (root / "management-key").write_text("unused")
            entry = {"id": "unique-id", "name": "oauth.json", "path": str(credential),
                     "type": "codex", "status": "error", "unavailable": True,
                     "next_retry_after": (now + dt.timedelta(hours=2)).isoformat(),
                     "cooldowns": [{"reason": "quota"}],
                     "auth_index": "index-1", "priority": 5}
            result = self.limits(used=95,
                                 expiry=int((now + dt.timedelta(hours=12)).timestamp()),
                                 reset=int((now + dt.timedelta(days=2)).timestamp()))
            args = SimpleNamespace(management_key_file=str(root / "management-key"),
                                   claude_offers_file=str(root / "offers.json"),
                                   state_file=str(root / "state.json"),
                                   dry_run=False, priority_only=False)
            methods = []
            def management(method, path, key, payload=None):
                methods.append(method)
                return {"files": [entry]} if method == "GET" else {"status": "ok"}
            with patch.object(router, "management", side_effect=management), \
                 patch.object(router, "app_server", side_effect=[
                     (result, None), (result, None), (result, "reset")]) as read:
                router.run_once(args, now)
            self.assertEqual(read.call_count, 3)
            self.assertIn("consume", read.call_args.kwargs)
            self.assertNotIn("pending_cooldowns", router.load_state(args.state_file))
            self.assertEqual(methods, ["GET", "POST"])


if __name__ == "__main__":
    unittest.main()
