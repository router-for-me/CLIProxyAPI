# OpenClaw Project Bootstrap

Use this guide to turn this fork into an OpenClaw-managed, self-healing local service project.

## Goal

After bootstrap, the project should have:
- installable OpenClaw skill
- heartbeat check
- self-heal entrypoint
- optional cron attachment
- documented local-operations boundary

## Step 1 — clone your fork

```bash
git clone https://github.com/<your-user>/CLIProxyAPI.git
cd CLIProxyAPI
```

## Step 2 — install the OpenClaw skill

### Shared install
```bash
bash scripts/install_openclaw_skill.sh --shared
```

### Workspace-local install
```bash
bash scripts/install_openclaw_skill.sh --workspace /path/to/your/openclaw/workspace
```

## Step 3 — define local operations

Copy or edit:

- `LOCAL_OPERATIONS.md`

Document your machine-local values instead of hardcoding them into shared tracked files.

Recommended variables:
- `CLI_PROXY_CONFIG_PATH`
- `CLI_PROXY_AUTH_PATH`
- `CLI_PROXY_LOG_PATH`
- `CLI_PROXY_HEALTH_URL`
- `CLI_PROXY_COMPOSE_CMD`
- `CLI_PROXY_SERVICE_NAME`

## Step 4 — verify the service manually

```bash
docker compose config
docker compose ps
curl -fsS http://127.0.0.1:8317/health
bash scripts/heartbeat_example.sh
```

If unhealthy:

```bash
bash scripts/self_heal_example.sh
```

## Step 5 — attach OpenClaw cron (optional but recommended)

Use:

- `openclaw/CRON_TEMPLATE.md`

Typical flow:

```bash
openclaw cron add ...
openclaw cron edit <JOB_ID> --failure-alert ...
```

## Step 6 — keep the fork synced with upstream

```bash
bash scripts/sync_upstream.sh
```

Dry run without push:

```bash
PUSH=0 bash scripts/sync_upstream.sh
```

## Operational boundary

- This repository owns service code, shared templates, and fork-level OpenClaw assets.
- Your personal OpenClaw workspace owns machine-local runbooks, memory, cron IDs, and delivery destinations.
- Do not bury private host-specific values into shared templates unless they are intentionally generic defaults.

## Minimal success criteria

You are done when:
- heartbeat returns `HEARTBEAT_OK` in healthy state
- self-heal can be invoked as a recovery entrypoint
- local overrides are documented in `LOCAL_OPERATIONS.md`
- OpenClaw skill is installed
- optional cron can run without spamming on healthy status


## Self-heal decision boundary

Before enabling aggressive automation, read `openclaw/SELF_HEAL_RUNBOOK.md`.
