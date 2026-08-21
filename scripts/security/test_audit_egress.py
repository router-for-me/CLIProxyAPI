#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

MODULE_PATH = Path(__file__).with_name("audit_egress.py")
SPEC = importlib.util.spec_from_file_location("audit_egress", MODULE_PATH)
assert SPEC and SPEC.loader
AUDIT = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = AUDIT
SPEC.loader.exec_module(AUDIT)


class AuditEgressTests(unittest.TestCase):
    def test_decode_ipv4_proc_format(self) -> None:
        self.assertEqual(AUDIT.decode_ipv4("0100007F"), "127.0.0.1")

    def test_decode_ipv6_proc_format(self) -> None:
        self.assertEqual(AUDIT.decode_ipv6("00000000000000000000000001000000"), "::1")

    def test_classify_known_and_unknown_hosts(self) -> None:
        self.assertEqual(AUDIT.classify_host("api.openai.com")[0], "provider")
        self.assertEqual(AUDIT.classify_host("models.router-for.me")[0], "support")
        self.assertEqual(AUDIT.classify_host("telemetry.suspicious.invalid")[0], "unknown")
        self.assertEqual(AUDIT.classify_host("notopenai.com")[0], "unknown")
        self.assertEqual(AUDIT.classify_host("evilgoogleapis.com")[0], "unknown")

    def test_static_scan_skips_tests_and_examples(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "internal").mkdir()
            (root / "examples").mkdir()
            (root / "internal" / "live.go").write_text(
                'package internal\nconst endpoint = "https://telemetry.vendor.test/collect"\n',
                encoding="utf-8",
            )
            (root / "internal" / "live_test.go").write_text(
                'package internal\nconst endpoint = "https://ignored.test/unit"\n',
                encoding="utf-8",
            )
            (root / "examples" / "demo.go").write_text(
                'package main\nconst endpoint = "https://ignored.test/demo"\n',
                encoding="utf-8",
            )
            findings = AUDIT.scan_static(root, None)
            self.assertEqual([finding.host for finding in findings], ["telemetry.vendor.test"])

    def test_static_scan_includes_config_without_exposing_secrets(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            root.joinpath("main.go").write_text("package main\n", encoding="utf-8")
            config = root / "config.yaml"
            config.write_text(
                "api-key: super-secret-value\n"
                "base-url: https://user:password@custom.provider.test:8443/v1?token=SECRET#private\n",
                encoding="utf-8",
            )
            findings = AUDIT.scan_static(root, config)
            self.assertEqual(len(findings), 1)
            self.assertEqual(findings[0].host, "custom.provider.test")
            self.assertEqual(findings[0].endpoint, "https://custom.provider.test:8443")
            rendered = repr(findings)
            for secret in ("super-secret-value", "user", "password", "SECRET", "/v1"):
                self.assertNotIn(secret, rendered)

    def test_sanitized_endpoint_ignores_format_port(self) -> None:
        self.assertEqual(AUDIT.sanitized_endpoint("http://localhost:%d/callback"), "http://localhost")

    def test_example_like_active_destination_is_not_hidden(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            root.joinpath("main.go").write_text("package main\n", encoding="utf-8")
            config = root / "config.yaml"
            config.write_text("base-url: https://notexample.com/v1\n", encoding="utf-8")
            findings = AUDIT.scan_static(root, config)
            self.assertEqual([finding.host for finding in findings], ["notexample.com"])

    def test_static_scan_skips_comments_and_schema_identifiers(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            root.joinpath("main.go").write_text(
                'package main\n'
                '// Docs: https://docs.vendor.test/help\n'
                'const enabled = true // https://inline-comment.vendor.test/help\n'
                '/* https://block-comment.vendor.test/help */\n'
                'const schema = "http://json-schema.org/draft-07/schema#"\n'
                'const live = "https://collector.vendor.test/v1"\n',
                encoding="utf-8",
            )
            config = root / "config.yaml"
            config.write_text(
                '# base-url: https://ignored.vendor.test/v1\n'
                'enabled: true # docs https://also-ignored.vendor.test/help\n',
                encoding="utf-8",
            )
            findings = AUDIT.scan_static(root, config)
            self.assertEqual([finding.host for finding in findings], ["collector.vendor.test"])


if __name__ == "__main__":
    unittest.main()
