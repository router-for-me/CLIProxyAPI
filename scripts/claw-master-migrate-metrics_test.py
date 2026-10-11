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
