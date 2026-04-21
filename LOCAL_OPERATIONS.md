# Local Operations Template

This file is for machine-local operational conventions.

Use it to document:
- local config file paths
- compose overrides
- service names
- log locations
- deployment-specific environment variables
- notification / cron destinations

## Rules

1. Prefer environment variables over hard-coded machine paths.
2. If a local absolute path is unavoidable, explain why it exists.
3. Keep local overlays documented here rather than silently modifying shared files.
4. Re-run the validation sequence after any local ops change.

## Suggested sections

### Config paths
- `CLI_PROXY_CONFIG_PATH=`
- `CLI_PROXY_AUTH_PATH=`
- `CLI_PROXY_LOG_PATH=`

### Validation order
1. `docker compose config`
2. `docker compose ps`
3. `curl -fsS http://127.0.0.1:8317/health`
4. `bash scripts/heartbeat_example.sh`
5. `bash scripts/self_heal_example.sh` if needed

### Cron / notification
- OpenClaw cron job id:
- Failure alert destination:
- Local notification mode:
