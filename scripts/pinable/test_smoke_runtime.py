#!/usr/bin/env python3
"""Verify the smoke HTTP helper without launching a runtime or making requests."""
import io
import json
import unittest
import tempfile
from pathlib import Path
from unittest.mock import Mock
from unittest.mock import patch
from subprocess import CompletedProcess
from urllib.error import HTTPError

import smoke_runtime


class SmokeRequestTests(unittest.TestCase):
    def setUp(self):
        patcher = patch("smoke_runtime.urllib.request.build_opener")
        self.opener = patcher.start()
        self.addCleanup(patcher.stop)
        self.client = self.opener.return_value
        response = self.client.open.return_value.__enter__.return_value
        response.status = 200
        response.read.return_value = b'{"ok":true}'

    def request_sent(self):
        args, kwargs = self.client.open.call_args
        self.assertEqual(kwargs, {"timeout": 2})
        self.assertEqual(self.opener.call_args.args[0].proxies, {})
        return args[0]

    def test_get_has_no_body_or_default_authentication(self):
        self.assertEqual(smoke_runtime.request(12345, "/v1/models"), (200, b'{"ok":true}'))
        request = self.request_sent()
        self.assertEqual(request.full_url, "http://127.0.0.1:12345/v1/models")
        self.assertEqual(request.get_method(), "GET")
        self.assertIsNone(request.data)
        self.assertIsNone(request.get_header("Authorization"))
        self.assertIsNone(request.get_header("Content-type"))

    def test_post_preserves_empty_shutdown_body(self):
        smoke_runtime.request(12345, "/v0/management/runtime-shutdown", "test-key", "POST")
        request = self.request_sent()
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.data, b"")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-key")
        self.assertIsNone(request.get_header("Content-type"))

    def test_put_sends_exact_v8_json_body_and_content_type(self):
        payload = b'["rotated-test-key"]'
        smoke_runtime.request(12345, "/v8/management/config/access/api-keys", "test-key", "PUT", payload)
        request = self.request_sent()
        self.assertEqual(request.get_method(), "PUT")
        self.assertEqual(request.data, payload)
        self.assertEqual(request.get_header("Content-type"), "application/json")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-key")

    def test_http_errors_remain_available_to_negative_auth_checks(self):
        self.client.open.side_effect = HTTPError("http://127.0.0.1/", 401, "Unauthorized", {}, io.BytesIO(b"denied"))
        self.assertEqual(smoke_runtime.request(12345, "/v8/management/config", "wrong"), (401, b"denied"))


class DiscoveryProbeTests(unittest.TestCase):
    @patch("smoke_runtime.subprocess.run")
    def test_success_parses_clean_json(self, run):
        run.return_value = CompletedProcess(["runtime"], 0, b'{"gateways":[]}', b'')
        self.assertEqual(smoke_runtime.discovery_probe(["runtime"], ".", {}), {"gateways": []})
        run.assert_called_once_with(["runtime"], cwd=".", env={}, timeout=15, capture_output=True)

    @patch("smoke_runtime.subprocess.run")
    def test_failure_reports_output_without_accepting_failed_probe(self, run):
        run.return_value = CompletedProcess(["runtime"], 1, b'{"error":"network unavailable"}', b'diagnostic')
        with self.assertRaisesRegex(ValueError, "network unavailable.*diagnostic"):
            smoke_runtime.discovery_probe(["runtime"], ".", {})

    @patch("smoke_runtime.subprocess.run")
    def test_explicit_multicast_limitation_is_reported_not_silently_passed(self, run):
        payload = {"error": "discovery: send browse queries: interface en0: write udp 0.0.0.0:5353->224.0.0.251:5353: sendmsg: no route to host", "gateways": []}
        run.return_value = CompletedProcess(["runtime"], 1, json.dumps(payload).encode(), b'')
        with self.assertRaises(ValueError):
            smoke_runtime.discovery_probe(["runtime"], ".", {})
        self.assertEqual(smoke_runtime.discovery_probe(["runtime"], ".", {}, True), payload)

    @patch("smoke_runtime.subprocess.run")
    def test_multicast_allowance_rejects_other_errors_and_wrong_schema(self, run):
        for payload in (
            {"error": "discovery: open mDNS sockets: not implemented", "gateways": []},
            {"error": "discovery: send browse queries: interface en0: write udp x: sendmsg: permission denied", "gateways": []},
            {"error": "discovery: send browse queries: ", "gateways": []},
            {"error": "discovery: send browse queries: interface en0: write udp x: sendmsg: no route to host", "gateways": ["unexpected"]},
        ):
            run.return_value = CompletedProcess(["runtime"], 1, json.dumps(payload).encode(), b'')
            with self.assertRaises(ValueError):
                smoke_runtime.discovery_probe(["runtime"], ".", {}, True)

    @patch("smoke_runtime.subprocess.run")
    def test_startup_banner_is_rejected(self, run):
        run.return_value = CompletedProcess(["runtime"], 0, b'banner\n{"gateways":[]}', b'')
        with self.assertRaises(ValueError):
            smoke_runtime.discovery_probe(["runtime"], ".", {})



class NativePluginSmokeTests(unittest.TestCase):
    def listing(self):
        return {"plugins_enabled": True, "plugins": [{
            "id": "management-api-c", "registered": True, "effective_enabled": True,
            "metadata": {"name": "example-management-api-c"},
        }]}

    def test_header_and_actual_registered_identity_are_both_required(self):
        valid = self.listing()
        smoke_runtime.verify_plugin_response(200, json.dumps(valid).encode(), {"X-CPA-SUPPORT-PLUGIN": "1"})
        for header in ("0", "", None):
            with self.assertRaises(ValueError):
                smoke_runtime.verify_plugin_response(200, json.dumps(valid).encode(), {"X-CPA-SUPPORT-PLUGIN": header})
        for change in ({"id": "other"}, {"registered": False}, {"effective_enabled": False},
                       {"metadata": {"name": "wrong"}}):
            invalid = self.listing()
            invalid["plugins"][0].update(change)
            with self.assertRaises(ValueError):
                smoke_runtime.verify_plugin_response(200, json.dumps(invalid).encode(), {"X-CPA-SUPPORT-PLUGIN": "1"})

    @patch("smoke_runtime.Path.is_file", return_value=True)
    @patch("smoke_runtime.subprocess.check_output", return_value="arm64\n")
    @patch("smoke_runtime.subprocess.run")
    def test_build_uses_fixed_repository_demo_and_matching_architecture(self, run, output, exists):
        with tempfile.TemporaryDirectory() as tmp:
            library = smoke_runtime.build_darwin_plugin(Path(tmp), "arm64", {})
            command = run.call_args_list[0].args[0]
            self.assertEqual(command[:5], ["/usr/bin/xcrun", "clang", "-dynamiclib", "-arch", "arm64"])
            self.assertTrue(command[-1].endswith("/examples/plugin/management-api/c/src/plugin.c"))
            self.assertEqual(library.name, "management-api-c.dylib")
            self.assertEqual(library.parent.parts[-2:], ("darwin", "arm64"))
            self.assertEqual(len(run.call_args_list), 3)
            self.assertEqual(output.call_args.args[0], ["/usr/bin/lipo", "-archs", str(library)])

    @patch("smoke_runtime.Path.is_file", return_value=True)
    @patch("smoke_runtime.subprocess.check_output", return_value="x86_64\n")
    @patch("smoke_runtime.subprocess.run")
    def test_wrong_architecture_cannot_be_signed_or_reported_success(self, run, output, exists):
        with tempfile.TemporaryDirectory() as tmp, self.assertRaisesRegex(ValueError, "architecture mismatch"):
            smoke_runtime.build_darwin_plugin(Path(tmp), "arm64", {})
        self.assertEqual(run.call_count, 1)

    @patch("smoke_runtime.Path.is_file", return_value=False)
    @patch("smoke_runtime.subprocess.run")
    def test_missing_demo_fails_without_install_or_network_fallback(self, run, exists):
        with tempfile.TemporaryDirectory() as tmp, self.assertRaisesRegex(ValueError, "demo is missing"):
            smoke_runtime.build_darwin_plugin(Path(tmp), "amd64", {})
        run.assert_not_called()

    @patch("smoke_runtime.build_darwin_plugin")
    @patch("smoke_runtime.stop")
    @patch("smoke_runtime.wait_until")
    @patch("smoke_runtime.subprocess.Popen")
    @patch("smoke_runtime.request")
    def test_loopback_fixture_executes_plugin_and_cleans_up_on_failure(self, request, popen, wait, stop, build):
        for resource_status in (200, 500):
            request.reset_mock()
            def reply(port, path, key="", method="GET", **kwargs):
                if kwargs.get("include_headers"):
                    return 200, json.dumps(self.listing()).encode(), {"X-CPA-SUPPORT-PLUGIN": "1"}
                if path == "/v8/management/plugins":
                    return 401, b"denied"
                if path == "/v0/resource/plugins/management-api-c/status":
                    return resource_status, b"<main>Management API resource</main>"
                return 202, b""
            request.side_effect = reply
            popen.return_value.returncode = 0
            with tempfile.TemporaryDirectory() as tmp:
                if resource_status == 200:
                    smoke_runtime.smoke_darwin_plugin(Path("/trusted/released-binary"), Path(tmp), "arm64", {})
                else:
                    with self.assertRaisesRegex(ValueError, "callback"):
                        smoke_runtime.smoke_darwin_plugin(Path("/trusted/released-binary"), Path(tmp), "arm64", {})
                cfg = (Path(tmp) / "native-plugin/config.yaml").read_text()
                self.assertIn('host: "127.0.0.1"', cfg)
                self.assertIn('allow-remote: false', cfg)
                self.assertIn('management-api-c:', cfg)
                self.assertIn('disable-auto-update-panel: true', cfg)
                self.assertEqual(popen.call_args.args[0][0], "/trusted/released-binary")
                self.assertIn("--local-model", popen.call_args.args[0])
            stop.assert_called_with(popen.return_value)

if __name__ == "__main__":
    unittest.main()
