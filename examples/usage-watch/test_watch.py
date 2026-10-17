import importlib.util
import unittest
from unittest import mock
from datetime import datetime, timezone
from pathlib import Path


spec = importlib.util.spec_from_file_location("watch", Path(__file__).with_name("watch.py"))
watch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(watch)


class WatchTests(unittest.TestCase):
    def test_codex_account_and_quota(self):
        record = {"provider": "codex", "model": "gpt-6.1-sol", "auth_index": "a1", "source": "fallback", "tokens": {"total_tokens": 30}}
        credentials = [{"provider": "codex", "auth_index": "a1", "email": "user@example.com", "quota": {"signals": {"X-Codex-Primary-Used-Percent": "35", "X-Codex-Secondary-Used-Percent": "10"}}}]
        line = watch.format_record(record, credentials)
        self.assertIn("user@example.com", line)
        self.assertIn("primary 65% left", line)
        self.assertIn("secondary 90% left", line)
        self.assertIn("30 tokens", line)

    def test_claude_quota_and_missing_quota(self):
        credential = {"provider": "claude", "quota": {"observed_at": "2026-10-16T12:00:00Z", "signals": {"Anthropic-Ratelimit-Unified-5h-Utilization": "0.73"}}}
        summary = watch.quota_summary(credential, "claude-sonnet-4-6", datetime(2026, 10, 16, 12, 1, tzinfo=timezone.utc))
        self.assertEqual(summary, "5h 27% left, observed 60s ago")
        self.assertEqual(watch.quota_summary({"provider": "claude"}, "claude-sonnet-4-6"), "quota unavailable")

    def test_account_change_is_per_provider(self):
        previous = {}
        credentials = [
            {"provider": "codex", "auth_index": "a1", "email": "first@example.com"},
            {"provider": "codex", "auth_index": "a2", "email": "second@example.com"},
        ]
        self.assertIsNone(watch.account_change({"provider": "codex", "auth_index": "a1"}, credentials, previous))
        self.assertIsNone(watch.account_change({"provider": "claude", "source": "claude@example.com"}, credentials, previous))
        self.assertEqual(watch.account_change({"provider": "codex", "auth_index": "a2"}, credentials, previous), "codex account changed: first@example.com -> second@example.com")

    def test_api_key_source_is_never_printed(self):
        secret = "sk-secret-upstream-key"
        record = {"provider": "claude", "model": "claude-sonnet-4-6", "source": secret, "auth_index": "opaque-123"}
        credentials = [{"provider": "claude", "auth_index": "opaque-123"}]
        previous = {}
        self.assertNotIn(secret, watch.format_record(record, credentials))
        self.assertNotIn(secret, str(watch.account_change(record, credentials, previous)))
        self.assertIn("credential opaque-123", watch.format_record(record, credentials))

    def test_credential_failure_does_not_pop_usage_queue(self):
        with mock.patch.object(watch, "get_json", side_effect=TimeoutError("credential lookup")) as get_json:
            with mock.patch.dict(watch.os.environ, {"CLIPROXY_MANAGEMENT_KEY": "test-key"}):
                self.assertEqual(watch.main(["--once"]), 1)
        self.assertEqual(get_json.call_count, 1)
        self.assertEqual(get_json.call_args.args[2], "/credentials")


if __name__ == "__main__":
    unittest.main()
