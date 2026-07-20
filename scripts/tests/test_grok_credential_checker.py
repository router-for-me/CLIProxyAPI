#!/usr/bin/env python3
"""Tests for scripts/grok_credential_checker.py."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import threading
import time
import unittest
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

import grok_credential_checker as gcc  # noqa: E402


def ts(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


class FakeClock:
    def __init__(self, start: Optional[datetime] = None) -> None:
        self.now = start or datetime(2026, 7, 20, 12, 0, 0, tzinfo=timezone.utc)

    def __call__(self) -> datetime:
        return self.now

    def advance(self, **kwargs: Any) -> None:
        self.now = self.now + timedelta(**kwargs)


class TestArgParseAndDefaults(unittest.TestCase):
    def test_help_exits_zero(self) -> None:
        parser = gcc.build_arg_parser()
        with self.assertRaises(SystemExit) as ctx:
            parser.parse_args(["--help"])
        self.assertEqual(ctx.exception.code, 0)

    def test_defaults_and_dry_run(self) -> None:
        os.environ["CLIPROXY_MANAGEMENT_URL"] = "http://127.0.0.1:9"
        os.environ["CLIPROXY_MANAGEMENT_KEY"] = "test-key-secret"
        try:
            parser = gcc.build_arg_parser()
            args = parser.parse_args([])
            cfg = gcc.config_from_args(args)
            self.assertTrue(cfg.dry_run)
            self.assertFalse(cfg.apply)
            self.assertEqual(cfg.target_active, 500)
            self.assertEqual(cfg.quota_limit_tokens, 1_000_000)
            self.assertEqual(cfg.reset_window_hours, 24)
            self.assertEqual(cfg.concurrency, 32)
            self.assertEqual(cfg.batch_size, 100)
            self.assertEqual(cfg.max_probes_per_cycle, 50)
        finally:
            os.environ.pop("CLIPROXY_MANAGEMENT_URL", None)
            os.environ.pop("CLIPROXY_MANAGEMENT_KEY", None)

    def test_apply_disables_dry_run(self) -> None:
        parser = gcc.build_arg_parser()
        args = parser.parse_args(
            [
                "--management-url",
                "http://127.0.0.1:9",
                "--management-key",
                "k",
                "--apply",
            ]
        )
        cfg = gcc.config_from_args(args)
        self.assertTrue(cfg.apply)
        self.assertFalse(cfg.dry_run)

    def test_missing_url_fails_before_network(self) -> None:
        parser = gcc.build_arg_parser()
        args = parser.parse_args(["--management-key", "k"])
        with self.assertRaises(SystemExit) as ctx:
            gcc.config_from_args(args)
        self.assertIn("management-url", str(ctx.exception))

    def test_concurrency_cap(self) -> None:
        parser = gcc.build_arg_parser()
        args = parser.parse_args(
            [
                "--management-url",
                "http://127.0.0.1:9",
                "--management-key",
                "k",
                "--concurrency",
                "64",
            ]
        )
        with self.assertRaises(SystemExit):
            gcc.config_from_args(args)


class TestRedaction(unittest.TestCase):
    def test_redact_bearer_and_tokens(self) -> None:
        text = "Authorization: Bearer super-secret-token access_token=abc123"
        out = gcc.redact_text(text)
        self.assertNotIn("super-secret-token", out)
        self.assertNotIn("abc123", out)
        self.assertIn("[REDACTED]", out)

    def test_redact_obj_keys(self) -> None:
        obj = {"Authorization": "Bearer x", "name": "a.json", "nested": {"refresh_token": "r"}}
        out = gcc.redact_obj(obj)
        self.assertEqual(out["Authorization"], "[REDACTED]")
        self.assertEqual(out["nested"]["refresh_token"], "[REDACTED]")
        self.assertEqual(out["name"], "a.json")


class TestInventoryFilter(unittest.TestCase):
    def test_filters_xai_oauth_only(self) -> None:
        files = [
            {"name": "x1.json", "provider": "xai", "auth_index": "1", "disabled": False},
            {"name": "c1.json", "provider": "codex", "auth_index": "2", "disabled": False},
            {"name": "x2.json", "type": "grok", "auth_index": "3", "disabled": True},
            {"name": "noindex.json", "provider": "xai", "disabled": False},
            {
                "name": "cfg.json",
                "provider": "xai",
                "auth_index": "9",
                "source": "config",
                "disabled": False,
            },
            {
                "name": "virt.json",
                "provider": "xai",
                "auth_index": "8",
                "runtime_only": True,
                "disabled": True,
            },
        ]
        creds = gcc.discover_xai_credentials(files)
        names = [c.name for c in creds]
        self.assertEqual(names, ["x1.json", "x2.json"])


class TestQuotaAdapter(unittest.TestCase):
    def setUp(self) -> None:
        self.now = datetime(2026, 7, 20, 12, 0, 0, tzinfo=timezone.utc)

    def test_free_usage_exhausted_message_24h(self) -> None:
        body = (
            '{"code":"subscription:free-usage-exhausted",'
            '"error":"You\'ve used all the included free usage for model grok-4.5-build-free '
            "for now. Usage resets over a rolling 24-hour window — "
            'tokens (actual/limit): 1065387/1000000."}'
        )
        cls = gcc.classify_upstream_response(429, body, {}, self.now, 24, "test")
        self.assertEqual(cls.classification, gcc.CLASS_EXHAUSTED)
        self.assertEqual(cls.usage_tokens, 1065387)
        self.assertEqual(cls.usage_limit_tokens, 1_000_000)
        self.assertIsNotNone(cls.reset_at)
        assert cls.reset_at is not None
        self.assertEqual(cls.reset_at, self.now + timedelta(hours=24))

    def test_included_free_usage_phrase(self) -> None:
        body = "You've used all the included free usage for now."
        cls = gcc.classify_upstream_response(429, body, {}, self.now, 24, "test")
        self.assertEqual(cls.classification, gcc.CLASS_EXHAUSTED)

    def test_generic_429_not_exhaustion(self) -> None:
        body = '{"code":"rate_limit","error":"too many requests"}'
        cls = gcc.classify_upstream_response(429, body, {}, self.now, 24, "test")
        self.assertEqual(cls.classification, gcc.CLASS_UPSTREAM_ERROR)
        self.assertEqual(cls.reason, "generic_429")

    def test_retry_after_preferred_over_24h(self) -> None:
        body = "included free usage exhausted"
        cls = gcc.classify_upstream_response(
            429, body, {"Retry-After": ["3600"]}, self.now, 24, "test"
        )
        self.assertEqual(cls.classification, gcc.CLASS_EXHAUSTED)
        assert cls.reset_at is not None
        self.assertEqual(cls.reset_at, self.now + timedelta(seconds=3600))

    def test_auth_401_403(self) -> None:
        self.assertEqual(
            gcc.classify_upstream_response(401, "unauthorized", {}, self.now, 24, "t").classification,
            gcc.CLASS_AUTH_INVALID,
        )
        self.assertEqual(
            gcc.classify_upstream_response(
                403, "phone verification required", {}, self.now, 24, "t"
            ).classification,
            gcc.CLASS_VERIFICATION,
        )

    def test_5xx_and_timeout_unknown_schema(self) -> None:
        self.assertEqual(
            gcc.classify_upstream_response(503, "oops", {}, self.now, 24, "t").classification,
            gcc.CLASS_UPSTREAM_ERROR,
        )
        self.assertEqual(
            gcc.classify_upstream_response(0, "", {}, self.now, 24, "t").classification,
            gcc.CLASS_UPSTREAM_ERROR,
        )
        self.assertEqual(
            gcc.classify_upstream_response(418, "teapot", {}, self.now, 24, "t").classification,
            gcc.CLASS_UNKNOWN,
        )

    def test_runtime_next_retry_after(self) -> None:
        cred = gcc.Credential(
            name="a.json",
            auth_index="1",
            provider="xai",
            disabled=False,
            next_retry_after=self.now + timedelta(hours=2),
            status_message="cooldown",
        )
        cls = gcc.classify_from_runtime(cred, self.now, 24)
        self.assertIsNotNone(cls)
        assert cls is not None
        self.assertEqual(cls.classification, gcc.CLASS_COOLDOWN)


class TestOwnershipAndState(unittest.TestCase):
    def test_atomic_state_and_manual_override(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "state.json")
            store = gcc.StateStore(path)
            store.load()
            self.assertTrue(store.safe_mode)
            st = store.get("a.json")
            st.ownership = gcc.OWNERSHIP_MANAGED
            st.last_script_disabled = True
            store.safe_mode = False
            store.save()

            store2 = gcc.StateStore(path)
            store2.load()
            self.assertFalse(store2.safe_mode)
            st2 = store2.get("a.json")
            gcc.sync_ownership(st2, live_disabled=False)
            self.assertEqual(st2.ownership, gcc.OWNERSHIP_MANUAL_OVERRIDE)

    def test_lock_blocks_second_instance(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            lock_path = os.path.join(tmp, "state.lock")
            with gcc.ProcessLock(lock_path):
                with self.assertRaises(SystemExit):
                    gcc.ProcessLock(lock_path).acquire()

    def test_corrupt_state_safe_mode(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "state.json")
            with open(path, "w", encoding="utf-8") as fh:
                fh.write("{not-json")
            store = gcc.StateStore(path)
            store.load()
            self.assertTrue(store.safe_mode)

    def test_no_secrets_in_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "state.json")
            store = gcc.StateStore(path)
            st = store.get("a.json")
            st.last_action = "Bearer secret-value-should-not-persist-raw"
            # redact on save via redact_obj — bearer pattern in strings
            store.save()
            with open(path, encoding="utf-8") as fh:
                raw = fh.read()
            self.assertNotIn("secret-value-should-not-persist-raw", raw)


class TestPolicyAndPool(unittest.TestCase):
    def setUp(self) -> None:
        self.now = datetime(2026, 7, 20, 12, 0, 0, tzinfo=timezone.utc)

    def _cred(self, name: str, disabled: bool, idx: str = "") -> gcc.Credential:
        return gcc.Credential(
            name=name,
            auth_index=idx or name,
            provider="xai",
            disabled=disabled,
        )

    def test_exhaustion_disables_active(self) -> None:
        cred = self._cred("a.json", False)
        state = gcc.CredentialState(name="a.json", ownership=gcc.OWNERSHIP_EXTERNAL)
        cls = gcc.Classification(
            gcc.CLASS_EXHAUSTED,
            reason=gcc.REASON_QUOTA_EXHAUSTED,
            reset_at=self.now + timedelta(hours=24),
        )
        action = gcc.desired_disable_for_active(cred, state, cls)
        self.assertIsNotNone(action)
        assert action is not None
        self.assertTrue(action.new_disabled)
        self.assertEqual(action.reason, gcc.REASON_QUOTA_EXHAUSTED)

    def test_5xx_does_not_disable(self) -> None:
        cred = self._cred("a.json", False)
        state = gcc.CredentialState(name="a.json")
        cls = gcc.Classification(gcc.CLASS_UPSTREAM_ERROR, reason="http_503")
        self.assertIsNone(gcc.desired_disable_for_active(cred, state, cls))

    def test_manual_override_never_enabled(self) -> None:
        creds = [self._cred("a.json", True), self._cred("b.json", False)]
        states = {
            "a.json": gcc.CredentialState(
                name="a.json",
                ownership=gcc.OWNERSHIP_MANUAL_OVERRIDE,
                disable_reason=gcc.REASON_QUOTA_EXHAUSTED,
                reset_at=ts(self.now - timedelta(hours=1)),
                classification=gcc.CLASS_HEALTHY,
            ),
            "b.json": gcc.CredentialState(
                name="b.json",
                ownership=gcc.OWNERSHIP_MANAGED,
                classification=gcc.CLASS_HEALTHY,
            ),
        }
        classifications = {
            "a.json": gcc.Classification(gcc.CLASS_HEALTHY),
            "b.json": gcc.Classification(gcc.CLASS_HEALTHY),
        }
        actions, shortfall, _ = gcc.reconcile_pool(
            creds,
            states,
            classifications,
            target_active=2,
            quota_limit=1_000_000,
            round_robin=True,
            rr_global=0,
            safe_mode=False,
            now=self.now,
        )
        enable_names = [a.name for a in actions if not a.new_disabled]
        self.assertNotIn("a.json", enable_names)
        self.assertGreaterEqual(shortfall, 1)

    def test_only_script_owned_quota_disabled_becomes_eligible(self) -> None:
        creds = [
            self._cred("managed.json", True),
            self._cred("external.json", True),
            self._cred("invalid.json", True),
        ]
        states = {
            "managed.json": gcc.CredentialState(
                name="managed.json",
                ownership=gcc.OWNERSHIP_MANAGED,
                disable_reason=gcc.REASON_QUOTA_EXHAUSTED,
                reset_at=ts(self.now - timedelta(minutes=1)),
                last_script_disabled=True,
                classification=gcc.CLASS_HEALTHY,
            ),
            "external.json": gcc.CredentialState(
                name="external.json",
                ownership=gcc.OWNERSHIP_EXTERNAL,
                disable_reason=None,
                classification=gcc.CLASS_HEALTHY,
            ),
            "invalid.json": gcc.CredentialState(
                name="invalid.json",
                ownership=gcc.OWNERSHIP_MANAGED,
                disable_reason=gcc.REASON_AUTH_INVALID,
                last_script_disabled=True,
                classification=gcc.CLASS_AUTH_INVALID,
            ),
        }
        classifications = {
            "managed.json": gcc.Classification(gcc.CLASS_HEALTHY),
            "external.json": gcc.Classification(gcc.CLASS_HEALTHY),
            "invalid.json": gcc.Classification(gcc.CLASS_AUTH_INVALID),
        }
        actions, shortfall, _ = gcc.reconcile_pool(
            creds,
            states,
            classifications,
            target_active=1,
            quota_limit=1_000_000,
            round_robin=True,
            rr_global=0,
            safe_mode=False,
            now=self.now,
        )
        enabled = [a for a in actions if a.new_disabled is False]
        self.assertEqual(len(enabled), 1)
        self.assertEqual(enabled[0].name, "managed.json")
        self.assertEqual(shortfall, 0)

    def test_pool_exactly_and_surplus(self) -> None:
        creds = [self._cred(f"c{i:03d}.json", False) for i in range(5)]
        states = {
            c.name: gcc.CredentialState(
                name=c.name,
                ownership=gcc.OWNERSHIP_MANAGED,
                last_script_disabled=False,
                classification=gcc.CLASS_HEALTHY,
            )
            for c in creds
        }
        classifications = {c.name: gcc.Classification(gcc.CLASS_HEALTHY) for c in creds}
        actions, shortfall, _ = gcc.reconcile_pool(
            creds,
            states,
            classifications,
            target_active=5,
            quota_limit=1_000_000,
            round_robin=True,
            rr_global=0,
            safe_mode=False,
            now=self.now,
        )
        self.assertEqual(actions, [])
        self.assertEqual(shortfall, 0)

        actions2, shortfall2, _ = gcc.reconcile_pool(
            creds,
            states,
            classifications,
            target_active=3,
            quota_limit=1_000_000,
            round_robin=True,
            rr_global=0,
            safe_mode=False,
            now=self.now,
        )
        demotes = [a for a in actions2 if a.new_disabled]
        self.assertEqual(len(demotes), 2)
        self.assertEqual(shortfall2, 0)

    def test_safe_mode_blocks_enable(self) -> None:
        creds = [self._cred("a.json", True)]
        states = {
            "a.json": gcc.CredentialState(
                name="a.json",
                ownership=gcc.OWNERSHIP_MANAGED,
                disable_reason=gcc.REASON_POOL_STANDBY,
                last_script_disabled=True,
                classification=gcc.CLASS_HEALTHY,
            )
        }
        classifications = {"a.json": gcc.Classification(gcc.CLASS_HEALTHY)}
        actions, shortfall, skipped = gcc.reconcile_pool(
            creds,
            states,
            classifications,
            target_active=1,
            quota_limit=1_000_000,
            round_robin=True,
            rr_global=0,
            safe_mode=True,
            now=self.now,
        )
        self.assertEqual([a for a in actions if not a.new_disabled], [])
        self.assertEqual(shortfall, 1)
        self.assertTrue(any(s.get("reason") == "safe_mode_no_enable" for s in skipped))


class MockManagementServer:
    """Local mock Management API for integration tests."""

    def __init__(self, files: List[Dict[str, Any]]) -> None:
        self.files = {f["name"]: dict(f) for f in files}
        self.patches: List[Dict[str, Any]] = []
        self.api_calls: List[Dict[str, Any]] = []
        self.in_flight = 0
        self.peak_in_flight = 0
        self._lock = threading.Lock()
        self.responses: Dict[str, Any] = {}  # auth_index -> response override
        self.handler_self = self

        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A003
                return

            def _read_json(self) -> Any:
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b"{}"
                try:
                    return json.loads(raw.decode("utf-8"))
                except json.JSONDecodeError:
                    return {}

            def _write(self, code: int, payload: Any) -> None:
                data = json.dumps(payload).encode("utf-8")
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self) -> None:  # noqa: N802
                path = urlparse(self.path).path
                if path.endswith("/auth-files"):
                    self._write(200, {"files": list(outer.files.values())})
                    return
                self._write(404, {"error": "not found"})

            def do_PATCH(self) -> None:  # noqa: N802
                path = urlparse(self.path).path
                body = self._read_json()
                if path.endswith("/auth-files/status"):
                    name = body.get("name")
                    disabled = bool(body.get("disabled"))
                    with outer._lock:
                        outer.patches.append({"name": name, "disabled": disabled})
                        if name in outer.files:
                            outer.files[name]["disabled"] = disabled
                    self._write(200, {"status": "ok", "disabled": disabled})
                    return
                self._write(404, {"error": "not found"})

            def do_POST(self) -> None:  # noqa: N802
                path = urlparse(self.path).path
                body = self._read_json()
                if path.endswith("/api-call"):
                    with outer._lock:
                        outer.in_flight += 1
                        outer.peak_in_flight = max(outer.peak_in_flight, outer.in_flight)
                        outer.api_calls.append(body)
                    try:
                        # small sleep to make concurrency observable under load
                        time.sleep(0.002)
                        auth_index = str(body.get("auth_index") or "")
                        override = outer.responses.get(auth_index)
                        if override is None:
                            payload = {
                                "status_code": 200,
                                "header": {},
                                "body": json.dumps({"ok": True}),
                            }
                        else:
                            payload = override
                        self._write(200, payload)
                    finally:
                        with outer._lock:
                            outer.in_flight -= 1
                    return
                self._write(404, {"error": "not found"})

        self._httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base_url = f"http://127.0.0.1:{self._httpd.server_address[1]}/v0/management"
        self._thread = threading.Thread(target=self._httpd.serve_forever, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._httpd.shutdown()
        self._thread.join(timeout=5)


class TestEndToEndMock(unittest.TestCase):
    def test_disable_refill_and_idempotent(self) -> None:
        files = []
        # 3 active healthy, 1 active exhausted, 2 managed standby after reset
        for i in range(3):
            files.append(
                {
                    "name": f"active{i}.json",
                    "provider": "xai",
                    "auth_index": f"a{i}",
                    "disabled": False,
                    "status": "active",
                }
            )
        files.append(
            {
                "name": "exhausted.json",
                "provider": "xai",
                "auth_index": "ex",
                "disabled": False,
                "status": "error",
                "status_message": "subscription:free-usage-exhausted included free usage",
            }
        )
        for i in range(2):
            files.append(
                {
                    "name": f"standby{i}.json",
                    "provider": "xai",
                    "auth_index": f"s{i}",
                    "disabled": True,
                }
            )

        server = MockManagementServer(files)
        server.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                state_path = os.path.join(tmp, "state.json")
                # seed managed standby ownership
                store = gcc.StateStore(state_path)
                for i in range(2):
                    st = store.get(f"standby{i}.json")
                    st.ownership = gcc.OWNERSHIP_MANAGED
                    st.last_script_disabled = True
                    st.disable_reason = gcc.REASON_POOL_STANDBY
                    st.classification = gcc.CLASS_HEALTHY
                # pre-seed so not safe_mode
                store.safe_mode = False
                store.save()

                cfg = gcc.Config(
                    management_url=server.base_url,
                    management_key="test-key",
                    apply=True,
                    dry_run=False,
                    target_active=3,
                    state_file=state_path,
                    concurrency=4,
                    batch_size=10,
                    max_probes_per_cycle=10,
                    timeout_seconds=5,
                )
                client = gcc.ManagementClient(cfg.management_url, cfg.management_key, timeout_seconds=5)
                store2 = gcc.StateStore(state_path)
                store2.load()
                checker = gcc.Checker(cfg, client=client, store=store2)
                report = checker.run_once()
                self.assertFalse(report.errors, report.errors)
                # exhausted should be disabled; at least one standby enabled if shortfall
                patch_map = {(p["name"], p["disabled"]) for p in server.patches}
                self.assertIn(("exhausted.json", True), patch_map)

                # second cycle should be mostly idempotent (no thrash)
                patches_before = len(server.patches)
                report2 = checker.run_once()
                self.assertFalse(report2.errors)
                # may still have small adjustments; ensure no unbounded growth — second cycle limited
                self.assertLessEqual(len(server.patches) - patches_before, 3)
        finally:
            server.stop()

    def test_reset_boundary_with_fake_clock(self) -> None:
        clock = FakeClock()
        cred = gcc.Credential(
            name="q.json",
            auth_index="q",
            provider="xai",
            disabled=True,
        )
        state = gcc.CredentialState(
            name="q.json",
            ownership=gcc.OWNERSHIP_MANAGED,
            disable_reason=gcc.REASON_QUOTA_EXHAUSTED,
            last_script_disabled=True,
            reset_at=ts(clock.now + timedelta(hours=24)),
            classification=gcc.CLASS_EXHAUSTED,
        )
        self.assertFalse(gcc.is_reset_due(state, clock.now))
        clock.advance(hours=24, seconds=1)
        self.assertTrue(gcc.is_reset_due(state, clock.now))
        classifications = {"q.json": gcc.Classification(gcc.CLASS_HEALTHY)}
        actions, shortfall, _ = gcc.reconcile_pool(
            [cred],
            {"q.json": state},
            classifications,
            target_active=1,
            quota_limit=1_000_000,
            round_robin=True,
            rr_global=0,
            safe_mode=False,
            now=clock.now,
        )
        self.assertEqual(len(actions), 1)
        self.assertFalse(actions[0].new_disabled)
        self.assertEqual(shortfall, 0)


class TestScale10000(unittest.TestCase):
    def test_bounded_probes_and_concurrency(self) -> None:
        files: List[Dict[str, Any]] = []
        # 500 active + 9500 standby
        for i in range(500):
            files.append(
                {
                    "name": f"active-{i:05d}.json",
                    "provider": "xai",
                    "auth_index": f"A{i}",
                    "disabled": False,
                    "status": "active",
                }
            )
        for i in range(9500):
            files.append(
                {
                    "name": f"standby-{i:05d}.json",
                    "provider": "xai",
                    "auth_index": f"S{i}",
                    "disabled": True,
                }
            )
        server = MockManagementServer(files)
        server.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                state_path = os.path.join(tmp, "state.json")
                store = gcc.StateStore(state_path)
                store.safe_mode = False
                store.save()
                cfg = gcc.Config(
                    management_url=server.base_url,
                    management_key="scale-key",
                    apply=False,
                    dry_run=True,
                    target_active=500,
                    state_file=state_path,
                    concurrency=32,
                    batch_size=100,
                    max_probes_per_cycle=50,
                    timeout_seconds=10,
                    probe_rate_per_second=1000.0,
                )
                client = gcc.ManagementClient(cfg.management_url, cfg.management_key, timeout_seconds=10)
                store2 = gcc.StateStore(state_path)
                store2.load()
                store2.safe_mode = False
                checker = gcc.Checker(cfg, client=client, store=store2)
                t0 = time.time()
                report = checker.run_once()
                elapsed = time.time() - t0
                self.assertEqual(report.xai_total, 10000)
                self.assertLessEqual(report.probes_used, 50)
                self.assertLessEqual(client.peak_in_flight, 32)
                self.assertLessEqual(server.peak_in_flight, 32)
                # Must not probe every account: api-calls << 10000
                self.assertLess(len(server.api_calls), 2000)
                self.assertLess(elapsed, 120)
                # secrets not in JSON
                blob = json.dumps(report.to_dict())
                self.assertNotIn("scale-key", blob)
        finally:
            server.stop()


class TestCLISmoke(unittest.TestCase):
    def test_main_help(self) -> None:
        with self.assertRaises(SystemExit) as ctx:
            gcc.main(["--help"])
        self.assertEqual(ctx.exception.code, 0)


if __name__ == "__main__":
    unittest.main()
