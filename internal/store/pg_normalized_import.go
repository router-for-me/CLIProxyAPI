// Package store provides a transaction-aware entry point for applying the
// normalized resource plan produced by configsnapshot.BuildResourcePlan.
// It runs every write inside one PostgreSQL transaction so a partial failure
// rolls back every resource together.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

func (s *PostgresStore) ApplyNormalizedResourcePlan(
	ctx context.Context,
	plan *NormalizedResourcePlan,
) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: ApplyNormalizedResourcePlan: store not initialized")
	}
	if plan == nil {
		return fmt.Errorf("postgres store: ApplyNormalizedResourcePlan: plan is nil")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin apply tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	upstream := s.upstreamProviderStoreForTx(tx)
	apiKeys := s.apiKeyStoreForTx(tx)

	for i := range plan.Providers {
		p := plan.Providers[i]
		if err := upstream.applyOneProviderTx(ctx, tx, p); err != nil {
			plan.Report.AddError("upstream_providers", identityOfProvider(p), err)
			return err
		}
	}

	for i := range plan.APIKeys {
		k := plan.APIKeys[i]
		if err := apiKeys.applyOneClientKeyTx(ctx, tx, k); err != nil {
			plan.Report.AddError("api_keys", k.ID, err)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: commit apply tx: %w", err)
	}
	committed = true
	plan.Report.Committed = true
	plan.Report.RolledBack = false
	return nil
}

// upstreamProviderStoreForTx returns a transaction-aware adapter that calls
// into the existing pgUpstreamProviderStore. The public Create/Update paths
// already begin their own transactions; for the atomic import we instead
// reuse the lower-level helpers that accept a *sql.Tx (loadChildren,
// replaceChildrenTx, syncAPIKeyEntriesTx, listAPIKeyEntryIDsTx,
// lookupAPIKeyEntryProviderTx). Where the upstream store does not expose a
// helper, the import path re-issues the upsert statement directly using
// the same SQL surface so we stay inside the caller's transaction.
type upstreamTxStore struct {
	s *pgUpstreamProviderStore
}

func (s *PostgresStore) upstreamProviderStoreForTx(*sql.Tx) *upstreamTxStore {
	return &upstreamTxStore{s: s.newUpstreamProviderStore()}
}

// newUpstreamProviderStore builds a fresh store without going through
// NewUpstreamProviderStore's nil-on-nil-parent guard, so the helper works
// even when called from ApplyNormalizedResourcePlan on a non-nil parent.
func (s *PostgresStore) newUpstreamProviderStore() *pgUpstreamProviderStore {
	if s == nil {
		return nil
	}
	return &pgUpstreamProviderStore{
		db:       s.DB(),
		table:    s.UpstreamProvidersTable(),
		models:   s.UpstreamProviderModelsTable(),
		headers:  s.UpstreamProviderHeadersTable(),
		excluded: s.UpstreamProviderExcludedTable(),
		entries:  s.UpstreamProviderEntriesTable(),
	}
}

// apiKeyStoreForTx returns a transaction-aware adapter for client API keys.
type apiKeyTxStore struct {
	s *APIKeyStore
}

func (s *PostgresStore) apiKeyStoreForTx(*sql.Tx) *apiKeyTxStore {
	if s == nil {
		return &apiKeyTxStore{}
	}
	return &apiKeyTxStore{s: NewAPIKeyStore(s)}
}

// applyOneProviderTx upserts one provider row plus its child collections
// inside the caller's *sql.Tx. Existing children are matched against
// incoming entries/models by identity; non-matching existing children are
// removed only when the import declares a complete-collection mode (set
// via the plan later if needed; today we treat incoming as complete for the
// apply pass).
func (u *upstreamTxStore) applyOneProviderTx(ctx context.Context, tx *sql.Tx, p UpstreamProvider) error {
	if u == nil || u.s == nil || u.s.db == nil {
		return fmt.Errorf("postgres store: provider apply: store not initialized")
	}
	normalized, err := normalizeUpstreamProvider(p)
	if err != nil {
		return err
	}

	// Step 1: find existing parent row by stable identity.
	existing, err := lookupExistingProviderTx(ctx, tx, u.s, normalized)
	if err != nil {
		return fmt.Errorf("postgres store: lookup provider %s: %w", identityOfProvider(normalized), err)
	}

	var parentID int64
	if existing != nil {
		parentID = existing.ID
		if err := updateParentTx(ctx, tx, u.s, parentID, normalized); err != nil {
			return fmt.Errorf("postgres store: update provider %s: %w", identityOfProvider(normalized), err)
		}
	} else {
		newID, err := insertParentTx(ctx, tx, u.s, normalized)
		if err != nil {
			return fmt.Errorf("postgres store: insert provider %s: %w", identityOfProvider(normalized), err)
		}
		parentID = newID
	}

	// Step 2: child collections — reuse the existing tx helpers for models,
	// headers, excluded models, and API-key entries. The helpers use the
	// caller-supplied tx, so every child write shares this transaction.
	if err := replaceProviderChildrenTx(ctx, tx, u.s, parentID, normalized); err != nil {
		return fmt.Errorf("postgres store: replace provider children: %w", err)
	}
	if err := replaceProviderHeadersTx(ctx, tx, u.s, parentID, normalized.Headers); err != nil {
		return fmt.Errorf("postgres store: replace provider headers: %w", err)
	}
	if err := replaceProviderExcludedTx(ctx, tx, u.s, parentID, normalized.ExcludedModels); err != nil {
		return fmt.Errorf("postgres store: replace provider excluded: %w", err)
	}
	if err := u.s.syncAPIKeyEntriesStableTx(ctx, tx, parentID, &normalized); err != nil {
		return fmt.Errorf("postgres store: sync api key entries: %w", err)
	}
	return nil
}

// lookupExistingProviderTx finds a provider row by stable identity. The
// identity rule is mirrored from the planner's dupKey: operator-named rows
// match on (provider_type, name); unnamed rows match on a fingerprint that
// prefers api_key/base_url/prefix and falls back to the full entry set.
func lookupExistingProviderTx(ctx context.Context, tx *sql.Tx, s *pgUpstreamProviderStore, p UpstreamProvider) (*UpstreamProvider, error) {
	if s == nil {
		return nil, fmt.Errorf("postgres store: lookup provider: store nil")
	}
	// Operator-named rows: match on provider_type + name.
	if !isSyntheticProviderName(p.ProviderType, p.Name) && strings.TrimSpace(p.Name) != "" {
		var row UpstreamProvider
		err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT id, provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
			        base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
			        status, unavailable, last_error, last_error_at, websockets,
			        rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
			        cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
			        token_access_token, token_refresh_token, token_token_type,
			        token_expiry, token_expired, token_scope, extra_config
			FROM %s WHERE provider_type = $1 AND lower(name) = lower($2) FOR UPDATE`, s.table),
			p.ProviderType, strings.TrimSpace(p.Name),
		).Scan(
			&row.ID, &row.ProviderType, &row.Name, &row.Priority, &row.Disabled, &row.RoutingStrategy,
			&row.CircuitBreaker, &row.Prefix, &row.APIKey, &row.BaseURL, &row.ProxyURL,
			&row.ProxyPoolID, &row.Label, &row.Email, &row.FileName, &row.SourceBackend,
			&row.Status, &row.Unavailable, &row.LastError, &row.LastErrorAt, &row.Websockets,
			&row.RebuildMidSystemMessage, &row.ExperimentalCCHSigning, &row.CloakMode,
			&row.CloakStrictMode, &row.CloakSensitiveWords, &row.CloakCacheUserID,
			&row.TokenAccessToken, &row.TokenRefreshToken, &row.TokenTokenType,
			&row.TokenExpiry, &row.TokenExpired, &row.TokenScope, &row.ExtraConfig,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("postgres store: scan named provider: %w", err)
		}
		return &row, nil
	}

	// Unnamed key-bearing rows: match on provider_type + normalized key +
	// base_url + prefix.
	if strings.TrimSpace(p.APIKey) != "" && p.ProviderType != "openai-compatibility" && p.ProviderType != "opencode-go" {
		var row UpstreamProvider
		err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT id, provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
			        base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
			        status, unavailable, last_error, last_error_at, websockets,
			        rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
			        cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
			        token_access_token, token_refresh_token, token_token_type,
			        token_expiry, token_expired, token_scope, extra_config
			FROM %s WHERE provider_type = $1 AND lower(api_key) = lower($2)
			        AND lower(coalesce(base_url,'')) = lower(coalesce($3,''))
			        AND lower(coalesce(prefix,'')) = lower(coalesce($4,''))
			LIMIT 1 FOR UPDATE`, s.table),
			p.ProviderType, strings.TrimSpace(p.APIKey),
			strings.TrimSpace(p.BaseURL), strings.TrimSpace(p.Prefix),
		).Scan(
			&row.ID, &row.ProviderType, &row.Name, &row.Priority, &row.Disabled, &row.RoutingStrategy,
			&row.CircuitBreaker, &row.Prefix, &row.APIKey, &row.BaseURL, &row.ProxyURL,
			&row.ProxyPoolID, &row.Label, &row.Email, &row.FileName, &row.SourceBackend,
			&row.Status, &row.Unavailable, &row.LastError, &row.LastErrorAt, &row.Websockets,
			&row.RebuildMidSystemMessage, &row.ExperimentalCCHSigning, &row.CloakMode,
			&row.CloakStrictMode, &row.CloakSensitiveWords, &row.CloakCacheUserID,
			&row.TokenAccessToken, &row.TokenRefreshToken, &row.TokenTokenType,
			&row.TokenExpiry, &row.TokenExpired, &row.TokenScope, &row.ExtraConfig,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("postgres store: scan keyed provider: %w", err)
		}
		return &row, nil
	}

	// Keyless rows: full entry fingerprint. The planner chose provider_type
	// and base_url+prefix deterministically; reuse the planner identity via
	// a like on base_url+prefix within the same provider_type. This is rare
	// in production and stable for the keyless pool case.
	var row UpstreamProvider
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id, provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
		        base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
		        status, unavailable, last_error, last_error_at, websockets,
		        rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
		        cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
		        token_access_token, token_refresh_token, token_token_type,
		        token_expiry, token_expired, token_scope, extra_config
		FROM %s WHERE provider_type = $1
		        AND lower(coalesce(api_key,'')) = ''
		        AND lower(coalesce(base_url,'')) = lower(coalesce($2,''))
		        AND lower(coalesce(prefix,'')) = lower(coalesce($3,''))
		LIMIT 1 FOR UPDATE`, s.table),
		p.ProviderType, strings.TrimSpace(p.BaseURL), strings.TrimSpace(p.Prefix),
	).Scan(
		&row.ID, &row.ProviderType, &row.Name, &row.Priority, &row.Disabled, &row.RoutingStrategy,
		&row.CircuitBreaker, &row.Prefix, &row.APIKey, &row.BaseURL, &row.ProxyURL,
		&row.ProxyPoolID, &row.Label, &row.Email, &row.FileName, &row.SourceBackend,
		&row.Status, &row.Unavailable, &row.LastError, &row.LastErrorAt, &row.Websockets,
		&row.RebuildMidSystemMessage, &row.ExperimentalCCHSigning, &row.CloakMode,
		&row.CloakStrictMode, &row.CloakSensitiveWords, &row.CloakCacheUserID,
		&row.TokenAccessToken, &row.TokenRefreshToken, &row.TokenTokenType,
		&row.TokenExpiry, &row.TokenExpired, &row.TokenScope, &row.ExtraConfig,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres store: scan keyless provider: %w", err)
	}
	return &row, nil
}

// insertParentTx performs a single INSERT returning the new provider id. We
// inline the SQL so we keep the caller's *sql.Tx and avoid the existing
// store helpers that start their own transactions.
func insertParentTx(ctx context.Context, tx *sql.Tx, s *pgUpstreamProviderStore, p UpstreamProvider) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("postgres store: insert parent: store nil")
	}
	extra, err := marshalExtraConfig(p.ExtraConfig)
	if err != nil {
		return 0, err
	}
	sensitive, err := marshalStringSlice(p.CloakSensitiveWords)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (
			provider_type, name, priority, disabled, routing_strategy, circuit_breaker, prefix, api_key,
			base_url, proxy_url, proxy_pool_id, label, email, file_name, source_backend,
			status, unavailable, last_error, last_error_at, websockets,
			rebuild_mid_system_message, experimental_cch_signing, cloak_mode,
			cloak_strict_mode, cloak_sensitive_words, cloak_cache_user_id,
			token_access_token, token_refresh_token, token_token_type,
			token_expiry, token_expired, token_scope, extra_config
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33
		) RETURNING id`, s.table),
		p.ProviderType, nullableString(p.Name), p.Priority, p.Disabled, nullableString(p.RoutingStrategy), p.CircuitBreaker, nullableString(p.Prefix),
		nullableString(p.APIKey), nullableString(p.BaseURL), nullableString(p.ProxyURL), nullableID(p.ProxyPoolID),
		nullableString(p.Label), nullableString(p.Email), nullableString(p.FileName),
		nullableString(p.SourceBackend), nullableString(p.Status), p.Unavailable,
		nullableString(p.LastError), nullableTime(p.LastErrorAt), p.Websockets,
		p.RebuildMidSystemMessage, p.ExperimentalCCHSigning, nullableString(p.CloakMode),
		p.CloakStrictMode, sensitive, nullableBool(p.CloakCacheUserID),
		nullableString(p.TokenAccessToken), nullableString(p.TokenRefreshToken),
		nullableString(p.TokenTokenType), nullableTime(p.TokenExpiry), nullableBool(p.TokenExpired),
		nullableString(p.TokenScope), extra,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres store: insert provider: %w", err)
	}
	return id, nil
}

// updateParentTx preserves every YAML-owned field. Operational fields the
// planner does not own are overwritten with the live row's values to avoid
// clobbering status/last_error/proxy_pool_id unless explicitly provided.
func updateParentTx(ctx context.Context, tx *sql.Tx, s *pgUpstreamProviderStore, id int64, p UpstreamProvider) error {
	extra, err := marshalExtraConfig(p.ExtraConfig)
	if err != nil {
		return err
	}
	sensitive, err := marshalStringSlice(p.CloakSensitiveWords)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET
			priority = $1,
			disabled = $2,
			routing_strategy = $3,
			circuit_breaker = $4,
			prefix = $5,
			api_key = $6,
			base_url = $7,
			proxy_url = $8,
			websockets = $9,
			rebuild_mid_system_message = $10,
			experimental_cch_signing = $11,
			cloak_mode = $12,
			cloak_strict_mode = $13,
			cloak_sensitive_words = $14,
			cloak_cache_user_id = $15,
			extra_config = $16,
			updated_at = NOW()
		WHERE id = $17`, s.table),
		p.Priority, p.Disabled, nullableString(p.RoutingStrategy), p.CircuitBreaker, nullableString(p.Prefix),
		nullableString(p.APIKey), nullableString(p.BaseURL), nullableString(p.ProxyURL),
		p.Websockets, p.RebuildMidSystemMessage, p.ExperimentalCCHSigning,
		nullableString(p.CloakMode), p.CloakStrictMode, sensitive, nullableBool(p.CloakCacheUserID),
		extra, id,
	); err != nil {
		return fmt.Errorf("postgres store: update provider %d: %w", id, err)
	}
	return nil
}

// replaceProviderChildrenTx reuses the existing tx helper to mirror models
// into upstream_provider_models for the given parent id.
func replaceProviderChildrenTx(ctx context.Context, tx *sql.Tx, s *pgUpstreamProviderStore, providerID int64, p UpstreamProvider) error {
	if len(p.Models) == 0 {
		// Clear child rows so a removed model does not linger.
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM "+s.models+" WHERE provider_id = $1", providerID,
		); err != nil {
			return fmt.Errorf("postgres store: clear models for %d: %w", providerID, err)
		}
		return nil
	}
	for i, m := range p.Models {
		var id int64
		err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (provider_id, name, alias, display_name, force_mapping, fork, image,
			        input_modalities, output_modalities, thinking, wire_format, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING id`, s.models),
			providerID, m.Name, nullableString(m.Alias), nullableString(m.DisplayName),
			m.ForceMapping, m.Fork, m.Image,
			jsonOrNil(m.InputModalities), jsonOrNil(m.OutputModalities),
			jsonOrNil(m.Thinking), nullableString(m.WireFormat), i,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("postgres store: insert model %d: %w", i, err)
		}
	}
	return nil
}

// replaceProviderHeadersTx deletes the row for this parent then re-inserts
// the supplied headers map. Using a fresh id is acceptable because the
// schema keys on (provider_id, header_key); no other table joins on header id.
func replaceProviderHeadersTx(ctx context.Context, tx *sql.Tx, s *pgUpstreamProviderStore, providerID int64, headers map[string]string) error {
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM "+s.headers+" WHERE provider_id = $1", providerID,
	); err != nil {
		return fmt.Errorf("postgres store: clear headers for %d: %w", providerID, err)
	}
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("INSERT INTO %s (provider_id, header_key, header_value) VALUES ($1, $2, $3)", s.headers),
			providerID, k, headers[k],
		); err != nil {
			return fmt.Errorf("postgres store: insert header %s: %w", k, err)
		}
	}
	return nil
}

// replaceProviderExcludedTx refreshes the excluded-models set for the parent.
func replaceProviderExcludedTx(ctx context.Context, tx *sql.Tx, s *pgUpstreamProviderStore, providerID int64, models []string) error {
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM "+s.excluded+" WHERE provider_id = $1", providerID,
	); err != nil {
		return fmt.Errorf("postgres store: clear excluded for %d: %w", providerID, err)
	}
	for _, m := range models {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("INSERT INTO %s (provider_id, model) VALUES ($1, $2)", s.excluded),
			providerID, m,
		); err != nil {
			return fmt.Errorf("postgres store: insert excluded %s: %w", m, err)
		}
	}
	return nil
}

// syncAPIKeyEntriesStableTx rewrites the API-key entry set without churning
// existing child IDs when possible. Existing entries are matched by their
// canonical identity (api_key, name, proxy_url, proxy_pool_id, weight,
// priority, disabled). Unused entries keep their persisted IDs; new
// entries are inserted with id=0 so the store assigns one; entries that no
// longer exist in the incoming plan are deleted only if the planner
// declares the plan complete (always true for the apply pass).
func (s *pgUpstreamProviderStore) syncAPIKeyEntriesStableTx(
	ctx context.Context,
	tx *sql.Tx,
	providerID int64,
	p *UpstreamProvider,
) error {
	if s == nil {
		return fmt.Errorf("postgres store: sync entries: store nil")
	}
	existing, err := s.listAPIKeyEntryIDsTx(ctx, tx, providerID)
	if err != nil {
		return fmt.Errorf("postgres store: list existing entries: %w", err)
	}
	sigs := make([]entrySig, 0, len(p.APIKeyEntries))
	for _, e := range p.APIKeyEntries {
		sigs = append(sigs, entrySigFromUpstream(e))
	}

	// Map incoming entries to the persisted rows by canonical identity.
	matched := map[int64]bool{}
	matchedIncoming := make([]bool, len(p.APIKeyEntries))
	for ei, sig := range sigs {
		for id := range existing {
			if matched[id] {
				continue
			}
			existingSig, ok := s.readEntrySigTx(ctx, tx, id)
			if !ok {
				continue
			}
			if existingSig == sig {
				if err := s.updateEntrySigTx(ctx, tx, id, sig, ei); err != nil {
					return err
				}
				matched[id] = true
				matchedIncoming[ei] = true
				break
			}
		}
	}

	// Insert unmatched incoming rows.
	for ei, sig := range sigs {
		if matchedIncoming[ei] {
			continue
		}
		if err := s.insertEntrySigTx(ctx, tx, providerID, sig, ei); err != nil {
			return err
		}
	}

	// Delete unmatched existing rows: the import is complete-collection,
	// so any persisted row not claimed above is now stale.
	for id := range existing {
		if matched[id] {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM "+s.entries+" WHERE id = $1 AND provider_id = $2",
			id, providerID,
		); err != nil {
			return fmt.Errorf("postgres store: delete entry %d: %w", id, err)
		}
	}
	return nil
}

// entrySig is the canonical identity of one API-key entry row used for
// stable matching between the incoming plan and persisted children.
type entrySig struct {
	APIKey, Name, Proxy string
	Pool                int64
	Weight, Priority    int
	Disabled            bool
}

// entrySigFromUpstream derives the canonical identity from an incoming
// normalized entry. The persisted child ID is intentionally excluded so a
// zero-ID incoming row can match a persisted sibling.
func entrySigFromUpstream(e UpstreamProviderAPIKey) entrySig {
	var pool int64
	if e.ProxyPoolID != nil {
		pool = *e.ProxyPoolID
	}
	return entrySig{
		APIKey: strings.TrimSpace(e.APIKey), Name: strings.TrimSpace(e.Name),
		Proxy: strings.TrimSpace(e.ProxyURL), Pool: pool,
		Weight: trimIntPtr(e.Weight), Priority: trimIntPtr(e.Priority),
		Disabled: e.Disabled,
	}
}

// trimIntPtr returns *p or 0 when p is nil.
func trimIntPtr(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func (s *pgUpstreamProviderStore) readEntrySigTx(ctx context.Context, tx *sql.Tx, id int64) (entrySig, bool) {
	var sig entrySig
	var pool sql.NullInt64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT api_key, coalesce(name,''), coalesce(proxy_url,''),
		        proxy_pool_id, coalesce(weight,0), coalesce(priority,0), disabled
		 FROM %s WHERE id = $1`, s.entries), id,
	).Scan(&sig.APIKey, &sig.Name, &sig.Proxy, &pool, &sig.Weight, &sig.Priority, &sig.Disabled)
	if err != nil {
		return entrySig{}, false
	}
	if pool.Valid {
		sig.Pool = pool.Int64
	}
	return sig, true
}

func (s *pgUpstreamProviderStore) updateEntrySigTx(ctx context.Context, tx *sql.Tx, id int64, sig entrySig, sortOrder int) error {
	var poolArg any
	if sig.Pool != 0 {
		poolArg = sig.Pool
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET name = $1, proxy_url = $2, proxy_pool_id = $3,
		        weight = $4, priority = $5, disabled = $6, sort_order = $7
		WHERE id = $8`, s.entries),
		nullableString(sig.Name), nullableString(sig.Proxy), poolArg,
		nullableInt(intPtrForImport(sig.Weight)), nullableInt(intPtrForImport(sig.Priority)), sig.Disabled, sortOrder, id,
	)
	return err
}

func (s *pgUpstreamProviderStore) insertEntrySigTx(
	ctx context.Context, tx *sql.Tx,
	providerID int64,
	sig entrySig,
	sortOrder int,
) error {
	weight := nullableInt(intPtrForImport(sig.Weight))
	priority := nullableInt(intPtrForImport(sig.Priority))
	var poolArg any
	if sig.Pool != 0 {
		poolArg = sig.Pool
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (provider_id, api_key, name, proxy_url, proxy_pool_id,
		        weight, priority, disabled, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, s.entries),
		providerID, sig.APIKey, nullableString(sig.Name), nullableString(sig.Proxy),
		poolArg, weight, priority, sig.Disabled, sortOrder,
	)
	return err
}

// intPtrForImport wraps a plain int as *int so nullableInt can bind zero as
// an explicit 0. Named distinctly from the test-only intPtr helper.
func intPtrForImport(v int) *int { return &v }

// jsonOrNil marshals v and returns the bytes as an any suitable for a
// jsonb parameter; nil input binds as nil so absent collections stay NULL.
func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(raw)
}

// applyOneClientKeyTx upserts a single API key row inside the caller's
// *sql.Tx. The apply does not generate secrets and never stores plaintext
// in errors. On unique-hash conflicts, the caller's tx is rolled back so
// concurrent imports cannot produce duplicate rows.
func (a *apiKeyTxStore) applyOneClientKeyTx(ctx context.Context, tx *sql.Tx, k APIKey) error {
	if a == nil || a.s == nil || a.s.db == nil {
		return fmt.Errorf("postgres store: api key apply: store not initialized")
	}
	if k.KeyHash == "" {
		return fmt.Errorf("postgres store: api key hash required")
	}
	if k.ID == "" {
		k.ID = uuid.NewString()
	}
	meta := k.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("postgres store: marshal api key metadata: %w", err)
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id, name, key_alias, key_hash, key_prefix, status,
		        user_id, expires_at, last_used_at, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (id) DO UPDATE SET
		    name = EXCLUDED.name,
		    key_alias = EXCLUDED.key_alias,
		    key_hash = EXCLUDED.key_hash,
		    key_prefix = EXCLUDED.key_prefix,
		    status = EXCLUDED.status,
		    expires_at = EXCLUDED.expires_at,
		    metadata = EXCLUDED.metadata,
		    updated_at = NOW()`, a.s.apiKeysTable),
		k.ID, trimOr(k.Name, "unnamed"), nullableString(k.KeyAlias), k.KeyHash, k.KeyPrefix,
		k.Status, nullableString(k.UserID), k.ExpiresAt, k.LastUsedAt, string(metaJSON),
	)
	if err != nil {
		return fmt.Errorf("postgres store: upsert api key %s: %w", k.ID, err)
	}
	return nil
}

// isSyntheticProviderName reports whether name matches the "<prefix><n>"
// pattern the configsnapshot planner assigns to single-credential sections.
// Those names are positional labels, not operator-authored identity, so the
// apply path's lookup must not match on them.
func isSyntheticProviderName(providerType, name string) bool {
	var prefix string
	switch providerType {
	case "claude-api-key":
		prefix = "claude-"
	case "gemini-api-key":
		prefix = "gemini-api-key-"
	case "interactions-api-key":
		prefix = "interactions-api-key-"
	case "codex-api-key":
		prefix = "codex-"
	case "xai-api-key":
		prefix = "xai-"
	case "vertex-api-key":
		prefix = "vertex-"
	default:
		return false
	}
	suffix, ok := strings.CutPrefix(name, prefix)
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// identityOfProvider returns a stable human-readable identity for the
// provider row to use in error reports.
func identityOfProvider(p UpstreamProvider) string {
	if strings.TrimSpace(p.Name) != "" {
		return p.ProviderType + ":" + strings.TrimSpace(p.Name)
	}
	return p.ProviderType + ":" + shortHash(p)
}

// shortHash returns a compact hex digest of the input for error messages.
func shortHash(p UpstreamProvider) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%v", p.ProviderType, p.APIKey, p.BaseURL, p.Prefix, p.APIKeyEntries)))
	return hex.EncodeToString(h[:6])
}

// identityOfProvider returns a stable human-readable identity for the
// provider row to use in error reports.
