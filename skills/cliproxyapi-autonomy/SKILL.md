---
name: cliproxyapi-autonomy
description: Operate, verify, and govern CLIProxyAPI under OpenClaw with heartbeat, self-heal, cron, and boundary rules.
user-invocable: true
metadata: {"openclaw": {"emoji": "🛡️", "always": false}}
---

# CLIProxyAPI Autonomy

## Purpose
Use this skill when maintaining a local CLIProxyAPI deployment under OpenClaw governance.

## Core rules
1. Prefer environment-variable indirection over hard-coded machine paths.
2. Separate clearly:
   - the service repository
   - the OpenClaw workspace that hosts runbooks / heartbeat / self-heal / cron docs
3. Default verification order:
   - `docker compose config`
   - `docker compose ps`
   - `curl -fsS http://127.0.0.1:8317/health`
   - heartbeat script
   - self-heal script if needed
   - cron run history if cron is attached
4. Healthy state should remain quiet (`HEARTBEAT_OK`).
5. Only notify on failure / recovery / needs-attention states.
6. If introducing machine-local config defaults, use env fallback instead of direct hardcoding.
7. Do not mix local operational overlays with upstream service logic without documenting the boundary.

## Recommended assets
- Service repo: `docker-compose.yml`, `.env`, `LOCAL_OPERATIONS.md`
- OpenClaw workspace: runbook, memory note, heartbeat script, self-heal script, cron attachment docs

## Install note
Copy this directory into either:
- `<workspace>/skills/`
- `~/.openclaw/skills/`
