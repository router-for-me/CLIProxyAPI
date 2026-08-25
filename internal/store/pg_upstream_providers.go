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
	ID                      int64          `json:"id"`
	ProviderType            string         `json:"provider_type"`
	Name                    string         `json:"name,omitempty"`
	Priority                int            `json:"priority"`
	Disabled                bool           `json:"disabled"`
	Prefix                  string         `json:"prefix,omitempty"`
	APIKey                  string         `json:"api_key,omitempty"`
	BaseURL                 string         `json:"base_url,omitempty"`
	ProxyURL                string         `json:"proxy_url,omitempty"`
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
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`

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
	SortOrder        int            `json:"sort_order,omitempty"`
}

// UpstreamProviderAPIKey is one entry of an openai-compatibility provider's
// api-key-entries[] list.
type UpstreamProviderAPIKey struct {
	ID         int64  `json:"id,omitempty"`
	ProviderID int64  `json:"provider_id,omitempty"`
	APIKey     string `json:"api_key"`
	Name       string `json:"name,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	SortOrder  int    `json:"sort_order,omitempty"`
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
		SELECT id, provider_type, name, priority, disabled, prefix, api_key,
		       base_url, proxy_url, label, email, file_name, source_backend,
		       status, unavailable, last_error, last_error_at, websockets,
		       rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		       cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		       token_access_token, token_refresh_token, token_token_type,
		       token_expiry, token_expired, token_scope, extra_config,
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
			provider_type, name, priority, disabled, prefix, api_key,
			base_url, proxy_url, label, email, file_name, source_backend,
			status, unavailable, last_error, last_error_at, websockets,
			rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
			cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
			token_access_token, token_refresh_token, token_token_type,
			token_expiry, token_expired, token_scope, extra_config
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30
		)
		RETURNING id, provider_type, name, priority, disabled, prefix, api_key,
		          base_url, proxy_url, label, email, file_name, source_backend,
		          status, unavailable, last_error, last_error_at, websockets,
		          rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		          cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		          token_access_token, token_refresh_token, token_token_type,
		          token_expiry, token_expired, token_scope, extra_config,
		          created_at, updated_at
	`, s.table),
		p.ProviderType, nullableString(p.Name), p.Priority, p.Disabled, nullableString(p.Prefix),
		nullableString(p.APIKey), nullableString(p.BaseURL), nullableString(p.ProxyURL),
		nullableString(p.Label), nullableString(p.Email), nullableString(p.FileName),
		nullableString(p.SourceBackend), nullableString(p.Status), p.Unavailable,
		nullableString(p.LastError), nullableTime(p.LastErrorAt), p.Websockets,
		p.RebuildMidSystemMessage, p.ExperimentalCCHSigning, nullableString(p.CloakMode),
		p.CloakStrictMode, sensitive, nullableBool(p.CloakCacheUserID),
		nullableString(p.TokenAccessToken), nullableString(p.TokenRefreshToken),
		nullableString(p.TokenTokenType), nullableTime(p.TokenExpiry), nullableBool(p.TokenExpired),
		nullableString(p.TokenScope), extra,
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
			prefix = $5,
			api_key = $6,
			base_url = $7,
			proxy_url = $8,
			label = $9,
			email = $10,
			file_name = $11,
			source_backend = $12,
			status = $13,
			unavailable = $14,
			last_error = $15,
			last_error_at = $16,
			websockets = $17,
			rebuild_mid_system_message = $18,
			experimental_cch_signing = $19,
			cloak_mode = $20,
			cloak_strict_mode = $21,
			cloak_sensitive_words = $22,
			cloak_cache_user_id = $23,
			token_access_token = $24,
			token_refresh_token = $25,
			token_token_type = $26,
			token_expiry = $27,
			token_expired = $28,
			token_scope = $29,
			extra_config = $30,
			updated_at = NOW()
		WHERE id = $31
		RETURNING id, provider_type, name, priority, disabled, prefix, api_key,
		          base_url, proxy_url, label, email, file_name, source_backend,
		          status, unavailable, last_error, last_error_at, websockets,
		          rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		          cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		          token_access_token, token_refresh_token, token_token_type,
		          token_expiry, token_expired, token_scope, extra_config,
		          created_at, updated_at
	`, s.table),
		p.ProviderType, nullableString(p.Name), p.Priority, p.Disabled, nullableString(p.Prefix),
		nullableString(p.APIKey), nullableString(p.BaseURL), nullableString(p.ProxyURL),
		nullableString(p.Label), nullableString(p.Email), nullableString(p.FileName),
		nullableString(p.SourceBackend), nullableString(p.Status), p.Unavailable,
		nullableString(p.LastError), nullableTime(p.LastErrorAt), p.Websockets,
		p.RebuildMidSystemMessage, p.ExperimentalCCHSigning, nullableString(p.CloakMode),
		p.CloakStrictMode, sensitive, nullableBool(p.CloakCacheUserID),
		nullableString(p.TokenAccessToken), nullableString(p.TokenRefreshToken),
		nullableString(p.TokenTokenType), nullableTime(p.TokenExpiry), nullableBool(p.TokenExpired),
		nullableString(p.TokenScope), extra, p.ID,
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
		       input_modalities, output_modalities, thinking, sort_order
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
			&m.Fork, &m.Image, &inputMod, &outputMod, &thinking, &m.SortOrder); err != nil {
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
		SELECT id, provider_id, api_key, name, proxy_url, sort_order
		FROM %s WHERE provider_id = $1 ORDER BY sort_order, id
	`, s.entries), p.ID)
	if err != nil {
		return fmt.Errorf("postgres store: list upstream provider api key entries: %w", err)
	}
	for aRows.Next() {
		var e UpstreamProviderAPIKey
		var entryName, proxyURL sql.NullString
		if err = aRows.Scan(&e.ID, &e.ProviderID, &e.APIKey, &entryName, &proxyURL, &e.SortOrder); err != nil {
			aRows.Close()
			return fmt.Errorf("postgres store: scan upstream provider api key entry: %w", err)
		}
		if entryName.Valid {
			e.Name = entryName.String
		}
		if proxyURL.Valid {
			e.ProxyURL = proxyURL.String
		}
		p.APIKeyEntries = append(p.APIKeyEntries, e)
	}
	aRows.Close()
	if err = aRows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate upstream provider api key entries: %w", err)
	}
	return nil
}

// replaceChildrenTx wipes and re-inserts all child rows for providerID within
// the given transaction, mirroring the in-memory collections on p.
func (s *pgUpstreamProviderStore) replaceChildrenTx(ctx context.Context, tx *sql.Tx, providerID int64, p *UpstreamProvider) error {
	// Delete existing children.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.models), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider models: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.headers), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider headers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.excluded), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider excluded models: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE provider_id = $1`, s.entries), providerID); err != nil {
		return fmt.Errorf("postgres store: clear upstream provider api key entries: %w", err)
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
			                input_modalities, output_modalities, thinking, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, s.models), providerID, m.Name, nullableString(m.Alias), nullableString(m.DisplayName),
			m.ForceMapping, m.Fork, m.Image, input, output, thinking, sortOrder,
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

	// Insert api-key entries.
	for i, e := range p.APIKeyEntries {
		sortOrder := e.SortOrder
		if sortOrder == 0 {
			sortOrder = i
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (provider_id, api_key, name, proxy_url, sort_order)
			VALUES ($1,$2,$3,$4,$5)
		`, s.entries), providerID, e.APIKey, nullableString(e.Name), nullableString(e.ProxyURL), sortOrder,
		); err != nil {
			return fmt.Errorf("postgres store: insert upstream provider api key entry: %w", err)
		}
	}
	return nil
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
		name, prefix, apiKey, baseURL, proxyURL, label, email,
		fileName, sourceBackend, status, lastError,
		cloakMode, tokenAccess, tokenRefresh, tokenType, tokenScope sql.NullString
		lastErrorAt, tokenExpiry       sql.NullTime
		cloakCacheUserID, tokenExpired sql.NullBool
		sensitiveBytes, extraBytes     []byte
	)
	if err := sc.Scan(
		&p.ID, &p.ProviderType, &name, &p.Priority, &p.Disabled, &prefix, &apiKey,
		&baseURL, &proxyURL, &label, &email, &fileName, &sourceBackend,
		&status, &p.Unavailable, &lastError, &lastErrorAt, &p.Websockets,
		&p.RebuildMidSystemMessage, &p.ExperimentalCCHSigning, &cloakMode,
		&p.CloakStrictMode, &sensitiveBytes, &cloakCacheUserID,
		&tokenAccess, &tokenRefresh, &tokenType, &tokenExpiry, &tokenExpired,
		&tokenScope, &extraBytes, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return err
	}
	p.Name = name.String
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
func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

// Compile-time assertion that *pgUpstreamProviderStore implements the contract.
var _ UpstreamProviderStore = (*pgUpstreamProviderStore)(nil)
