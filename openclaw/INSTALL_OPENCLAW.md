# Install into OpenClaw

This fork includes an OpenClaw-compatible autonomy layer for operating CLIProxyAPI as a managed local service.

## Included

- `skills/cliproxyapi-autonomy/SKILL.md`
- `scripts/install_openclaw_skill.sh`

## Install options

### Option A — shared skill install

```bash
bash scripts/install_openclaw_skill.sh --shared
```

This installs the skill into:

```text
~/.openclaw/skills/cliproxyapi-autonomy/
```

### Option B — workspace-local install

```bash
bash scripts/install_openclaw_skill.sh --workspace /path/to/your/openclaw/workspace
```

This installs the skill into:

```text
/path/to/your/openclaw/workspace/skills/cliproxyapi-autonomy/
```

## What this autonomy layer encodes

- heartbeat / self-heal governance model
- quiet healthy output (`HEARTBEAT_OK`)
- failure / recovery notification policy
- cron attachment pattern
- verification order
- separation between service repository and OpenClaw workspace assets
- environment-variable fallback instead of machine-path hardcoding

## Recommended structure for actual deployment

Keep service code/config in this repository.
Keep local runbooks / scripts / cron wiring in your OpenClaw workspace.


## Sync fork with upstream

This fork keeps OpenClaw-specific autonomy files while tracking upstream service updates.

Use:

```bash
bash scripts/sync_upstream.sh
```

Behavior:
- fetch `upstream` and `origin`
- merge `upstream/main` into local `main`
- push the merged result back to `origin/main`

If you only want a local dry run without push:

```bash
PUSH=0 bash scripts/sync_upstream.sh
```


## Included templates

This fork also ships example operational templates:

- `scripts/heartbeat_example.sh`
- `scripts/self_heal_example.sh`
- `openclaw/CRON_TEMPLATE.md`

These are templates, not machine-specific production scripts. Adjust environment variables and paths for your own deployment.


## Bootstrap the project

For a full step-by-step handoff from plain fork to OpenClaw-managed project, see:

- `openclaw/PROJECT_BOOTSTRAP.md`
- `LOCAL_OPERATIONS.md`


## Self-heal policy

See `openclaw/SELF_HEAL_RUNBOOK.md` for the boundary between automatic recovery, alert-only mode, and mandatory human intervention.


## Unified navigation

For the full OpenClaw ops map, see `openclaw/OPS_INDEX.md`.
