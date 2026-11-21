#!/usr/bin/env python3
"""Exercise the real Claude CLI through this core against a local fake upstream.

No Anthropic credentials or quota are used. Run from an Orca terminal to verify
the same process/terminal host used by Orca's Claude integration.
"""

import argparse
import http.client
import json
import math
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--claude", default="claude")
    parser.add_argument("--cooldown", type=int, default=6)
    parser.add_argument("--expect-wait", type=int, default=0,
                        help="Check that the CLI remains pending for this many seconds during a long cooldown")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    model = "claude-sonnet-4-6"
    marker = "RESUMED_AFTER_COOLDOWN"
    upstream_calls = []
    downstream_calls = []
    reset_at = None

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def reply(self, status, body, content_type="application/json", headers=None):
            if not isinstance(body, bytes):
                body = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            for key, value in (headers or {}).items():
                self.send_header(key, value)
            self.end_headers()
            self.wfile.write(body)

    class Upstream(Handler):
        def do_POST(self):
            nonlocal reset_at
            payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
            if "count_tokens" in self.path:
                return self.reply(200, {"input_tokens": 1})
            now = time.monotonic()
            upstream_calls.append(now)
            if reset_at is None:
                reset_at = now + args.cooldown
            if now < reset_at:
                return self.reply(429, {"type": "error", "error": {
                    "type": "rate_limit_error", "message": "Synthetic five-hour quota exhausted"
                }}, headers={"Retry-After": str(math.ceil(reset_at - now))})
            message = {"id": "msg_resume_test", "type": "message", "role": "assistant",
                       "content": [{"type": "text", "text": marker}], "model": model,
                       "stop_reason": "end_turn", "stop_sequence": None,
                       "usage": {"input_tokens": 1, "output_tokens": 6}}
            if not payload.get("stream"):
                return self.reply(200, message)
            start = {**message, "content": [], "stop_reason": None,
                     "usage": {"input_tokens": 1, "output_tokens": 0}}
            events = [
                ("message_start", {"type": "message_start", "message": start}),
                ("content_block_start", {"type": "content_block_start", "index": 0,
                                         "content_block": {"type": "text", "text": ""}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                         "delta": {"type": "text_delta", "text": marker}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                ("message_delta", {"type": "message_delta", "delta": {
                    "stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 6}}),
                ("message_stop", {"type": "message_stop"}),
            ]
            data = "".join(f"event: {kind}\ndata: {json.dumps(event)}\n\n" for kind, event in events).encode()
            self.reply(200, data, "text/event-stream")

    with tempfile.TemporaryDirectory(prefix="claude-resume-") as scratch:
        scratch = Path(scratch)
        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            core_port = sock.getsockname()[1]

        class Relay(Handler):
            def do_POST(self):
                data = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                conn = http.client.HTTPConnection("127.0.0.1", core_port, timeout=30)
                headers = {k: v for k, v in self.headers.items()
                           if k.lower() not in {"host", "content-length", "connection", "accept-encoding"}}
                conn.request("POST", self.path, data, headers)
                response = conn.getresponse()
                body = response.read()
                if "count_tokens" not in self.path:
                    downstream_calls.append((response.status, time.monotonic(), response.getheader("Retry-After"), body))
                self.reply(response.status, body, response.getheader("Content-Type", "application/json"),
                           {k: v for k, v in response.getheaders()
                            if k.lower() not in {"content-type", "content-length", "connection", "transfer-encoding"}})
                conn.close()

        relay = ThreadingHTTPServer(("127.0.0.1", 0), Relay)
        threading.Thread(target=relay.serve_forever, daemon=True).start()
        config = f"""config-version: 8
server:
  host: 127.0.0.1
  port: {core_port}
management:
  disable-control-panel: true
auth:
  dir: {scratch / 'auth'}
access:
  api-keys: [test-client-key]
routing:
  retry:
    request-retry: 0
    max-retry-interval: 0
  cooldown:
    status-code: 529
api-keys:
  claude:
    - name: synthetic-upstream
      base-url: http://127.0.0.1:{upstream.server_port}
      keys:
        - api-key: test-upstream-key
      models:
        - name: {model}
"""
        (scratch / "config.yaml").write_text(config)
        with (scratch / "core.log").open("w+") as log:
            core = subprocess.Popen([binary, "--config", str(scratch / "config.yaml"), "--local-model"],
                                    cwd=scratch, stdout=log, stderr=log)
            try:
                for _ in range(150):
                    if core.poll() is not None:
                        log.seek(0)
                        raise RuntimeError("Core exited: " + log.read()[-5000:])
                    try:
                        with socket.create_connection(("127.0.0.1", core_port), timeout=0.1):
                            break
                    except OSError:
                        time.sleep(0.1)
                else:
                    raise RuntimeError("Core did not start")
                env = {k: v for k, v in os.environ.items()
                       if not k.startswith(("ANTHROPIC_", "CLAUDE_")) and k != "CLAUDECODE"}
                env.update(ANTHROPIC_BASE_URL=f"http://127.0.0.1:{relay.server_port}",
                           ANTHROPIC_API_KEY="test-client-key", CLAUDE_CODE_MAX_RETRIES="100000",
                           CLAUDE_CODE_RETRY_WATCHDOG="1",
                           CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS="21600000",
                           CLAUDE_CODE_RETRY_WATCHDOG_MAX_USAGE_LIMIT_WAIT_MS="21600000",
                           CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1",
                           CLAUDE_CONFIG_DIR=str(scratch / "claude"))
                started = time.monotonic()
                command = [shutil.which(args.claude) or args.claude, "--bare", "-p",
                                         "--no-session-persistence", "--setting-sources", "",
                                         "--strict-mcp-config", "--tools", "", "--model", model,
                                         "--system-prompt", "Reply with the test marker.", "Resume test"]
                try:
                    result = subprocess.run(command, cwd=scratch, env=env, capture_output=True,
                                            text=True, timeout=args.expect_wait or 90)
                except subprocess.TimeoutExpired:
                    if not args.expect_wait:
                        raise
                    assert downstream_calls and downstream_calls[0][0] == 529, downstream_calls
                    assert len(upstream_calls) == 1, upstream_calls
                    assert json.loads(downstream_calls[0][3])["error"]["type"] == "overloaded_error"
                    print(json.dumps({"result": "PASS", "waiting_through_long_cooldown": True,
                                      "cooldown_seconds": args.cooldown, "observed_seconds": args.expect_wait,
                                      "upstream_requests": len(upstream_calls)}, indent=2), flush=True)
                    return
                if args.expect_wait:
                    raise AssertionError(("Client stopped instead of waiting", result.stdout, result.stderr))
                elapsed = time.monotonic() - started
                assert result.returncode == 0 and marker in result.stdout, (
                    result.returncode, result.stdout[-2000:], result.stderr[-2000:])
                assert downstream_calls[0][0] == 529, downstream_calls
                error = json.loads(downstream_calls[0][3])
                assert error["error"]["type"] == "overloaded_error", error
                assert downstream_calls[0][2], "Missing Retry-After"
                assert any(call[0] == 200 for call in downstream_calls), downstream_calls
                assert len(upstream_calls) >= 2 and upstream_calls[1] >= reset_at, upstream_calls
                print(json.dumps({"result": "PASS", "claude": subprocess.check_output(
                    [args.claude, "--version"], text=True).strip(), "elapsed_seconds": round(elapsed, 2),
                    "client_statuses": [call[0] for call in downstream_calls],
                    "upstream_requests": len(upstream_calls), "no_upstream_requests_during_cooldown": True,
                    "automatic_resume": marker in result.stdout}, indent=2), flush=True)
            finally:
                core.terminate()
                try:
                    core.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    core.kill()
                    core.wait()
                relay.shutdown()
                upstream.shutdown()


if __name__ == "__main__":
    main()
