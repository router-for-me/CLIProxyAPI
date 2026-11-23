#!/usr/bin/env python3
"""Verify the smoke HTTP helper without launching a runtime or making requests."""
import io
import json
import unittest
import tempfile
import struct
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
            library = smoke_runtime.build_native_plugin(Path(tmp), "darwin", "arm64", {})
            command = run.call_args_list[0].args[0]
            self.assertEqual(command[:5], ["/usr/bin/xcrun", "clang", "-dynamiclib", "-arch", "arm64"])
            expected_source = Path(smoke_runtime.__file__).resolve().parents[2] / "examples/plugin/management-api/c/src/plugin.c"
            self.assertEqual(Path(command[-1]), expected_source)
            self.assertEqual(library.name, "management-api-c.dylib")
            self.assertEqual(library.parent.parts[-2:], ("darwin", "arm64"))
            self.assertEqual(len(run.call_args_list), 3)
            self.assertEqual(output.call_args.args[0], ["/usr/bin/lipo", "-archs", str(library)])

    @patch("smoke_runtime.Path.is_file", return_value=True)
    @patch("smoke_runtime.subprocess.check_output", return_value="x86_64\n")
    @patch("smoke_runtime.subprocess.run")
    def test_wrong_architecture_cannot_be_signed_or_reported_success(self, run, output, exists):
        with tempfile.TemporaryDirectory() as tmp, self.assertRaisesRegex(ValueError, "architecture mismatch"):
            smoke_runtime.build_native_plugin(Path(tmp), "darwin", "arm64", {})
        self.assertEqual(run.call_count, 1)

    @patch("smoke_runtime.Path.is_file", return_value=False)
    @patch("smoke_runtime.subprocess.run")
    def test_missing_demo_fails_without_install_or_network_fallback(self, run, exists):
        with tempfile.TemporaryDirectory() as tmp, self.assertRaisesRegex(ValueError, "demo is missing"):
            smoke_runtime.build_native_plugin(Path(tmp), "darwin", "amd64", {})
        run.assert_not_called()

    @patch("smoke_runtime.build_native_plugin")
    @patch("smoke_runtime.stop")
    @patch("smoke_runtime.wait_until")
    @patch("smoke_runtime.subprocess.Popen")
    @patch("smoke_runtime.request")
    def test_loopback_fixture_executes_plugin_and_cleans_up_on_failure(self, request, popen, wait, stop, build):
        for target, resource_status in ((t, s) for t in ("darwin", "linux", "windows") for s in (200, 500)):
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
                binary = Path(tmp) / "trusted" / "released-binary"
                if resource_status == 200:
                    smoke_runtime.smoke_native_plugin(binary, Path(tmp), target, "arm64", {})
                else:
                    with self.assertRaisesRegex(ValueError, "callback"):
                        smoke_runtime.smoke_native_plugin(binary, Path(tmp), target, "arm64", {})
                build.assert_called_with(Path(tmp) / "native-plugin", target, "arm64", {})
                cfg = (Path(tmp) / "native-plugin/config.yaml").read_text()
                self.assertIn('host: "127.0.0.1"', cfg)
                self.assertIn('allow-remote: false', cfg)
                self.assertIn('management-api-c:', cfg)
                self.assertIn('disable-auto-update-panel: true', cfg)
                self.assertEqual(popen.call_args.args[0][0], str(binary))
                self.assertIn("--local-model", popen.call_args.args[0])
            stop.assert_called_with(popen.return_value)


class AllPlatformPluginTests(unittest.TestCase):
    def pe(self, arch="amd64", dll=True):
        data = bytearray(128)
        data[:2] = b"MZ"
        struct.pack_into("<I", data, 60, 64)
        data[64:68] = b"PE\0\0"
        struct.pack_into("<H", data, 68, {"amd64": 0x8664, "arm64": 0xaa64}[arch])
        struct.pack_into("<H", data, 86, 0x2000 if dll else 0)
        struct.pack_into("<H", data, 88, 0x20b)
        return bytes(data)

    def elf(self, arch="amd64", shared=True):
        data = bytearray(120)
        data[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<HH", data, 16, 3 if shared else 2, {"amd64": 62, "arm64": 183}[arch])
        struct.pack_into("<Q", data, 32, 64)
        struct.pack_into("<HH", data, 54, 56, 1)
        struct.pack_into("<I", data, 64, 1)
        return bytes(data)

    def test_library_format_and_architecture_are_required(self):
        for target, make in (("windows", self.pe), ("linux", self.elf)):
            for arch in ("amd64", "arm64"):
                smoke_runtime.verify_plugin_library(make(arch), target, arch)
                other = "arm64" if arch == "amd64" else "amd64"
                with self.assertRaises(ValueError):
                    smoke_runtime.verify_plugin_library(make(other), target, arch)
                with self.assertRaises(ValueError):
                    smoke_runtime.verify_plugin_library(make(arch, False), target, arch)
                with self.assertRaises(ValueError):
                    smoke_runtime.verify_plugin_library(b"not a library", target, arch)

    @patch("smoke_runtime.Path.is_file", return_value=True)
    @patch("smoke_runtime.subprocess.run")
    def test_native_compile_commands_contain_paths_with_spaces_and_all_intermediates(self, run, exists):
        for target, make, extension in (("windows", self.pe, ".dll"), ("linux", self.elf, ".so")):
            for arch in ("amd64", "arm64"):
                with tempfile.TemporaryDirectory(prefix="native plugin spaces ") as tmp:
                    root = Path(tmp)
                    output = root / "plugins" / target / arch / ("management-api-c" + extension)
                    run.side_effect = lambda *a, **kw: output.write_bytes(make(arch))
                    result = smoke_runtime.build_native_plugin(root, target, arch, {"PATH": "fixture compiler path"})
                    self.assertEqual(result, output)
                    args, kwargs = run.call_args.args[0], run.call_args.kwargs
                    self.assertEqual(kwargs["cwd"], root / "plugin-build")
                    self.assertTrue(kwargs["check"])
                    self.assertNotIn("shell", kwargs)
                    if target == "windows":
                        self.assertEqual(args[0], "cl.exe")
                        self.assertIn("/Fo" + str(root / "plugin-build/plugin.obj"), args)
                        self.assertIn("/IMPLIB:" + str(root / "plugin-build/plugin.lib"), args)
                        self.assertIn("/OUT:" + str(output), args)
                        self.assertIn("/MACHINE:" + {"amd64": "X64", "arm64": "ARM64"}[arch], args)
                        self.assertIn("/MT", args)
                    else:
                        self.assertEqual(args[:4], ["cc", "-fPIC", "-shared", "-o"])
                        self.assertEqual(args[4], str(output))

if __name__ == "__main__":
    unittest.main()
