# Persistent Usage Statistics and Legacy Import Guide

This guide explains how to:

- enable durable usage statistics with PostgreSQL
- run the Docker Compose stack
- migrate old in-memory usage exports into the new persistent store
- avoid duplicate imports when you have overlapping export files

## What changed

CLIProxyAPI can now store usage statistics in two modes:

- `memory` — the previous behavior; usage data lives only in memory and is lost on restart
- `postgres` — new persistent behavior; usage events are stored in PostgreSQL and survive restarts

The management usage endpoints stay compatible with the previous management center shape:

- `GET /v0/management/usage`
- `GET /v0/management/usage/export`
- `POST /v0/management/usage/import`

## Configuration

Usage recording is controlled by two settings:

```yaml
usage-statistics-enabled: true

usage-storage:
  driver: "postgres"
  database-url: "postgres://postgres:postgres@postgres:5432/cliproxyapi?sslmode=disable"
  auto-migrate: true
```

### Settings

#### `usage-statistics-enabled`

- `false`: usage recording is disabled completely
- `true`: usage recording is enabled

#### `usage-storage.driver`

- `memory`: keep the old in-process behavior
- `postgres`: persist usage events in PostgreSQL

#### `usage-storage.database-url`

Required when `driver: postgres`.

Example:

```yaml
usage-storage:
  driver: "postgres"
  database-url: "postgres://postgres:postgres@postgres:5432/cliproxyapi?sslmode=disable"
  auto-migrate: true
```

#### `usage-storage.auto-migrate`

- `true`: automatically create the required usage table and indexes on startup
- `false`: schema must already exist before the server starts using PostgreSQL storage

## Docker Compose setup

The repository `docker-compose.yml` now includes a PostgreSQL service with a persistent volume.

### 1. Prepare `config.yaml`

Start from `config.example.yaml` and update at least these sections:

```yaml
remote-management:
  allow-remote: true
  secret-key: "your-management-password"

usage-statistics-enabled: true

usage-storage:
  driver: "postgres"
  database-url: "postgres://postgres:postgres@postgres:5432/cliproxyapi?sslmode=disable"
  auto-migrate: true
```

Notes:

- `allow-remote: true` is needed if you want to access the management portal through Docker-published ports from your browser.
- `secret-key` is the password you use for the management portal and management API.
- On startup, the server may rewrite the plaintext `secret-key` to a bcrypt hash. That is expected. You still log in with the original plaintext password.

### 2. Start the stack

```bash
docker compose up -d
```

### 3. Verify containers

```bash
docker compose ps
docker compose logs -f cli-proxy-api
```

PostgreSQL should start first, then CLIProxyAPI should connect to it and create the usage schema automatically when `auto-migrate: true`.

### 4. Persistent data location

PostgreSQL data is stored in the Docker volume declared in `docker-compose.yml`:

```yaml
volumes:
  cli_proxy_api_postgres:
```

That means usage statistics remain available across container restarts and re-creations unless you remove that volume.

## Migration from old in-memory exports

If you used the old in-memory behavior, you may already have one or many files exported from:

```text
GET /v0/management/usage/export
```

This is common when you exported every 30 minutes. Those files usually overlap heavily.

### Important migration rule

Do **not** assume the newest export file is enough.

If the old server restarted between exports, usage may have reset to zero and then started accumulating again. In that case:

- newer files may miss older requests
- older files may still contain unique usage history
- many files will contain duplicates of the same requests

The migration helper is designed for this exact situation.

## Import helper command

The repository includes a command that imports one or more legacy export files into the current server:

```bash
go run ./cmd/import_usage_exports --server-url http://localhost:8317 --management-key "YOUR_PASSWORD" exports/*.json
```

It works with:

- individual files
- glob patterns like `exports/*.json`
- directories containing `.json` export files

The command processes files in stable sorted order and sends them to:

```text
POST /v0/management/usage/import
```

### Why duplicates are safe

The new persistent import path is idempotent at the event level.

That means:

- overlapping exports can be imported safely
- already imported request events are skipped
- unique request events from older files are still preserved

So the correct migration strategy is usually:

1. collect all historical export files
2. import all of them
3. let the server skip duplicates automatically

## Dry-run mode

Before sending anything to the server, you can estimate overlap locally:

```bash
go run ./cmd/import_usage_exports --dry-run exports/*.json
```

Example output:

```text
Resolved 96 input files
Source requests: 48231
Unique merged requests: 13754
Deduplicated overlaps: 34477
Dry run complete. No import requests were sent.
```

This is useful when you want to understand how much overlap exists before running the real import.

## Real import examples

### Import all JSON files from a directory

```bash
go run ./cmd/import_usage_exports \
  --server-url http://localhost:8317 \
  --management-key "YOUR_PASSWORD" \
  ./usage-exports
```

### Import a glob of export files

```bash
go run ./cmd/import_usage_exports \
  --server-url http://localhost:8317 \
  --management-key "YOUR_PASSWORD" \
  ./usage-exports/*.json
```

### Import a few specific files

```bash
go run ./cmd/import_usage_exports \
  --server-url http://localhost:8317 \
  --management-key "YOUR_PASSWORD" \
  ./usage-exports/export-2026-03-01.json \
  ./usage-exports/export-2026-03-02.json
```

## Expected import output

For each file, the command prints something like:

```text
Imported /path/to/export-001.json -> added=120 skipped=0 total_requests=120
Imported /path/to/export-002.json -> added=12 skipped=108 total_requests=132
Imported /path/to/export-003.json -> added=0 skipped=120 total_requests=132
```

At the end it prints a summary:

```text
Imported 3 files successfully
Server added total: 132
Server skipped total: 228
Server total requests: 132
Server failed requests: 4
```

Interpretation:

- `added`: new unique request events inserted into the current usage store
- `skipped`: duplicate request events that were already present
- `total_requests`: current total requests visible in the target server after that import step

## Recommended migration workflow

### Scenario: old server with in-memory stats, new server with PostgreSQL

1. Start the new server with PostgreSQL-backed usage persistence enabled.
2. Make sure you can log into the management portal.
3. Gather **all** old usage export files into one directory.
4. Run a dry run first:

   ```bash
   go run ./cmd/import_usage_exports --dry-run ./usage-exports
   ```

5. Run the real import:

   ```bash
   go run ./cmd/import_usage_exports \
     --server-url http://localhost:8317 \
     --management-key "YOUR_PASSWORD" \
     ./usage-exports
   ```

6. Verify in the management portal that the usage totals look correct.
7. Optionally call `GET /v0/management/usage/export` on the new server to create a fresh consolidated backup.

## Troubleshooting

### "Access denied" or import authentication errors

Make sure:

- `remote-management.secret-key` is set
- you use the plaintext password with `--management-key`
- `remote-management.allow-remote: true` is set if you are accessing through Docker or another remote client

### "missing management key" or "invalid management key"

The import command sends the key as `X-Management-Key`. Check the value you passed to `--management-key`.

### PostgreSQL persistence not working

Check these settings:

```yaml
usage-statistics-enabled: true
usage-storage:
  driver: "postgres"
  database-url: "postgres://postgres:postgres@postgres:5432/cliproxyapi?sslmode=disable"
  auto-migrate: true
```

Then inspect logs:

```bash
docker compose logs -f postgres
docker compose logs -f cli-proxy-api
```

### Can I rerun the import command?

Yes.

Re-running the same files is safe. Existing events are skipped as duplicates.

### Can I import while the server is already running and collecting new events?

Yes. The import goes through the normal management API and merges into the active usage store.

## Quick reference

### Enable persistent statistics

```yaml
usage-statistics-enabled: true
usage-storage:
  driver: "postgres"
  database-url: "postgres://postgres:postgres@postgres:5432/cliproxyapi?sslmode=disable"
  auto-migrate: true
```

### Start containers

```bash
docker compose up -d
```

### Dry run import

```bash
go run ./cmd/import_usage_exports --dry-run ./usage-exports
```

### Real import

```bash
go run ./cmd/import_usage_exports \
  --server-url http://localhost:8317 \
  --management-key "YOUR_PASSWORD" \
  ./usage-exports
```
