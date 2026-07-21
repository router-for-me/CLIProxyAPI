package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultConfigTable        = "config_store"
	defaultAuthTable          = "auth_store"
	defaultConfigKey          = "config"
	defaultAPIKeysTable       = "api_keys"
	defaultPoliciesTable      = "api_key_policies"
	defaultUsageEventsTable   = "usage_events"
	defaultUsageErrorsTable   = "usage_errors"
	defaultUsageWindowsTable  = "usage_windows"
	defaultModelsTable        = "models_catalog"
	defaultModelPricingTable  = "model_pricing"
	defaultErrorMessagesTable = "error_messages"
	defaultInternalUsersTable = "internal_users"
	defaultUserWindowsTable   = "user_windows"
)

// PostgresStoreConfig captures configuration required to initialize a Postgres-backed store.
type PostgresStoreConfig struct {
	DSN         string
	Schema      string
	ConfigTable string
	AuthTable   string
	SpoolDir    string

	// APIKeysTable is the table that stores client-facing API keys with per-key policy.
	APIKeysTable string
	// PoliciesTable stores per-key policy attached to APIKeysTable rows.
	PoliciesTable string
	// UsageEventsTable stores per-request usage records (async-flushed);
	// only successful responses are written here. Failed attempts are
	// routed to UsageErrorsTable instead.
	UsageEventsTable string
	// UsageErrorsTable stores per-request failure records (async-flushed):
	// upstream errors, stream errors, and other request-level failures with
	// their error_message and fail_status_code. Kept separate from
	// UsageEventsTable so the success-table aggregates stay clean.
	UsageErrorsTable string
	// UsageWindowsTable stores time-windowed aggregate counters for budget enforcement.
	UsageWindowsTable string
	// ModelsTable stores the model catalog mirrored from the registry updater.
	ModelsTable string
	// ModelPricingTable stores per-model unit pricing used to compute usage cost.
	ModelPricingTable string
	// ErrorMessagesTable stores operator-customized error responses keyed by
	// HTTP status code. Served by the errormessages package.
	ErrorMessagesTable string

	// InternalUsersTable stores the internal-user entity (a key owner with
	// per-user budget, role, and model access). Referenced by api_keys.user_id
	// and usage_events.user_id.
	InternalUsersTable string
	// UserWindowsTable stores time-windowed aggregate counters for per-user
	// budget enforcement, parallel to UsageWindowsTable but keyed by user_id.
	UserWindowsTable string

	// UsageEncryptionKey is the passphrase used to derive an AES-256-GCM
	// key for sealing sensitive columns (api_key_principal in usage_events)
	// at rest. Empty/nil disables encryption: writes stay in plaintext and
	// reads tolerate existing plaintext rows. Held in memory only; never
	// persisted to the database or to disk.
	UsageEncryptionKey []byte
}

// PostgresStore persists configuration and authentication metadata using PostgreSQL as backend
// while mirroring data to a local workspace so existing file-based workflows continue to operate.
type PostgresStore struct {
	db         *sql.DB
	cfg        PostgresStoreConfig
	spoolRoot  string
	configPath string
	authDir    string
	mu         sync.Mutex
}

// NewPostgresStore establishes a connection to PostgreSQL and prepares the local workspace.
func NewPostgresStore(ctx context.Context, cfg PostgresStoreConfig) (*PostgresStore, error) {
	trimmedDSN := strings.TrimSpace(cfg.DSN)
	if trimmedDSN == "" {
		return nil, fmt.Errorf("postgres store: DSN is required")
	}
	cfg.DSN = trimmedDSN
	if cfg.ConfigTable == "" {
		cfg.ConfigTable = defaultConfigTable
	}
	if cfg.AuthTable == "" {
		cfg.AuthTable = defaultAuthTable
	}
	if cfg.APIKeysTable == "" {
		cfg.APIKeysTable = defaultAPIKeysTable
	}
	if cfg.PoliciesTable == "" {
		cfg.PoliciesTable = defaultPoliciesTable
	}
	if cfg.UsageEventsTable == "" {
		cfg.UsageEventsTable = defaultUsageEventsTable
	}
	if cfg.UsageErrorsTable == "" {
		cfg.UsageErrorsTable = defaultUsageErrorsTable
	}
	if cfg.UsageWindowsTable == "" {
		cfg.UsageWindowsTable = defaultUsageWindowsTable
	}
	if cfg.ModelsTable == "" {
		cfg.ModelsTable = defaultModelsTable
	}
	if cfg.ModelPricingTable == "" {
		cfg.ModelPricingTable = defaultModelPricingTable
	}
	if cfg.ErrorMessagesTable == "" {
		cfg.ErrorMessagesTable = defaultErrorMessagesTable
	}
	if cfg.InternalUsersTable == "" {
		cfg.InternalUsersTable = defaultInternalUsersTable
	}
	if cfg.UserWindowsTable == "" {
		cfg.UserWindowsTable = defaultUserWindowsTable
	}

	spoolRoot := strings.TrimSpace(cfg.SpoolDir)
	if spoolRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			spoolRoot = filepath.Join(cwd, "pgstore")
		} else {
			spoolRoot = filepath.Join(os.TempDir(), "pgstore")
		}
	}
	absSpool, err := filepath.Abs(spoolRoot)
	if err != nil {
		return nil, fmt.Errorf("postgres store: resolve spool directory: %w", err)
	}
	configDir := filepath.Join(absSpool, "config")
	authDir := filepath.Join(absSpool, "auths")
	if err = os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("postgres store: create config directory: %w", err)
	}
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return nil, fmt.Errorf("postgres store: create auth directory: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres store: open database connection: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres store: ping database: %w", err)
	}

	store := &PostgresStore{
		db:         db,
		cfg:        cfg,
		spoolRoot:  absSpool,
		configPath: filepath.Join(configDir, "config.yaml"),
		authDir:    authDir,
	}
	return store, nil
}

// Close releases the underlying database connection.
func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// EnsureSchema creates the required tables (and schema when provided).
func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("postgres store: create schema: %w", err)
		}
	}
	configTable := s.fullTableName(s.cfg.ConfigTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, configTable)); err != nil {
		return fmt.Errorf("postgres store: create config table: %w", err)
	}
	authTable := s.fullTableName(s.cfg.AuthTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			content JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, authTable)); err != nil {
		return fmt.Errorf("postgres store: create auth table: %w", err)
	}

	if err := s.ensurePolicySchema(ctx); err != nil {
		return err
	}
	return nil
}

// ensurePolicySchema creates the tables backing client-facing API keys, per-key policies,
// usage events, usage windows, the model catalog, and per-model pricing. All statements are
// idempotent (CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS).
func (s *PostgresStore) ensurePolicySchema(ctx context.Context) error {
	apiKeysTable := s.fullTableName(s.cfg.APIKeysTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			key_hash     TEXT NOT NULL UNIQUE,
			key_prefix   TEXT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'active',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at   TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ,
			metadata     JSONB NOT NULL DEFAULT '{}'::jsonb
		)
	`, apiKeysTable)); err != nil {
		return fmt.Errorf("postgres store: create api_keys table: %w", err)
	}
	// Backfill the key_alias column added in the usage-stats-encryption
	// schema iteration. Idempotent so existing deployments upgrade
	// transparently on next startup.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS key_alias TEXT`, apiKeysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_keys add key_alias: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_api_keys_key_alias ON %s(key_alias) WHERE key_alias IS NOT NULL`, apiKeysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create api_keys key_alias index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON %s(key_hash)`, apiKeysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create api_keys key_hash index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_api_keys_status ON %s(status) WHERE status = 'active'`, apiKeysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create api_keys status index: %w", err)
	}
	// Backfill the user_id column (linking an API key to an internal user
	// owner). Idempotent so existing deployments upgrade transparently.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS user_id TEXT`, apiKeysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_keys add user_id: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON %s(user_id) WHERE user_id IS NOT NULL`, apiKeysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create api_keys user_id index: %w", err)
	}

	policiesTable := s.fullTableName(s.cfg.PoliciesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			api_key_id          TEXT PRIMARY KEY REFERENCES %s(id) ON DELETE CASCADE,
			rpm_limit           INTEGER,
			hourly_rate_limit   INTEGER,
			budget_hourly_usd   NUMERIC(12,6),
			budget_weekly_usd   NUMERIC(12,6),
			budget_monthly_usd  NUMERIC(12,6),
			allowed_models      JSONB NOT NULL DEFAULT '[]'::jsonb,
			blocked_models      JSONB NOT NULL DEFAULT '[]'::jsonb,
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, policiesTable, apiKeysTable)); err != nil {
		return fmt.Errorf("postgres store: create api_key_policies table: %w", err)
	}
	// Backfill max_parallel_requests on api_key_policies (key-level in-flight
	// cap; LiteLLM equivalent). Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS max_parallel_requests INTEGER`, policiesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_key_policies add max_parallel_requests: %w", err)
	}

	usageEventsTable := s.fullTableName(s.cfg.UsageEventsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                      BIGSERIAL PRIMARY KEY,
			request_id              TEXT,
			api_key_id              TEXT,
			api_key_principal       TEXT,
			provider                TEXT NOT NULL,
			executor_type           TEXT,
			model                   TEXT NOT NULL,
			alias                   TEXT,
			endpoint                TEXT,
			auth_type               TEXT,
			source                  TEXT,
			reasoning_effort        TEXT,
			service_tier            TEXT,
			response_service_tier   TEXT,
			input_tokens            BIGINT NOT NULL DEFAULT 0,
			output_tokens           BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens        BIGINT NOT NULL DEFAULT 0,
			cached_tokens           BIGINT NOT NULL DEFAULT 0,
			cache_creation_tokens   BIGINT NOT NULL DEFAULT 0,
			total_tokens            BIGINT NOT NULL DEFAULT 0,
			cost_usd                NUMERIC(12,6) NOT NULL DEFAULT 0,
			latency_ms              BIGINT,
			ttft_ms                 BIGINT,
			failed                  BOOLEAN NOT NULL DEFAULT FALSE,
			fail_status_code        INTEGER,
			generate                BOOLEAN NOT NULL DEFAULT FALSE,
			requested_at            TIMESTAMPTZ NOT NULL,
			flushed_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, usageEventsTable)); err != nil {
		return fmt.Errorf("postgres store: create usage_events table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_events_api_key ON %s(api_key_id, requested_at)`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_events api_key index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_events_model ON %s(model, requested_at)`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_events model index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_events_requested_at ON %s(requested_at DESC)`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_events requested_at index: %w", err)
	}
	// Backfill the user_id column on usage_events, stamped by the usage
	// flusher when resolving the API key. Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS user_id TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add user_id: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_events_user_id ON %s(user_id, requested_at) WHERE user_id IS NOT NULL`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_events user_id index: %w", err)
	}

	// usage_errors mirrors usage_events but holds only failed attempts. It is
	// populated by the usage flusher whenever record.Failed is true. Kept
	// separate so success-table aggregates (request_count, token totals) stay
	// clean of failures while still allowing failure_rate and error drill-down.
	usageErrorsTable := s.fullTableName(s.cfg.UsageErrorsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                      BIGSERIAL PRIMARY KEY,
			request_id              TEXT,
			api_key_id              TEXT,
			api_key_principal       TEXT,
			user_id                 TEXT,
			provider                TEXT NOT NULL,
			executor_type           TEXT,
			model                   TEXT NOT NULL,
			alias                   TEXT,
			endpoint                TEXT,
			auth_type               TEXT,
			source                  TEXT,
			reasoning_effort        TEXT,
			service_tier            TEXT,
			response_service_tier   TEXT,
			input_tokens            BIGINT NOT NULL DEFAULT 0,
			output_tokens           BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens        BIGINT NOT NULL DEFAULT 0,
			cached_tokens           BIGINT NOT NULL DEFAULT 0,
			cache_creation_tokens   BIGINT NOT NULL DEFAULT 0,
			total_tokens            BIGINT NOT NULL DEFAULT 0,
			cost_usd                NUMERIC(12,6) NOT NULL DEFAULT 0,
			latency_ms              BIGINT,
			ttft_ms                 BIGINT,
			fail_status_code        INTEGER,
			error_message           TEXT NOT NULL DEFAULT '',
			generate                BOOLEAN NOT NULL DEFAULT FALSE,
			requested_at            TIMESTAMPTZ NOT NULL,
			flushed_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, usageErrorsTable)); err != nil {
		return fmt.Errorf("postgres store: create usage_errors table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_errors_api_key ON %s(api_key_id, requested_at)`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_errors api_key index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_errors_model ON %s(model, requested_at)`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_errors model index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_errors_requested_at ON %s(requested_at DESC)`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_errors requested_at index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_errors_user_id ON %s(user_id, requested_at) WHERE user_id IS NOT NULL`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_errors user_id index: %w", err)
	}

	usageWindowsTable := s.fullTableName(s.cfg.UsageWindowsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			api_key_id    TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			window_type   TEXT NOT NULL,
			window_start  TIMESTAMPTZ NOT NULL,
			window_end    TIMESTAMPTZ NOT NULL,
			request_count BIGINT NOT NULL DEFAULT 0,
			total_tokens  BIGINT NOT NULL DEFAULT 0,
			cost_usd      NUMERIC(12,6) NOT NULL DEFAULT 0,
			PRIMARY KEY (api_key_id, window_type, window_start)
		)
	`, usageWindowsTable, apiKeysTable)); err != nil {
		return fmt.Errorf("postgres store: create usage_windows table: %w", err)
	}

	internalUsersTable := s.fullTableName(s.cfg.InternalUsersTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                TEXT PRIMARY KEY,
			user_alias        TEXT,
			user_email        TEXT UNIQUE,
			user_role         TEXT NOT NULL DEFAULT 'internal_user',
			models            JSONB NOT NULL DEFAULT '[]'::jsonb,
			metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
			max_budget        NUMERIC(12,6),
			budget_duration   TEXT,
			budget_reset_at   TIMESTAMPTZ,
			rpm_limit         BIGINT,
			tpm_limit         BIGINT,
			spend             NUMERIC(12,6) NOT NULL DEFAULT 0,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, internalUsersTable)); err != nil {
		return fmt.Errorf("postgres store: create internal_users table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_internal_users_role ON %s(user_role)`, internalUsersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create internal_users role index: %w", err)
	}
	// Backfill max_parallel_requests (LiteLLM InternalUser equivalent) for
	// deployments that already have internal_users without the column.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS max_parallel_requests INTEGER`, internalUsersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter internal_users add max_parallel_requests: %w", err)
	}

	userWindowsTable := s.fullTableName(s.cfg.UserWindowsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			user_id       TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			window_type   TEXT NOT NULL,
			window_start  TIMESTAMPTZ NOT NULL,
			window_end    TIMESTAMPTZ NOT NULL,
			request_count BIGINT NOT NULL DEFAULT 0,
			total_tokens  BIGINT NOT NULL DEFAULT 0,
			cost_usd      NUMERIC(12,6) NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, window_type, window_start)
		)
	`, userWindowsTable, internalUsersTable)); err != nil {
		return fmt.Errorf("postgres store: create user_windows table: %w", err)
	}

	modelsTable := s.fullTableName(s.cfg.ModelsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                            TEXT NOT NULL,
			provider                      TEXT NOT NULL,
			official_provider             TEXT,
			object                        TEXT NOT NULL DEFAULT 'model',
			created                       BIGINT NOT NULL DEFAULT 0,
			owned_by                      TEXT NOT NULL,
			type                          TEXT NOT NULL,
			display_name                  TEXT,
			name                          TEXT,
			version                       TEXT,
			description                   TEXT,
			input_token_limit             INTEGER,
			output_token_limit            INTEGER,
			supported_generation_methods  JSONB,
			context_length                INTEGER NOT NULL DEFAULT 0,
			max_completion_tokens         INTEGER NOT NULL DEFAULT 0,
			supported_parameters          JSONB,
			input_modalities              JSONB,
			output_modalities             JSONB,
			supports_web_search           BOOLEAN NOT NULL DEFAULT FALSE,
			thinking_config               JSONB,
			override_header               JSONB,
			user_defined                  BOOLEAN NOT NULL DEFAULT FALSE,
			updated_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, provider)
		)
	`, modelsTable)); err != nil {
		return fmt.Errorf("postgres store: create models_catalog table: %w", err)
	}
	// Backfill columns added in later schema iterations. Idempotent so
	// existing deployments upgrade transparently on next startup.
	for _, col := range []struct{ name, def string }{
		{"name", "TEXT"},
		{"version", "TEXT"},
		{"description", "TEXT"},
		{"supported_generation_methods", "JSONB"},
		{"supported_parameters", "JSONB"},
		{"input_modalities", "JSONB"},
		{"output_modalities", "JSONB"},
		{"official_provider", "TEXT"},
	} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s`,
			modelsTable, quoteIdentifier(col.name), col.def,
		)); err != nil {
			return fmt.Errorf("postgres store: alter models_catalog add column %s: %w", col.name, err)
		}
	}
	// Backfill official_provider from provider for rows that predate the
	// column. Idempotent: only touches rows with NULL/empty values so the
	// operator's later hand-edits are preserved.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET official_provider = provider WHERE official_provider IS NULL OR official_provider = ''`,
		modelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: backfill models_catalog.official_provider: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_models_catalog_provider ON %s(provider)`, modelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create models_catalog provider index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_models_catalog_official_provider ON %s(official_provider)`, modelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create models_catalog official_provider index: %w", err)
	}

	modelPricingTable := s.fullTableName(s.cfg.ModelPricingTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                       TEXT PRIMARY KEY,
			input_per_1m_usd         NUMERIC(12,6) NOT NULL DEFAULT 0,
			output_per_1m_usd        NUMERIC(12,6) NOT NULL DEFAULT 0,
			cached_input_per_1m_usd  NUMERIC(12,6) NOT NULL DEFAULT 0,
			cached_read_per_1m_usd   NUMERIC(12,6) NOT NULL DEFAULT 0,
			reasoning_per_1m_usd     NUMERIC(12,6) NOT NULL DEFAULT 0,
			updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, modelPricingTable)); err != nil {
		return fmt.Errorf("postgres store: create model_pricing table: %w", err)
	}
	// Backfill cached_read_per_1m_usd for existing deployments that already
	// have the table without this column (added in the pricing-cached-read
	// schema iteration).
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS cached_read_per_1m_usd NUMERIC(12,6) NOT NULL DEFAULT 0`,
		modelPricingTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_pricing add cached_read_per_1m_usd: %w", err)
	}

	// error_messages stores operator-customized error responses keyed by
	// HTTP status code. The errormessages package caches rows in memory and
	// falls back to a curated default when no override exists.
	errorMessagesTable := s.fullTableName(s.cfg.ErrorMessagesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			status_code   INTEGER PRIMARY KEY,
			title         TEXT NOT NULL,
			message       TEXT NOT NULL,
			body_template TEXT,
			enabled       BOOLEAN NOT NULL DEFAULT TRUE,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, errorMessagesTable)); err != nil {
		return fmt.Errorf("postgres store: create error_messages table: %w", err)
	}
	return nil
}

// Bootstrap synchronizes configuration and auth records between PostgreSQL and the local workspace.
func (s *PostgresStore) Bootstrap(ctx context.Context, exampleConfigPath string) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if err := s.syncConfigFromDatabase(ctx, exampleConfigPath); err != nil {
		return err
	}
	if err := s.syncAuthFromDatabase(ctx); err != nil {
		return err
	}
	return nil
}

// ConfigPath returns the managed configuration file path inside the spool directory.
func (s *PostgresStore) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

// AuthDir returns the local directory containing mirrored auth files.
func (s *PostgresStore) AuthDir() string {
	if s == nil {
		return ""
	}
	return s.authDir
}

// WorkDir exposes the root spool directory used for mirroring.
func (s *PostgresStore) WorkDir() string {
	if s == nil {
		return ""
	}
	return s.spoolRoot
}

// SetBaseDir implements the optional interface used by authenticators; it is a no-op because
// the Postgres-backed store controls its own workspace.
func (s *PostgresStore) SetBaseDir(string) {}

// DB exposes the underlying *sql.DB connection for sibling store implementations
// (api keys, usage, models). Callers must not close the handle.
func (s *PostgresStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Schema returns the configured PostgreSQL schema (may be empty).
func (s *PostgresStore) Schema() string {
	if s == nil {
		return ""
	}
	return s.cfg.Schema
}

// APIKeysTable returns the fully-qualified name of the client-facing API keys table.
func (s *PostgresStore) APIKeysTable() string {
	if s == nil {
		return quoteIdentifier(defaultAPIKeysTable)
	}
	return s.fullTableName(s.cfg.APIKeysTable)
}

// PoliciesTable returns the fully-qualified name of the per-key policy table.
func (s *PostgresStore) PoliciesTable() string {
	if s == nil {
		return quoteIdentifier(defaultPoliciesTable)
	}
	return s.fullTableName(s.cfg.PoliciesTable)
}

// UsageEventsTable returns the fully-qualified name of the per-request usage events table.
func (s *PostgresStore) UsageEventsTable() string {
	if s == nil {
		return quoteIdentifier(defaultUsageEventsTable)
	}
	return s.fullTableName(s.cfg.UsageEventsTable)
}

// UsageErrorsTable returns the fully-qualified name of the per-request usage
// errors table. Failed attempts (upstream errors, stream errors, etc.) are
// routed here instead of UsageEventsTable so success aggregates stay clean.
func (s *PostgresStore) UsageErrorsTable() string {
	if s == nil {
		return quoteIdentifier(defaultUsageErrorsTable)
	}
	return s.fullTableName(s.cfg.UsageErrorsTable)
}

// UsageWindowsTable returns the fully-qualified name of the time-windowed usage counter table.
func (s *PostgresStore) UsageWindowsTable() string {
	if s == nil {
		return quoteIdentifier(defaultUsageWindowsTable)
	}
	return s.fullTableName(s.cfg.UsageWindowsTable)
}

// ModelsTable returns the fully-qualified name of the model catalog table.
func (s *PostgresStore) ModelsTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelsTable)
	}
	return s.fullTableName(s.cfg.ModelsTable)
}

// ModelPricingTable returns the fully-qualified name of the per-model pricing table.
func (s *PostgresStore) ModelPricingTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelPricingTable)
	}
	return s.fullTableName(s.cfg.ModelPricingTable)
}

// ErrorMessagesTable returns the fully-qualified name of the operator-
// customizable error messages table.
func (s *PostgresStore) ErrorMessagesTable() string {
	if s == nil {
		return quoteIdentifier(defaultErrorMessagesTable)
	}
	return s.fullTableName(s.cfg.ErrorMessagesTable)
}

// InternalUsersTable returns the fully-qualified name of the internal users
// (key owners with per-user budget) table.
func (s *PostgresStore) InternalUsersTable() string {
	if s == nil {
		return quoteIdentifier(defaultInternalUsersTable)
	}
	return s.fullTableName(s.cfg.InternalUsersTable)
}

// UserWindowsTable returns the fully-qualified name of the per-user
// time-windowed usage counter table.
func (s *PostgresStore) UserWindowsTable() string {
	if s == nil {
		return quoteIdentifier(defaultUserWindowsTable)
	}
	return s.fullTableName(s.cfg.UserWindowsTable)
}

// Save persists authentication metadata to disk and PostgreSQL.
func (s *PostgresStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("postgres store: missing file path attribute for %s", auth.ID)
	}

	if auth.Disabled {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			return "", nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("postgres store: create auth directory: %w", err)
	}

	switch {
	case auth.Storage != nil:
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disabled"] = auth.Disabled
		if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if err = auth.Storage.SaveTokenToFile(path); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("postgres store: marshal metadata: %w", errMarshal)
		}
		if existing, errRead := os.ReadFile(path); errRead == nil {
			if jsonEqual(existing, raw) {
				return path, nil
			}
		} else if errRead != nil && !errors.Is(errRead, fs.ErrNotExist) {
			return "", fmt.Errorf("postgres store: read existing metadata: %w", errRead)
		}
		tmp := path + ".tmp"
		if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("postgres store: write temp auth file: %w", errWrite)
		}
		if errRename := os.Rename(tmp, path); errRename != nil {
			return "", fmt.Errorf("postgres store: rename auth file: %w", errRename)
		}
	default:
		return "", fmt.Errorf("postgres store: nothing to persist for %s", auth.ID)
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[cliproxyauth.AttributePath] = path
	auth.Attributes[cliproxyauth.AttributeSourceBackend] = cliproxyauth.AuthSourcePostgres

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}

	relID, err := s.relativeAuthID(path)
	if err != nil {
		return "", err
	}
	if err = s.upsertAuthRecord(ctx, relID, path); err != nil {
		return "", err
	}
	return path, nil
}

// List enumerates all auth records stored in PostgreSQL.
func (s *PostgresStore) List(ctx context.Context) ([]*cliproxyauth.Auth, error) {
	query := fmt.Sprintf("SELECT id, content, created_at, updated_at FROM %s ORDER BY id", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list auth: %w", err)
	}
	defer rows.Close()

	auths := make([]*cliproxyauth.Auth, 0, 32)
	for rows.Next() {
		var (
			id        string
			payload   string
			createdAt time.Time
			updatedAt time.Time
		)
		if err = rows.Scan(&id, &payload, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan auth row: %w", err)
		}
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: skipping auth %s outside spool", id)
			continue
		}
		metadata := make(map[string]any)
		if err = json.Unmarshal([]byte(payload), &metadata); err != nil {
			log.WithError(err).Warnf("postgres store: skipping auth %s with invalid json", id)
			continue
		}
		provider := strings.TrimSpace(valueAsString(metadata["type"]))
		if provider == "" {
			provider = "unknown"
		}
		attr := map[string]string{
			cliproxyauth.AttributePath:          path,
			cliproxyauth.AttributeSourceBackend: cliproxyauth.AuthSourcePostgres,
		}
		if email := strings.TrimSpace(valueAsString(metadata["email"])); email != "" {
			attr["email"] = email
		}
		auth := &cliproxyauth.Auth{
			ID:               normalizeAuthID(id),
			Provider:         provider,
			FileName:         normalizeAuthID(id),
			Label:            labelFor(metadata),
			Status:           cliproxyauth.StatusActive,
			Attributes:       attr,
			Metadata:         metadata,
			CreatedAt:        createdAt,
			UpdatedAt:        updatedAt,
			LastRefreshedAt:  time.Time{},
			NextRefreshAfter: time.Time{},
		}
		cliproxyauth.ApplyCustomHeadersFromMetadata(auth)
		if disabled, ok := metadata["disabled"].(bool); ok && disabled {
			auth.Disabled = true
			auth.Status = cliproxyauth.StatusDisabled
		}
		auths = append(auths, auth)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate auth rows: %w", err)
	}
	return auths, nil
}

// Delete removes an auth file and the corresponding database record.
func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("postgres store: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("postgres store: delete auth file: %w", err)
	}
	relID, err := s.relativeAuthID(path)
	if err != nil {
		return err
	}
	return s.deleteAuthRecord(ctx, relID)
}

// PersistAuthFiles stores the provided auth file changes in PostgreSQL.
func (s *PostgresStore) PersistAuthFiles(ctx context.Context, _ string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		relID, err := s.relativeAuthID(trimmed)
		if err != nil {
			// Attempt to resolve absolute path under authDir.
			abs := trimmed
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(s.authDir, trimmed)
			}
			relID, err = s.relativeAuthID(abs)
			if err != nil {
				log.WithError(err).Warnf("postgres store: ignoring auth path %s", trimmed)
				continue
			}
			trimmed = abs
		}
		if err = s.syncAuthFile(ctx, relID, trimmed); err != nil {
			return err
		}
	}
	return nil
}

// PersistConfig mirrors the local configuration file to PostgreSQL.
func (s *PostgresStore) PersistConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteConfigRecord(ctx)
		}
		return fmt.Errorf("postgres store: read config file: %w", err)
	}
	return s.persistConfig(ctx, data)
}

// syncConfigFromDatabase writes the database-stored config to disk or seeds the database from template.
func (s *PostgresStore) syncConfigFromDatabase(ctx context.Context, exampleConfigPath string) error {
	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	var content string
	err := s.db.QueryRowContext(ctx, query, defaultConfigKey).Scan(&content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, errStat := os.Stat(s.configPath); errors.Is(errStat, fs.ErrNotExist) {
			if exampleConfigPath != "" {
				if errCopy := misc.CopyConfigTemplate(exampleConfigPath, s.configPath); errCopy != nil {
					return fmt.Errorf("postgres store: copy example config: %w", errCopy)
				}
			} else {
				if errCreate := os.MkdirAll(filepath.Dir(s.configPath), 0o700); errCreate != nil {
					return fmt.Errorf("postgres store: prepare config directory: %w", errCreate)
				}
				if errWrite := os.WriteFile(s.configPath, []byte{}, 0o600); errWrite != nil {
					return fmt.Errorf("postgres store: create empty config: %w", errWrite)
				}
			}
		}
		data, errRead := os.ReadFile(s.configPath)
		if errRead != nil {
			return fmt.Errorf("postgres store: read local config: %w", errRead)
		}
		if errPersist := s.persistConfig(ctx, data); errPersist != nil {
			return errPersist
		}
	case err != nil:
		return fmt.Errorf("postgres store: load config from database: %w", err)
	default:
		if err = os.MkdirAll(filepath.Dir(s.configPath), 0o700); err != nil {
			return fmt.Errorf("postgres store: prepare config directory: %w", err)
		}
		normalized := normalizeLineEndings(content)
		if err = os.WriteFile(s.configPath, []byte(normalized), 0o600); err != nil {
			return fmt.Errorf("postgres store: write config to spool: %w", err)
		}
	}
	return nil
}

// syncAuthFromDatabase populates the local auth directory from PostgreSQL data.
func (s *PostgresStore) syncAuthFromDatabase(ctx context.Context) error {
	query := fmt.Sprintf("SELECT id, content FROM %s", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("postgres store: load auth from database: %w", err)
	}
	defer rows.Close()

	if err = os.RemoveAll(s.authDir); err != nil {
		return fmt.Errorf("postgres store: reset auth directory: %w", err)
	}
	if err = os.MkdirAll(s.authDir, 0o700); err != nil {
		return fmt.Errorf("postgres store: recreate auth directory: %w", err)
	}

	for rows.Next() {
		var (
			id      string
			payload string
		)
		if err = rows.Scan(&id, &payload); err != nil {
			return fmt.Errorf("postgres store: scan auth row: %w", err)
		}
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: skipping auth %s outside spool", id)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("postgres store: create auth subdir: %w", err)
		}
		if err = os.WriteFile(path, []byte(payload), 0o600); err != nil {
			return fmt.Errorf("postgres store: write auth file: %w", err)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate auth rows: %w", err)
	}
	return nil
}

func (s *PostgresStore) syncAuthFile(ctx context.Context, relID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteAuthRecord(ctx, relID)
		}
		return fmt.Errorf("postgres store: read auth file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteAuthRecord(ctx, relID)
	}
	return s.persistAuth(ctx, relID, data)
}

func (s *PostgresStore) upsertAuthRecord(ctx context.Context, relID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("postgres store: read auth file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteAuthRecord(ctx, relID)
	}
	return s.persistAuth(ctx, relID, data)
}

func (s *PostgresStore) persistAuth(ctx context.Context, relID string, data []byte) error {
	jsonPayload := json.RawMessage(data)
	query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.AuthTable))
	if _, err := s.db.ExecContext(ctx, query, relID, jsonPayload); err != nil {
		return fmt.Errorf("postgres store: upsert auth record: %w", err)
	}
	return nil
}

func (s *PostgresStore) deleteAuthRecord(ctx context.Context, relID string) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.AuthTable))
	if _, err := s.db.ExecContext(ctx, query, relID); err != nil {
		return fmt.Errorf("postgres store: delete auth record: %w", err)
	}
	return nil
}

func (s *PostgresStore) persistConfig(ctx context.Context, data []byte) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.ConfigTable))
	normalized := normalizeLineEndings(string(data))
	if _, err := s.db.ExecContext(ctx, query, defaultConfigKey, normalized); err != nil {
		return fmt.Errorf("postgres store: upsert config: %w", err)
	}
	return nil
}

func (s *PostgresStore) deleteConfigRecord(ctx context.Context) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	if _, err := s.db.ExecContext(ctx, query, defaultConfigKey); err != nil {
		return fmt.Errorf("postgres store: delete config: %w", err)
	}
	return nil
}

func (s *PostgresStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		return filepath.Join(s.authDir, fileName), nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("postgres store: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	return filepath.Join(s.authDir, filepath.FromSlash(auth.ID)), nil
}

func (s *PostgresStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	return filepath.Join(s.authDir, filepath.FromSlash(id)), nil
}

func (s *PostgresStore) relativeAuthID(path string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("postgres store: store not initialized")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.authDir, path)
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(s.authDir, clean)
	if err != nil {
		return "", fmt.Errorf("postgres store: compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("postgres store: path %s outside managed directory", path)
	}
	return filepath.ToSlash(rel), nil
}

func (s *PostgresStore) absoluteAuthPath(id string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("postgres store: store not initialized")
	}
	clean := filepath.Clean(filepath.FromSlash(id))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("postgres store: invalid auth identifier %s", id)
	}
	path := filepath.Join(s.authDir, clean)
	rel, err := filepath.Rel(s.authDir, path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("postgres store: resolved auth path escapes auth directory")
	}
	return path, nil
}

func (s *PostgresStore) fullTableName(name string) string {
	if strings.TrimSpace(s.cfg.Schema) == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(s.cfg.Schema) + "." + quoteIdentifier(name)
}

func quoteIdentifier(identifier string) string {
	replaced := strings.ReplaceAll(identifier, "\"", "\"\"")
	return "\"" + replaced + "\""
}

func valueAsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v := strings.TrimSpace(valueAsString(metadata["label"])); v != "" {
		return v
	}
	if v := strings.TrimSpace(valueAsString(metadata["email"])); v != "" {
		return v
	}
	if v := strings.TrimSpace(valueAsString(metadata["project_id"])); v != "" {
		return v
	}
	return ""
}

func normalizeAuthID(id string) string {
	return filepath.ToSlash(filepath.Clean(id))
}

func normalizeLineEndings(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}
