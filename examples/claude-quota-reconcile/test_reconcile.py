import copy
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import tempfile
from pathlib import Path
import threading
import unittest
import reconcile


class ReconciliationTests(unittest.TestCase):
    def row(self):
        return {"auth_index": "synthetic-index", "provider": "claude", "disabled": False,
                "quota_blocked": True, "cooldowns": [{"scope": "credential", "reason": "credential_quota"}]}

    def test_only_quota_blocked_claude_candidates(self):
        valid = self.row()
        rows = [valid, copy.deepcopy(valid)]
        for field, value in (("disabled", True), ("provider", "codex"),
                             ("quota_blocked", False), ("requires_sign_in", True),
                             ("cooldowns", [{"scope": "model", "reason": "auth"}])):
            row = self.row()
            row[field] = value
            rows.append(row)
        self.assertEqual(reconcile.candidates({"files": rows}), ["synthetic-index"])

    def test_unknown_inventory_fails_closed(self):
        for data in ({}, {"files": None}, {"files": ["bad"]}, []):
            with self.assertRaises(reconcile.ManagementError):
                reconcile.candidates(data)

    def test_retry_after(self):
        self.assertEqual(reconcile.retry_seconds("7200"), 7200)
        self.assertEqual(reconcile.retry_seconds("invalid"), 0)
        self.assertEqual(reconcile.retry_seconds(None), 0)
        class FailingClient:
            def call(_, path, body=None):
                if path == "credentials":
                    return {"files": [self.row()]}
                raise reconcile.ManagementError("rate limited", retry_after=7200)
        self.assertEqual(reconcile.run_once(FailingClient())["retry_after_seconds"], 7200)

    def test_origin_and_key_safety(self):
        for url in ("http://example.com", "https://user:key@example.com", "https://example.com/?key=x"):
            with self.assertRaises(ValueError):
                reconcile.Client(url, "synthetic")
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory) / "key"
            p.write_text("synthetic")
            p.chmod(0o644)
            with self.assertRaises(reconcile.ManagementError):
                reconcile.read_key(p)
            p.chmod(0o600)
            self.assertEqual(reconcile.read_key(p), "synthetic")

    def test_loopback_no_unsafe_fallback_or_redirect(self):
        requests = []
        row = self.row()
        status = [200]

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                requests.append(("GET", self.path))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({"files": [row]}).encode())

            def do_POST(self):
                requests.append(("POST", self.path))
                self.rfile.read(int(self.headers["Content-Length"]))
                self.send_response(status[0])
                if status[0] == 302:
                    self.send_header("Location", "/must-not-follow")
                self.end_headers()
                self.wfile.write(b'{"status":"reconciled"}')

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = reconcile.Client(f"http://127.0.0.1:{server.server_port}", "synthetic")
            self.assertEqual(reconcile.run_once(client)["reconciled"], 1)
            status[0] = 409
            self.assertEqual(reconcile.run_once(client)["state_changed"], 1)
            for code in (401, 403, 429, 500, 302):
                status[0] = code
                self.assertEqual(reconcile.run_once(client)["failed"], 1)
            status[0] = 404
            with self.assertRaises(reconcile.UnsupportedBackend):
                reconcile.run_once(client)
            self.assertTrue(all(path in (
                "/v8/management/credentials", "/v8/management/routing/cooldown/reconcile")
                for _, path in requests))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
