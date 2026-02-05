# CLIProxyAPI Fork

Fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) with SQLite persistence for usage statistics.

## Changes from Upstream

### SQLite Usage Statistics Persistence

Added SQLite-based persistence for usage statistics to survive container restarts.

**New/Modified Files:**
- `internal/usage/sqlite_plugin.go` - SQLite persistence plugin
- `internal/usage/sqlite_plugin_test.go` - Unit tests
- `go.mod` - Added `go-sqlite3` dependency
- `Dockerfile` - Enabled CGO for SQLite support
- `docker-compose.yml` - Added data volume and environment variable

**Usage:**

```bash
# Enable SQLite persistence via environment variable
export USAGE_SQLITE_PATH=/path/to/usage.db

# Or use docker-compose (auto-configured)
docker compose up -d
```

Data is persisted to `./data/usage.db` and automatically restored on restart.

## Upstream Sync

This fork automatically syncs with the upstream repository and builds Docker images.

## License

MIT License - see [LICENSE](LICENSE)
