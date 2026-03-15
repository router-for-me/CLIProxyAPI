"""Unit tests for shadow-claw gateway metrics module."""

import threading
import time
import unittest
from unittest.mock import MagicMock

import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from metrics import MetricsCollector, get_metrics_summary, install_metrics, _percentiles


class TestMetricsCollectorSingleton(unittest.TestCase):
    """MetricsCollector singleton behaviour and reset."""

    def setUp(self):
        MetricsCollector.reset()

    def tearDown(self):
        MetricsCollector.reset()

    def test_singleton_returns_same_instance(self):
        a = MetricsCollector()
        b = MetricsCollector()
        self.assertIs(a, b)

    def test_reset_creates_new_instance(self):
        a = MetricsCollector()
        MetricsCollector.reset()
        b = MetricsCollector()
        self.assertIsNot(a, b)

    def test_singleton_thread_safety(self):
        """Concurrent instantiation must always yield the same object."""
        instances = []
        barrier = threading.Barrier(4)

        def grab():
            barrier.wait()
            instances.append(MetricsCollector())

        threads = [threading.Thread(target=grab) for _ in range(4)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        self.assertTrue(all(i is instances[0] for i in instances))


class TestRecordChatEvents(unittest.TestCase):
    """record() correctly buckets chat success / fallback / failed events."""

    def setUp(self):
        MetricsCollector.reset()
        self.mc = MetricsCollector(window_seconds=3600)

    def tearDown(self):
        MetricsCollector.reset()

    def test_chat_success(self):
        self.mc.record("chat.request.success", {
            "route": "openai", "model": "gpt-4", "duration_ms": 120
        })
        snap = self.mc.snapshot()
        self.assertEqual(snap["chat"]["total"], 1)
        self.assertEqual(snap["chat"]["ok"], 1)
        self.assertEqual(snap["chat"]["fallback"], 0)
        self.assertEqual(snap["chat"]["failed"], 0)

    def test_chat_fallback(self):
        self.mc.record("chat.request.success", {
            "route": "openai", "model": "gpt-4",
            "duration_ms": 200, "fallback_used": True,
        })
        snap = self.mc.snapshot()
        self.assertEqual(snap["chat"]["fallback"], 1)
        self.assertEqual(snap["chat"]["ok"], 0)

    def test_chat_failed(self):
        self.mc.record("chat.request.error", {
            "route": "openai", "model": "gpt-4", "duration_ms": 50
        })
        snap = self.mc.snapshot()
        self.assertEqual(snap["chat"]["failed"], 1)
        self.assertEqual(snap["chat"]["ok"], 0)

    def test_multiple_chat_events(self):
        self.mc.record("chat.request.success", {"route": "a", "duration_ms": 10})
        self.mc.record("chat.request.success", {"route": "a", "duration_ms": 20})
        self.mc.record("chat.request.error", {"route": "b", "duration_ms": 30})
        snap = self.mc.snapshot()
        self.assertEqual(snap["chat"]["total"], 3)
        self.assertEqual(snap["chat"]["ok"], 2)
        self.assertEqual(snap["chat"]["failed"], 1)


class TestRecordToolEvents(unittest.TestCase):
    """record() correctly buckets tool completed / timeout events."""

    def setUp(self):
        MetricsCollector.reset()
        self.mc = MetricsCollector(window_seconds=3600)

    def tearDown(self):
        MetricsCollector.reset()

    def test_tool_completed_success(self):
        self.mc.record("tool.job.completed", {
            "ok": True, "tool_name": "bash", "duration_ms": 500
        })
        snap = self.mc.snapshot()
        self.assertEqual(snap["tools"]["total"], 1)
        self.assertEqual(snap["tools"]["ok"], 1)
        self.assertEqual(snap["tools"]["failed"], 0)
        self.assertEqual(snap["tools"]["timeout"], 0)

    def test_tool_completed_failure(self):
        self.mc.record("tool.job.completed", {
            "ok": False, "tool_name": "bash", "duration_ms": 100
        })
        snap = self.mc.snapshot()
        self.assertEqual(snap["tools"]["failed"], 1)

    def test_tool_timeout(self):
        self.mc.record("tool.exec.timeout", {"tool_name": "slow_tool"})
        snap = self.mc.snapshot()
        self.assertEqual(snap["tools"]["timeout"], 1)
        self.assertEqual(snap["tools"]["total"], 1)

    def test_tool_per_tool_breakdown(self):
        self.mc.record("tool.job.completed", {
            "ok": True, "tool_name": "bash", "duration_ms": 100
        })
        self.mc.record("tool.job.completed", {
            "ok": True, "tool_name": "python", "duration_ms": 200
        })
        snap = self.mc.snapshot()
        self.assertIn("bash", snap["tools"]["tools"])
        self.assertIn("python", snap["tools"]["tools"])
        self.assertEqual(snap["tools"]["tools"]["bash"]["runs"], 1)
        self.assertEqual(snap["tools"]["tools"]["python"]["runs"], 1)


class TestRecordAuthEvents(unittest.TestCase):
    """record() counts auth denial events."""

    def setUp(self):
        MetricsCollector.reset()
        self.mc = MetricsCollector(window_seconds=3600)

    def tearDown(self):
        MetricsCollector.reset()

    def test_auth_denied(self):
        self.mc.record("gateway.auth.denied", {"user_id": 42})
        self.mc.record("gateway.auth.denied", {"user_id": 99})
        snap = self.mc.snapshot()
        self.assertEqual(snap["auth"]["denials"], 2)

    def test_unknown_event_ignored(self):
        """Events with unrecognised names should not crash or count."""
        self.mc.record("some.random.event", {"foo": "bar"})
        snap = self.mc.snapshot()
        self.assertEqual(snap["chat"]["total"], 0)
        self.assertEqual(snap["tools"]["total"], 0)
        self.assertEqual(snap["auth"]["denials"], 0)


class TestSnapshot(unittest.TestCase):
    """snapshot() returns correct counters and structure."""

    def setUp(self):
        MetricsCollector.reset()
        self.mc = MetricsCollector(window_seconds=3600)

    def tearDown(self):
        MetricsCollector.reset()

    def test_empty_snapshot(self):
        snap = self.mc.snapshot()
        self.assertEqual(snap["window_seconds"], 3600)
        self.assertEqual(snap["chat"]["total"], 0)
        self.assertEqual(snap["tools"]["total"], 0)
        self.assertEqual(snap["auth"]["denials"], 0)

    def test_snapshot_has_expected_keys(self):
        snap = self.mc.snapshot()
        self.assertIn("window_seconds", snap)
        self.assertIn("chat", snap)
        self.assertIn("tools", snap)
        self.assertIn("auth", snap)

    def test_snapshot_route_latencies(self):
        for ms in [100, 200, 300]:
            self.mc.record("chat.request.success", {
                "route": "openai", "model": "gpt-4", "duration_ms": ms
            })
        snap = self.mc.snapshot()
        routes = snap["chat"]["routes"]
        self.assertIn("openai", routes)
        self.assertIn("p50", routes["openai"])


class TestPercentileCalculation(unittest.TestCase):
    """_percentiles helper returns correct p50/p95/p99."""

    def test_single_value(self):
        result = _percentiles([42.0])
        self.assertEqual(result["p50"], 42)
        self.assertNotIn("p95", result)

    def test_two_values(self):
        result = _percentiles([10.0, 20.0])
        self.assertIn("p50", result)
        self.assertIn("p95", result)
        self.assertIn("p99", result)

    def test_empty_list(self):
        result = _percentiles([])
        self.assertEqual(result, {})

    def test_many_values_p50_in_middle(self):
        values = list(range(1, 101))
        result = _percentiles([float(v) for v in values])
        # p50 should be near the median
        self.assertGreaterEqual(result["p50"], 40)
        self.assertLessEqual(result["p50"], 60)

    def test_p95_near_high_end(self):
        values = [float(v) for v in range(1, 101)]
        result = _percentiles(values)
        self.assertGreaterEqual(result["p95"], 90)


class TestGetMetricsSummary(unittest.TestCase):
    """get_metrics_summary() returns a formatted multi-line string."""

    def setUp(self):
        MetricsCollector.reset()
        self.mc = MetricsCollector(window_seconds=3600)

    def tearDown(self):
        MetricsCollector.reset()

    def test_summary_contains_header(self):
        summary = get_metrics_summary()
        self.assertIn("Shadow-Claw Metrics", summary)
        self.assertIn("1h", summary)

    def test_summary_empty_state(self):
        summary = get_metrics_summary()
        self.assertIn("Chat: 0 total", summary)
        self.assertIn("Tools: 0 total", summary)
        self.assertIn("Auth: 0 denials", summary)

    def test_summary_with_data(self):
        self.mc.record("chat.request.success", {
            "route": "openai", "model": "gpt-4", "duration_ms": 100
        })
        self.mc.record("chat.request.error", {
            "route": "openai", "model": "gpt-4", "duration_ms": 50
        })
        summary = get_metrics_summary()
        self.assertIn("Chat: 2 total", summary)
        self.assertIn("1 ok", summary)
        self.assertIn("1 failed", summary)

    def test_summary_window_minutes(self):
        MetricsCollector.reset()
        MetricsCollector(window_seconds=1800)
        summary = get_metrics_summary()
        self.assertIn("30m", summary)

    def test_summary_window_seconds(self):
        MetricsCollector.reset()
        MetricsCollector(window_seconds=45)
        summary = get_metrics_summary()
        self.assertIn("45s", summary)


class TestInstallMetrics(unittest.TestCase):
    """install_metrics wraps the original function and feeds the collector."""

    def setUp(self):
        MetricsCollector.reset()
        MetricsCollector(window_seconds=3600)

    def tearDown(self):
        MetricsCollector.reset()

    def test_original_is_called(self):
        original = MagicMock()
        wrapped = install_metrics(original)
        wrapped("chat.request.success", route="openai", model="gpt-4", duration_ms=10)
        original.assert_called_once_with(
            "chat.request.success", route="openai", model="gpt-4", duration_ms=10
        )

    def test_collector_receives_event(self):
        original = MagicMock()
        wrapped = install_metrics(original)
        wrapped("chat.request.success", route="r", model="m", duration_ms=55)
        snap = MetricsCollector().snapshot()
        self.assertEqual(snap["chat"]["total"], 1)

    def test_wrapped_preserves_multiple_calls(self):
        original = MagicMock()
        wrapped = install_metrics(original)
        wrapped("chat.request.success", route="r", duration_ms=10)
        wrapped("chat.request.error", route="r", duration_ms=20)
        self.assertEqual(original.call_count, 2)
        snap = MetricsCollector().snapshot()
        self.assertEqual(snap["chat"]["total"], 2)


class TestRollingWindowEviction(unittest.TestCase):
    """Rolling window eviction drops events beyond max_events."""

    def setUp(self):
        MetricsCollector.reset()
        self.mc = MetricsCollector(window_seconds=3600, max_events=5)

    def tearDown(self):
        MetricsCollector.reset()

    def test_eviction_caps_bucket_size(self):
        for i in range(10):
            self.mc.record("chat.request.success", {
                "route": "r", "model": "m", "duration_ms": i
            })
        # Internal bucket should be capped at 5
        self.assertLessEqual(len(self.mc._chat_events), 5)

    def test_eviction_keeps_newest(self):
        for i in range(10):
            self.mc.record("chat.request.success", {
                "route": "r", "model": "m", "duration_ms": i * 100
            })
        # The oldest events should have been evicted; newest remain
        durations = [e["duration_ms"] for _, e in self.mc._chat_events]
        # Last 5 inserted had durations 500..900
        self.assertEqual(durations, [500, 600, 700, 800, 900])

    def test_eviction_applies_to_tools(self):
        for i in range(8):
            self.mc.record("tool.job.completed", {
                "ok": True, "tool_name": "t", "duration_ms": i
            })
        self.assertLessEqual(len(self.mc._tool_events), 5)

    def test_eviction_applies_to_auth(self):
        for i in range(8):
            self.mc.record("gateway.auth.denied", {"user_id": i})
        self.assertLessEqual(len(self.mc._auth_events), 5)


if __name__ == "__main__":
    unittest.main()
