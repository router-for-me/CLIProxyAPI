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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultConfigTable                   = "config_store"
	defaultAuthTable                     = "auth_store"
	defaultCooldownTable                 = "cooldown_store"
	defaultConfigKey                     = "config"
	defaultAPIKeysTable                  = "api_keys"
	defaultPoliciesTable                 = "api_key_policies"
	defaultUsageEventsTable              = "usage_events"
	defaultUsageErrorsTable              = "usage_errors"
	defaultUsageWindowsTable             = "usage_windows"
	defaultUsageStatDayTable             = "usage_stat_day"
	defaultModelsTable                   = "models_catalog"
	defaultModelPricingTable             = "model_pricing"
	defaultModelRoutingTable             = "model_routing"
	defaultErrorMessagesTable            = "error_messages"
	defaultInternalUsersTable            = "internal_users"
	defaultUserWindowsTable              = "user_windows"
	defaultPricingSourcesTable           = "pricing_sources"
	defaultManagementTokensTable         = "management_tokens"
	defaultManagementTokenPoliciesTable  = "management_token_policies"
	defaultManagementAuditLogTable       = "management_audit_log"
	defaultUpstreamProvidersTable        = "upstream_providers"
	defaultUpstreamProviderModelsTable   = "upstream_provider_models"
	defaultUpstreamProviderHeadersTable  = "upstream_provider_headers"
	defaultUpstreamProviderExcludedTable = "upstream_provider_excluded_models"
	defaultUpstreamProviderEntriesTable  = "upstream_provider_api_key_entries"
	defaultProxyPoolsTable               = "proxy_pools"
	defaultUpstreamSyncLogTable          = "upstream_sync_log"
	defaultModelGroupsTable              = "model_groups"
	defaultAutoRoutersTable              = "auto_routers"
	defaultAutoRouterProfilesTable       = "auto_router_profiles"
	defaultModelHealthTable              = "model_health"
	defaultModelHealthLogTable           = "model_health_log"
	defaultModelHealthSettingsTable      = "model_health_settings"
	defaultAlertsTable                   = "alerts"
	defaultAlertSettingsTable            = "alert_settings"
	// Manage-LiteLLM tables. These mirror LiteLLM's own schema (internal
	// users + API keys + key policies) but are intentionally separate from the
	// runtime tables (internal_users / api_keys / api_key_policies) so the
	// Manage LiteLLM feature can hold complete LiteLLM-style data without
	// affecting the request-serving path.
	defaultLiteLLMUsersTable       = "litellm_internal_users"
	defaultLiteLLMKeysTable        = "litellm_api_keys"
	defaultLiteLLMKeyPoliciesTable = "litellm_key_policies"
	// Manage-LiteLLM sync settings table stores the singleton connection +
	// sync configuration for an external LiteLLM instance (base URL + sealed
	// master API key) plus the last-sync outcome. Mirrors the alert_settings
	// singleton pattern.
	defaultLiteLLMSyncSettingsTable = "litellm_sync_settings"
	// RuntimeConfigTable is the PG-first control-plane singleton: stores the
	// canonical runtime configuration (settings + extra metadata + revision
	// counter + update provenance) that supersedes the legacy config.yaml /
	// auths/ mirror for the embedded core bridge. One row, id = 1; CHECK
	// constraint enforces the singleton.
	defaultRuntimeConfigTable = "runtime_config"
	// ConfigRevisionsTable is the PG-first control-plane append-only history
	// table for the runtime_config singleton: one row per accepted change,
	// keyed by a monotonic BIGINT revision. Stores the settings/resource_snapshot
	// JSONB blobs, the checksum, and the audit metadata (created_at, created_by,
	// reason). Writers append; nothing updates or deletes existing rows.
	defaultConfigRevisionsTable = "config_revisions"
	// ConfigImportsTable is the PG-first control-plane audit table for every
	// import attempt (Phase 1 Task 3). One row per outcome — dry_run,
	// committed, rejected, or replaced — with the source + canonical
	// checksums, the runtime_config revision when relevant, a JSONB
	// resource_counts payload, an optional error_summary, the actor, and the
	// mode that triggered the import (e.g. "import-config"). No row is
	// seeded; writers append per attempt.
	defaultConfigImportsTable = "config_imports"
)

// loggedMissingUpstreamProvider tracks which auth filenames we've already
// logged a "no upstream_providers row" warning for, so refresh loops don't
// flood the log with the same message every 15 minutes.
var loggedMissingUpstreamProvider sync.Map // map[string]struct{}

// PostgresStoreConfig captures configuration required to initialize a Postgres-backed store.
type PostgresStoreConfig struct {
	DSN         string
	Schema      string
	ConfigTable string
	// MaxOpenConns caps the number of concurrent database connections. 0 keeps
	// the database/sql default (unlimited). Bounded here so bursty stat polling
	// can't exhaust Postgres max_connections.
	MaxOpenConns int
	// MaxIdleConns caps idle connections kept in the pool. Default (0) keeps
	// the database/sql default of 2.
	MaxIdleConns int
	// ConnMaxLifetime bounds the reuse duration of a single pooled connection.
	// 0 means "no limit".
	ConnMaxLifetime time.Duration
	AuthTable       string
	SpoolDir        string
	// CooldownTable persists runtime cooldown state (auth_id,model pairs)
	// independently from auth tokens, so cooldowns survive process restarts.
	CooldownTable string

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
	// UsageStatDayTable stores the pre-aggregated daily rollup of usage_events
	// (one row per day × user × api_key × model × provider × source). Refreshed
	// incrementally by RunRollup/BackfillRollup so aggregate queries can read
	// O(buckets) instead of O(events). Defaults to "usage_stat_day".
	UsageStatDayTable string
	// ModelsTable stores the model catalog mirrored from the registry updater.
	ModelsTable string
	// ModelPricingTable stores per-model unit pricing used to compute usage cost.
	ModelPricingTable string
	// ModelRoutingTable stores per-model-id global routing overrides (pinned
	// providers + strategy + priorities) applied to every request for the model,
	// independent of any per-API-key Models Group policy.
	ModelRoutingTable string
	// ErrorMessagesTable stores operator-customized error responses keyed by
	// HTTP status code. Served by the errormessages package.
	ErrorMessagesTable string

	// PricingSourcesTable stores operator-managed external pricing catalogs
	// (LiteLLM JSON URLs / uploaded files). Served by the pricingsource
	// package's external source registry.
	PricingSourcesTable string

	// PricingSourcesDir is the local directory used to persist uploaded
	// pricing catalog files. Defaults to <spool>/pricing_sources.
	PricingSourcesDir string

	// InternalUsersTable stores the internal-user entity (a key owner with
	// per-user budget, role, and model access). Referenced by api_keys.user_id
	// and usage_events.user_id.
	InternalUsersTable string
	// UserWindowsTable stores time-windowed aggregate counters for per-user
	// budget enforcement, parallel to UsageWindowsTable but keyed by user_id.
	UserWindowsTable string

	// ManagementTokensTable stores management API tokens that gate access to
	// the /v0/management REST surface. Each token has a scope (read|write),
	// optional policy (RPM, max-parallel, endpoint allow/block lists), and an
	// expiry. The plaintext secret is never stored; only the SHA-256 hash.
	ManagementTokensTable string
	// ManagementTokenPoliciesTable stores per-token policy attached to
	// ManagementTokensTable rows (FK ON DELETE CASCADE).
	ManagementTokenPoliciesTable string
	// ManagementAuditLogTable stores the audit trail of every management API
	// call made with a management token (request/response bodies, status,
	// latency, error). Bodies are AES-GCM-sealed when UsageEncryptionKey is set.
	ManagementAuditLogTable string

	// UpstreamProvidersTable stores every upstream provider (API-key providers
	// and OAuth/file-backed auths) as normalized rows. When PG is configured it
	// is the source of truth; the config.yaml provider sections and auth-dir
	// JSON files are rendered from these rows.
	UpstreamProvidersTable string
	// UpstreamProviderModelsTable stores the models[] array for each upstream
	// provider as child rows (one row per model entry). FK cascade.
	UpstreamProviderModelsTable string
	// UpstreamProviderHeadersTable stores the headers{} map for each upstream
	// provider as child rows keyed by (provider_id, header_key). FK cascade.
	UpstreamProviderHeadersTable string
	// UpstreamProviderExcludedTable stores the excluded-models[] list for each
	// upstream provider as child rows. FK cascade.
	UpstreamProviderExcludedTable string
	// UpstreamProviderEntriesTable stores the api-key-entries[] list for
	// openai-compatibility providers as child rows. FK cascade.
	UpstreamProviderEntriesTable string

	// ProxyPoolsTable stores the named egress-proxy pools (standard http/socks
	// pools + relay pools of type vercel/cloudflare/deno). Rows and entries of
	// upstream_providers reference a pool by id in proxy_pool_id; the
	// upstreamsync renderer resolves the binding into concrete proxy URLs.
	ProxyPoolsTable string

	// UpstreamSyncLogTable stores the outcome of every upstream OAuth/auth
	// token refresh (success + failure, auto/on-demand/unauthorized-retry
	// triggers). The error_message column is AES-GCM-sealed at rest when
	// UsageEncryptionKey is configured. Auto-swept to a 30-day retention.
	UpstreamSyncLogTable string

	// ModelGroupsTable stores reusable Model Group templates: an
	// allowed_models / blocked_models grant list plus optional per-model
	// upstream routing (ModelRoutes). Groups can be attached to either an
	// API-key policy or an internal user via their model_group_id column.
	// Attachment is single-valued (one group per entity); when attached the
	// group's allowed/blocked lists (and, for API-key policies, routes) are
	// the source of truth, overriding the entity's own fields.
	ModelGroupsTable string

	// AutoRoutersTable stores Auto Router definitions (the router entity that
	// scores requests and forwards them to a tier-appropriate model). Multiple
	// routers are supported, each with a unique name and requestable model_id.
	AutoRoutersTable string
	// AutoRouterProfilesTable stores one active, versioned scoring profile per
	// Auto Router. Empty uses auto_router_profiles.
	AutoRouterProfilesTable string

	// ModelHealthTable stores the latest health-check snapshot for each model
	// id (one row per model). Updated in place on every sweep; surfaced on the
	// Analysis → Model Health page and through the public
	// /v0/model-health/uptime endpoint.
	ModelHealthTable string
	// ModelHealthLogTable stores the append-only history of model health checks
	// (one row per check per model) used by the dashboard's trend views. Auto-
	// swept to a 30-day retention.
	ModelHealthLogTable string
	// ModelHealthSettingsTable stores the singleton operator configuration row
	// (enabled, interval_seconds, excluded_models, max_tokens) for the model
	// health check sweep.
	ModelHealthSettingsTable string

	// AlertsTable stores the notification feed produced by the alert detectors
	// (max-spend, error-rate, provider cooldown, model-health). One row per
	// alert occurrence, deduplicated by fingerprint + suppression window.
	AlertsTable string
	// AlertSettingsTable stores the singleton operator configuration row for the
	// alert sweep (enabled, interval_seconds, per-category toggles, thresholds).
	AlertSettingsTable string

	// LiteLLMUsersTable stores the Manage-LiteLLM internal-user entity
	// (LiteLLM-style key owners). Kept separate from InternalUsersTable so the
	// Manage LiteLLM feature holds its own data without touching the runtime
	// user rows.
	LiteLLMUsersTable string
	// LiteLLMKeysTable stores the Manage-LiteLLM API keys, including complete
	// LiteLLM-style fields (spend, tags) not present on the runtime api_keys
	// table.
	LiteLLMKeysTable string
	// LiteLLMKeyPoliciesTable stores per-key policy for LiteLLMKeysTable rows
	// (FK ON DELETE CASCADE), including LiteLLM-style budget + tpm_limit +
	// aliases.
	LiteLLMKeyPoliciesTable string
	// LiteLLMSyncSettingsTable stores the singleton Manage-LiteLLM external
	// sync settings (base URL + sealed master API key + last-sync outcome).
	LiteLLMSyncSettingsTable string

	// RuntimeConfigTable stores the PG-first control-plane singleton: one
	// row (id = 1, CHECK enforced) holding the canonical runtime configuration
	// (settings + extra metadata + revision counter + update provenance) that
	// supersedes the legacy config.yaml / auths/ mirror for the embedded
	// core bridge. No row is seeded; callers initialize the singleton on
	// first write.
	RuntimeConfigTable string
	// ConfigRevisionsTable stores the append-only history of every accepted
	// runtime_config change (Phase 1 Task 2): one row per revision with the
	// settings/resource_snapshot JSONB blobs, the checksum, and the audit
	// metadata (created_at, created_by, reason). Keyed by a monotonic
	// BIGINT revision so the history is replayable in order. No row is
	// seeded; writers append on every accepted change.
	ConfigRevisionsTable string
	// ConfigImportsTable stores the audit trail of every runtime_config
	// import attempt (Phase 1 Task 3): one row per outcome (dry_run,
	// committed, rejected, or replaced) with the source + canonical
	// checksums, the runtime_config revision when relevant, a JSONB
	// resource_counts payload, an optional error_summary, the actor, and the
	// mode that triggered the import. No row is seeded; writers append per
	// attempt.
	ConfigImportsTable string

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
	db                *sql.DB
	cfg               PostgresStoreConfig
	spoolRoot         string
	configPath        string
	authDir           string
	pricingSourcesDir string
	cooldownStore     *postgresCooldownStateStore
	mu                sync.Mutex
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
	if cfg.CooldownTable == "" {
		cfg.CooldownTable = defaultCooldownTable
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
	if cfg.UsageStatDayTable == "" {
		cfg.UsageStatDayTable = defaultUsageStatDayTable
	}
	if cfg.ModelsTable == "" {
		cfg.ModelsTable = defaultModelsTable
	}
	if cfg.ModelPricingTable == "" {
		cfg.ModelPricingTable = defaultModelPricingTable
	}
	if cfg.ModelRoutingTable == "" {
		cfg.ModelRoutingTable = defaultModelRoutingTable
	}
	if cfg.ErrorMessagesTable == "" {
		cfg.ErrorMessagesTable = defaultErrorMessagesTable
	}
	if cfg.PricingSourcesTable == "" {
		cfg.PricingSourcesTable = defaultPricingSourcesTable
	}
	if cfg.InternalUsersTable == "" {
		cfg.InternalUsersTable = defaultInternalUsersTable
	}
	if cfg.UserWindowsTable == "" {
		cfg.UserWindowsTable = defaultUserWindowsTable
	}
	if cfg.ManagementTokensTable == "" {
		cfg.ManagementTokensTable = defaultManagementTokensTable
	}
	if cfg.ManagementTokenPoliciesTable == "" {
		cfg.ManagementTokenPoliciesTable = defaultManagementTokenPoliciesTable
	}
	if cfg.ManagementAuditLogTable == "" {
		cfg.ManagementAuditLogTable = defaultManagementAuditLogTable
	}
	if cfg.UpstreamProvidersTable == "" {
		cfg.UpstreamProvidersTable = defaultUpstreamProvidersTable
	}
	if cfg.UpstreamProviderModelsTable == "" {
		cfg.UpstreamProviderModelsTable = defaultUpstreamProviderModelsTable
	}
	if cfg.UpstreamProviderHeadersTable == "" {
		cfg.UpstreamProviderHeadersTable = defaultUpstreamProviderHeadersTable
	}
	if cfg.UpstreamProviderExcludedTable == "" {
		cfg.UpstreamProviderExcludedTable = defaultUpstreamProviderExcludedTable
	}
	if cfg.UpstreamProviderEntriesTable == "" {
		cfg.UpstreamProviderEntriesTable = defaultUpstreamProviderEntriesTable
	}
	if cfg.ProxyPoolsTable == "" {
		cfg.ProxyPoolsTable = defaultProxyPoolsTable
	}
	if cfg.UpstreamSyncLogTable == "" {
		cfg.UpstreamSyncLogTable = defaultUpstreamSyncLogTable
	}
	if cfg.ModelGroupsTable == "" {
		cfg.ModelGroupsTable = defaultModelGroupsTable
	}
	if cfg.AutoRoutersTable == "" {
		cfg.AutoRoutersTable = defaultAutoRoutersTable
	}
	if cfg.AutoRouterProfilesTable == "" {
		cfg.AutoRouterProfilesTable = defaultAutoRouterProfilesTable
	}
	if cfg.ModelHealthTable == "" {
		cfg.ModelHealthTable = defaultModelHealthTable
	}
	if cfg.ModelHealthLogTable == "" {
		cfg.ModelHealthLogTable = defaultModelHealthLogTable
	}
	if cfg.ModelHealthSettingsTable == "" {
		cfg.ModelHealthSettingsTable = defaultModelHealthSettingsTable
	}
	if cfg.AlertsTable == "" {
		cfg.AlertsTable = defaultAlertsTable
	}
	if cfg.AlertSettingsTable == "" {
		cfg.AlertSettingsTable = defaultAlertSettingsTable
	}
	if cfg.LiteLLMUsersTable == "" {
		cfg.LiteLLMUsersTable = defaultLiteLLMUsersTable
	}
	if cfg.LiteLLMKeysTable == "" {
		cfg.LiteLLMKeysTable = defaultLiteLLMKeysTable
	}
	if cfg.LiteLLMKeyPoliciesTable == "" {
		cfg.LiteLLMKeyPoliciesTable = defaultLiteLLMKeyPoliciesTable
	}
	if cfg.LiteLLMSyncSettingsTable == "" {
		cfg.LiteLLMSyncSettingsTable = defaultLiteLLMSyncSettingsTable
	}
	if cfg.RuntimeConfigTable == "" {
		cfg.RuntimeConfigTable = defaultRuntimeConfigTable
	}
	if cfg.ConfigRevisionsTable == "" {
		cfg.ConfigRevisionsTable = defaultConfigRevisionsTable
	}
	if cfg.ConfigImportsTable == "" {
		cfg.ConfigImportsTable = defaultConfigImportsTable
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
	// pricingSourcesDir persists uploaded LiteLLM-format catalogs. Lax perms
	// (0o755) match the rest of the spool: files contain only public prices.
	pricingSourcesDir := filepath.Join(absSpool, "pricing_sources")
	if cfg.PricingSourcesDir != "" {
		if abs, err := filepath.Abs(cfg.PricingSourcesDir); err == nil {
			pricingSourcesDir = abs
		}
	}
	if err = os.MkdirAll(pricingSourcesDir, 0o755); err != nil {
		return nil, fmt.Errorf("postgres store: create pricing_sources directory: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres store: open database connection: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres store: ping database: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	store := &PostgresStore{
		db:                db,
		cfg:               cfg,
		spoolRoot:         absSpool,
		configPath:        filepath.Join(configDir, "config.yaml"),
		authDir:           authDir,
		pricingSourcesDir: pricingSourcesDir,
	}
	store.cooldownStore = &postgresCooldownStateStore{store: store}
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

	cooldownTable := s.fullTableName(s.cfg.CooldownTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			auth_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			content JSONB NOT NULL,
			deleted BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (auth_id, model)
		)
	`, cooldownTable)); err != nil {
		return fmt.Errorf("postgres store: create cooldown table: %w", err)
	}

	if err := s.ensurePolicySchema(ctx); err != nil {
		return err
	}
	if err := s.ensureLiteLLMSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureRuntimeConfigSchema(ctx); err != nil {
		return err
	}
	return nil
}

// ensureRuntimeConfigSchema creates the PG-first control-plane singleton
// runtime_config table, its append-only history table config_revisions, and
// the config_imports audit table. The CHECK id = 1 constraint on
// runtime_config guarantees only one canonical row; callers (subsequent
// tasks) seed the singleton on first write. config_revisions is keyed by a
// monotonic BIGINT revision so writers append one row per accepted change and
// the history stays replayable in revision order. config_imports records one
// row per import attempt — dry_run, committed, rejected, or replaced — and
// carries the source + canonical checksums, the runtime_config revision when
// relevant, and a JSONB resource_counts payload for downstream analysis. No
// row is seeded in any table here so this step stays purely a
// schema-availability step.
func (s *PostgresStore) ensureRuntimeConfigSchema(ctx context.Context) error {
	runtimeConfigTable := s.fullTableName(s.cfg.RuntimeConfigTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id              INTEGER PRIMARY KEY,
			settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
			extra           JSONB NOT NULL DEFAULT '{}'::jsonb,
			revision        BIGINT NOT NULL DEFAULT 1,
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_by      TEXT,
			updated_source  TEXT NOT NULL DEFAULT 'system',
			CONSTRAINT runtime_config_singleton CHECK (id = 1)
		)
	`, runtimeConfigTable)); err != nil {
		return fmt.Errorf("postgres store: create runtime_config table: %w", err)
	}

	// config_revisions is the append-only history of every accepted
	// runtime_config change. Keyed by a monotonic BIGINT revision; the
	// settings/resource_snapshot JSONB blobs capture the full pre-write state,
	// checksum is the integrity handle, and reason + created_by carry the
	// audit provenance. created_by is nullable because some writers (e.g. the
	// boot-time loader) have no associated operator. No DEFAULT on revision
	// so every insert is forced to pick a value explicitly — the writer is
	// responsible for monotonically advancing it.
	configRevisionsTable := s.fullTableName(s.cfg.ConfigRevisionsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			revision          BIGINT PRIMARY KEY,
			settings          JSONB NOT NULL,
			resource_snapshot JSONB NOT NULL,
			checksum          TEXT NOT NULL,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_by        TEXT,
			reason            TEXT NOT NULL
		)
	`, configRevisionsTable)); err != nil {
		return fmt.Errorf("postgres store: create config_revisions table: %w", err)
	}
	// Idempotent DESC index over created_at to make history reads (latest
	// first, paging by time) cheap as the table grows. The PRIMARY KEY on
	// revision already supports revision-ordered scans; this index serves
	// the secondary created_at-ordered access path the dashboard timeline
	// view uses.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_config_revisions_created_at ON %s(created_at DESC)`,
		configRevisionsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create config_revisions created_at index: %w", err)
	}

	// config_imports is the per-attempt audit table for the import-config /
	// export-config / verify-config CLI surface. BIGSERIAL keeps the inserts
	// idempotent under repeated dry-runs; status is a free-form TEXT so the
	// existing four values (dry_run, committed, rejected, replaced) can
	// evolve without a schema change. mode captures the calling CLI
	// surface, and resource_counts is a JSONB blob so structured counters
	// round-trip without lossy stringification. revision is nullable
	// because dry_run and rejected attempts are not associated with a
	// runtime_config row. error_summary is nullable for the same reason on
	// the happy path. No DEFAULT on status or mode: the writer must always
	// pick a value explicitly so the audit trail stays unambiguous.
	configImportsTable := s.fullTableName(s.cfg.ConfigImportsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                  BIGSERIAL PRIMARY KEY,
			source_label        TEXT,
			source_checksum     TEXT,
			canonical_checksum  TEXT,
			status              TEXT NOT NULL,
			revision            BIGINT,
			resource_counts     JSONB NOT NULL DEFAULT '{}'::jsonb,
			error_summary       TEXT,
			actor               TEXT,
			mode                TEXT NOT NULL,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, configImportsTable)); err != nil {
		return fmt.Errorf("postgres store: create config_imports table: %w", err)
	}
	// Idempotent DESC index over created_at so listing the most recent
	// import outcomes is index-driven rather than a sequential scan. The
	// BIGSERIAL id already supports id-ordered scans; this index serves the
	// secondary created_at-ordered access path the dashboard timeline view
	// uses.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_config_imports_created_at ON %s(created_at DESC)`,
		configImportsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create config_imports created_at index: %w", err)
	}
	return nil
}

// ensureLiteLLMSchema creates the tables backing the Manage LiteLLM feature
// (LiteLLM-style internal users + API keys + key policies). The tables are
// intentionally separate from the runtime tables so the Manage LiteLLM feature
// can store complete LiteLLM-style data (key spend, budget + duration,
// tpm_limit, tags, aliases) without affecting the request-serving path. All
// statements are idempotent (CREATE TABLE IF NOT EXISTS).
func (s *PostgresStore) ensureLiteLLMSchema(ctx context.Context) error {
	usersTable := s.fullTableName(s.cfg.LiteLLMUsersTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                  TEXT PRIMARY KEY,
			user_alias          TEXT,
			user_email          TEXT UNIQUE,
			user_role           TEXT NOT NULL DEFAULT 'internal_user',
			models              JSONB NOT NULL DEFAULT '[]'::jsonb,
			metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
			max_budget          NUMERIC(12,6),
			budget_duration     TEXT,
			budget_reset_at     TIMESTAMPTZ,
			rpm_limit           BIGINT,
			tpm_limit           BIGINT,
			max_parallel_requests INTEGER,
			spend               NUMERIC(12,6) NOT NULL DEFAULT 0,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, usersTable)); err != nil {
		return fmt.Errorf("postgres store: create litellm_internal_users table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_litellm_internal_users_role ON %s(user_role)`, usersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create litellm_internal_users role index: %w", err)
	}

	keysTable := s.fullTableName(s.cfg.LiteLLMKeysTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL,
			key_alias     TEXT,
			key_hash      TEXT NOT NULL UNIQUE,
			key_prefix    TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'active',
			user_id       TEXT,
			key_spend     NUMERIC(12,6) NOT NULL DEFAULT 0,
			tags          JSONB NOT NULL DEFAULT '[]'::jsonb,
			metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at    TIMESTAMPTZ,
			last_used_at  TIMESTAMPTZ
		)
	`, keysTable)); err != nil {
		return fmt.Errorf("postgres store: create litellm_api_keys table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_litellm_api_keys_key_hash ON %s(key_hash)`, keysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create litellm_api_keys key_hash index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_litellm_api_keys_status ON %s(status) WHERE status = 'active'`, keysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create litellm_api_keys status index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_litellm_api_keys_user_id ON %s(user_id) WHERE user_id IS NOT NULL`, keysTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create litellm_api_keys user_id index: %w", err)
	}

	policiesTable := s.fullTableName(s.cfg.LiteLLMKeyPoliciesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			api_key_id            TEXT PRIMARY KEY REFERENCES %s(id) ON DELETE CASCADE,
			rpm_limit             INTEGER,
			tpm_limit             INTEGER,
			budget_usd            NUMERIC(12,6),
			budget_duration       TEXT,
			max_parallel_requests INTEGER,
			allowed_models        JSONB NOT NULL DEFAULT '[]'::jsonb,
			blocked_models        JSONB NOT NULL DEFAULT '[]'::jsonb,
			aliases               JSONB NOT NULL DEFAULT '{}'::jsonb,
			allowed_ips           JSONB NOT NULL DEFAULT '[]'::jsonb,
			blocked_ips           JSONB NOT NULL DEFAULT '[]'::jsonb,
			model_routes          JSONB NOT NULL DEFAULT '[]'::jsonb,
			updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, policiesTable, keysTable)); err != nil {
		return fmt.Errorf("postgres store: create litellm_key_policies table: %w", err)
	}

	// litellm_sync_settings stores the singleton Manage-LiteLLM external sync
	// configuration (base URL + sealed master API key) and the last-sync
	// outcome. master_key_sealed holds the SHA-256-keyed AES-GCM ciphertext of
	// the external LiteLLM master API key when PGSTORE_ENCRYPTION_KEY is set;
	// otherwise plaintext (the legacy-tolerant path shared with other stores).
	syncTable := s.fullTableName(s.cfg.LiteLLMSyncSettingsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                  INTEGER PRIMARY KEY DEFAULT 1,
			enabled             BOOLEAN NOT NULL DEFAULT FALSE,
			interval_seconds    INTEGER NOT NULL DEFAULT 300,
			base_url            TEXT,
			master_key_sealed   TEXT,
			master_key_prefix   TEXT,
			last_sync_at        TIMESTAMPTZ,
			last_sync_status    TEXT,
			last_sync_error     TEXT,
			last_sync_users     INTEGER NOT NULL DEFAULT 0,
			last_sync_keys      INTEGER NOT NULL DEFAULT 0,
			last_sync_logs      INTEGER NOT NULL DEFAULT 0,
			last_nixllm_sync_at         TIMESTAMPTZ,
			last_nixllm_sync_status     TEXT,
			last_nixllm_sync_error      TEXT,
			last_nixllm_sync_users      INTEGER NOT NULL DEFAULT 0,
			last_nixllm_sync_keys       INTEGER NOT NULL DEFAULT 0,
			last_nixllm_sync_logs       INTEGER NOT NULL DEFAULT 0,
			last_nixllm_sync_usage      BOOLEAN NOT NULL DEFAULT FALSE,
			last_litellm_users_updated_at TIMESTAMPTZ,
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT litellm_sync_settings_singleton CHECK (id = 1)
		)
	`, syncTable)); err != nil {
		return fmt.Errorf("postgres store: create litellm_sync_settings table: %w", err)
	}
	// Seed the singleton row so GetLitellmSyncSettings always finds a row.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id) VALUES (1) ON CONFLICT (id) DO NOTHING`, syncTable,
	)); err != nil {
		return fmt.Errorf("postgres store: seed litellm_sync_settings singleton: %w", err)
	}
	// Backfill the "sync to NixLLM" outcome columns (external LiteLLM users →
	// runtime internal_users). Idempotent so existing deployments upgrade
	// transparently without recreating the table.
	for _, col := range []string{
		`last_sync_logs INTEGER NOT NULL DEFAULT 0`,
		`last_nixllm_sync_at TIMESTAMPTZ`,
		`last_nixllm_sync_status TEXT`,
		`last_nixllm_sync_error TEXT`,
		`last_nixllm_sync_users INTEGER NOT NULL DEFAULT 0`,
		`last_nixllm_sync_keys INTEGER NOT NULL DEFAULT 0`,
		`last_nixllm_sync_logs INTEGER NOT NULL DEFAULT 0`,
		`last_nixllm_sync_usage BOOLEAN NOT NULL DEFAULT FALSE`,
		`last_litellm_users_updated_at TIMESTAMPTZ`,
	} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s`, syncTable, col,
		)); err != nil {
			return fmt.Errorf("postgres store: alter litellm_sync_settings add column %q: %w", col, err)
		}
	}
	return nil
}

// Migrate runs idempotent schema migrations for tables/indexes that were added
// after initial schema creation. It must be safe to run on every startup.
// Applied migrations are appended here by later tasks (new usage_events indexes,
// the usage_stat_day rollup table).
//
// Migrate is intentionally a distinct step from EnsureSchema: EnsureSchema
// creates the base tables at first construction, while Migrate evolves the
// schema for already-running deployments. Tests exercise it explicitly via
// ensureMigrated rather than folding it into EnsureSchema so the two phases
// stay independently testable.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	// usage_events aggregate indexes. These target queries that otherwise full-scan
	// the table: LiteLLM /spend/users (group-by-user over a time window),
	// per-model spend leaderboards, and provider-filtered aggregates. The table
	// name is dynamic (schema-qualified), so each statement is built with
	// fmt.Sprintf. All are idempotent and safe to run on every startup. The
	// partial WHERE user_id IS NOT NULL style matches the existing
	// idx_usage_events_user_id index created in EnsureSchema.
	usageEventsTable := s.fullTableName(s.cfg.UsageEventsTable)
	migrations := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_usage_events_requested_at_user ON %s(requested_at DESC, user_id) WHERE user_id IS NOT NULL`, usageEventsTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_usage_events_user_model_at ON %s(user_id, model, requested_at) WHERE user_id IS NOT NULL`, usageEventsTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_usage_events_provider ON %s(provider, requested_at)`, usageEventsTable),
	}
	for _, q := range migrations {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("postgres store: migrate usage indexes: %w", err)
		}
	}

	// upstream_provider_api_key_entries identity migration. Existing rows may
	// predate the nullable name column; keep this separate from EnsureSchema so
	// old installations are upgraded on startup.
	upstreamEntriesTable := s.fullTableName(s.cfg.UpstreamProviderEntriesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS name TEXT`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries name column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_provider_entries_provider_name
		 ON %s(provider_id, lower(name))
		 WHERE name IS NOT NULL AND btrim(name) <> ''`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries name index: %w", err)
	}
	// upstream_provider_api_key_entries weight column. The dashboard now lets
	// each API-key entry carry a proportional selection weight under
	// weighted-round-robin routing. The column is nullable so legacy rows
	// survive the upgrade with their existing routing behavior (nil falls back
	// to the scheduler default). No DEFAULT: a non-null default would silently
	// hide the "user did not pick a weight" signal from the dashboard.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS weight INTEGER`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries weight column: %w", err)
	}
	// upstream_providers.routing_strategy column. The optional in-pool
	// selection strategy for entry-bearing providers; NULL = unset = today's
	// behavior. Nullable TEXT with no DEFAULT so legacy rows keep NULL.
	upstreamProvidersTable := s.fullTableName(s.cfg.UpstreamProvidersTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS routing_strategy TEXT`, upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream providers routing strategy column: %w", err)
	}
	// upstream_providers.circuit_breaker column. The opt-in pool-level circuit
	// breaker (design G3): rows set to true feed the breaker and are subject
	// to its pool-wide blocking; false (default) keeps today's behavior.
	// NOT NULL DEFAULT FALSE so legacy rows survive the upgrade opted out.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS circuit_breaker BOOLEAN NOT NULL DEFAULT FALSE`, upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream providers circuit breaker column: %w", err)
	}
	// upstream_provider_api_key_entries priority column. Optional per-entry
	// selection tier; NULL = inherit the provider row priority. Nullable
	// INTEGER with no DEFAULT, mirroring the weight column contract.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS priority INTEGER`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries priority column: %w", err)
	}
	// upstream_provider_api_key_entries disabled column. Per-entry on/off
	// toggle for the dashboard's entries editor; the upstreamsync renderer
	// skips disabled entries when rendering config.yaml. NOT NULL DEFAULT
	// FALSE so legacy rows keep their routing behavior after the upgrade
	// and no NULL-scan handling is needed.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries disabled column: %w", err)
	}

	// proxy_pool_id binding columns (Proxy Pools feature). Entries override;
	// the provider row value is the default for entries that leave it NULL.
	// Both reference proxy_pools(id): deleting a pool the DB still references
	// fails at the store layer before the handler's bound_entry_count check.
	// proxy_pools itself is created by EnsureSchema, which runs before Migrate.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS proxy_pool_id BIGINT REFERENCES %s(id)`,
		upstreamProvidersTable, s.fullTableName(s.cfg.ProxyPoolsTable),
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream providers proxy_pool_id column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS proxy_pool_id BIGINT REFERENCES %s(id)`,
		upstreamEntriesTable, s.fullTableName(s.cfg.ProxyPoolsTable),
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream entries proxy_pool_id column: %w", err)
	}

	// upstream_provider_api_key_entries retry columns. Per-entry overrides
	// for the routing.retry block in config (zero-downtime design 2026-09-18).
	// All three are nullable; NULL means "fall back to global config default."
	// retry_max_attempts SMALLINT (matches RoutingConfig.Retry.MaxAttempts uint16).
	// retry_max_time_ms / retry_backoff_ms INTEGER (matches uint32 in Go;
	// PG INTEGER is int4 / ~2.1e9 — far above any sane budget).
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s
			ADD COLUMN IF NOT EXISTS retry_max_attempts SMALLINT,
			ADD COLUMN IF NOT EXISTS retry_max_time_ms INTEGER,
			ADD COLUMN IF NOT EXISTS retry_backoff_ms INTEGER`,
		upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream entries retry columns: %w", err)
	}

	// upstream_provider_api_key_entries max_concurrent / max_wait_ms columns
	// (Max Concurrent feature, design 2026-09-25). Per-entry in-flight hard
	// cap + wait budget before failover. Both nullable: NULL means unlimited /
	// default wait (feature off for that entry). No DEFAULT, mirroring the
	// weight/priority contract so legacy rows survive the upgrade unchanged
	// and "user did not set a cap" stays distinct from "cap 0".
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s
			ADD COLUMN IF NOT EXISTS max_concurrent INTEGER,
			ADD COLUMN IF NOT EXISTS max_wait_ms INTEGER`,
		upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream entries max concurrent columns: %w", err)
	}
	// upstream_provider_api_key_entries auto_disabled runtime flag + attribution
	// (Auto-Disable feature). Runtime-written by the server-side sink on a
	// matching upstream error, never operator input; the renderer skips the
	// entry exactly like Disabled. NOT NULL DEFAULT FALSE so legacy rows stay
	// enabled. auto_disabled_at / auto_disabled_reason are nullable; the
	// sweeper uses them to auto re-enable after the provider cooldown.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s
			ADD COLUMN IF NOT EXISTS auto_disabled BOOLEAN NOT NULL DEFAULT FALSE,
			ADD COLUMN IF NOT EXISTS auto_disabled_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS auto_disabled_reason TEXT`,
		upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream entries auto-disable columns: %w", err)
	}
	// upstream_providers auto-disable config (Auto-Disable feature). The error
	// codes that permanently auto-disable an entry + the re-enable cooldown.
	// Both nullable: NULL / empty = feature off for the provider, manual
	// re-enable only. TEXT[] keeps the list native (no JSONB decode needed).
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s
			ADD COLUMN IF NOT EXISTS auto_disable_error_codes TEXT[],
			ADD COLUMN IF NOT EXISTS auto_disable_cooldown_seconds INTEGER`,
		upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream providers auto-disable columns: %w", err)
	}

	// usage_stat_day is the pre-aggregated daily rollup of usage_events. It folds
	// per-request rows into per-(day, user, api_key, model, provider, source)
	// counters so daily aggregate queries (LiteLLM /spend/users, per-model/API-key
	// spend over a range) read O(buckets) instead of O(events). The bucket columns
	// user_id/api_key_id/model/provider/source may legitimately be empty or NULL
	// for some events, and are COALESCE'd to '' by the fold so the PK stays free
	// of NULLs (Postgres treats '' as distinct from NULL). Counters are overwritten
	// (SET, not +=) by RunRollup/BackfillRollup so re-running the fold over the
	// append-only source recomputes the same totals — the idempotency contract the
	// daily driver relies on. Idempotent CREATE TABLE IF NOT EXISTS.
	rollupTable := s.fullTableName(s.cfg.UsageStatDayTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			stat_day      DATE NOT NULL,
			user_id       TEXT,
			api_key_id    TEXT,
			model         TEXT,
			provider      TEXT,
			source        TEXT,
			request_count BIGINT NOT NULL DEFAULT 0,
			fail_count    BIGINT NOT NULL DEFAULT 0,
			input_tokens  BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			tot_tokens    BIGINT NOT NULL DEFAULT 0,
			cost_usd      NUMERIC(12,6) NOT NULL DEFAULT 0,
			PRIMARY KEY (stat_day, user_id, api_key_id, model, provider, source)
		)
	`, rollupTable)); err != nil {
		return fmt.Errorf("postgres store: create usage_stat_day rollup table: %w", err)
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
	// Backfill model_routes on api_key_policies (per-allowed-model upstream
	// provider pinning). Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS model_routes JSONB NOT NULL DEFAULT '[]'::jsonb`, policiesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_key_policies add model_routes: %w", err)
	}
	// Backfill allowed_ips / blocked_ips on api_key_policies (per-API-key
	// source IP allowlist/blocklist). Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS allowed_ips JSONB NOT NULL DEFAULT '[]'::jsonb`, policiesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_key_policies add allowed_ips: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS blocked_ips JSONB NOT NULL DEFAULT '[]'::jsonb`, policiesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_key_policies add blocked_ips: %w", err)
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
			route_model             TEXT,
			served_model            TEXT,
			endpoint                TEXT,
			auth_type               TEXT,
			source                  TEXT,
			reasoning_effort        TEXT,
			service_tier            TEXT,
			response_service_tier   TEXT,
			tier                    TEXT,
			router_id               TEXT,
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
	// Partial unique index on request_id for idempotent historical log imports
	// (the "sync to NixLLM" spend-log migration). Postgres treats NULLs as
	// distinct, so only non-NULL/non-empty request_ids are deduplicated.
	//
	// Existing deployments may already hold duplicate request_ids (the usage
	// flusher can re-record a request that had a request-id context set), which
	// would make CREATE UNIQUE INDEX fail with a 23505 duplicate error. Dedupe
	// first — keeping the earliest row per request_id, the same semantics as the
	// ON CONFLICT (request_id) DO NOTHING used by the spend-log import — then
	// create the index. Both steps run only when the index is missing so an
	// already-migrated deployment is not re-scanned on every boot.
	requestIDIdxName := "idx_usage_events_request_id"
	schemaName := strings.TrimSpace(s.cfg.Schema)
	if schemaName == "" {
		// No explicit schema: resolve the connection's current schema so the
		// existence check is scoped to the actual table (index names are only
		// unique per schema, and tests create one schema per case). A driver
		// that answers no rows (some unit tests mock every query) falls back to
		// "public" — the schema name only feeds the EXISTS probe below.
		var currentSchema string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(current_schema(), 'public')`).Scan(&currentSchema); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				currentSchema = "public"
			} else {
				return fmt.Errorf("postgres store: resolve current_schema: %w", err)
			}
		}
		schemaName = currentSchema
	}
	var requestIDIdxExists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1 AND schemaname = $2 AND tablename = $3)`,
		requestIDIdxName, schemaName, s.cfg.UsageEventsTable,
	).Scan(&requestIDIdxExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			requestIDIdxExists = false // driver cannot answer; treat as missing
		} else {
			return fmt.Errorf("postgres store: check usage_events request_id index: %w", err)
		}
	}
	if !requestIDIdxExists {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
			DELETE FROM %s a USING %s b
			WHERE a.request_id = b.request_id
			  AND a.request_id IS NOT NULL AND a.request_id <> ''
			  AND a.id > b.id
		`, usageEventsTable, usageEventsTable)); err != nil {
			return fmt.Errorf("postgres store: dedupe usage_events request_id: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`CREATE UNIQUE INDEX %s ON %s(request_id) WHERE request_id IS NOT NULL AND request_id <> ''`, requestIDIdxName, usageEventsTable,
		)); err != nil {
			return fmt.Errorf("postgres store: create usage_events request_id unique index: %w", err)
		}
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
	// Backfill the tier and router_id columns on usage_events. These store the
	// Auto Router decision (complexity tier + owning router id) and are empty
	// for requests not routed by the Auto Router. Idempotent so existing
	// deployments pick them up without a full rebuild.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS tier TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add tier: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS router_id TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add router_id: %w", err)
	}
	// decision explainability columns: scored/effective/mapping tiers, the
	// decision cause (literal_keyword_match | complexity_scorer), and the
	// active profile identity. Auto Router Decision snapshot is a JSONB blob
	// carrying the full per-request decision. Idempotent so older deployments
	// pick the columns up on next boot without rebuild.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"scored_tier", "TEXT"},
		{"effective_tier", "TEXT"},
		{"mapping_tier", "TEXT"},
		{"decision_cause", "TEXT"},
		{"profile_version", "BIGINT"},
		{"profile_hash", "TEXT"},
		{"auto_router_decision", "JSONB"},
	} {
		def := col.def
		if col.name == "auto_router_decision" {
			def = "JSONB"
		}
		statement := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s`, usageEventsTable, col.name, def)
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("postgres store: alter usage_events add %s: %w", col.name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_usage_events_router_effective ON %s(router_id, effective_tier, requested_at) WHERE router_id IS NOT NULL`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_events router+effective index: %w", err)
	}
	// Backfill the discount_pct column on usage_events. Stamped by the usage
	// flusher with the resolved model-group discount percentage (0-100; 0/NULL
	// = none) so the historical cost derivation stays accurate even after a
	// group's discount is later changed. Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS discount_pct NUMERIC(5,2) NOT NULL DEFAULT 0`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add discount_pct: %w", err)
	}
	// original_cost_usd is the pre-discount cost (before the model-group
	// discount was applied), stamped at flush time alongside discount_pct. We
	// persist it rather than re-deriving it at read time so the dashboard's
	// "was $X" figure is always authoritative and immune to the pricing row
	// (or the cost_computation order) drifting between flush and read. Equals
	// cost_usd when no discount was applied. Idempotent backfill.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS original_cost_usd NUMERIC(12,6) NOT NULL DEFAULT 0`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add original_cost_usd: %w", err)
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
			route_model             TEXT,
			served_model            TEXT,
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
	// route_model records the client-requested model name (before alias/upstream
	// resolution) for usage_errors so misrouting can be diagnosed by comparing
	// it against the resolved model id. Idempotent ALTER keeps pre-existing
	// stores in sync. Mirrored on usage_events for schema consistency.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS route_model TEXT`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_errors.route_model column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS route_model TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_events.route_model column: %w", err)
	}
	// served_model records the model the upstream response reported serving so
	// a silent provider-side substitution is auditable after the fact.
	// Idempotent ALTER; mirrored on both usage tables.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS served_model TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add served_model: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS served_model TEXT`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_errors add served_model: %w", err)
	}
	// client_ip / forwarded_for record the requesting client's address for both
	// usage_events and usage_errors so failed requests can be attributed to a
	// source IP and multi-hop proxy chains stay auditable. client_ip comes from
	// gin ClientIP() (honors trusted-proxy headers); forwarded_for is the raw
	// X-Forwarded-For header captured at the edge. Idempotent ALTER so
	// pre-existing stores pick the columns up on the next start.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS client_ip TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_events.client_ip column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS forwarded_for TEXT`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_events.forwarded_for column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS client_ip TEXT`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_errors.client_ip column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS forwarded_for TEXT`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_errors.forwarded_for column: %w", err)
	}
	// Backfill discount_pct on usage_errors so the failed-attempts mirror table
	// carries the same resolved-group discount column as usage_events. Stamped
	// by the usage flusher. Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS discount_pct NUMERIC(5,2) NOT NULL DEFAULT 0`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_errors add discount_pct: %w", err)
	}
	// original_cost_usd mirrors usage_events: the pre-discount cost stamped at
	// flush time, persisted so the failed-attempt detail shows the same
	// authoritative "was $X" figure. Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS original_cost_usd NUMERIC(12,6) NOT NULL DEFAULT 0`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_errors add original_cost_usd: %w", err)
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

	// model_routing stores per-model-id global routing overrides applied to
	// every request for the model, independent of any per-API-key Models Group
	// policy. Keyed by model id only (mirroring model_pricing) because a global
	// route pins the provider set for the model across all providers that serve
	// it; the request-time handler intersects this pinned set with the registry
	// providers, exactly as it does for a Models Group route.
	modelRoutingTable := s.fullTableName(s.cfg.ModelRoutingTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id          TEXT PRIMARY KEY,
			providers   JSONB NOT NULL DEFAULT '[]'::jsonb,
			strategy    TEXT NOT NULL DEFAULT '',
			priorities  JSONB NOT NULL DEFAULT '[]'::jsonb,
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, modelRoutingTable)); err != nil {
		return fmt.Errorf("postgres store: create model_routing table: %w", err)
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

	// pricing_sources stores operator-managed external pricing catalogs:
	// remote LiteLLM-format JSON URLs and locally uploaded catalog files.
	// The pricingsource package loads each enabled row on refresh and merges
	// its entries into the in-memory match index so SyncPricingPreview can
	// surface suggestions from operator-chosen sources alongside the
	// bundled defaults.
	pricingSourcesTable := s.fullTableName(s.cfg.PricingSourcesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id              SERIAL PRIMARY KEY,
			name            TEXT NOT NULL UNIQUE,
			source_type     TEXT NOT NULL,
			url             TEXT,
			file_path       TEXT,
			format          TEXT NOT NULL DEFAULT 'litellm',
			enabled         BOOLEAN NOT NULL DEFAULT TRUE,
			last_fetched_at TIMESTAMPTZ,
			last_error      TEXT,
			entry_count     INTEGER NOT NULL DEFAULT 0,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, pricingSourcesTable)); err != nil {
		return fmt.Errorf("postgres store: create pricing_sources table: %w", err)
	}

	// management_tokens stores management API tokens that gate access to the
	// /v0/management REST surface. Mirrors the api_keys schema shape: only the
	// SHA-256 hash is persisted; the plaintext secret is returned exactly once
	// at create/regenerate time. scope (read|write) gates which HTTP verbs the
	// token may use. 503 when the PG backend is absent (feature-detected via
	// the ManagementTokenStore nil check).
	mgmtTokensTable := s.fullTableName(s.cfg.ManagementTokensTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			key_hash     TEXT NOT NULL UNIQUE,
			key_prefix   TEXT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'active',
			scope        TEXT NOT NULL DEFAULT 'read',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at   TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ,
			metadata     JSONB NOT NULL DEFAULT '{}'::jsonb
		)
	`, mgmtTokensTable)); err != nil {
		return fmt.Errorf("postgres store: create management_tokens table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_management_tokens_key_hash ON %s(key_hash)`, mgmtTokensTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create management_tokens key_hash index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_management_tokens_status ON %s(status) WHERE status = 'active'`, mgmtTokensTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create management_tokens status index: %w", err)
	}

	// management_token_policies stores per-token policy (RPM, max-parallel,
	// endpoint allow/block lists) attached to management_tokens rows. FK
	// ON DELETE CASCADE mirrors api_key_policies.
	mgmtPoliciesTable := s.fullTableName(s.cfg.ManagementTokenPoliciesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			token_id              TEXT PRIMARY KEY REFERENCES %s(id) ON DELETE CASCADE,
			rpm_limit             INTEGER,
			max_parallel_requests INTEGER,
			hourly_rate_limit     INTEGER,
			allowed_endpoints     JSONB NOT NULL DEFAULT '[]'::jsonb,
			blocked_endpoints     JSONB NOT NULL DEFAULT '[]'::jsonb,
			allowed_ips           JSONB NOT NULL DEFAULT '[]'::jsonb,
			blocked_ips           JSONB NOT NULL DEFAULT '[]'::jsonb,
			updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, mgmtPoliciesTable, mgmtTokensTable)); err != nil {
		return fmt.Errorf("postgres store: create management_token_policies table: %w", err)
	}
	// Backfill allowed_ips / blocked_ips columns (IP allowlist/blocklist) for
	// deployments that already have management_token_policies without them.
	// Idempotent so existing deployments upgrade transparently.
	for _, col := range []string{"allowed_ips", "blocked_ips"} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s JSONB NOT NULL DEFAULT '[]'::jsonb`,
			mgmtPoliciesTable, quoteIdentifier(col),
		)); err != nil {
			return fmt.Errorf("postgres store: alter management_token_policies add %s: %w", col, err)
		}
	}

	// management_audit_log stores the audit trail of every management API call
	// made with a management token: request audit (who/what/when/latency),
	// request/response bodies for mutations, and error message for 4xx/5xx.
	// Bodies are AES-GCM-sealed at rest when UsageEncryptionKey is configured;
	// otherwise plaintext (legacy rows remain readable, forward-encrypting).
	mgmtAuditTable := s.fullTableName(s.cfg.ManagementAuditLogTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id            BIGSERIAL PRIMARY KEY,
			token_id      TEXT,
			actor_ip      TEXT,
			method        TEXT NOT NULL,
			path          TEXT NOT NULL,
			status_code   INTEGER NOT NULL DEFAULT 0,
			latency_ms    BIGINT NOT NULL DEFAULT 0,
			request_body  TEXT,
			response_body TEXT,
			error_message TEXT,
			is_error      BOOLEAN NOT NULL DEFAULT FALSE,
			occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, mgmtAuditTable)); err != nil {
		return fmt.Errorf("postgres store: create management_audit_log table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_management_audit_token ON %s(token_id, occurred_at DESC) WHERE token_id IS NOT NULL`, mgmtAuditTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create management_audit_log token index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_management_audit_occurred_at ON %s(occurred_at DESC)`, mgmtAuditTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create management_audit_log occurred_at index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_management_audit_is_error ON %s(occurred_at DESC) WHERE is_error = TRUE`, mgmtAuditTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create management_audit_log is_error index: %w", err)
	}

	// upstream_sync_log records the outcome of every upstream OAuth/auth token
	// refresh (success + failure) performed by the auth manager, including the
	// trigger that caused it (auto-scheduled / on-demand / unauthorized-retry).
	// The error_message column is AES-GCM-sealed at rest when
	// UsageEncryptionKey is configured (forward-encrypting; legacy plaintext
	// rows remain readable). Auto-swept to a 30-day retention window.
	upstreamSyncLogTable := s.fullTableName(s.cfg.UpstreamSyncLogTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id            BIGSERIAL PRIMARY KEY,
			auth_id       TEXT NOT NULL,
			provider      TEXT NOT NULL,
			trigger       TEXT NOT NULL,
			success       BOOLEAN NOT NULL DEFAULT FALSE,
			error_message TEXT,
			duration_ms   BIGINT NOT NULL DEFAULT 0,
			occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, upstreamSyncLogTable)); err != nil {
		return fmt.Errorf("postgres store: create upstream_sync_log table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_upstream_sync_log_occurred_at ON %s(occurred_at DESC)`, upstreamSyncLogTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_sync_log occurred_at index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_upstream_sync_log_provider ON %s(provider, occurred_at DESC)`, upstreamSyncLogTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_sync_log provider index: %w", err)
	}

	// upstream_providers stores every upstream provider as one normalized row.
	// It is the source of truth for both the config.yaml-based API-key
	// providers (gemini/codex/xai/claude/openai-compatibility/vertex/
	// interactions) and the OAuth/file-backed auths (claude/codex/kimi/xai/
	// vertex/aistudio/antigravity). provider_type disambiguates the kind; the
	// "oauth:" prefix marks OAuth/file-backed entries.
	upstreamProvidersTable := s.fullTableName(s.cfg.UpstreamProvidersTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                         BIGSERIAL PRIMARY KEY,
			provider_type              TEXT NOT NULL,
			name                       TEXT,
			priority                   INTEGER NOT NULL DEFAULT 0,
			disabled                   BOOLEAN NOT NULL DEFAULT FALSE,
			routing_strategy           TEXT,
			circuit_breaker            BOOLEAN NOT NULL DEFAULT FALSE,
			prefix                     TEXT,
			api_key                    TEXT,
			base_url                   TEXT,
			proxy_url                  TEXT,
			label                      TEXT,
			email                      TEXT,
			file_name                  TEXT,
			source_backend             TEXT,
			status                     TEXT,
			unavailable                BOOLEAN NOT NULL DEFAULT FALSE,
			last_error                 TEXT,
			last_error_at              TIMESTAMPTZ,
			websockets                 BOOLEAN NOT NULL DEFAULT FALSE,
			rebuild_mid_system_message BOOLEAN NOT NULL DEFAULT FALSE,
			experimental_cch_signing   BOOLEAN NOT NULL DEFAULT FALSE,
			cloak_mode                 TEXT,
			cloak_strict_mode          BOOLEAN NOT NULL DEFAULT FALSE,
			cloak_sensitive_words      JSONB NOT NULL DEFAULT '[]'::jsonb,
			cloak_cache_user_id        BOOLEAN,
			token_access_token         TEXT,
			token_refresh_token        TEXT,
			token_token_type           TEXT,
			token_expiry               TIMESTAMPTZ,
			token_expired              BOOLEAN,
			token_scope                TEXT,
			extra_config               JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, upstreamProvidersTable)); err != nil {
		return fmt.Errorf("postgres store: create upstream_providers table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_providers_type_file ON %s(provider_type, file_name) WHERE file_name IS NOT NULL`, upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_providers uniqueness index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_upstream_providers_type ON %s(provider_type)`, upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_providers type index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_upstream_providers_disabled ON %s(disabled) WHERE disabled = TRUE`, upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_providers disabled index: %w", err)
	}

	upstreamModelsTable := s.fullTableName(s.cfg.UpstreamProviderModelsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                 BIGSERIAL PRIMARY KEY,
			provider_id        BIGINT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			name               TEXT NOT NULL,
			alias              TEXT,
			display_name       TEXT,
				force_mapping      BOOLEAN NOT NULL DEFAULT FALSE,
				fork               BOOLEAN NOT NULL DEFAULT FALSE,
				image              BOOLEAN NOT NULL DEFAULT FALSE,
				input_modalities   JSONB NOT NULL DEFAULT '[]'::jsonb,
				output_modalities  JSONB NOT NULL DEFAULT '[]'::jsonb,
				thinking           JSONB,
				wire_format        TEXT NOT NULL DEFAULT 'openai',
				sort_order         INTEGER NOT NULL DEFAULT 0
		)
	`, upstreamModelsTable, upstreamProvidersTable)); err != nil {
		return fmt.Errorf("postgres store: create upstream_provider_models table: %w", err)
	}
	// Idempotently add the fork column for databases running an older schema
	// (CREATE TABLE IF NOT EXISTS does not backfill new columns to existing
	// tables). Ignore the error when the column already exists.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS fork BOOLEAN NOT NULL DEFAULT FALSE`,
		upstreamModelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter upstream_provider_models add fork column: %w", err)
	}
	// Idempotently add wire_format for the same reason; the default covers
	// pre-opencode-go rows (empty string in Go = the openai default).
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS wire_format TEXT NOT NULL DEFAULT 'openai'`,
		upstreamModelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter upstream_provider_models add wire_format column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_upstream_provider_models_provider ON %s(provider_id)`, upstreamModelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_provider_models index: %w", err)
	}

	upstreamHeadersTable := s.fullTableName(s.cfg.UpstreamProviderHeadersTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			provider_id  BIGINT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			header_key   TEXT NOT NULL,
			header_value TEXT NOT NULL,
			PRIMARY KEY (provider_id, header_key)
		)
	`, upstreamHeadersTable, upstreamProvidersTable)); err != nil {
		return fmt.Errorf("postgres store: create upstream_provider_headers table: %w", err)
	}

	upstreamExcludedTable := s.fullTableName(s.cfg.UpstreamProviderExcludedTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			provider_id BIGINT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			model       TEXT NOT NULL,
			PRIMARY KEY (provider_id, model)
		)
	`, upstreamExcludedTable, upstreamProvidersTable)); err != nil {
		return fmt.Errorf("postgres store: create upstream_provider_excluded_models table: %w", err)
	}

	upstreamEntriesTable := s.fullTableName(s.cfg.UpstreamProviderEntriesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id          BIGSERIAL PRIMARY KEY,
			provider_id BIGINT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
			api_key     TEXT NOT NULL,
			name        TEXT,
			proxy_url   TEXT,
			sort_order  INTEGER NOT NULL DEFAULT 0,
			weight      INTEGER,
			priority    INTEGER
		)
	`, upstreamEntriesTable, upstreamProvidersTable)); err != nil {
		return fmt.Errorf("postgres store: create upstream_provider_api_key_entries table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_upstream_provider_entries_provider ON %s(provider_id)`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create upstream_provider_api_key_entries index: %w", err)
	}

	// proxy_pools stores named egress-proxy pools. type discriminates standard
	// http/socks proxies ("http") from deployed relay workers ("vercel",
	// "cloudflare", "deno") whose proxy_url is a relay base URL used with the
	// x-relay-target/x-relay-path header contract. strict_proxy defaults TRUE:
	// a proxy failure fails the request (no silent direct fallback = no IP
	// leak); tests are status-only and never flip is_active.
	proxyPoolsTable := s.fullTableName(s.cfg.ProxyPoolsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id             BIGSERIAL PRIMARY KEY,
			name           TEXT NOT NULL,
			proxy_url      TEXT NOT NULL,
			no_proxy       TEXT NOT NULL DEFAULT '',
			type           TEXT NOT NULL DEFAULT 'http',
			is_active      BOOLEAN NOT NULL DEFAULT TRUE,
			strict_proxy   BOOLEAN NOT NULL DEFAULT TRUE,
			test_status    TEXT NOT NULL DEFAULT 'unknown',
			last_tested_at TIMESTAMPTZ,
			last_error     TEXT,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, proxyPoolsTable)); err != nil {
		return fmt.Errorf("postgres store: create proxy_pools table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_pools_name ON %s(lower(name))`, proxyPoolsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create proxy_pools name index: %w", err)
	}

	// model_groups stores reusable Model Group templates: an allowed_models /
	// blocked_models grant list plus optional per-model upstream routing
	// (model_routes). Groups are attached to API-key policies or internal
	// users via the model_group_id nullable TEXT column added below. The group
	// is the source of truth for allowed/blocked (and routes, for API-key
	// policies) once attached — see policy.enforce.go for the override path.
	modelGroupsTable := s.fullTableName(s.cfg.ModelGroupsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id             TEXT PRIMARY KEY,
			name           TEXT NOT NULL UNIQUE,
			description    TEXT,
			allowed_models JSONB NOT NULL DEFAULT '[]'::jsonb,
			blocked_models JSONB NOT NULL DEFAULT '[]'::jsonb,
			model_routes   JSONB NOT NULL DEFAULT '[]'::jsonb,
			metadata       JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, modelGroupsTable)); err != nil {
		return fmt.Errorf("postgres store: create model_groups table: %w", err)
	}
	// Per-model RPM and budget caps for group-attached keys. Keyed by model id
	// (lowercased at enforcement). Idempotent backfill.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS model_rpm_limits JSONB NOT NULL DEFAULT '{}'::jsonb`, modelGroupsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_groups add model_rpm_limits: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS model_budget_limits JSONB NOT NULL DEFAULT '{}'::jsonb`, modelGroupsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_groups add model_budget_limits: %w", err)
	}
	// Group-level default discount percentage (0-100) and per-model discount
	// overrides. Applied to the computed cost_usd of requests made by keys
	// attached to this group. Nil/0 discount_pct = no discount; a per-model
	// entry takes precedence over the group default. Idempotent backfill.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS discount_pct NUMERIC(5,2)`, modelGroupsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_groups add discount_pct: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS model_discount_pcts JSONB NOT NULL DEFAULT '{}'::jsonb`, modelGroupsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_groups add model_discount_pcts: %w", err)
	}
	// Backfill model_group_id on api_key_policies (1:1 nullable attachment to
	// a model group). When set, the group's allowed/blocked lists and routes
	// override the policy's own values at enforcement time. Idempotent.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS model_group_id TEXT`, policiesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter api_key_policies add model_group_id: %w", err)
	}
	// model_group_id is nullable on api_key_policies (no FK so policy updates
	// do not require a join validation; group existence is checked at attach
	// time). An index helps the "which entities use this group?" lookup used
	// by the delete guard.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_api_key_policies_model_group_id ON %s(model_group_id) WHERE model_group_id IS NOT NULL`, policiesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create api_key_policies model_group_id index: %w", err)
	}

	// auto_routers stores Auto Router definitions (the "smart model" / router
	// entity). An Auto Router is a kind of global model: it exposes a requestable
	// model_id plus name/display/pricing metadata, and maps each complexity tier
	// (simple/medium/complex/reasoning) to a concrete upstream target model.
	// Multiple routers with distinct names/model ids are supported. At request
	// time the scorer classifies the incoming request and the router forwards it
	// to the chosen tier's target model.
	autoRoutersTable := s.fullTableName(s.cfg.AutoRoutersTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL UNIQUE,
			model_id     TEXT NOT NULL UNIQUE,
			description  TEXT,
			display_name TEXT,
			tier_mappings JSONB NOT NULL DEFAULT '[]'::jsonb,
			pricing      JSONB,
			enabled      BOOLEAN NOT NULL DEFAULT TRUE,
			metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, autoRoutersTable)); err != nil {
		return fmt.Errorf("postgres store: create auto_routers table: %w", err)
	}
	// Idempotent backfill for deployments that created the table before the
	// metadata column existed (mirrors the model_groups backfills below).
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb`, autoRoutersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter auto_routers add metadata: %w", err)
	}
	// Idempotent backfill for the optional per-router vision bridge model
	// column. Nullable; empty = feature off for that router.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS vision_bridge_model TEXT`, autoRoutersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter auto_routers add vision_bridge_model: %w", err)
	}

	// auto_router_profiles stores one active scoring policy per Auto Router.
	// JSONB fields retain the operator-editable thresholds, scorer weights, and
	// deterministic literal keyword rules; profile_version/hash identify the
	// exact policy used by new requests.
	autoRouterProfilesTable := s.fullTableName(s.cfg.AutoRouterProfilesTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			router_id           TEXT PRIMARY KEY REFERENCES %s(id) ON DELETE CASCADE,
			profile_version    BIGINT NOT NULL DEFAULT 1,
			profile_hash       TEXT NOT NULL,
			thresholds         JSONB NOT NULL,
			weights            JSONB NOT NULL,
			keyword_tier_rules JSONB NOT NULL DEFAULT '[]'::jsonb,
			updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, autoRouterProfilesTable, autoRoutersTable)); err != nil {
		return fmt.Errorf("postgres store: create auto_router_profiles table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_auto_router_profiles_hash ON %s(profile_hash)`, autoRouterProfilesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create auto_router_profiles hash index: %w", err)
	}

	// model_health stores the latest health-check snapshot for each model id
	modelHealthTable := s.fullTableName(s.cfg.ModelHealthTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			model_id          TEXT NOT NULL,
			status            TEXT NOT NULL,
			success           BOOLEAN NOT NULL DEFAULT FALSE,
			response_time_ms  BIGINT NOT NULL DEFAULT 0,
			tokens_per_second REAL,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			error_message     TEXT,
			provider          TEXT,
			prompt_message    TEXT,
			completion        TEXT,
			upstream_provider TEXT,
			upstream_auth_id  TEXT,
			upstream_model    TEXT,
			checked_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (model_id)
		)
	`, modelHealthTable)); err != nil {
		return fmt.Errorf("postgres store: create model_health table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_model_health_status ON %s(status)`, modelHealthTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create model_health status index: %w", err)
	}
	// Backfill the prompt/completion/upstream columns on older schemas.
	for _, col := range []string{"prompt_message", "completion", "upstream_provider", "upstream_auth_id", "upstream_model"} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s TEXT`, modelHealthTable, col,
		)); err != nil {
			return fmt.Errorf("postgres store: alter model_health add %s: %w", col, err)
		}
	}

	// model_health_log stores the append-only history of model health checks
	// (one row per check per model) used by the dashboard's trend views. The
	// error_message, prompt_message, and completion columns are AES-GCM-sealed
	// at rest when UsageEncryptionKey is configured. Auto-swept to retention.
	modelHealthLogTable := s.fullTableName(s.cfg.ModelHealthLogTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                BIGSERIAL PRIMARY KEY,
			model_id          TEXT NOT NULL,
			status            TEXT NOT NULL,
			success           BOOLEAN NOT NULL DEFAULT FALSE,
			response_time_ms  BIGINT NOT NULL DEFAULT 0,
			tokens_per_second REAL,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			error_message     TEXT,
			provider          TEXT,
			prompt_message    TEXT,
			completion        TEXT,
			upstream_provider TEXT,
			upstream_auth_id  TEXT,
			upstream_model    TEXT,
			checked_at        TIMESTAMPTZ NOT NULL
		)
	`, modelHealthLogTable)); err != nil {
		return fmt.Errorf("postgres store: create model_health_log table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_model_health_log_model_time ON %s(model_id, checked_at DESC)`, modelHealthLogTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create model_health_log model_time index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_model_health_log_time ON %s(checked_at DESC)`, modelHealthLogTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create model_health_log time index: %w", err)
	}
	// Backfill the prompt/completion/upstream columns on older schemas.
	for _, col := range []string{"prompt_message", "completion", "upstream_provider", "upstream_auth_id", "upstream_model"} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s TEXT`, modelHealthLogTable, col,
		)); err != nil {
			return fmt.Errorf("postgres store: alter model_health_log add %s: %w", col, err)
		}
	}

	// model_health_settings stores the singleton operator configuration row
	// (enabled, interval_seconds, excluded_models, max_tokens, retention_days,
	// max_log_rows) for the model health check sweep. The CHECK constraint
	// guarantees only the id=1 row.
	modelHealthSettingsTable := s.fullTableName(s.cfg.ModelHealthSettingsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id               INTEGER PRIMARY KEY DEFAULT 1,
			enabled          BOOLEAN NOT NULL DEFAULT TRUE,
			interval_seconds INTEGER NOT NULL DEFAULT 900,
			excluded_models  JSONB NOT NULL DEFAULT '[]'::jsonb,
			max_tokens       INTEGER NOT NULL DEFAULT 1,
			retention_days   INTEGER NOT NULL DEFAULT 30,
			max_log_rows     INTEGER NOT NULL DEFAULT 1000,
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT model_health_settings_singleton CHECK (id = 1)
		)
	`, modelHealthSettingsTable)); err != nil {
		return fmt.Errorf("postgres store: create model_health_settings table: %w", err)
	}
	// Idempotently backfill the retention columns for databases running an
	// older schema (CREATE TABLE IF NOT EXISTS does not add new columns to
	// existing tables). Ignore the error when the columns already exist.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS retention_days INTEGER NOT NULL DEFAULT 30`,
		modelHealthSettingsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_health_settings add retention_days: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS max_log_rows INTEGER NOT NULL DEFAULT 1000`,
		modelHealthSettingsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter model_health_settings add max_log_rows: %w", err)
	}
	// Seed the singleton row so GetSettings always finds a row. ON CONFLICT
	// keeps existing customizations on re-runs of EnsureSchema.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id) VALUES (1) ON CONFLICT (id) DO NOTHING`, modelHealthSettingsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: seed model_health_settings singleton: %w", err)
	}

	// alerts stores the notification feed produced by the alert detectors
	// (max-spend, error-rate, provider cooldown, model-health). One row per
	// alert occurrence. Active rows are deduplicated by fingerprint; recurring
	// conditions bump occurrences and refresh last_seen_at/info instead of
	// inserting a new row until the suppression window elapses. The message and
	// data columns are AES-GCM-sealed at rest when UsageEncryptionKey is set.
	alertsTable := s.fullTableName(s.cfg.AlertsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id               BIGSERIAL PRIMARY KEY,
			alert_type       TEXT NOT NULL,
			severity         TEXT NOT NULL,
			title            TEXT NOT NULL,
			message          TEXT,
			entity_id        TEXT NOT NULL DEFAULT '',
			entity_name      TEXT NOT NULL DEFAULT '',
			model            TEXT NOT NULL DEFAULT '',
			provider         TEXT NOT NULL DEFAULT '',
			value            DOUBLE PRECISION NOT NULL DEFAULT 0,
			limit_value      DOUBLE PRECISION NOT NULL DEFAULT 0,
			data             JSONB NOT NULL DEFAULT '{}'::jsonb,
			fingerprint      TEXT NOT NULL,
			occurrences      INTEGER NOT NULL DEFAULT 1,
			last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			suppressed_until TIMESTAMPTZ,
			read             BOOLEAN NOT NULL DEFAULT FALSE,
			dismissed        BOOLEAN NOT NULL DEFAULT FALSE,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, alertsTable)); err != nil {
		return fmt.Errorf("postgres store: create alerts table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_alerts_created_at ON %s(created_at DESC)`, alertsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create alerts created_at index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_alerts_entity_health ON %s(entity_id, alert_type) WHERE dismissed = FALSE`, alertsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create alerts entity index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_alerts_suppression ON %s(fingerprint) WHERE dismissed = FALSE`, alertsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create alerts fingerprint index: %w", err)
	}

	// alert_settings stores the singleton operator configuration row (enabled,
	// interval_seconds, per-category toggles, thresholds) for the alert sweep.
	alertSettingsTable := s.fullTableName(s.cfg.AlertSettingsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                            INTEGER PRIMARY KEY DEFAULT 1,
			enabled                       BOOLEAN NOT NULL DEFAULT TRUE,
			interval_seconds              INTEGER NOT NULL DEFAULT 60,
			suppression_minutes           INTEGER NOT NULL DEFAULT 60,
			enable_user_budget            BOOLEAN NOT NULL DEFAULT TRUE,
			enable_api_key_budget         BOOLEAN NOT NULL DEFAULT TRUE,
			enable_error_rate             BOOLEAN NOT NULL DEFAULT TRUE,
			enable_provider_cooldown      BOOLEAN NOT NULL DEFAULT TRUE,
			enable_model_substitution     BOOLEAN NOT NULL DEFAULT TRUE,
			error_rate_threshold          DOUBLE PRECISION NOT NULL DEFAULT 0.5,
			error_window_minutes          INTEGER NOT NULL DEFAULT 5,
			updated_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT alert_settings_singleton CHECK (id = 1)
		)
	`, alertSettingsTable)); err != nil {
		return fmt.Errorf("postgres store: create alert_settings table: %w", err)
	}
	// Drop the model-health category toggle column, retired from the alert
	// sweep. Idempotent so a dev DB that created the earlier schema upgrades
	// cleanly.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s DROP COLUMN IF EXISTS enable_model_health`, alertSettingsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: drop alert_settings enable_model_health: %w", err)
	}
	// Add the model-substitution category toggle column for stores created
	// before the detector existed. Idempotent ALTER; NOT NULL DEFAULT TRUE
	// backfills existing rows so the new detector is on unless an operator
	// turns it off (same default posture as the other four toggles).
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS enable_model_substitution BOOLEAN NOT NULL DEFAULT TRUE`, alertSettingsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add alert_settings enable_model_substitution: %w", err)
	}
	// Seed the singleton row so GetAlertSettings always finds a row.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id) VALUES (1) ON CONFLICT (id) DO NOTHING`, alertSettingsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: seed alert_settings singleton: %w", err)
	}
	return nil
}

// Bootstrap synchronizes configuration and auth records between PostgreSQL and the local workspace.
func (s *PostgresStore) Bootstrap(ctx context.Context, exampleConfigPath string) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if err := s.Migrate(ctx); err != nil {
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

// ConfigTable returns the fully-qualified name of the config mirror table.
func (s *PostgresStore) ConfigTable() string {
	if s == nil {
		return quoteIdentifier(defaultConfigTable)
	}
	return s.fullTableName(s.cfg.ConfigTable)
}

// RuntimeConfigTable returns the fully-qualified name of the PG-first
// control-plane runtime_config singleton table.
func (s *PostgresStore) RuntimeConfigTable() string {
	if s == nil {
		return quoteIdentifier(defaultRuntimeConfigTable)
	}
	return s.fullTableName(s.cfg.RuntimeConfigTable)
}

// ConfigRevisionsTable returns the fully-qualified name of the PG-first
// control-plane append-only history table for runtime_config revisions.
func (s *PostgresStore) ConfigRevisionsTable() string {
	if s == nil {
		return quoteIdentifier(defaultConfigRevisionsTable)
	}
	return s.fullTableName(s.cfg.ConfigRevisionsTable)
}

// ConfigImportsTable returns the fully-qualified name of the PG-first
// control-plane audit table for runtime_config import attempts (dry_run,
// committed, rejected, or replaced). Phase 1 writers append one row per
// attempt; the table is the source of truth for the import timeline view.
func (s *PostgresStore) ConfigImportsTable() string {
	if s == nil {
		return quoteIdentifier(defaultConfigImportsTable)
	}
	return s.fullTableName(s.cfg.ConfigImportsTable)
}

// CooldownTable returns the fully-qualified name of the runtime cooldown state table.
func (s *PostgresStore) CooldownTable() string {
	if s == nil {
		return quoteIdentifier(defaultCooldownTable)
	}
	return s.fullTableName(s.cfg.CooldownTable)
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

// RollupTable returns the fully-qualified name of the pre-aggregated daily
// usage rollup table (usage_stat_day).
func (s *PostgresStore) RollupTable() string {
	if s == nil {
		return quoteIdentifier(defaultUsageStatDayTable)
	}
	return s.fullTableName(s.cfg.UsageStatDayTable)
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

// ModelRoutingTable returns the fully-qualified name of the model_routing
// table holding per-model-id global routing overrides.
func (s *PostgresStore) ModelRoutingTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelRoutingTable)
	}
	return s.fullTableName(s.cfg.ModelRoutingTable)
}

// ErrorMessagesTable returns the fully-qualified name of the operator-
// customizable error messages table.
func (s *PostgresStore) ErrorMessagesTable() string {
	if s == nil {
		return quoteIdentifier(defaultErrorMessagesTable)
	}
	return s.fullTableName(s.cfg.ErrorMessagesTable)
}

// PricingSourcesTable returns the fully-qualified name of the operator-
// managed external pricing catalog sources table.
func (s *PostgresStore) PricingSourcesTable() string {
	if s == nil {
		return quoteIdentifier(defaultPricingSourcesTable)
	}
	return s.fullTableName(s.cfg.PricingSourcesTable)
}

// PricingSourcesDir returns the local directory backing uploaded pricing
// catalog files. Guaranteed to exist when the store is non-nil.
func (s *PostgresStore) PricingSourcesDir() string {
	if s == nil {
		return ""
	}
	return s.pricingSourcesDir
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

// ManagementTokensTable returns the fully-qualified name of the management API
// tokens table (gate access to /v0/management REST surface).
func (s *PostgresStore) ManagementTokensTable() string {
	if s == nil {
		return quoteIdentifier(defaultManagementTokensTable)
	}
	return s.fullTableName(s.cfg.ManagementTokensTable)
}

// ManagementTokenPoliciesTable returns the fully-qualified name of the
// per-management-token policy table.
func (s *PostgresStore) ManagementTokenPoliciesTable() string {
	if s == nil {
		return quoteIdentifier(defaultManagementTokenPoliciesTable)
	}
	return s.fullTableName(s.cfg.ManagementTokenPoliciesTable)
}

// ManagementAuditLogTable returns the fully-qualified name of the management
// API audit log table (request audit + bodies + error log).
func (s *PostgresStore) ManagementAuditLogTable() string {
	if s == nil {
		return quoteIdentifier(defaultManagementAuditLogTable)
	}
	return s.fullTableName(s.cfg.ManagementAuditLogTable)
}

// UpstreamProvidersTable returns the fully-qualified name of the upstream
// providers table (the source of truth for both API-key providers and OAuth
// auths when PG is configured).
func (s *PostgresStore) UpstreamProvidersTable() string {
	if s == nil {
		return quoteIdentifier(defaultUpstreamProvidersTable)
	}
	return s.fullTableName(s.cfg.UpstreamProvidersTable)
}

// UpstreamSyncLogTable returns the fully-qualified name of the upstream sync
// log table (records every upstream OAuth/auth token refresh outcome).
func (s *PostgresStore) UpstreamSyncLogTable() string {
	if s == nil {
		return quoteIdentifier(defaultUpstreamSyncLogTable)
	}
	return s.fullTableName(s.cfg.UpstreamSyncLogTable)
}

// UpstreamProviderModelsTable returns the fully-qualified name of the child
// table holding each provider's models[] list.
func (s *PostgresStore) UpstreamProviderModelsTable() string {
	if s == nil {
		return quoteIdentifier(defaultUpstreamProviderModelsTable)
	}
	return s.fullTableName(s.cfg.UpstreamProviderModelsTable)
}

// UpstreamProviderHeadersTable returns the fully-qualified name of the child
// table holding each provider's headers{} map.
func (s *PostgresStore) UpstreamProviderHeadersTable() string {
	if s == nil {
		return quoteIdentifier(defaultUpstreamProviderHeadersTable)
	}
	return s.fullTableName(s.cfg.UpstreamProviderHeadersTable)
}

// UpstreamProviderExcludedTable returns the fully-qualified name of the child
// table holding each provider's excluded-models[] list.
func (s *PostgresStore) UpstreamProviderExcludedTable() string {
	if s == nil {
		return quoteIdentifier(defaultUpstreamProviderExcludedTable)
	}
	return s.fullTableName(s.cfg.UpstreamProviderExcludedTable)
}

// UpstreamProviderEntriesTable returns the fully-qualified name of the child
// table holding openai-compatibility providers' api-key-entries[] list.
func (s *PostgresStore) UpstreamProviderEntriesTable() string {
	if s == nil {
		return quoteIdentifier(defaultUpstreamProviderEntriesTable)
	}
	return s.fullTableName(s.cfg.UpstreamProviderEntriesTable)
}

// ProxyPoolsTable returns the fully-qualified name of the proxy_pools table
// (the named egress-proxy pools bound to upstream provider rows/entries).
func (s *PostgresStore) ProxyPoolsTable() string {
	if s == nil {
		return quoteIdentifier(defaultProxyPoolsTable)
	}
	return s.fullTableName(s.cfg.ProxyPoolsTable)
}

// ModelGroupsTable returns the fully-qualified name of the model_groups
// table (reusable Model Group templates attachable to API-key policies and
// internal users).
func (s *PostgresStore) ModelGroupsTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelGroupsTable)
	}
	return s.fullTableName(s.cfg.ModelGroupsTable)
}

// AutoRoutersTable returns the fully-qualified name of the auto_routers table
// (the Auto Router entity definitions).
func (s *PostgresStore) AutoRoutersTable() string {
	if s == nil {
		return quoteIdentifier(defaultAutoRoutersTable)
	}
	return s.fullTableName(s.cfg.AutoRoutersTable)
}

// AutoRouterProfilesTable returns the fully-qualified name of the
// auto_router_profiles table containing one active scoring profile per router.
func (s *PostgresStore) AutoRouterProfilesTable() string {
	if s == nil {
		return quoteIdentifier(defaultAutoRouterProfilesTable)
	}
	return s.fullTableName(s.cfg.AutoRouterProfilesTable)
}

// ModelHealthTable returns the fully-qualified name of the model_health table
// (latest health-check snapshot per model id).
func (s *PostgresStore) ModelHealthTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelHealthTable)
	}
	return s.fullTableName(s.cfg.ModelHealthTable)
}

// ModelHealthLogTable returns the fully-qualified name of the model_health_log
// table (append-only history of model health checks).
func (s *PostgresStore) ModelHealthLogTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelHealthLogTable)
	}
	return s.fullTableName(s.cfg.ModelHealthLogTable)
}

// ModelHealthSettingsTable returns the fully-qualified name of the
// model_health_settings table (singleton operator configuration for the sweep).
func (s *PostgresStore) ModelHealthSettingsTable() string {
	if s == nil {
		return quoteIdentifier(defaultModelHealthSettingsTable)
	}
	return s.fullTableName(s.cfg.ModelHealthSettingsTable)
}

// AlertsTable returns the fully-qualified name of the alerts feed table.
func (s *PostgresStore) AlertsTable() string {
	if s == nil {
		return quoteIdentifier(defaultAlertsTable)
	}
	return s.fullTableName(s.cfg.AlertsTable)
}

// AlertSettingsTable returns the fully-qualified name of the alert_settings
// table (singleton operator configuration for the alert sweep).
func (s *PostgresStore) AlertSettingsTable() string {
	if s == nil {
		return quoteIdentifier(defaultAlertSettingsTable)
	}
	return s.fullTableName(s.cfg.AlertSettingsTable)
}

// LiteLLMUsersTable returns the fully-qualified name of the Manage-LiteLLM
// internal users table.
func (s *PostgresStore) LiteLLMUsersTable() string {
	if s == nil {
		return quoteIdentifier(defaultLiteLLMUsersTable)
	}
	return s.fullTableName(s.cfg.LiteLLMUsersTable)
}

// LiteLLMKeysTable returns the fully-qualified name of the Manage-LiteLLM API
// keys table.
func (s *PostgresStore) LiteLLMKeysTable() string {
	if s == nil {
		return quoteIdentifier(defaultLiteLLMKeysTable)
	}
	return s.fullTableName(s.cfg.LiteLLMKeysTable)
}

// LiteLLMKeyPoliciesTable returns the fully-qualified name of the
// Manage-LiteLLM per-key policy table.
func (s *PostgresStore) LiteLLMKeyPoliciesTable() string {
	if s == nil {
		return quoteIdentifier(defaultLiteLLMKeyPoliciesTable)
	}
	return s.fullTableName(s.cfg.LiteLLMKeyPoliciesTable)
}

// LiteLLMSyncSettingsTable returns the fully-qualified name of the
// Manage-LiteLLM external sync settings table.
func (s *PostgresStore) LiteLLMSyncSettingsTable() string {
	if s == nil {
		return quoteIdentifier(defaultLiteLLMSyncSettingsTable)
	}
	return s.fullTableName(s.cfg.LiteLLMSyncSettingsTable)
}

// Save persists authentication metadata to disk and PostgreSQL.
func (s *PostgresStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}
	if errWeight := cliproxyauth.ValidateAuthWeight(auth); errWeight != nil {
		return "", fmt.Errorf("postgres store: %w", errWeight)
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
	// Best-effort: mirror the refreshed token + metadata into the normalized
	// upstream_providers row for the oauth:* provider matching this auth file.
	// Non-fatal when the table is unseeded or the row is absent (logged at
	// debug to avoid noisy refresh loops). No secrets are logged.
	s.syncUpstreamProviderToken(ctx, auth)
	return path, nil
}

// syncUpstreamProviderToken updates the normalized token columns of the
// upstream_providers row backing this OAuth auth, if one exists. It is
// invoked after every Save (which fires on token refresh) so the table stays
// current without a separate refresh pipeline. Failures are logged at debug
// level and never propagate — a stale normalized row must not break refresh.
func (s *PostgresStore) syncUpstreamProviderToken(ctx context.Context, auth *cliproxyauth.Auth) {
	if s == nil || auth == nil {
		return
	}
	fileName := strings.TrimSpace(auth.FileName)
	if fileName == "" {
		return
	}
	// Only OAuth/file-backed auths map to an oauth:* row.
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "" {
		return
	}
	// API-key providers are persisted via config, not auth files.
	if !authIsOAuthProvider(provider) {
		return
	}
	var (
		access  any
		refresh any
		expiry  any
		scope   any
	)
	if auth.Metadata != nil {
		access = tokenStringFromMeta(auth.Metadata, "access_token", "access-token")
		refresh = tokenStringFromMeta(auth.Metadata, "refresh_token", "refresh-token")
		scope = tokenStringFromMeta(auth.Metadata, "scope")
		if t, ok := auth.ExpirationTime(); ok && !t.IsZero() {
			expiry = t.UTC()
		}
	}
	table := s.fullTableName(s.cfg.UpstreamProvidersTable)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET
			token_access_token  = COALESCE($1, token_access_token),
			token_refresh_token = COALESCE($2, token_refresh_token),
			token_scope         = COALESCE($3, token_scope),
			token_expiry        = COALESCE($4, token_expiry),
			updated_at          = NOW()
		WHERE file_name = $5 AND provider_type = $6
	`, table), access, refresh, scope, expiry, fileName, "oauth:"+provider)
	if err != nil {
		log.WithError(err).Debugf("postgres store: sync upstream provider token for %s", fileName)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Row not present yet (table unseeded or this auth has no normalized
		// row). Not an error — seeding or a later management write will create it.
		if _, loaded := loggedMissingUpstreamProvider.LoadOrStore(fileName, struct{}{}); !loaded {
			log.Debugf("postgres store: no upstream_providers row for auth %s (further occurrences suppressed)", fileName)
		}
	}
}

// authIsOAuthProvider reports whether the given provider channel is an
// OAuth/file-backed auth (as opposed to an API-key provider whose creds live
// in config.yaml). The list mirrors the synthesizer's supported channels.
func authIsOAuthProvider(provider string) bool {
	switch provider {
	case "claude", "codex", "kimi", "xai", "vertex", "aistudio", "antigravity", "meta":
		return true
	}
	return false
}

// tokenStringFromMeta extracts a non-empty trimmed string value from a metadata
// map, trying each key in order.
func tokenStringFromMeta(meta map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := meta[k]; ok {
			if s, ok := castString(v); ok && s != "" {
				return s
			}
		}
	}
	return nil
}

// castString coerces common JSON-decoded scalar types to a trimmed string.
func castString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t), true
	case json.Number:
		return t.String(), true
	}
	return "", false
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
		if errWeight := cliproxyauth.ValidateAuthWeight(&cliproxyauth.Auth{Metadata: metadata}); errWeight != nil {
			log.WithError(errWeight).Warnf("postgres store: skipping auth %s with invalid weight", id)
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
// syncConfigFromDatabase previously mirrored the runtime_config JSONB row
// to a local `pgstore/config/config.yaml` spool file. Phase 5 removes
// that spool write: the ephemeral bridge (`internal/runtimebridge`)
// reads the runtime_config singleton on every boot and writes its own
// private config.yaml, so a duplicate local mirror serves no purpose.
//
// The function remains callable for backwards compatibility with tests
// and the reverse-direction file→PG path (`PersistConfig`); it is now a
// no-op when called from `Bootstrap`. The error returns are preserved so
// callers that depended on a particular signature keep compiling.
func (s *PostgresStore) syncConfigFromDatabase(ctx context.Context, exampleConfigPath string) error {
	if ctx == nil {
		return nil
	}
	_ = exampleConfigPath // accepted for signature stability; no longer read.
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
