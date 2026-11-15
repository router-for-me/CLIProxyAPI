// Package usagestats persists one row per upstream model execution in a local
// SQLite database and answers aggregate queries over those rows.
//
// Rows are deliberately raw: every statistic (tokens per day, cost, cache hit
// ratio, latency) is derived at query time, so new statistics never require a
// schema change or a backfill.
package usagestats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	// Pure-Go SQLite driver, registered as "sqlite".
	_ "modernc.org/sqlite"
)

// DefaultFileName is the database file name placed under the auth directory.
const DefaultFileName = "usage.db"

// Row is one persisted model execution. Token buckets are mutually exclusive,
// mirroring the canonical usage.TokenBreakdown contract, so they can be priced
// independently and summed without double counting.
type Row struct {
	Timestamp     time.Time
	RequestID     string
	TraceID       string
	SessionID     string
	Provider      string
	ExecutorType  string
	Model         string
	Alias         string
	ResponseModel string
	Endpoint      string
	AuthIndex     string
	AuthType      string
	Account       string
	ClientKey     string
	ClientIP      string
	UserAgent     string
	// WorkingDir is the directory the calling agent declared; Project is derived from it.
	WorkingDir      string
	Project         string
	ReasoningEffort string
	ServiceTier     string
	Stream          bool
	Generate        bool
	Failed          bool
	StatusCode      int
	FailCode        string
	LatencyMs       int64
	TTFTMs          int64
	ParentSessionID string
	StopReason      coreusage.StopReason

	InputTokens        int64 // uncached input
	CacheReadTokens    int64
	CacheWriteTokens   int64 // all cache writes, including the TTL buckets below
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	OutputTokens       int64 // non-reasoning output
	ReasoningTokens    int64
	UnclassifiedTokens int64
	TotalTokens        int64

	WebSearchRequests int64
	// UpstreamCostUSD is the cost reported by the upstream itself, when it reports one.
	UpstreamCostUSD *float64
}

// migrations are applied in order; the database's user_version records how
// many have run. Append new statements, never edit an applied one.
var migrations = []string{
	`CREATE TABLE requests (
	id                    INTEGER PRIMARY KEY,
	ts_ms                 INTEGER NOT NULL,
	request_id            TEXT    NOT NULL,
	trace_id              TEXT    NOT NULL,
	session_id            TEXT    NOT NULL,
	provider              TEXT    NOT NULL,
	executor_type         TEXT    NOT NULL,
	model                 TEXT    NOT NULL,
	alias                 TEXT    NOT NULL,
	response_model        TEXT    NOT NULL,
	endpoint              TEXT    NOT NULL,
	auth_index            TEXT    NOT NULL,
	auth_type             TEXT    NOT NULL,
	account               TEXT    NOT NULL,
	client_key            TEXT    NOT NULL,
	client_ip             TEXT    NOT NULL,
	user_agent            TEXT    NOT NULL,
	working_dir           TEXT    NOT NULL,
	project               TEXT    NOT NULL,
	reasoning_effort      TEXT    NOT NULL,
	service_tier          TEXT    NOT NULL,
	stream                INTEGER NOT NULL,
	generate              INTEGER NOT NULL,
	failed                INTEGER NOT NULL,
	status_code           INTEGER NOT NULL,
	fail_code             TEXT    NOT NULL,
	latency_ms            INTEGER NOT NULL,
	ttft_ms               INTEGER NOT NULL,
	input_tokens          INTEGER NOT NULL,
	cache_read_tokens     INTEGER NOT NULL,
	cache_write_tokens    INTEGER NOT NULL,
	cache_write_5m_tokens INTEGER NOT NULL,
	cache_write_1h_tokens INTEGER NOT NULL,
	output_tokens         INTEGER NOT NULL,
	reasoning_tokens      INTEGER NOT NULL,
	unclassified_tokens   INTEGER NOT NULL,
	total_tokens          INTEGER NOT NULL,
	web_search_requests   INTEGER NOT NULL,
	upstream_cost_usd     REAL
);
CREATE INDEX idx_requests_ts ON requests (ts_ms);
CREATE INDEX idx_requests_session ON requests (session_id) WHERE session_id != '';
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`,
	`CREATE TABLE quota_samples (
	id           INTEGER PRIMARY KEY,
	ts_ms        INTEGER NOT NULL,
	provider     TEXT    NOT NULL,
	auth_index   TEXT    NOT NULL,
	account      TEXT    NOT NULL,
	"window"     TEXT    NOT NULL,
	used_percent REAL    NOT NULL,
	resets_at_ms INTEGER NOT NULL
);
CREATE INDEX idx_quota_samples_series ON quota_samples (provider, auth_index, "window", ts_ms);`,
	`ALTER TABLE requests ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT '';`,
}

const insertSQL = `
INSERT INTO requests (
	ts_ms, request_id, trace_id, session_id, provider, executor_type, model, alias, response_model,
	endpoint, auth_index, auth_type, account, client_key, client_ip, user_agent, working_dir, project,
	reasoning_effort, service_tier, stream, generate, failed, status_code, fail_code, latency_ms, ttft_ms,
	input_tokens, cache_read_tokens, cache_write_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
	output_tokens, reasoning_tokens, unclassified_tokens, total_tokens, web_search_requests, upstream_cost_usd,
	stop_reason, parent_session_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// Store is a SQLite-backed usage store. It is safe for concurrent use.
type Store struct {
	db     *sql.DB
	insert *sql.Stmt
	path   string

	quotaMu sync.Mutex
	// newestQuota caches the newest stored point of each quota series.
	newestQuota map[QuotaSeriesKey]QuotaPoint
}

// Open opens (creating when needed) the usage database at path.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("usagestats: empty database path")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("usagestats: create database directory: %w", errMkdir)
	}
	if errRestrict := restrictToOwner(path); errRestrict != nil {
		return nil, errRestrict
	}

	// WAL keeps dashboard reads from blocking the writer; NORMAL is durable
	// across process crashes, which is the failure mode that matters here.
	pragmas := url.Values{}
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	db, errOpen := sql.Open("sqlite", path+"?"+pragmas.Encode())
	if errOpen != nil {
		return nil, fmt.Errorf("usagestats: open database: %w", errOpen)
	}
	if errMigrate := migrate(db); errMigrate != nil {
		_ = db.Close()
		return nil, errMigrate
	}
	insert, errPrepare := db.Prepare(insertSQL)
	if errPrepare != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usagestats: prepare insert: %w", errPrepare)
	}
	return &Store{db: db, insert: insert, path: path, newestQuota: make(map[QuotaSeriesKey]QuotaPoint)}, nil
}

// restrictToOwner makes the database file readable by its owner only, creating
// it when needed. SQLite gives its WAL and shared-memory files the same mode.
// The auth directory may predate this store and be readable by other users.
func restrictToOwner(path string) error {
	file, errOpen := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if errOpen != nil {
		return fmt.Errorf("usagestats: create database file: %w", errOpen)
	}
	if errClose := file.Close(); errClose != nil {
		return fmt.Errorf("usagestats: create database file: %w", errClose)
	}
	if errChmod := os.Chmod(path, 0o600); errChmod != nil {
		return fmt.Errorf("usagestats: restrict database file: %w", errChmod)
	}
	return nil
}

// migrate brings the schema up to date, one transaction per migration.
func migrate(db *sql.DB) error {
	var version int
	if errVersion := db.QueryRow("PRAGMA user_version").Scan(&version); errVersion != nil {
		return fmt.Errorf("usagestats: read schema version: %w", errVersion)
	}
	if version > len(migrations) {
		return fmt.Errorf("usagestats: database schema version %d is newer than this build supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, errBegin := db.Begin()
		if errBegin != nil {
			return fmt.Errorf("usagestats: begin migration %d: %w", i+1, errBegin)
		}
		// PRAGMA does not accept bound parameters; the value is a loop index.
		_, errApply := tx.Exec(fmt.Sprintf("%s\nPRAGMA user_version = %d;", migrations[i], i+1))
		if errApply != nil {
			_ = tx.Rollback()
			return fmt.Errorf("usagestats: apply migration %d: %w", i+1, errApply)
		}
		if errCommit := tx.Commit(); errCommit != nil {
			return fmt.Errorf("usagestats: commit migration %d: %w", i+1, errCommit)
		}
	}
	return nil
}

// WorkingDirForSession returns the most recent working directory recorded for
// a session, or "" when none is known. Agents that only declare their working
// directory on the first request of a session rely on this for later requests.
// Session ids can be chosen by clients, so the lookup is scoped to the stored
// (masked) client key: one client never inherits another's directory.
func (s *Store) WorkingDirForSession(ctx context.Context, sessionID, clientKey string) (string, error) {
	if s == nil || sessionID == "" {
		return "", nil
	}
	var dir string
	errScan := s.db.QueryRowContext(ctx,
		"SELECT working_dir FROM requests WHERE session_id = ? AND client_key = ? AND working_dir != '' ORDER BY id DESC LIMIT 1",
		sessionID, clientKey,
	).Scan(&dir)
	if errors.Is(errScan, sql.ErrNoRows) {
		return "", nil
	}
	if errScan != nil {
		return "", fmt.Errorf("usagestats: look up session working directory: %w", errScan)
	}
	return dir, nil
}

// Path returns the database file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Insert persists one row.
func (s *Store) Insert(ctx context.Context, row Row) error {
	if s == nil {
		return errors.New("usagestats: store is closed")
	}
	var cost any
	if row.UpstreamCostUSD != nil {
		cost = *row.UpstreamCostUSD
	}
	_, errExec := s.insert.ExecContext(ctx,
		row.Timestamp.UnixMilli(), row.RequestID, row.TraceID, row.SessionID, row.Provider, row.ExecutorType,
		row.Model, row.Alias, row.ResponseModel, row.Endpoint, row.AuthIndex, row.AuthType, row.Account,
		row.ClientKey, row.ClientIP, row.UserAgent, row.WorkingDir, row.Project, row.ReasoningEffort, row.ServiceTier,
		row.Stream, row.Generate, row.Failed, row.StatusCode, row.FailCode, row.LatencyMs, row.TTFTMs,
		row.InputTokens, row.CacheReadTokens, row.CacheWriteTokens, row.CacheWrite5mTokens, row.CacheWrite1hTokens,
		row.OutputTokens, row.ReasoningTokens, row.UnclassifiedTokens, row.TotalTokens, row.WebSearchRequests, cost,
		string(row.StopReason), row.ParentSessionID,
	)
	if errExec != nil {
		return fmt.Errorf("usagestats: insert row: %w", errExec)
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	errStmt := s.insert.Close()
	if errDB := s.db.Close(); errDB != nil {
		return fmt.Errorf("usagestats: close database: %w", errDB)
	}
	if errStmt != nil {
		return fmt.Errorf("usagestats: close insert statement: %w", errStmt)
	}
	return nil
}
