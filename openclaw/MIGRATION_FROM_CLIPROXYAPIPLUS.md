# Migration from local CLIProxyAPIPlus deployment

This guide describes how to migrate a live local setup from:

- `/Users/macbook/CLIProxyAPIPlus`

to the new main repository layout:

- `luyuehm/CLIProxyAPI` as the main service/OpenClaw repository
- optional `luyuehm/Cli-Proxy-API-Management-Center` as companion UI

## Verified current local state

The currently active local deployment is typically identified by:
- project directory: `/Users/macbook/CLIProxyAPIPlus`
- compose file: `/Users/macbook/CLIProxyAPIPlus/docker-compose.yml`
- heartbeat/self-heal scripts pointing to that directory
- OpenClaw cron using the workspace heartbeat wrapper, which defaults to that directory

## Migration goal

After migration:
- the live service path should be switched deliberately
- heartbeat/self-heal defaults should point to the new main repository checkout
- docs/runbooks should no longer identify `CLIProxyAPIPlus` as the active deployment path
- optional Management Center remains companion-only and does not replace the main service repo

## Safe migration sequence

### Phase 0 — Verify current live state

Before changing anything, verify:
1. `docker compose ps` in `/Users/macbook/CLIProxyAPIPlus`
2. health/port check on the currently used port
3. current OpenClaw cron run status for `cliproxyapi.heartbeat`
4. current config/auth/log paths

### Phase 1 — Prepare new main repository checkout

1. Clone or update `luyuehm/CLIProxyAPI`
2. Decide target live path (recommended: a clean service path, not an export temp directory)
3. Copy or map required runtime assets:
   - config
   - auth files
   - env file
   - logs path strategy
4. Validate compose/runtime behavior in the new location without cutting over cron yet

### Phase 2 — Dry-run operational scripts against the new path

Test with environment overrides instead of editing live defaults first:

```bash
CLI_PROXY_PROJECT_DIR=/path/to/new/CLIProxyAPI CLI_PROXY_NOTIFY=0 bash /Users/macbook/.openclaw/workspaces/main/scripts/cliproxyapi_heartbeat.sh
```

Then:

```bash
CLI_PROXY_PROJECT_DIR=/path/to/new/CLIProxyAPI CLI_PROXY_NOTIFY=0 bash /Users/macbook/.openclaw/workspaces/main/scripts/cliproxyapi_self_heal.sh
```

Do not switch cron until these pass.

### Phase 3 — Cut over OpenClaw automation

Once the new path is verified:
1. update the active project path used by heartbeat/self-heal
2. run one manual heartbeat
3. run one manual cron execution
4. confirm healthy output is still quiet (`HEARTBEAT_OK`)
5. confirm failure alert path still works as expected

### Phase 4 — Update docs and governance truth

After cutover, update:
- local runbooks
- `TOOLS.md`
- any active workspace docs that still point to `/Users/macbook/CLIProxyAPIPlus`
- migration note / date / rollback path

### Phase 5 — Keep rollback available briefly

Do not immediately delete the old path.

Keep:
- old project directory intact for rollback
- old config snapshot
- old env snapshot
- recent compose/log evidence

## What should *not* change

- the Management Center remains optional
- the OpenClaw autonomy layer remains attached to the main service repository
- healthy cron output should remain quiet
- alerting semantics should not regress during migration

## Cutover success criteria

Migration is successful only when:
- live service is running from the new repository path
- heartbeat targets the new path and returns `HEARTBEAT_OK`
- self-heal targets the new path and works as expected
- docs no longer misidentify `CLIProxyAPIPlus` as the active deployment path
- rollback path is documented
