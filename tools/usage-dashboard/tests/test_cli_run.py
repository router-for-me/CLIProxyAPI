"""CLI subprocess tests: run/serve/collect via uvicorn and asyncio collector."""

import socket
import subprocess
import sys
import time
from pathlib import Path

import httpx

PKG = Path(__file__).resolve().parent.parent  # e.g. …/tools/usage-dashboard


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _wait_http(url: str, timeout: float = 10):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            r = httpx.get(url, timeout=0.5)
            if r.status_code:
                return r
        except Exception:
            time.sleep(0.2)
    raise TimeoutError(url)


def test_run_starts_uvicorn_and_collector(tmp_path):
    """`run` starts uvicorn + async collector on the configured port."""
    port = _free_port()
    env = {
        "USAGE_DASHBOARD_DATA_DIR": str(tmp_path),
        "DASHBOARD_PORT": str(port),
        "CLIPROXY_BASE_URL": "http://127.0.0.1:1",  # collector will fail but not crash
        "CLIPROXY_MANAGEMENT_KEY": "dummy",
    }
    proc = subprocess.Popen(
        [sys.executable, "usage_dashboard.py", "run"],
        cwd=str(PKG),
        env={**env, "PATH": "/usr/bin:/bin"},
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    try:
        resp = _wait_http(f"http://127.0.0.1:{port}/api/v1/health")
        assert resp.status_code == 200
        body = resp.json()
        assert "last_poll_ok" in body
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_serve_starts_uvicorn_and_collector(tmp_path):
    """`serve` starts uvicorn AND the async collector.

    Regression: historically `serve` only ran uvicorn while `run` also started
    the collector. Since the Makefile dev-backend and e2e fixture use `serve`,
    production silently collected nothing and the dashboard showed stale data
    forever. Both commands must now start the collector via FastAPI lifespan.
    """
    port = _free_port()
    env = {
        "USAGE_DASHBOARD_DATA_DIR": str(tmp_path),
        "DASHBOARD_PORT": str(port),
        # Point at an unreachable host so the first poll fails fast without
        # crashing the task — we only need to see last_poll_at become non-null.
        "CLIPROXY_BASE_URL": "http://127.0.0.1:1",
        "CLIPROXY_MANAGEMENT_KEY": "dummy",
    }
    proc = subprocess.Popen(
        [sys.executable, "usage_dashboard.py", "serve"],
        cwd=str(PKG),
        env={**env, "PATH": "/usr/bin:/bin"},
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    try:
        resp = _wait_http(f"http://127.0.0.1:{port}/api/v1/health")
        assert resp.status_code == 200
        # The collector must have attempted at least one poll. With an
        # unreachable cliproxy host, last_poll_ok flips to false but
        # last_poll_at is still recorded — proving the task ran.
        body = _wait_lambda(
            f"http://127.0.0.1:{port}/api/v1/health",
            lambda b: b.get("last_poll_at") is not None,
            timeout=10,
        )
        assert body.get("last_poll_at") is not None
        assert body.get("last_poll_ok") is False
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def _wait_lambda(url, predicate, timeout=10):
    """Poll `url` until predicate(body) is truthy or timeout."""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            r = httpx.get(url, timeout=0.5)
            if r.status_code == 200:
                last = r.json()
                if predicate(last):
                    return last
        except Exception:
            pass
        time.sleep(0.3)
    return last or {}


def test_legacy_help_still_works():
    """`--help` lists all subcommands."""
    result = subprocess.run(
        [sys.executable, "usage_dashboard.py", "--help"],
        cwd=str(PKG),
        capture_output=True,
        text=True,
        env={"PATH": "/usr/bin:/bin"},
    )
    assert result.returncode == 0
    for sub in ("init", "collect", "serve", "run", "report", "compact", "quota"):
        assert sub in result.stdout
