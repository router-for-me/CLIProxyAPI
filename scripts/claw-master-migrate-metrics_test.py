"""Run: python3 -m unittest scripts/claw-master-migrate-metrics_test.py (boto3 not needed)."""

import importlib.util
import pathlib
import sys
import types
import unittest

sys.modules.setdefault("boto3", types.ModuleType("boto3"))
_path = pathlib.Path(__file__).with_name("claw-master-migrate-metrics.py")
_spec = importlib.util.spec_from_file_location("migrate", _path)
migrate = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(migrate)


def dims(**kv):
    return [{"Name": k.replace("_", "."), "Value": v} for k, v in kv.items()]


class TargetOf(unittest.TestCase):
    def test_a_claude_measurement_is_renamed_and_tagged(self):
        got = migrate.target_of(
            {
                "MetricName": "claude_master.inference.tokens",
                "Dimensions": dims(profile="claude-a", service_name="claude-master", service_instance_id="claude-master-green", type="output"),
            }
        )
        self.assertEqual(got["MetricName"], "claw_master.inference.tokens")
        self.assertEqual(
            {d["Name"]: d["Value"] for d in got["Dimensions"]},
            {"profile": "claude-a", "provider": "claude", "service.name": "claw-master", "service.instance.id": "claw-master-green", "type": "output"},
        )

    def test_the_pools_own_measurements_get_no_provider(self):
        for name in ("claude_master.proxy.connections", "claude_master.process.heap_bytes"):
            got = migrate.target_of({"MetricName": name, "Dimensions": dims(service_name="claude-master")})
            self.assertNotIn("provider", {d["Name"] for d in got["Dimensions"]}, name)
            self.assertTrue(got["MetricName"].startswith("claw_master."), name)

    def test_host_metrics_keep_their_names(self):
        got = migrate.target_of({"MetricName": "mem_used_percent", "Dimensions": dims(host="example-host")})
        self.assertEqual(got, {"MetricName": "mem_used_percent", "Dimensions": dims(host="example-host")})


if __name__ == "__main__":
    unittest.main()


import datetime as dt
from unittest import mock


class FakeCloudWatch:
    def __init__(self, fail_put_call=None, statuses=None):
        self.puts = []
        self.put_calls = 0
        self.fail_put_call = fail_put_call
        self.statuses = list(statuses or [])

    def put_metric_data(self, Namespace, MetricData):
        self.put_calls += 1
        if self.put_calls == self.fail_put_call:
            raise RuntimeError("interrupted")
        self.puts.extend(MetricData)

    def get_metric_data(self, **kwargs):
        status = self.statuses.pop(0) if self.statuses else "Complete"
        t = dt.datetime(2026, 10, 9, tzinfo=dt.timezone.utc)
        return {
            "MetricDataResults": [
                {"Id": q["Id"], "StatusCode": status, "Timestamps": [t], "Values": [1.0]}
                for q in kwargs["MetricDataQueries"]
            ]
        }


def minutes(n):
    t0 = dt.datetime(2026, 10, 9, tzinfo=dt.timezone.utc)
    return {
        t0 + dt.timedelta(minutes=i): {"SampleCount": 1.0, "Sum": float(i), "Minimum": float(i), "Maximum": float(i)}
        for i in range(n)
    }


class Writing(unittest.TestCase):
    TARGET = {"MetricName": "claw_master.x", "Dimensions": []}

    def test_reads_only_what_cloudwatch_still_accepts(self):
        self.assertLess(migrate.LOOKBACK, dt.timedelta(days=14))

    def test_a_resumed_stream_writes_each_minute_once(self):
        data = minutes(1201)
        cw = FakeCloudWatch(fail_put_call=2)
        last = []
        with self.assertRaises(RuntimeError):
            migrate.put_minutes(cw, "ClawMaster", self.TARGET, data, None, None, last.append)
        cw.fail_put_call = None
        migrate.put_minutes(cw, "ClawMaster", self.TARGET, data, None, last[-1], last.append)
        stamps = [d["Timestamp"] for d in cw.puts]
        self.assertEqual(len(stamps), len(set(stamps)))
        self.assertEqual(sorted(stamps), sorted(data))

    def test_keeps_the_streams_unit(self):
        cw = FakeCloudWatch()
        migrate.put_minutes(cw, "ClawMaster", self.TARGET, minutes(2), "Percent", None, lambda t: None)
        self.assertEqual({d.get("Unit") for d in cw.puts}, {"Percent"})

    def test_a_failed_read_is_retried_then_fatal(self):
        metric = {"MetricName": "claude_master.x", "Dimensions": []}
        start, end = dt.datetime(2026, 10, 8, tzinfo=dt.timezone.utc), dt.datetime(2026, 10, 10, tzinfo=dt.timezone.utc)
        with mock.patch.object(migrate.time, "sleep"):
            got = migrate.read_minutes(FakeCloudWatch(statuses=["InternalError", "Complete"]), "ClaudeMaster", metric, start, end)
            self.assertEqual(len(got), 1)
            with self.assertRaises(RuntimeError):
                migrate.read_minutes(FakeCloudWatch(statuses=["InternalError"] * 9), "ClaudeMaster", metric, start, end)
