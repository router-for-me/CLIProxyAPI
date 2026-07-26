"""SQLite storage: connection lifecycle, schema, migrations, collector state."""
import contextlib
import json
import sqlite3

from . import config as cfg_mod

SCHEMA_VERSION = 4

_TARGET_COLUMNS = (
    "id INTEGER PRIMARY KEY AUTOINCREMENT",
    "event_key TEXT NOT NULL UNIQUE",
    "timestamp TEXT NOT NULL",
    "ts_epoch REAL NOT NULL",
    "utc_date TEXT NOT NULL",
    "utc_hour TEXT NOT NULL",
    "request_id TEXT",
    "account_hash TEXT",
    "provider TEXT",
    "model TEXT",
    "alias TEXT",
    "endpoint TEXT",
    "auth_type TEXT",
    "executor_type TEXT",
    "service_tier TEXT",
    "reasoning_effort TEXT",
    "failed INTEGER NOT NULL DEFAULT 0",
    "fail_status INTEGER DEFAULT 0",
    "latency_ms INTEGER DEFAULT 0",
    "ttft_ms INTEGER DEFAULT 0",
    "input_tokens INTEGER DEFAULT 0",
    "output_tokens INTEGER DEFAULT 0",
    "reasoning_tokens INTEGER DEFAULT 0",
    "cached_tokens INTEGER DEFAULT 0",
    "cache_read_tokens INTEGER DEFAULT 0",
    "cache_creation_tokens INTEGER DEFAULT 0",
    "total_tokens INTEGER DEFAULT 0",
)
_COLUMN_NAMES = tuple(c.split()[0] for c in _TARGET_COLUMNS)


@contextlib.contextmanager
def db_connect(cfg):
    """Yield a configured SQLite connection and CLOSE it on exit."""
    cfg_mod.ensure_dirs(cfg)
    conn = sqlite3.connect(cfg_mod.db_path_for(cfg))
    conn.row_factory = sqlite3.Row
    try:
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA foreign_keys=ON")
        conn.execute("PRAGMA busy_timeout=5000")
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


def _has_column(conn, table, column):
    cols = {r[1] for r in conn.execute(f"PRAGMA table_info({table})")}
    return column in cols


def _has_table(conn, table):
    return conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (table,)
    ).fetchone() is not None


def _ensure_schema_meta(conn):
    conn.execute("CREATE TABLE IF NOT EXISTS schema_meta(key TEXT PRIMARY KEY, value TEXT)")


def _current_version(conn):
    row = conn.execute("SELECT value FROM schema_meta WHERE key='version'").fetchone()
    if row:
        return int(row["value"])
    return 0


def current_version(cfg):
    with db_connect(cfg) as conn:
        _ensure_schema_meta(conn)
        return _current_version(conn)


def _set_version(conn, version):
    conn.execute(
        "INSERT OR REPLACE INTO schema_meta(key, value) VALUES ('version', ?)",
        (str(version),),
    )


def _exec_ddl(conn, statement):
    """Run one DDL statement, tolerating 'already exists' / 'duplicate column'."""
    try:
        conn.execute(statement)
    except sqlite3.OperationalError as e:
        msg = str(e).lower()
        if "already exists" in msg or "duplicate column" in msg:
            return
        raise


def _migrate_v1(conn):
    """Fresh schema: create usage_events with all target columns and indexes."""
    _exec_ddl(conn, f"CREATE TABLE IF NOT EXISTS usage_events (\n    {',\n    '.join(_TARGET_COLUMNS)}\n)")
    indexes = [
        ("idx_events_ts", "usage_events", "ts_epoch"),
        ("idx_events_model", "usage_events", "model"),
        ("idx_events_account", "usage_events", "account_hash"),
        ("idx_events_date", "usage_events", "utc_date"),
        ("idx_events_hour", "usage_events", "utc_hour"),
        ("idx_events_failed", "usage_events", "failed"),
        ("idx_events_hour_model", "usage_events", "utc_hour, model"),
    ]
    for idx, tbl, cols in indexes:
        _exec_ddl(conn, f"CREATE INDEX IF NOT EXISTS {idx} ON {tbl}({cols})")
    _ensure_schema_meta(conn)


def _migrate_v2(conn):
    """Add extended token/dimension columns (safe on fresh DBs via IF NOT EXISTS)."""
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN cache_read_tokens INTEGER DEFAULT 0")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN cache_creation_tokens INTEGER DEFAULT 0")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN ttft_ms INTEGER DEFAULT 0")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN auth_type TEXT")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN executor_type TEXT")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN service_tier TEXT")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN reasoning_effort TEXT")
    _exec_ddl(conn, "ALTER TABLE usage_events ADD COLUMN alias TEXT")


def _migrate_v3(conn):
    """Reconcile legacy v1/v2 databases to the target schema."""
    _backfill_utc(conn)
    _backfill_account_hash(conn)
    _rebuild_without_legacy(conn)


def _backfill_utc(conn):
    from .collector import parse_rfc3339
    for row in conn.execute("SELECT id, timestamp FROM usage_events WHERE utc_date IS NULL OR utc_date = ''"):
        try:
            ts = parse_rfc3339(row["timestamp"])
        except Exception:
            continue
        conn.execute(
            "UPDATE usage_events SET utc_date=?, utc_hour=? WHERE id=?",
            (ts.strftime("%Y-%m-%d"), ts.strftime("%Y-%m-%d %H:00"), row["id"]),
        )


def _backfill_account_hash(conn):
    """Set account_hash from legacy source/auth_index using a STABLE sha256 digest.
    Tolerates missing columns (fresh DB schema)."""
    if not _has_column(conn, "usage_events", "source"):
        return
    import hashlib
    for row in conn.execute(
        "SELECT id, source, auth_index FROM usage_events WHERE account_hash IS NULL OR account_hash = ''"
    ):
        raw = json.dumps({"source": row["source"], "auth_index": row["auth_index"]}, sort_keys=True)
        digest = "sha256:" + hashlib.sha256(raw.encode()).hexdigest()[:12]
        conn.execute("UPDATE usage_events SET account_hash=? WHERE id=?", (digest, row["id"]))


def _rebuild_without_legacy(conn):
    """Rebuild usage_events keeping only target columns. Uses individual
    ALTER TABLE DROP COLUMN statements (SQLite 3.35+) for each legacy column."""
    legacy = {"source", "auth_index", "response_body"}
    existing = {r[1] for r in conn.execute("PRAGMA table_info(usage_events)")}
    for col in existing - set(_COLUMN_NAMES):
        if col in legacy:
            _exec_ddl(conn, f"ALTER TABLE usage_events DROP COLUMN {col}")
    indexes = [
        ("idx_events_ts", "usage_events", "ts_epoch"),
        ("idx_events_model", "usage_events", "model"),
        ("idx_events_account", "usage_events", "account_hash"),
        ("idx_events_date", "usage_events", "utc_date"),
        ("idx_events_hour", "usage_events", "utc_hour"),
        ("idx_events_failed", "usage_events", "failed"),
        ("idx_events_hour_model", "usage_events", "utc_hour, model"),
    ]
    for idx, tbl, cols in indexes:
        _exec_ddl(conn, f"CREATE INDEX IF NOT EXISTS {idx} ON {tbl}({cols})")


def _migrate_v4(conn):
    """Add key_aliases table for human-readable account aliases."""
    _exec_ddl(conn, """CREATE TABLE IF NOT EXISTS key_aliases (
        account_hash TEXT PRIMARY KEY,
        alias TEXT NOT NULL
    )""")


MIGRATIONS = [_migrate_v1, _migrate_v2, _migrate_v3, _migrate_v4]


def run_migrations(cfg):
    """Apply pending migrations atomically, one version per transaction."""
    with db_connect(cfg) as conn:
        _ensure_schema_meta(conn)
        current = _current_version(conn)
        for version in range(current, SCHEMA_VERSION):
            fn = MIGRATIONS[version]
            try:
                conn.execute("BEGIN")
                fn(conn)
                _set_version(conn, version + 1)
                conn.execute("COMMIT")
            except Exception:
                try:
                    conn.execute("ROLLBACK")
                except sqlite3.OperationalError:
                    pass
                raise
        return SCHEMA_VERSION


def init_schema(cfg):
    """Ensure data dir + schema exist; return current schema version."""
    cfg_mod.ensure_dirs(cfg)
    return run_migrations(cfg)


# --- key aliases CRUD --------------------------------------------------------


def get_aliases(cfg):
    """Return all key aliases as list of dicts."""
    with db_connect(cfg) as conn:
        rows = conn.execute(
            "SELECT account_hash, alias FROM key_aliases ORDER BY alias"
        ).fetchall()
        return [dict(r) for r in rows]


def upsert_alias(cfg, account_hash, alias):
    """Create or update a key alias."""
    with db_connect(cfg) as conn:
        conn.execute(
            "INSERT OR REPLACE INTO key_aliases(account_hash, alias) VALUES (?, ?)",
            (account_hash, alias),
        )


def delete_alias(cfg, account_hash):
    """Delete a key alias by account_hash."""
    with db_connect(cfg) as conn:
        conn.execute(
            "DELETE FROM key_aliases WHERE account_hash = ?",
            (account_hash,),
        )


# --- collector state persistence (cross-process health) -------------------


_STATE_KEYS = (
    "last_poll_ok",
    "last_poll_epoch",
    "last_poll_error",
    "last_commit_epoch",
    "last_usage_ts",
    "total_inserted",
    "schema_version",
)


def _ensure_state_table(conn):
    conn.execute("CREATE TABLE IF NOT EXISTS collector_state(key TEXT PRIMARY KEY, value TEXT)")


def load_state(cfg):
    with db_connect(cfg) as conn:
        _ensure_state_table(conn)
        rows = conn.execute("SELECT key, value FROM collector_state").fetchall()
    state = {
        "last_poll_ok": False,
        "last_poll_epoch": 0.0,
        "last_poll_error": "",
        "last_commit_epoch": 0.0,
        "last_usage_ts": 0.0,
        "total_inserted": 0,
        "schema_version": SCHEMA_VERSION,
    }
    for row in rows:
        if row["key"] in state:
            raw = row["value"]
            typ = type(state[row["key"]])
            if typ is bool:
                state[row["key"]] = raw == "1"
            elif typ is int:
                state[row["key"]] = int(raw)
            elif typ is float:
                state[row["key"]] = float(raw)
            else:
                state[row["key"]] = raw
    return state


def save_state(cfg, state):
    """Upsert collector_state rows from a state dict."""
    with db_connect(cfg) as conn:
        _ensure_state_table(conn)
        for key in _STATE_KEYS:
            if key not in state:
                continue
            val = state[key]
            if isinstance(val, bool):
                val = "1" if val else "0"
            else:
                val = str(val)
            conn.execute(
                "INSERT OR REPLACE INTO collector_state(key, value) VALUES(?, ?)",
                (key, val),
            )
