"""SQLite storage: connection lifecycle, schema, migrations, collector state."""
import contextlib
import sqlite3

from . import config as cfg_mod

SCHEMA_VERSION = 3

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
    """Yield a configured SQLite connection and CLOSE it on exit.

    Unlike the sqlite3 default context manager (which only commits/rolls back),
    this guarantees the connection and its file descriptors are released.
    """
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
    if row and row["value"] is not None:
        try:
            return int(row["value"])
        except ValueError:
            return 0
    return 0


def current_version(cfg):
    with db_connect(cfg) as conn:
        _ensure_schema_meta(conn)
        return _current_version(conn)


def _set_version(conn, version):
    conn.execute(
        "INSERT INTO schema_meta(key, value) VALUES('version', ?) "
        "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        (str(version),),
    )


def _exec_ddl(conn, statement):
    """Run one DDL statement, tolerating 'already exists' / 'duplicate column'."""
    try:
        conn.execute(statement)
    except sqlite3.OperationalError as exc:
        msg = str(exc).lower()
        if "duplicate column" in msg or "already exists" in msg:
            return
        raise


def _migrate_v1(conn):
    """Fresh schema: create usage_events with all target columns and indexes."""
    cols = ",\n  ".join(_TARGET_COLUMNS)
    _exec_ddl(conn, f"CREATE TABLE IF NOT EXISTS usage_events (\n  {cols}\n)")
    for idx, target in (
        ("idx_usage_ts", "usage_events(ts_epoch)"),
        ("idx_usage_date", "usage_events(utc_date)"),
        ("idx_usage_model_ts", "usage_events(model, ts_epoch)"),
        ("idx_usage_provider_ts", "usage_events(provider, ts_epoch)"),
    ):
        _exec_ddl(conn, f"CREATE INDEX IF NOT EXISTS {idx} ON {target}")
    _ensure_schema_meta(conn)


def _migrate_v2(conn):
    """Add extended token/dimension columns (safe on fresh DBs via IF NOT EXISTS)."""
    for stmt in (
        "ALTER TABLE usage_events ADD COLUMN cache_read_tokens INTEGER DEFAULT 0",
        "ALTER TABLE usage_events ADD COLUMN cache_creation_tokens INTEGER DEFAULT 0",
        "ALTER TABLE usage_events ADD COLUMN ttft_ms INTEGER DEFAULT 0",
        "ALTER TABLE usage_events ADD COLUMN alias TEXT",
        "ALTER TABLE usage_events ADD COLUMN executor_type TEXT",
        "ALTER TABLE usage_events ADD COLUMN service_tier TEXT",
        "ALTER TABLE usage_events ADD COLUMN reasoning_effort TEXT",
        "ALTER TABLE usage_events ADD COLUMN fail_status INTEGER DEFAULT 0",
    ):
        _exec_ddl(conn, stmt)


def _migrate_v3(conn):
    """Reconcile legacy v1/v2 databases to the target schema.

    Backfills UTC buckets and stable account hashes from legacy data, then
    rebuilds the table WITHOUT legacy credential-bearing columns.
    On fresh DBs there is nothing to reconcile.
    """
    if not _has_table(conn, "usage_events"):
        return
    for name, decl in zip(_COLUMN_NAMES, _TARGET_COLUMNS):
        if not _has_column(conn, "usage_events", name):
            _exec_ddl(conn, f"ALTER TABLE usage_events ADD COLUMN {decl}")
    legacy_cols = ("local_date", "local_hour", "source", "auth_index", "api_key_hash", "raw_json")
    has_legacy = any(_has_column(conn, "usage_events", c) for c in legacy_cols)
    if not has_legacy:
        return
    _backfill_utc(conn)
    _backfill_account_hash(conn)
    _rebuild_without_legacy(conn)


def _backfill_utc(conn):
    from .collector import parse_rfc3339

    rows = conn.execute("SELECT id, timestamp FROM usage_events WHERE utc_date IS NULL").fetchall()
    for row in rows:
        ts = parse_rfc3339(row["timestamp"])
        conn.execute(
            "UPDATE usage_events SET utc_date=?, utc_hour=? WHERE id=?",
            (ts.strftime("%Y-%m-%d"), ts.strftime("%Y-%m-%d %H:00"), row["id"]),
        )


def _backfill_account_hash(conn):
    """Set account_hash from legacy source/auth_index using a STABLE sha256 digest."""
    import hashlib

    rows = conn.execute(
        """SELECT id, source, auth_index, api_key_hash FROM usage_events
           WHERE account_hash IS NULL"""
    ).fetchall()
    for row in rows:
        ident = None
        if row["source"]:
            ident = str(row["source"])
        elif row["auth_index"]:
            ident = "idx:" + str(row["auth_index"])
        elif row["api_key_hash"]:
            ident = str(row["api_key_hash"])
        if ident:
            digest = "sha256:" + hashlib.sha256(ident.encode()).hexdigest()[:12]
            conn.execute("UPDATE usage_events SET account_hash=? WHERE id=?", (digest, row["id"]))


def _rebuild_without_legacy(conn):
    """Rebuild usage_events keeping only target columns. Uses individual
    execute() calls (NOT executescript, which auto-commits and breaks the
    outer transaction)."""
    present = {r[1] for r in conn.execute("PRAGMA table_info(usage_events)")}
    keep = [c for c in _COLUMN_NAMES if c in present]
    cols_def = ",\n  ".join(_TARGET_COLUMNS)
    conn.execute(f"CREATE TABLE usage_events_new (\n  {cols_def}\n)")
    conn.execute(
        f"INSERT INTO usage_events_new ({', '.join(_COLUMN_NAMES)}) "
        f"SELECT {', '.join(keep)} FROM usage_events"
    )
    conn.execute("DROP TABLE usage_events")
    conn.execute("ALTER TABLE usage_events_new RENAME TO usage_events")
    for idx, target in (
        ("idx_usage_ts", "usage_events(ts_epoch)"),
        ("idx_usage_date", "usage_events(utc_date)"),
        ("idx_usage_model_ts", "usage_events(model, ts_epoch)"),
        ("idx_usage_provider_ts", "usage_events(provider, ts_epoch)"),
    ):
        _exec_ddl(conn, f"CREATE INDEX IF NOT EXISTS {idx} ON {target}")


MIGRATIONS = [_migrate_v1, _migrate_v2, _migrate_v3]


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
                sval = "1" if val else "0"
            else:
                sval = str(val)
            conn.execute(
                "INSERT INTO collector_state(key, value) VALUES(?, ?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (key, sval),
            )