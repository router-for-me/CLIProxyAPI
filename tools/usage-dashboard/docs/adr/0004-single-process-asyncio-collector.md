# ADR 0004: Single-process uvicorn + asyncio collector

**Status**: Accepted
**Date**: 2026-07-27

## Context

The current deployment shape is `python3 usage_dashboard.py run`, which
starts one Python process that runs both the collector loop (polls the CPA
usage queue over HTTP and writes rows to SQLite) and the HTTP server (serves
the dashboard API + HTML). This is documented in `README.md`,
`docs/deployment.md`, a systemd unit, and a docker-compose snippet.
Operators are accustomed to a single process and a single port.

The rewrite moves the HTTP server from `BaseHTTPRequestHandler` to
FastAPI + uvicorn (asyncio). The collector is a polling loop with a
2-second sleep. The question is whether to keep them in one process.

There is an additional legacy concern: the current collector uses `fcntl`
file locking (`CollectorLock`) to prevent two collector processes from
inserting duplicate rows. If the collector moves into the asyncio task,
the lock is no longer needed for the common case but still matters if an
operator accidentally starts two sidecars against the same DB.

## Decision

Keep the single-process model. The FastAPI app's `lifespan` context
manager starts the collector as a single `asyncio.create_task` on startup
and cancels it on shutdown. The collector uses `httpx.AsyncClient` for
HTTP and `aiosqlite` (or SQLModel's async session) for inserts.

- **Lifecycle**: `python3 usage_dashboard.py run` → uvicorn serve →
  `lifespan` startup → collector task created → runs until shutdown signal.
- **`init` / `collect` / `serve`** CLI subcommands still exist as
  escape hatches:
  - `collect` runs the collector loop standalone (no HTTP server).
  - `serve` runs uvicorn without the collector (read-only dashboard).
  - `run` does both (default operator path).
- **fcntl file lock**: retained. Before the asyncio collector task starts
  polling, it acquires the same `CollectorLock`. Two sidecars pointed at
  the same DB → the second one's collector task fails fast with a clear
  log message; its HTTP server still serves stale data with a degraded
  health indicator.
- **Poll interval**: unchanged, 2 seconds default, configurable.

## Alternatives considered

- **Two processes (server + collector) supervised by systemd.** Rejected:
  doubles the operational surface (two units, two log streams, two
  restart policies) for no benefit. The two halves share the same SQLite
  file and have no independent scaling dimension.
- **Drop the fcntl lock, rely on SQLite UNIQUE constraint on
  `(request_id)` for idempotency.** Rejected: the constraint does make a
  double-insert safe, but silent duplicate work still wastes HTTP quota
  against the management API. The lock fails fast and loud.
- **Background task framework (Celery / APScheduler / arq).** Rejected:
  one polling loop does not justify a task queue. A raw `asyncio.Task`
  is the right-sized tool.
- **Run the collector in a thread, not an asyncio task.** Rejected: the
  rest of the app is asyncio (uvicorn + httpx + async SQLModel). Mixing
  threading in is complexity for no gain.

## Consequences

**Positive**

- Deployment model is unchanged from the operator's view: one process,
  one port, one log stream, one systemd unit, one container.
- The collector and the HTTP server share the same SQLModel engine and
  connection pool; no cross-process state to reconcile.
- Graceful shutdown: `lifespan` cancels the collector task before
  uvicorn closes, so in-flight inserts complete.

**Negative**

- A crash in the collector task takes down the HTTP server (and vice
  versa). Mitigated: the collector has its own try/except around each
  poll cycle, logs the error, sets `last_poll_ok=false`, and continues.
  A fatal error in the HTTP server (e.g., bind failure) should take the
  whole process down — that is correct.
- `asyncio`'s event loop is shared; a blocking operation in the
  collector stalls the HTTP server. The collector must use async
  HTTP (`httpx.AsyncClient`) and async DB I/O throughout. This is the
  standard FastAPI discipline.

**Neutral**

- `CollectorLock` (fcntl) is retained but only acquired by the collector
  task, not by ad-hoc SQL writes from HTTP handlers.
