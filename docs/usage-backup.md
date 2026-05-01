# Usage Backup Image

`Dockerfile.stats` builds a CLIProxyAPI image that keeps the in-memory usage statistics across container restarts. It wraps the normal server process, periodically exports `/v0/management/usage/export`, and imports the saved snapshot before serving traffic again.

The image supports three backup modes:

- PostgreSQL-compatible databases, including Supabase Postgres.
- S3-compatible object storage, including Cloudflare R2 and path-style object stores.
- Local file only, useful when `/CLIProxyAPI/data` is mounted as a persistent volume.

## Build

```sh
docker build -f Dockerfile.stats -t cli-proxy-api:stats .
```

## Required Management API Key

Usage import and export use the Management API, so the container must receive the same key configured in `remote-management.secret-key`:

```sh
MANAGEMENT_PASSWORD=your-management-key
```

If the key is missing, the server still starts, but usage backup import/export is disabled.

## PostgreSQL / Supabase

Set either `PGSTORE_DSN` or `DATABASE_URL`. `DATABASE_URL` works with the connection string provided by Supabase.

```sh
STATS_BACKEND=postgres
DATABASE_URL=postgresql://postgres:password@db.example.supabase.co:5432/postgres
MANAGEMENT_PASSWORD=your-management-key
```

The wrapper creates a small `usage_backup` table if it does not already exist:

```sql
CREATE TABLE IF NOT EXISTS usage_backup (
  id INT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Set `PGSTORE_SCHEMA` to store the table in a specific schema. The schema name must contain only letters, numbers, and underscores, and it must not start with a number.

## S3-Compatible Object Storage

Set all object storage variables below. The wrapper stores the snapshot at `usage/usage_backup.json` in the configured bucket.

```sh
STATS_BACKEND=s3
OBJECTSTORE_ENDPOINT=https://example.r2.cloudflarestorage.com
OBJECTSTORE_BUCKET=cliproxyapi
OBJECTSTORE_ACCESS_KEY=access-key
OBJECTSTORE_SECRET_KEY=secret-key
MANAGEMENT_PASSWORD=your-management-key
```

Cloudflare R2 endpoints use the `Cloudflare` rclone provider. Other endpoints default to the generic S3 provider with path-style access enabled.

## Other Settings

- `USAGE_BACKUP_INTERVAL`: export interval in seconds. Defaults to `300`.
- `USAGE_BACKUP_PORT`: management API port override. When omitted, the wrapper uses `PORT`, then common config file paths, then `8317`.
- `STATS_BACKEND`: optional backend override. Supported values are `postgres`, `postgresql`, `pgsql`, `s3`, `objectstore`, `local`, `none`, and `disabled`.

When no database or complete object storage configuration is present, the wrapper uses local-only backups at `/CLIProxyAPI/data/usage_backup.json`.
