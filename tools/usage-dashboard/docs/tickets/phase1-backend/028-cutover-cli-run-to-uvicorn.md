# Ticket 1.8 — Cutover CLI `run` to uvicorn + asyncio collector

**Phase**: 1 — Back end
**Blocks**: 1.9
**Blocked by**: 1.3, 1.4, 1.5, 1.6, 1.7
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/__main__.py` (rewrite CLI dispatch)
- `tools/usage-dashboard/usage_dashboard/api/__init__.py` (wire real collector task into lifespan)
- `tools/usage-dashboard/tests/test_cli_run.py` (new)

**Files NOT touched**: `usage_dashboard.py` (top-level shim), legacy `server.py` (still referenced by `serve` until 1.9)

---

## 🎯 Goal

`python3 usage_dashboard.py run` now starts uvicorn with the FastAPI app
and runs the asyncio collector inside the app's lifespan. The `serve`
subcommand switches to uvicorn too (no more `BaseHTTPRequestHandler`).
The `collect` subcommand uses the new async collector in a standalone loop.

After this ticket, the legacy `server.py` is **no longer invoked by any
CLI subcommand** but is not yet deleted (Ticket 1.9 deletes it).

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. CLI tests use `subprocess` to spawn real processes.

---

## 🪜 Steps

### Step 1 — Red: CLI tests

```python
# tests/test_cli_run.py
import subprocess
import sys
import time
import socket
import httpx
import pytest
from pathlib import Path

PKG = Path(__file__).resolve().parents[2]


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _wait_http(url, timeout=10):
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
    port = _free_port()
    env = {"USAGE_DASHBOARD_DATA_DIR": str(tmp_path),
           "USAGE_DASHBOARD_PORT": str(port),
           "CLIPROXY_BASE_URL": "http://127.0.0.1:1",  # unreachable; collector will fail but not crash
           "CLIPROXY_MANAGEMENT_KEY": "dummy"}
    proc = subprocess.Popen(
        [sys.executable, "usage_dashboard.py", "run"],
        cwd=str(PKG), env={**env, "PATH": "/usr/bin:/bin"},
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    try:
        resp = _wait_http(f"http://127.0.0.1:{port}/api/v1/health")
        assert resp.status_code == 200
        body = resp.json()
        # Collector has attempted at least one poll (will be failed=True
        # because the URL is unreachable)
        assert "last_poll_ok" in body
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_serve_starts_uvicorn_only(tmp_path):
    port = _free_port()
    env = {"USAGE_DASHBOARD_DATA_DIR": str(tmp_path),
           "USAGE_DASHBOARD_PORT": str(port)}
    proc = subprocess.Popen(
        [sys.executable, "usage_dashboard.py", "serve"],
        cwd=str(PKG), env={**env, "PATH": "/usr/bin:/bin"},
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    try:
        resp = _wait_http(f"http://127.0.0.1:{port}/api/v1/health")
        assert resp.status_code == 200
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_legacy_help_still_works():
    result = subprocess.run(
        [sys.executable, "usage_dashboard.py", "--help"],
        cwd=str(PKG), capture_output=True, text=True,
        env={"PATH": "/usr/bin:/bin"},
    )
    assert result.returncode == 0
    for sub in ("init", "collect", "serve", "run", "report", "compact", "quota"):
        assert sub in result.stdout
```

**Verify red**:
```bash
uv run pytest tests/test_cli_run.py -v
```
The `run` test will hang or fail because the CLI still launches the legacy
server.

Commit: `test(cli): red — run starts uvicorn + collector`

### Step 2 — Green: rewrite __main__.py

Replace the legacy `cmd_run` and `cmd_serve` with uvicorn-based launchers.
Keep `cmd_init`, `cmd_report`, `cmd_compact`, `cmd_quota` mostly as-is
(they don't start a server).

```python
# usage_dashboard/__main__.py
"""CLI entrypoint for the usage dashboard."""
import argparse
import asyncio
import datetime as dt
import json
import logging

import uvicorn

from . import config as cfg_mod
from . import storage as st
from . import collector_async as ca
from . import query as qy
from .api import app

log = logging.getLogger(__name__)


def _init(cfg):
    version = st.init_schema(cfg)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    return version


def cmd_init(cfg):
    version = _init(cfg)
    print(cfg_mod.db_path_for(cfg))
    print(f"schema_version={version}")


def cmd_serve(cfg):
    _init(cfg)
    app.state.cfg = cfg
    uvicorn.run(app, host=cfg["dashboard_host"], port=int(cfg["dashboard_port"]),
                log_config=None)


def cmd_run(cfg):
    _init(cfg)
    app.state.cfg = cfg

    @app.on_event("startup")
    async def _start_collector():
        async with ca.AsyncCollectorLock(cfg):
            await ca.collect_forever(cfg)

    uvicorn.run(app, host=cfg["dashboard_host"], port=int(cfg["dashboard_port"]),
                log_config=None)


def cmd_collect(cfg):
    _init(cfg)
    asyncio.run(_collect_standalone(cfg))


async def _collect_standalone(cfg):
    async with ca.AsyncCollectorLock(cfg):
        await ca.collect_forever(cfg)


def cmd_report(cfg, args):
    _init(cfg)
    qs = {"range": [args.range] if args.range else [],
          "from": [args.iso_from] if args.iso_from else [],
          "to": [args.iso_to] if args.iso_to else []}
    print(json.dumps(qy.query_summary(cfg, qs), ensure_ascii=False, indent=2))


def cmd_compact(cfg, args):
    _init(cfg)
    with st.db_connect(cfg) as conn:
        cur = conn.execute(
            "DELETE FROM usage_events WHERE ts_epoch < ?",
            ((dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=args.days)).timestamp(),),
        )
        conn.commit()
        print(f"deleted {cur.rowcount} rows older than {args.days} days")


def cmd_quota(cfg, args):
    if not cfg.get("quota_enabled"):
        print(json.dumps({"note": "quota feature disabled by default; set quota_enabled=true"}))
        return
    # Future: implement async quota refresh


def build_parser():
    p = argparse.ArgumentParser(description="CLIProxyAPI usage dashboard")
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("init")
    c = sub.add_parser("collect")
    s = sub.add_parser("serve")
    r = sub.add_parser("run")
    rep = sub.add_parser("report")
    rep.add_argument("range", nargs="?")
    rep.add_argument("--from", dest="iso_from")
    rep.add_argument("--to", dest="iso_to")
    rep.add_argument("--model", action="append")
    comp = sub.add_parser("compact")
    comp.add_argument("--days", type=int, default=30)
    sub.add_parser("quota")
    return p


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    cfg = cfg_mod.load_config()
    if args.cmd == "init":
        cmd_init(cfg)
    elif args.cmd == "collect":
        cmd_collect(cfg)
    elif args.cmd == "serve":
        cmd_serve(cfg)
    elif args.cmd == "run":
        cmd_run(cfg)
    elif args.cmd == "report":
        cmd_report(cfg, args)
    elif args.cmd == "compact":
        cmd_compact(cfg, args)
    elif args.cmd == "quota":
        cmd_quota(cfg, args)


if __name__ == "__main__":
    main()
```

Also update `usage_dashboard/api/__init__.py` to replace the `_collector_stub`
task with a no-op for the `serve` case (collector is started by `cmd_run`
via `@app.on_event("startup")`):

```python
# Remove the _collector_stub from lifespan. Lifespan only sets up state;
# collector task is registered by cmd_run via on_event("startup").
@asynccontextmanager
async def lifespan(app: FastAPI):
    log.info("usage-dashboard FastAPI starting")
    yield
    log.info("usage-dashboard FastAPI stopped")
```

**Verify green**:
```bash
uv run pytest tests/test_cli_run.py -v
uv run pytest tests/ -v
uv run pytest test_usage_dashboard.py -v
```

Commit: `feat(cli): run/serve via uvicorn + asyncio collector — green`

### Step 3 — Refactor: graceful collector shutdown

Register a shutdown handler that cancels the collector task cleanly:

```python
@app.on_event("shutdown")
async def _stop_collector():
    # If cmd_run registered a collector task, cancel it.
    task = app.state.__dict__.get("collector_task")
    if task and not task.done():
        task.cancel()
        try:
            await task
        except asyncio.CancelledError:
            pass
```

And update `cmd_run` to store the task:

```python
async def _start_collector_with_lock():
    async with ca.AsyncCollectorLock(cfg):
        await ca.collect_forever(cfg)

app.state.collector_task = asyncio.create_task(_start_collector_with_lock())
```

**Verify refactor**:
```bash
uv run pytest tests/test_cli_run.py -v
# Also: start `python usage_dashboard.py run`, Ctrl-C, confirm clean exit
```

Commit: `feat(cli): graceful collector shutdown on SIGTERM`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/__main__.py tests/test_cli_run.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/__main__.py` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_cli_run.py -v` |
| 5 | Integration Tests | `uv run pytest tests/ -v` (full new suite) |
| 6 | Functional Tests | Manual: `python usage_dashboard.py run` starts on `:8320`, `/legacy` works, `/api/v1/health` works |
| 7 | Contract Tests | `uv run pytest test_usage_dashboard.py -v` — legacy suite untouched and green |
| 8 | E2E | N/A |
| 9 | Code Review | Confirm legacy `server.py` is no longer imported anywhere |

All green → Ticket 1.9.
