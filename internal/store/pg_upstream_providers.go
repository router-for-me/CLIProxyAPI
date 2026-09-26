package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// UpstreamProvider is one row of the upstream_providers table — the
// normalized representation of either a config-backed API-key provider
// (gemini/codex/xai/claude/openai-compatibility/vertex/interactions) or an
// OAuth/file-backed auth (provider_type prefixed with "oauth:"). The
// variable-length children (Models, Headers, ExcludedModels, APIKeyEntries)
// are hydrated from child tables on read and persisted transactionally on
// write.
//
// provider_type is the discriminator without an "oauth:" prefix for API-key
// providers, e.g. "gemini-api-key", "claude-api-key", "openai-compatibility";
// OAuth/file-backed auths use the "oauth:<channel>" form, e.g.
// "oauth:claude", "oauth:codex".
type UpstreamProvider struct {
	ID           int64  `json:"id"`
	ProviderType string `json:"provider_type"`
	Name         string `json:"name,omitempty"`
	Priority     int    `json:"priority"`
	Disabled     bool   `json:"disabled"`
	// RoutingStrategy is the optional in-pool credential-selection strategy
	// for entry-bearing providers (claude-api-key, openai-compatibility).
	// Empty = unset: entries follow the global routing.strategy and
	// request-fault errors keep today's hard-stop behavior. Any valid value
	// additionally opts the pool into aggressive failover — any entry error
	// rotates to the next entry before surfacing to the client.
	RoutingStrategy string `json:"routing_strategy,omitempty"`
	// CircuitBreaker is the opt-in pool-level circuit breaker flag (design
	// G3): when true, 408/5xx failures from this row's pool feed the breaker
	// and the pool's auths become subject to its pool-wide blocking. false
	// (default) keeps failures scoped to per-auth cooldowns.
	CircuitBreaker bool   `json:"circuit_breaker"`
	Prefix         string `json:"prefix,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	ProxyURL       string `json:"proxy_url,omitempty"`
	// ProxyPoolID, when non-nil, binds the row to a proxy_pools entry; the
	// renderer resolves it into the concrete ProxyURL (or RelayBaseURL for
	// relay pools). nil = no row-level pool binding.
	ProxyPoolID             *int64         `json:"proxy_pool_id,omitempty"`
	Label                   string         `json:"label,omitempty"`
	Email                   string         `json:"email,omitempty"`
	FileName                string         `json:"file_name,omitempty"`
	SourceBackend           string         `json:"source_backend,omitempty"`
	Status                  string         `json:"status,omitempty"`
	Unavailable             bool           `json:"unavailable"`
	LastError               string         `json:"last_error,omitempty"`
	LastErrorAt             *time.Time     `json:"last_error_at,omitempty"`
	Websockets              bool           `json:"websockets,omitempty"`
	RebuildMidSystemMessage bool           `json:"rebuild_mid_system_message,omitempty"`
	ExperimentalCCHSigning  bool           `json:"experimental_cch_signing,omitempty"`
	CloakMode               string         `json:"cloak_mode,omitempty"`
	CloakStrictMode         bool           `json:"cloak_strict_mode,omitempty"`
	CloakSensitiveWords     []string       `json:"cloak_sensitive_words,omitempty"`
	CloakCacheUserID        *bool          `json:"cloak_cache_user_id,omitempty"`
	TokenAccessToken        string         `json:"token_access_token,omitempty"`
	TokenRefreshToken       string         `json:"token_refresh_token,omitempty"`
	TokenTokenType          string         `json:"token_token_type,omitempty"`
	TokenExpiry             *time.Time     `json:"token_expiry,omitempty"`
	TokenExpired            *bool          `json:"token_expired,omitempty"`
	TokenScope              string         `json:"token_scope,omitempty"`
	ExtraConfig             map[string]any `json:"extra_config,omitempty"`
	// AutoDisableErrorCodes lists the upstream error codes that permanently
	// auto-disable an entry (matched by the conductor and persisted via the
	// server-side sink). Empty = feature off for the provider. Stored nullable
	// TEXT[].
	AutoDisableErrorCodes []string `json:"auto_disable_error_codes,omitempty"`
	// AutoDisableCooldownSeconds is the auto-re-enable cooldown after an
	// entry was auto-disabled. nil = manual re-enable only (no sweeper).
	// Stored nullable INTEGER.
	AutoDisableCooldownSeconds *int      `json:"auto_disable_cooldown_seconds,omitempty"`
	CreatedAt                  time.Time `json:"created_at"`
	UpdatedAt                  time.Time `json:"updated_at"`

	// Child collections — hydrated from the child tables.
	Models         []UpstreamProviderModel  `json:"models,omitempty"`
	Headers        map[string]string        `json:"headers,omitempty"`
	ExcludedModels []string                 `json:"excluded_models,omitempty"`
	APIKeyEntries  []UpstreamProviderAPIKey `json:"api_key_entries,omitempty"`
}

// UpstreamProviderModel is one entry of a provider's models[] list.
// For API-key providers it maps into the per-provider config.Models slice;
// for OAuth providers it is rendered into the auth file's "model_aliases"
// array (config.OAuthModelAlias shape: name/alias/fork/display-name/force-mapping).
type UpstreamProviderModel struct {
	ID               int64          `json:"id,omitempty"`
	ProviderID       int64          `json:"provider_id,omitempty"`
	Name             string         `json:"name"`
	Alias            string         `json:"alias,omitempty"`
	DisplayName      string         `json:"display_name,omitempty"`
	ForceMapping     bool           `json:"force_mapping,omitempty"`
	Fork             bool           `json:"fork,omitempty"`
	Image            bool           `json:"image,omitempty"`
	InputModalities  []string       `json:"input_modalities,omitempty"`
	OutputModalities []string       `json:"output_modalities,omitempty"`
	Thinking         map[string]any `json:"thinking,omitempty"`
	// WireFormat selects the upstream protocol for this model: "openai"
	// (default) or "anthropic". Only meaningful for opencode-go rows today;
	// other provider types leave it empty.
	WireFormat string `json:"wire_format,omitempty"`
	SortOrder  int    `json:"sort_order,omitempty"`
}

// UpstreamProviderAPIKey is one entry of an openai-compatibility provider's
// api-key-entries[] list.
type UpstreamProviderAPIKey struct {
	ID         int64  `json:"id,omitempty"`
	ProviderID int64  `json:"provider_id,omitempty"`
	APIKey     string `json:"api_key"`
	Name       string `json:"name,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	// ProxyPoolID, when non-nil, binds the entry to a proxy_pools entry and
	// overrides the provider row's binding (see UpstreamProvider.ProxyPoolID).
	ProxyPoolID *int64 `json:"proxy_pool_id,omitempty"`
	SortOrder   int    `json:"sort_order,omitempty"`
	// Weight is the optional proportional selection weight under
	// weighted-round-robin routing. nil means the credential falls back to the
	// scheduler default (1). The dashboard editor restricts user input to
	// positive values 1..MaxCredentialWeight; the scheduler treats non-positive
	// weights as excluded. Stored as a nullable INTEGER so legacy rows survive
	// the column add and "user did not pick a weight" stays distinct from
	// "weight 0".
	Weight *int `json:"weight,omitempty"`
	// Priority is the optional selection tier for this entry within its
	// pool. nil = inherit the provider row's Priority (today's behavior);
	// the scheduler always serves the highest ready tier first and descends
	// when the upper tier cools down. Stored as nullable INTEGER so
	// "inherit" stays distinct from an explicit 0.
	Priority *int `json:"priority,omitempty"`
	// Disabled excludes this entry from routing without deleting it. The
	// upstreamsync renderer skips disabled entries when projecting the row
	// into config.yaml. Stored NOT NULL DEFAULT FALSE so legacy rows survive
	// the column add with unchanged behavior.
	Disabled bool `json:"disabled,omitempty"`

	// RetryMaxAttempts is the optional per-entry retry attempt cap. nil =
	// fall back to RoutingConfig.Retry.MaxAttempts global default.
	// Stored as nullable SMALLINT so "inherit" stays distinct from
	// explicit zero. uint16 in Go fits SMALLINT (int2) range.
	RetryMaxAttempts *uint16 `json:"retry_max_attempts,omitempty"`
	// RetryMaxTimeMS is the optional per-entry retry wall-time budget in
	// milliseconds. nil = fall back to global default. Stored nullable
	// INTEGER (int4 in PG; uint32 in Go fits comfortably).
	RetryMaxTimeMS *uint32 `json:"retry_max_time_ms,omitempty"`
	// RetryBackoffMS is the optional per-entry inter-attempt backoff base
	// in milliseconds. nil = fall back to global default. Stored nullable
	// INTEGER.
	RetryBackoffMS *uint32 `json:"retry_backoff_ms,omitempty"`
	// MaxConcurrent is the per-entry in-flight hard cap. nil/0 = unlimited.
	// The synthesizer stamps it onto auth.Attributes["max_parallel"], which
	// the round-2 scheduler already reads, making a full entry non-eligible.
	// Stored nullable INTEGER so "unlimited" stays distinct from an explicit 0.
	MaxConcurrent *int `json:"max_concurrent,omitempty"`
	// MaxWaitMs is the per-entry wait budget in milliseconds before an
	// eligible-but-full entry fails over. nil/0 = default wait. Stored nullable
	// INTEGER like MaxConcurrent.
	MaxWaitMs *int `json:"max_wait_ms,omitempty"`

	// AutoDisabled is the runtime-written (not operator input) auto-disable
	// flag. The sink sets it when an upstream error matches the provider's
	// auto_disable_error_codes; the renderer then skips the entry exactly like
	// Disabled. Stored NOT NULL DEFAULT FALSE so legacy rows stay enabled.
	AutoDisabled bool `json:"auto_disabled,omitempty"`
	// AutoDisabledAt is when the entry was auto-disabled (used by the re-enable
	// sweeper to decide when the cooldown elapsed). Nullable TIMESTAMPTZ.
	AutoDisabledAt *time.Time `json:"auto_disabled_at,omitempty"`
	// AutoDisabledReason is a short human-readable reason (e.g. the matched
	// upstream error code). Nullable TEXT.
	AutoDisabledReason string `json:"auto_disabled_reason,omitempty"`
}

// UpstreamProviderStore is the contract the management API consumes for the
// /v0/management/upstream-providers routes. The PG-backed implementation
// lives in pg_upstream_providers.go; a nil implementation is used when PG is
// not configured (routes then return 503).
type UpstreamProviderStore interface {
	List(ctx context.Context) ([]UpstreamProvider, error)
	Get(ctx context.Context, id int64) (*UpstreamProvider, error)
	Create(ctx context.Context, p UpstreamProvider) (*UpstreamProvider, error)
	Update(ctx context.Context, p UpstreamProvider) (*UpstreamProvider, error)
	Delete(ctx context.Context, id int64) error
	// Count returns the total number of rows. Used by the first-boot seeder to
	// decide whether migration from config.yaml/auth-dir is needed.
	Count(ctx context.Context) (int64, error)
	// SetEntryAutoDisabled is the server-side auto-disable sink's persistence
	// primitive (Fitur 2): it flips one API-key entry to disabled+auto_disabled
	// with the matched error code as the reason, returning true only when a row
	// changed. Idempotent: re-firing on an already-auto-disabled entry or on an
	// operator manually-disabled entry is a no-op (never clobbers manual
	// disable). Entry IDs are globally unique across providers, so the entryID
	// alone keys the update.
	SetEntryAutoDisabled(ctx context.Context, entryID int64, code string) (bool, error)
	// ReenableExpiredAutoDisabled is the auto-re-enable sweeper's persistence
	// primitive (Fitur 2 re-enable side): it clears the auto-disabled runtime
	// flags on every entry whose auto_disabled_at has passed the provider-
	// configured cooldown, returning the number of rows re-enabled. Manually
	// disabled entries (disabled=true, auto_disabled=false) are never touched.
	ReenableExpiredAutoDisabled(ctx context.Context) (int64, error)
}

// pgUpstreamProviderStore implements UpstreamProviderStore against the
// upstream_providers + child tables.
type pgUpstreamProviderStore struct {
	db       *sql.DB
	table    string
	models   string
	headers  string
	excluded string
	entries  string
}

// NewUpstreamProviderStore wires the store against a parent PostgresStore.
// Returns nil when parent is nil so callers can feature-detect via nil.
func NewUpstreamProviderStore(parent *PostgresStore) UpstreamProviderStore {
	if parent == nil {
		return nil
	}
	return &pgUpstreamProviderStore{
		db:       parent.DB(),
		table:    parent.UpstreamProvidersTable(),
		models:   parent.UpstreamProviderModelsTable(),
		headers:  parent.UpstreamProviderHeadersTable(),
		excluded: parent.UpstreamProviderExcludedTable(),
		entries:  parent.UpstreamProviderEntriesTable(),
	}
}

// ErrUpstreamProviderNotFound is returned by Get/Update when no row matches
// the supplied id.
var ErrUpstreamProviderNotFound = errors.New("postgres store: upstream provider not found")

// List loads every upstream provider with its child collections, ordered by
// provider_type then id for stable dashboard display.
func (s *pgUpstreamProviderStore) List(ctx context.Context) ([]UpstreamProvider, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	ids, err := s.listIDs(ctx, " ORDER BY provider_type, id")
	if err != nil {
		return nil, err
	}
	out := make([]UpstreamProvider, 0, len(ids))
	for _, id := range ids {
		p, errGet := s.Get(ctx, id)
		if errGet != nil {
			return nil, errGet
		}
		out = append(out, *p)
	}
	return out, nil
}

// listIDs returns the ordered id list for the providers table.
func (s *pgUpstreamProviderStore) listIDs(ctx context.Context, orderSuffix string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id FROM %s%s`, s.table, orderSuffix))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list upstream providers: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres store: scan upstream provider id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Get fetches one provider row plus its child collections.
func (s *pgUpstreamProviderStore) Get(ctx context.Context, id int64) (*UpstreamProvider, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
		       base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
		       status, unavailable, last_error, last_error_at, websockets,
		       rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		       cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		       token_access_token, token_refresh_token, token_token_type,
		       token_expiry, token_expired, token_scope, extra_config,
		       auto_disable_error_codes, auto_disable_cooldown_seconds,
		       created_at, updated_at
		FROM %s WHERE id = $1
	`, s.table), id)
	var p UpstreamProvider
	if err := scanUpstreamProvider(row, &p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUpstreamProviderNotFound
		}
		return nil, fmt.Errorf("postgres store: get upstream provider: %w", err)
	}
	if err := s.loadChildren(ctx, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts a new provider and its child collections within one tx.
func (s *pgUpstreamProviderStore) Create(ctx context.Context, p UpstreamProvider) (*UpstreamProvider, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	normalized, err := normalizeUpstreamProvider(p)
	if err != nil {
		return nil, err
	}
	p = normalized
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("postgres store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	extra, err := marshalExtraConfig(p.ExtraConfig)
	if err != nil {
		return nil, err
	}
	sensitive, err := marshalStringSlice(p.CloakSensitiveWords)
	if err != nil {
		return nil, err
	}
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
			base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
			status, unavailable, last_error, last_error_at, websockets,
			rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
			cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
			token_access_token, token_refresh_token, token_token_type,
			token_expiry, token_expired, token_scope, extra_config,
			auto_disable_error_codes, auto_disable_cooldown_seconds
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35
		)
		RETURNING id, provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
		          base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
		          status, unavailable, last_error, last_error_at, websockets,
		          rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		          cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		          token_access_token, token_refresh_token, token_token_type,
		          token_expiry, token_expired, token_scope, extra_config,
		          auto_disable_error_codes, auto_disable_cooldown_seconds,
		          created_at, updated_at
	`, s.table),
		p.ProviderType, nullableString(p.Name), p.Priority, p.Disabled, nullableString(p.RoutingStrategy), p.CircuitBreaker, nullableString(p.Prefix),
		nullableString(p.APIKey), nullableString(p.BaseURL), nullableString(p.ProxyURL), nullableID(p.ProxyPoolID),
		nullableString(p.Label), nullableString(p.Email), nullableString(p.FileName),
		nullableString(p.SourceBackend), nullableString(p.Status), p.Unavailable,
		nullableString(p.LastError), nullableTime(p.LastErrorAt), p.Websockets,
		p.RebuildMidSystemMessage, p.ExperimentalCCHSigning, nullableString(p.CloakMode),
		p.CloakStrictMode, sensitive, nullableBool(p.CloakCacheUserID),
		nullableString(p.TokenAccessToken), nullableString(p.TokenRefreshToken),
		nullableString(p.TokenTokenType), nullableTime(p.TokenExpiry), nullableBool(p.TokenExpired),
		nullableString(p.TokenScope), extra, marshalTextArray(p.AutoDisableErrorCodes), nullableInt(p.AutoDisableCooldownSeconds),
	)
	var created UpstreamProvider
	if err := scanUpstreamProvider(row, &created); err != nil {
		return nil, fmt.Errorf("postgres store: create upstream provider: %w", err)
	}
	if err := s.replaceChildrenTx(ctx, tx, created.ID, &p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("postgres store: commit upstream provider: %w", err)
	}
	created.Models, created.Headers, created.ExcludedModels, created.APIKeyEntries =
		p.Models, p.Headers, p.ExcludedModels, p.APIKeyEntries
	return &created, nil
}

// Update replaces the mutable fields and child collections of an existing
// row within one tx.
//
// auto_disable_error_codes / auto_disable_cooldown_seconds are written through
// COALESCE so an ABSENT value (nil on the store struct => marshalTextArray(nil)
// binds SQL NULL, nullableInt(nil) binds NULL) preserves the configured config,
// while an EXPLICITLY CLEARED value (empty-but-present []string{} binds '{}',
// or an explicit 0) passes through COALESCE and clears. The management DTO
// carries a pointer for the codes so "omitted from the PUT" stays distinct
// from "sent as []" — a legacy/non-dashboard PUT never wipes provider-level
// auto-disable config, and a dashboard save always round-trips exactly what is
// shown. The per-entry auto columns in syncAPIKeyEntriesTx are always-written
// (false/nil/empty clears) for the Re-enable contract — do not reintroduce
// COALESCE there.
func (s *pgUpstreamProviderStore) Update(ctx context.Context, p UpstreamProvider) (*UpstreamProvider, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	if p.ID == 0 {
		return nil, fmt.Errorf("postgres store: upstream provider id required for update")
	}
	normalized, err := normalizeUpstreamProvider(p)
	if err != nil {
		return nil, err
	}
	p = normalized
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("postgres store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	extra, err := marshalExtraConfig(p.ExtraConfig)
	if err != nil {
		return nil, err
	}
	sensitive, err := marshalStringSlice(p.CloakSensitiveWords)
	if err != nil {
		return nil, err
	}
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`
		UPDATE %s SET
			provider_type = $1,
			name = $2,
			priority = $3,
			disabled = $4,
			routing_strategy = $5,
			circuit_breaker = $6,
			prefix = $7,
			api_key = $8,
			base_url = $9,
			proxy_url = $10,
			proxy_pool_id = $11,
			label = $12,
			email = $13,
			file_name = $14,
			source_backend = $15,
			status = $16,
			unavailable = $17,
			last_error = $18,
			last_error_at = $19,
			websockets = $20,
			rebuild_mid_system_message = $21,
			experimental_cch_signing = $22,
			cloak_mode = $23,
			cloak_strict_mode = $24,
			cloak_sensitive_words = $25,
			cloak_cache_user_id = $26,
			token_access_token = $27,
			token_refresh_token = $28,
			token_token_type = $29,
			token_expiry = $30,
			token_expired = $31,
			token_scope = $32,
			extra_config = $33,
			auto_disable_error_codes = COALESCE($34::text[], auto_disable_error_codes),
			auto_disable_cooldown_seconds = COALESCE($35::integer, auto_disable_cooldown_seconds),
			updated_at = NOW()
		WHERE id = $36
		RETURNING id, provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
		          base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
		          status, unavailable, last_error, last_error_at, websockets,
		          rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		          cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		          token_access_token, token_refresh_token, token_token_type,
		          token_expiry, token_expired, token_scope, extra_config,
		          auto_disable_error_codes, auto_disable_cooldown_seconds,
		          created_at, updated_at
	`, s.table),
		p.ProviderType, nullableString(p.Name), p.Priority, p.Disabled, nullableString(p.RoutingStrategy), p.CircuitBreaker, nullableString(p.Prefix),
		nullableString(p.APIKey), nullableString(p.BaseURL), nullableString(p.ProxyURL), nullableID(p.ProxyPoolID),
		nullableString(p.Label), nullableString(p.Email), nullableString(p.FileName),
		nullableString(p.SourceBackend), nullableString(p.Status), p.Unavailable,
		nullableString(p.LastError), nullableTime(p.LastErrorAt), p.Websockets,
		p.RebuildMidSystemMessage, p.ExperimentalCCHSigning, nullableString(p.CloakMode),
		p.CloakStrictMode, sensitive, nullableBool(p.CloakCacheUserID),
		nullableString(p.TokenAccessToken), nullableString(p.TokenRefreshToken),
		nullableString(p.TokenTokenType), nullableTime(p.TokenExpiry), nullableBool(p.TokenExpired),
		nullableString(p.TokenScope), extra, marshalTextArray(p.AutoDisableErrorCodes), nullableInt(p.AutoDisableCooldownSeconds), p.ID,
	)
	var updated UpstreamProvider
	if err := scanUpstreamProvider(row, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUpstreamProviderNotFound
		}
		return nil, fmt.Errorf("postgres store: update upstream provider: %w", err)
	}
	if err := s.replaceChildrenTx(ctx, tx, updated.ID, &p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("postgres store: commit upstream provider update: %w", err)
	}
	updated.Models, updated.Headers, updated.ExcludedModels, updated.APIKeyEntries =
		p.Models, p.Headers, p.ExcludedModels, p.APIKeyEntries
	return &updated, nil
}

// Delete removes a provider and (via cascade) its children.
func (s *pgUpstreamProviderStore) Delete(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.table), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete upstream provider: %w", err)
	}
	return nil
}

// Count returns the total number of provider rows.
func (s *pgUpstreamProviderStore) Count(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	var n int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, s.table)).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres store: count upstream providers: %w", err)
	}
	return n, nil
}

// loadChildren populates the child collections (models/headers/excluded/entries)
// for a freshly-scanned provider row.
func (s *pgUpstreamProviderStore) loadChildren(ctx context.Context, p *UpstreamProvider) error {
	if p == nil {
		return nil
	}
	// Models.
	mRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, provider_id, name, alias, display_name, force_mapping, fork, image,
		       input_modalities, output_modalities, thinking, wire_format, sort_order
		FROM %s WHERE provider_id = $1 ORDER BY sort_order, id
	`, s.models), p.ID)
	if err != nil {
		return fmt.Errorf("postgres store: list upstream provider models: %w", err)
	}
	for mRows.Next() {
		var m UpstreamProviderModel
		var alias, displayName sql.NullString
		var inputMod, outputMod, thinking []byte
		if err = mRows.Scan(&m.ID, &m.ProviderID, &m.Name, &alias, &displayName, &m.ForceMapping,
			&m.Fork, &m.Image, &inputMod, &outputMod, &thinking, &m.WireFormat, &m.SortOrder); err != nil {
			mRows.Close()
			return fmt.Errorf("postgres store: scan upstream provider model: %w", err)
		}
		if alias.Valid {
			m.Alias = alias.String
		}
		if displayName.Valid {
			m.DisplayName = displayName.String
		}
		m.InputModalities = decodeStringArray(inputMod)
		m.OutputModalities = decodeStringArray(outputMod)
		m.Thinking = decodeJSONObject(thinking)
		p.Models = append(p.Models, m)
	}
	mRows.Close()
	if err = mRows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate upstream provider models: %w", err)
	}

	// Headers.
	hRows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT header_key, header_value FROM %s WHERE provider_id = $1`, s.headers,
	), p.ID)
	if err != nil {
		return fmt.Errorf("postgres store: list upstream provider headers: %w", err)
	}
	for hRows.Next() {
		var k, v string
		if err = hRows.Scan(&k, &v); err != nil {
			hRows.Close()
			return fmt.Errorf("postgres store: scan upstream provider header: %w", err)
		}
		if p.Headers == nil {
			p.Headers = make(map[string]string)
		}
		p.Headers[k] = v
	}
	hRows.Close()
	if err = hRows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate upstream provider headers: %w", err)
	}

	// Excluded models.
	eRows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT model FROM %s WHERE provider_id = $1 ORDER BY model`, s.excluded,
	), p.ID)
	if err != nil {
		return fmt.Errorf("postgres store: list upstream provider excluded models: %w", err)
	}
	for eRows.Next() {
		var m string
		if err = eRows.Scan(&m); err != nil {
			eRows.Close()
			return fmt.Errorf("postgres store: scan upstream provider excluded model: %w", err)
		}
		p.ExcludedModels = append(p.ExcludedModels, m)
	}
	eRows.Close()
	if err = eRows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate upstream provider excluded models: %w", err)
	}

	// API-key entries (openai-compatibility only).
	aRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, provider_id, api_key, name, proxy_url, proxy_pool_id, sort_order, weight, priority, disabled, retry_max_attempts, retry_max_time_ms, retry_backoff_ms,
		       max_concurrent, max_wait_ms, auto_disabled, auto_disabled_at, auto_disabled_reason
		FROM %s WHERE provider_id = $1 ORDER BY sort_order, id
	`, s.entries), p.ID)
	if err != nil {
		return fmt.Errorf("postgres store: list upstream provider api key entries: %w", err)
	}
	for aRows.Next() {
		var e UpstreamProviderAPIKey
		var entryName, proxyURL, autoDisabledReason sql.NullString
		var weight, priority, entryPoolID, retryMaxAttempts, retryMaxTimeMS, retryBackoffMS, maxConcurrent, maxWaitMS sql.NullInt64
		var autoDisabledAt sql.NullTime
		if err = aRows.Scan(&e.ID, &e.ProviderID, &e.APIKey, &entryName, &proxyURL, &entryPoolID, &e.SortOrder, &weight, &priority, &e.Disabled, &retryMaxAttempts, &retryMaxTimeMS, &retryBackoffMS,
			&maxConcurrent, &maxWaitMS, &e.AutoDisabled, &autoDisabledAt, &autoDisabledReason); err != nil {
			aRows.Close()
			return fmt.Errorf("postgres store: scan upstream provider api key entry: %w", err)
		}
		if entryName.Valid {
			e.Name = entryName.String
		}
		if proxyURL.Valid {
			e.ProxyURL = proxyURL.String
		}
		e.ProxyPoolID = nullableIDFromScan(entryPoolID)
		if weight.Valid {
			w := int(weight.Int64)
			e.Weight = &w
		}
		if priority.Valid {
			pr := int(priority.Int64)
			e.Priority = &pr
		}
		e.RetryMaxAttempts = nullableUint16FromScan(retryMaxAttempts)
		e.RetryMaxTimeMS = nullableUint32FromScan(retryMaxTimeMS)
		e.RetryBackoffMS = nullableUint32FromScan(retryBackoffMS)
		if maxConcurrent.Valid {
			mc := int(maxConcurrent.Int64)
			e.MaxConcurrent = &mc
		}
		if maxWaitMS.Valid {
			mw := int(maxWaitMS.Int64)
			e.MaxWaitMs = &mw
		}
		if autoDisabledAt.Valid {
			t := autoDisabledAt.Time
			e.AutoDisabledAt = &t
		}
		if autoDisabledReason.Valid {
			e.AutoDisabledReason = autoDisabledReason.String
		}
		p.APIKeyEntries = append(p.APIKeyEntries, e)
	}
	aRows.Close()
	if err = aRows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate upstream provider api key entries: %w", err)
	}
	return nil
}

// replaceChildrenTx replaces the non-entry child rows and synchronizes API-key
// entries for providerID within the given transaction, mirroring the in-memory
// collections on p.
func (s *pgUpstreamProviderStore) replaceChildrenTx(ctx context.Context, tx *sql.Tx, providerID int64, p *UpstreamProvider) error {
	// Delete existing non-entry children. API-key entries are synchronized below
	// so their persisted IDs can be retained across updates.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.models), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider models: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.headers), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider headers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.excluded), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider excluded models: %w", err)
	}

	// Insert models.
	for i, m := range p.Models {
		input, err := marshalStringSlice(m.InputModalities)
		if err != nil {
			return fmt.Errorf("postgres store: marshal model input modalities: %w", err)
		}
		output, err := marshalStringSlice(m.OutputModalities)
		if err != nil {
			return fmt.Errorf("postgres store: marshal model output modalities: %w", err)
		}
		var thinking any
		if thinking, err = marshalJSONObject(m.Thinking); err != nil {
			return fmt.Errorf("postgres store: marshal model thinking: %w", err)
		}
		sortOrder := m.SortOrder
		if sortOrder == 0 {
			sortOrder = i
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (provider_id, name, alias, display_name, force_mapping, fork, image,
			                input_modalities, output_modalities, thinking, wire_format, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE(NULLIF($11,''),'openai'),$12)
		`, s.models), providerID, m.Name, nullableString(m.Alias), nullableString(m.DisplayName),
			m.ForceMapping, m.Fork, m.Image, input, output, thinking, nullableString(m.WireFormat), sortOrder,
		); err != nil {
			return fmt.Errorf("postgres store: insert upstream provider model: %w", err)
		}
	}

	// Insert headers.
	for k, v := range p.Headers {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (provider_id, header_key, header_value) VALUES ($1,$2,$3)`,
			s.headers,
		), providerID, k, v); err != nil {
			return fmt.Errorf("postgres store: insert upstream provider header: %w", err)
		}
	}

	// Insert excluded models.
	for _, m := range p.ExcludedModels {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (provider_id, model) VALUES ($1,$2)`, s.excluded,
		), providerID, m); err != nil {
			return fmt.Errorf("postgres store: insert upstream provider excluded model: %w", err)
		}
	}

	// Synchronize API-key entries by stable child ID. This is deliberately done
	// after all non-entry child writes: any validation or SQL error rolls back the
	// whole parent/children transaction.
	if err := s.syncAPIKeyEntriesTx(ctx, tx, providerID, p); err != nil {
		return err
	}
	return nil
}

// syncAPIKeyEntriesTx synchronizes the API-key child rows for providerID. A
// positive incoming ID must already belong to providerID; zero means insert a
// new row. Each entry carries its optional weight and priority (nil = inherit
// the provider row's priority). The caller must hold the surrounding parent
// transaction.
func (s *pgUpstreamProviderStore) syncAPIKeyEntriesTx(ctx context.Context, tx *sql.Tx, providerID int64, p *UpstreamProvider) error {
	current, err := s.listAPIKeyEntryIDsTx(ctx, tx, providerID)
	if err != nil {
		return err
	}

	seenIncoming := make(map[int64]struct{}, len(p.APIKeyEntries))
	for _, entry := range p.APIKeyEntries {
		if entry.ID < 0 {
			return fmt.Errorf("postgres store: upstream provider api key entry id must be positive")
		}
		if entry.ID == 0 {
			continue
		}
		if _, duplicate := seenIncoming[entry.ID]; duplicate {
			return fmt.Errorf("postgres store: duplicate upstream provider api key entry id %d", entry.ID)
		}
		seenIncoming[entry.ID] = struct{}{}
		if _, belongs := current[entry.ID]; belongs {
			continue
		}

		ownerID, found, errLookup := s.lookupAPIKeyEntryProviderTx(ctx, tx, entry.ID)
		if errLookup != nil {
			return errLookup
		}
		if !found {
			return fmt.Errorf("postgres store: upstream provider api key entry id %d not found", entry.ID)
		}
		return fmt.Errorf("postgres store: upstream provider api key entry id %d belongs to upstream provider %d, not %d", entry.ID, ownerID, providerID)
	}

	// Delete omitted rows only after every positive incoming ID has been
	// validated. Restricting the DELETE by provider_id is an additional guard
	// against ever touching another provider's child row.
	for id := range current {
		if _, retained := seenIncoming[id]; retained {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s WHERE provider_id = $1 AND id = $2`, s.entries,
		), providerID, id); err != nil {
			return fmt.Errorf("postgres store: delete omitted upstream provider api key entry: %w", err)
		}
	}

	// Clear names on retained rows before applying the new values. PostgreSQL's
	// immediate unique index otherwise rejects a valid name swap (for example,
	// alpha -> beta and beta -> alpha) when rows are updated sequentially.
	for id := range seenIncoming {
		if _, exists := current[id]; !exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET name = NULL WHERE provider_id = $1 AND id = $2`, s.entries,
		), providerID, id); err != nil {
			return fmt.Errorf("postgres store: clear upstream provider api key entry name: %w", err)
		}
	}

	var out []UpstreamProviderAPIKey
	if p.APIKeyEntries != nil {
		out = make([]UpstreamProviderAPIKey, len(p.APIKeyEntries))
	}
	for i, entry := range p.APIKeyEntries {
		sortOrder := entry.SortOrder
		if sortOrder == 0 {
			sortOrder = i
		}
		entry.ProviderID = providerID
		entry.SortOrder = sortOrder
		if entry.ID == 0 {
			if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
				INSERT INTO %s (provider_id, api_key, name, proxy_url, proxy_pool_id, sort_order, weight, priority, disabled, retry_max_attempts, retry_max_time_ms, retry_backoff_ms,
				                max_concurrent, max_wait_ms, auto_disabled, auto_disabled_at, auto_disabled_reason)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
				RETURNING id
			`, s.entries), providerID, entry.APIKey, nullableString(entry.Name), nullableString(entry.ProxyURL), nullableID(entry.ProxyPoolID), sortOrder, nullableInt(entry.Weight), nullableInt(entry.Priority), entry.Disabled, nullableUint16(entry.RetryMaxAttempts), nullableUint32(entry.RetryMaxTimeMS), nullableUint32(entry.RetryBackoffMS),
				nullableInt(entry.MaxConcurrent), nullableInt(entry.MaxWaitMs), entry.AutoDisabled, nullableTime(entry.AutoDisabledAt), nullableString(entry.AutoDisabledReason)).Scan(&entry.ID); err != nil {
				return fmt.Errorf("postgres store: insert upstream provider api key entry: %w", err)
			}
		} else {
			var persistedID int64
			if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
				UPDATE %s
				SET api_key = $1, name = $2, proxy_url = $3, proxy_pool_id = $4, sort_order = $5, weight = $6, priority = $7, disabled = $8, retry_max_attempts = $9, retry_max_time_ms = $10, retry_backoff_ms = $11,
				    max_concurrent = $12, max_wait_ms = $13,
				    auto_disabled = $14,
				    auto_disabled_at = $15,
				    auto_disabled_reason = $16
				WHERE id = $17 AND provider_id = $18
				RETURNING id
			`, s.entries), entry.APIKey, nullableString(entry.Name), nullableString(entry.ProxyURL), nullableID(entry.ProxyPoolID), sortOrder, nullableInt(entry.Weight), nullableInt(entry.Priority), entry.Disabled, nullableUint16(entry.RetryMaxAttempts), nullableUint32(entry.RetryMaxTimeMS), nullableUint32(entry.RetryBackoffMS),
				nullableInt(entry.MaxConcurrent), nullableInt(entry.MaxWaitMs),
				entry.AutoDisabled, nullableTime(entry.AutoDisabledAt), nullableString(entry.AutoDisabledReason),
				entry.ID, providerID).Scan(&persistedID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("postgres store: upstream provider api key entry id %d is missing from upstream provider %d", entry.ID, providerID)
				}
				return fmt.Errorf("postgres store: update upstream provider api key entry %d: %w", entry.ID, err)
			}
			entry.ID = persistedID
		}
		out[i] = entry
	}
	p.APIKeyEntries = out
	return nil
}

// listAPIKeyEntryIDsTx locks and returns the existing child IDs for a provider.
func (s *pgUpstreamProviderStore) listAPIKeyEntryIDsTx(ctx context.Context, tx *sql.Tx, providerID int64) (map[int64]struct{}, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		`SELECT id FROM %s WHERE provider_id = $1 FOR UPDATE`, s.entries,
	), providerID)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list upstream provider api key entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres store: scan upstream provider api key entry id: %w", err)
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate upstream provider api key entry ids: %w", err)
	}
	return ids, nil
}

// lookupAPIKeyEntryProviderTx finds the owner of an incoming positive child ID.
// It intentionally returns only the provider ID and never reads the secret.
func (s *pgUpstreamProviderStore) lookupAPIKeyEntryProviderTx(ctx context.Context, tx *sql.Tx, entryID int64) (int64, bool, error) {
	var ownerID int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT provider_id FROM %s WHERE id = $1 FOR UPDATE`, s.entries,
	), entryID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("postgres store: find upstream provider api key entry %d: %w", entryID, err)
	}
	return ownerID, true, nil
}

var (
	upstreamProviderEntryNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	upstreamProviderEntryReservedPattern = regexp.MustCompile(`^key-[0-9]+$`)
)

// validateUpstreamProvider enforces required fields and entry identity rules.
// Entry API keys are never included in validation errors.
func validateUpstreamProvider(p UpstreamProvider) error {
	_, err := normalizeUpstreamProvider(p)
	return err
}

// normalizeUpstreamProvider returns a copy of p with API-key entry values
// trimmed and names normalized. The caller's slices remain untouched so
// validation does not unexpectedly rewrite caller-owned secret material.
func normalizeUpstreamProvider(p UpstreamProvider) (UpstreamProvider, error) {
	if p.ProviderType == "" {
		return UpstreamProvider{}, fmt.Errorf("postgres store: upstream provider provider_type is required")
	}

	for _, m := range p.Models {
		if m.Name == "" {
			return UpstreamProvider{}, fmt.Errorf("postgres store: upstream provider model name is required")
		}
	}

	normalized := p
	if p.APIKeyEntries != nil {
		normalized.APIKeyEntries = make([]UpstreamProviderAPIKey, len(p.APIKeyEntries))
		copy(normalized.APIKeyEntries, p.APIKeyEntries)
	}
	seenNames := make(map[string]struct{}, len(normalized.APIKeyEntries))
	seenIDs := make(map[int64]struct{}, len(normalized.APIKeyEntries))
	for i := range normalized.APIKeyEntries {
		entry := &normalized.APIKeyEntries[i]
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		if entry.APIKey == "" {
			return UpstreamProvider{}, fmt.Errorf("postgres store: upstream provider api key entry api_key is required")
		}

		name, ok := normalizeUpstreamProviderEntryName(entry.Name)
		if !ok {
			if upstreamProviderEntryReservedPattern.MatchString(name) {
				return UpstreamProvider{}, fmt.Errorf("postgres store: upstream provider api key entry name is reserved")
			}
			return UpstreamProvider{}, fmt.Errorf("postgres store: upstream provider api key entry name has invalid syntax")
		}
		entry.Name = name
		if name != "" {
			if _, exists := seenNames[name]; exists {
				return UpstreamProvider{}, fmt.Errorf("postgres store: duplicate upstream provider api key entry name")
			}
			seenNames[name] = struct{}{}
		}

		if entry.ID < 0 {
			return UpstreamProvider{}, fmt.Errorf("postgres store: upstream provider api key entry id must be positive")
		}
		if entry.ID > 0 {
			if _, exists := seenIDs[entry.ID]; exists {
				return UpstreamProvider{}, fmt.Errorf("postgres store: duplicate upstream provider api key entry id")
			}
			seenIDs[entry.ID] = struct{}{}
		}
	}
	return normalized, nil
}

// normalizeUpstreamProviderEntryName trims and lowercases an optional entry
// name, then checks the slug and reserved-name rules. The normalized value is
// returned even when validation fails so callers can classify the error
// without exposing any API-key material.
func normalizeUpstreamProviderEntryName(name string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return "", true
	}
	if !upstreamProviderEntryNamePattern.MatchString(normalized) {
		return normalized, false
	}
	if upstreamProviderEntryReservedPattern.MatchString(normalized) {
		return normalized, false
	}
	return normalized, true
}

// scanUpstreamProvider scans one row of upstream_providers (excluding
// collections) into p. The extra_config JSONB column is decoded into the
// ExtraConfig map.
func scanUpstreamProvider(sc scanner, p *UpstreamProvider) error {
	var (
		name, routingStrategy, prefix, apiKey, baseURL, proxyURL, label, email,
		fileName, sourceBackend, status, lastError,
		cloakMode, tokenAccess, tokenRefresh, tokenType, tokenScope sql.NullString
		lastErrorAt, tokenExpiry         sql.NullTime
		cloakCacheUserID, tokenExpired   sql.NullBool
		sensitiveBytes, extraBytes       []byte
		proxyPoolID, autoDisableCooldown sql.NullInt64
		autoDisableCodes                 []string
	)
	if err := sc.Scan(
		&p.ID, &p.ProviderType, &name, &p.Priority, &p.Disabled, &routingStrategy, &p.CircuitBreaker, &prefix, &apiKey,
		&baseURL, &proxyURL, &proxyPoolID, &label, &email, &fileName, &sourceBackend,
		&status, &p.Unavailable, &lastError, &lastErrorAt, &p.Websockets,
		&p.RebuildMidSystemMessage, &p.ExperimentalCCHSigning, &cloakMode,
		&p.CloakStrictMode, &sensitiveBytes, &cloakCacheUserID,
		&tokenAccess, &tokenRefresh, &tokenType, &tokenExpiry, &tokenExpired,
		&tokenScope, &extraBytes, textArrayScanner{dest: &autoDisableCodes}, &autoDisableCooldown,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return err
	}
	p.Name = name.String
	p.ProxyPoolID = nullableIDFromScan(proxyPoolID)
	p.RoutingStrategy = routingStrategy.String
	p.Prefix = prefix.String
	p.APIKey = apiKey.String
	p.BaseURL = baseURL.String
	p.ProxyURL = proxyURL.String
	p.Label = label.String
	p.Email = email.String
	p.FileName = fileName.String
	p.SourceBackend = sourceBackend.String
	p.Status = status.String
	if lastError.Valid {
		p.LastError = lastError.String
	}
	if lastErrorAt.Valid {
		t := lastErrorAt.Time
		p.LastErrorAt = &t
	}
	if cloakMode.Valid {
		p.CloakMode = cloakMode.String
	}
	if cloakCacheUserID.Valid {
		v := cloakCacheUserID.Bool
		p.CloakCacheUserID = &v
	}
	if tokenAccess.Valid {
		p.TokenAccessToken = tokenAccess.String
	}
	if tokenRefresh.Valid {
		p.TokenRefreshToken = tokenRefresh.String
	}
	if tokenType.Valid {
		p.TokenTokenType = tokenType.String
	}
	if tokenExpiry.Valid {
		t := tokenExpiry.Time
		p.TokenExpiry = &t
	}
	if tokenExpired.Valid {
		v := tokenExpired.Bool
		p.TokenExpired = &v
	}
	if tokenScope.Valid {
		p.TokenScope = tokenScope.String
	}
	p.CloakSensitiveWords = decodeStringArray(sensitiveBytes)
	p.ExtraConfig = decodeJSONObject(extraBytes)
	p.AutoDisableErrorCodes = autoDisableCodes
	if autoDisableCooldown.Valid {
		c := int(autoDisableCooldown.Int64)
		p.AutoDisableCooldownSeconds = &c
	}
	return nil
}

// marshalStringSlice serializes a []string into a JSONB-friendly []byte bind
// value (returns []byte("[]") or the marshalled JSON array).
func marshalStringSlice(in []string) (any, error) {
	if len(in) == 0 {
		return []byte("[]"), nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// marshalJSONObject serializes a map[string]any into a JSONB-friendly bind
// value ([]byte("null") when nil/empty).
func marshalJSONObject(m map[string]any) (any, error) {
	if len(m) == 0 {
		return []byte("null"), nil
	}
	return json.Marshal(m)
}

// marshalExtraConfig serializes the extra_config map. Empty/nil -> '{}'::jsonb.
func marshalExtraConfig(m map[string]any) (any, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeJSONObject parses a JSONB column into a map[string]any. Returns nil
// for null/empty/invalid bytes.
func decodeJSONObject(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// nullableBool binds a *bool as a nullable value. Used for optional boolean
// columns whose zero value is meaningful.
func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// nullableTime binds a *time.Time as nil when zero/unset.
// nullableID converts a *int64 binding into a NULL-able BIGINT column value.
func nullableID(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullableIDFromScan converts a scanned NullInt64 back into the *int64
// binding shape (nil when the column is NULL).
func nullableIDFromScan(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

// nullableInt binds a *int as nil when unset so the column round-trips
// faithfully between Go (nil pointer) and SQL (NULL). Used for optional
// integer columns whose zero value is meaningful (e.g. credential weight).
func nullableInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

// nullableUint16 mirrors nullableInt but for uint16 (used by the SMALLINT
// retry_max_attempts column). Returns nil for a nil pointer so the PG
// driver writes SQL NULL.
func nullableUint16(i *uint16) any {
	if i == nil {
		return nil
	}
	return int(*i)
}

// nullableUint32 mirrors nullableInt but for uint32 (used by the INTEGER
// retry_max_time_ms / retry_backoff_ms columns).
func nullableUint32(i *uint32) any {
	if i == nil {
		return nil
	}
	return int64(*i)
}

// nullableUint16FromScan reads a SMALLINT column into *uint16, treating
// sql.NullInt64 NULL as a nil pointer.
func nullableUint16FromScan(n sql.NullInt64) *uint16 {
	if !n.Valid {
		return nil
	}
	v := uint16(n.Int64)
	return &v
}

// nullableUint32FromScan reads an INTEGER column into *uint32, treating
// sql.NullInt64 NULL as a nil pointer.
func nullableUint32FromScan(n sql.NullInt64) *uint32 {
	if !n.Valid {
		return nil
	}
	v := uint32(n.Int64)
	return &v
}

// textArrayScanner implements sql.Scanner for a PG TEXT[] column. pgx's
// stdlib driver materializes arrays as their brace-literal text form (e.g.
// "{401,account_suspended}"), which database/sql cannot scan directly into a
// Go []string; this adapter parses that literal back into a slice. A NULL
// array scans to a nil slice.
type textArrayScanner struct {
	dest *[]string
}

func (s textArrayScanner) Scan(src any) error {
	// Each branch replaces *s.dest with a freshly parsed slice (never appends
	// to a caller-owned slice), so a reused scanner cannot leak an earlier
	// row's elements into a shorter array; that replacement semantics is safe
	// because pgx's stdlib driver delivers the whole array literal in a single
	// Scan call, never element-by-element.
	switch v := src.(type) {
	case nil:
		*s.dest = nil
		return nil
	case string:
		*s.dest = parsePGTextArrayLiteral(v)
		return nil
	case []byte:
		*s.dest = parsePGTextArrayLiteral(string(v))
		return nil
	default:
		*s.dest = nil
		return fmt.Errorf("postgres store: unsupported TEXT[] value %T", src)
	}
}

// parsePGTextArrayLiteral splits a PostgreSQL array literal ("{a,b,\"c,d\"}")
// into its elements, unescaping quoted and backslash-escaped values. Used to
// read TEXT[] columns through database/sql.
func parsePGTextArrayLiteral(s string) []string {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == '\\' && inQuote && i+1 < len(inner):
			cur.WriteByte(inner[i+1])
			i++
		case c == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

// marshalTextArray binds a []string as a PG TEXT[] argument. A nil slice
// writes SQL NULL (feature off); an empty slice writes the empty array '{}'.
// pgx natively encodes a Go []string for a TEXT[] parameter, so no manual
// literal building is needed.
func marshalTextArray(in []string) any {
	if in == nil {
		return nil
	}
	return in
}

// Compile-time assertion that *pgUpstreamProviderStore implements the contract.
var _ UpstreamProviderStore = (*pgUpstreamProviderStore)(nil)
