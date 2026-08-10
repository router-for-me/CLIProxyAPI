# Token Usage Plugin Data Persistence Design

## Goal

Keep CAP Token Usage Tracker history across CLIProxyAPI container recreation.

## Root Cause

The plugin stores its bbolt database at `/CLIProxyAPI/data/token-usage-tracker.db`, while the current Compose service persists configuration, authentication files, logs, and plugin binaries but not `/CLIProxyAPI/data`. Recreating the container therefore creates a new empty database.

## Chosen Design

Use a host bind mount dedicated to plugin runtime data:

```yaml
- ${CLI_PROXY_DATA_PATH:-./data}:/CLIProxyAPI/data
```

The deployed CLIProxyAPI configuration will explicitly keep the tracker on that mounted path:

```yaml
plugins:
  configs:
    cap-token-usage-tracker:
      enabled: true
      data_path: /CLIProxyAPI/data/token-usage-tracker.db
      sync_on_record: true
```

The existing plugin-store metadata remains unchanged.

## Repository Changes

- Add the `/CLIProxyAPI/data` bind mount to the main API service in `docker-compose.yml`.
- Make `docker-build.sh` create the host `data` directory with the other persistent directories.
- Ignore generated contents under `data/` in `.gitignore`.
- Update the Docker deployment documentation to list `data/` as persistent plugin state.

The real deployment `config.yaml` is not present as a file in this workspace, so its two plugin settings will be supplied as an exact configuration snippet rather than written into `config.example.yaml`.

## Existing Data Migration

Before the first container recreation with the new bind mount, stop the existing API container cleanly and copy `/CLIProxyAPI/data/token-usage-tracker.db` to the host `./data/` directory. Mounting an empty host directory first would hide the existing container-layer database.

## Validation

This is a configuration-only change, so validation replaces unit-test-first development:

- `docker compose config` must render successfully.
- The rendered main service must contain `/CLIProxyAPI/data` as a volume destination.
- The deployment script must create `data/`.
- Git must ignore `data/token-usage-tracker.db`.
- Existing unrelated working-tree files must remain untouched.

## Out of Scope

- Upgrading the installed plugin from v1.0.0.
- Changing model-price configuration or the dashboard expense-fetch behavior.
- Reconfiguring the public reverse proxy or plugin endpoint authentication.
