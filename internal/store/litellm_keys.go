package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LiteLLMKeyStatusActive is the default lifecycle status of a newly created
// Manage-LiteLLM key.
const LiteLLMKeyStatusActive = "active"

// ErrLiteLLMKeyNotFound is returned when no Manage-LiteLLM key matches the
// supplied identifier.
var ErrLiteLLMKeyNotFound = errors.New("postgres store: litellm key not found")

// LiteLLMPolicy captures the complete LiteLLM-style limits enforced on a
// Manage-LiteLLM key. Pointer-typed scalar fields distinguish "unset /
// unlimited" (nil) from explicit zero values. Unlike the runtime store.Policy,
// this carries a single budget_usd + budget_duration pair (LiteLLM semantics),
// a key-level tpm_limit, and a model→alias map.
type LiteLLMPolicy struct {
	APIKeyID            string            `json:"api_key_id"`
	RPMLimit            *int              `json:"rpm_limit,omitempty"`
	TPMLimit            *int              `json:"tpm_limit,omitempty"`
	BudgetUSD           *float64          `json:"budget_usd,omitempty"`
	BudgetDuration      string            `json:"budget_duration,omitempty"`
	MaxParallelRequests *int              `json:"max_parallel_requests,omitempty"`
	AllowedModels       []string          `json:"allowed_models,omitempty"`
	BlockedModels       []string          `json:"blocked_models,omitempty"`
	Aliases             map[string]string `json:"aliases,omitempty"`
	AllowedIPs          []string          `json:"allowed_ips,omitempty"`
	BlockedIPs          []string          `json:"blocked_ips,omitempty"`
	ModelRoutes         []ModelRoute      `json:"model_routes,omitempty"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

// LiteLLMKey mirrors a row in the litellm_api_keys table. The plaintext secret
// is never persisted: only KeyHash (SHA-256) is stored. Spend is the running
// total spend for this key (a complete LiteLLM-style field absent from the
// runtime api_keys table); the per-key policy lives in litellm_key_policies.
type LiteLLMKey struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	KeyAlias   string         `json:"key_alias,omitempty"`
	KeyHash    string         `json:"-"`
	KeyPrefix  string         `json:"key_prefix"`
	Status     string         `json:"status"`
	UserID     string         `json:"user_id,omitempty"`
	UserAlias  string         `json:"user_alias,omitempty"`
	UserEmail  string         `json:"user_email,omitempty"`
	Spend      float64        `json:"spend"`
	Tags       []string       `json:"tags,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
	// Policy is populated by the list paths (ListPaged) so the dashboard can
	// render per-key limits (Budget, RPM/TPM, model access) without a second
	// round-trip. It is nil when the key has no litellm_key_policies row.
	Policy *LiteLLMPolicy `json:"policy,omitempty"`
}

// LiteLLMKeyStore provides CRUD operations for Manage-LiteLLM API keys and
// their policies. It is backed by the same *sql.DB connection as PostgresStore
// and intentionally mirrors the runtime APIKeyStore surface so the two stay
// symmetric; the tables and policy shape are LiteLLM-specific.
type LiteLLMKeyStore struct {
	db                 *sql.DB
	keysTable          string
	policiesTable      string
	internalUsersTable string
}

// NewLiteLLMKeyStore builds a LiteLLMKeyStore that reuses the PostgresStore
// connection and table names. Returns nil when the parent store is nil so
// callers can feature-detect the absence of the PG backend with a nil check.
func NewLiteLLMKeyStore(parent *PostgresStore) *LiteLLMKeyStore {
	if parent == nil {
		return nil
	}
	return &LiteLLMKeyStore{
		db:                 parent.DB(),
		keysTable:          parent.LiteLLMKeysTable(),
		policiesTable:      parent.LiteLLMKeyPoliciesTable(),
		internalUsersTable: parent.LiteLLMUsersTable(),
	}
}

// Create inserts a new Manage-LiteLLM API key and (optionally) its policy in a
// single transaction. It returns the freshly generated plaintext secret; the
// caller is responsible for surfacing it to the user exactly once as the secret
// is never recoverable from the database.
func (s *LiteLLMKeyStore) Create(ctx context.Context, name string, alias string, secret string, expiresAt *time.Time, metadata map[string]any, tags []string, policy *LiteLLMPolicy) (*LiteLLMKey, string, error) {
	if s == nil || s.db == nil {
		return nil, "", fmt.Errorf("postgres store: litellm key store not initialized")
	}
	id := uuid.NewString()
	if secret == "" {
		var err error
		secret, err = GenerateSecret()
		if err != nil {
			return nil, "", err
		}
	} else if err := validateSecret(secret); err != nil {
		return nil, "", err
	}
	hash := HashSecret(secret)
	prefix := prefixOf(secret)
	displayName := trimOr(name, "unnamed")
	meta := metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: marshal litellm key metadata: %w", err)
	}
	tagsJSON, err := json.Marshal(normalizeStringSlice(tags))
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: marshal litellm key tags: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: begin litellm key create tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, key_alias, key_hash, key_prefix, status, expires_at, metadata, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb)
	`, s.keysTable), id, displayName, nullableString(alias), hash, prefix, LiteLLMKeyStatusActive, expiresAt, string(metaJSON), string(tagsJSON)); err != nil {
		return nil, "", fmt.Errorf("postgres store: insert litellm key: %w", err)
	}

	if policy != nil {
		policy.APIKeyID = id
		if err = upsertLiteLLMPolicyTx(ctx, tx, s.policiesTable, *policy); err != nil {
			return nil, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("postgres store: commit litellm key: %w", err)
	}

	created, _, err := s.LookupByID(ctx, id)
	if err != nil {
		// Best-effort reconstruction when the row is not yet visible.
		created = &LiteLLMKey{
			ID: id, Name: displayName, KeyAlias: alias, KeyHash: hash, KeyPrefix: prefix,
			Status: LiteLLMKeyStatusActive, ExpiresAt: expiresAt, Metadata: meta, Tags: tags,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	return created, secret, nil
}

// Upsert inserts a new Manage-LiteLLM key or updates an existing one by ID,
// attached to the supplied optional policy. It is used by the external-liteLLM
// sync: remote keys are created or refreshed in place, carrying the key's
// running spend and tags. The plaintext secret is supplied by the caller as
// `keyHash`/`keyPrefix` are derived from it; callers that do not retain the
// plaintext (e.g. the remote instance) pass the already-hashed form via
// `hashOverride`/`prefixOverride` (when a plaintext `secret` is non-empty those
// overrides are ignored and re-derived). Duplicate rows are updated, never
// error.
func (s *LiteLLMKeyStore) Upsert(ctx context.Context, key LiteLLMKey, secret string, hashOverride, prefixOverride string, policy *LiteLLMPolicy) (*LiteLLMKey, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	if key.ID == "" {
		key.ID = uuid.NewString()
	}
	if key.Status == "" {
		key.Status = LiteLLMKeyStatusActive
	}
	displayName := trimOr(key.Name, "unnamed")
	meta := key.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("postgres store: marshal litellm key metadata: %w", err)
	}
	tagsJSON, err := json.Marshal(normalizeStringSlice(key.Tags))
	if err != nil {
		return nil, fmt.Errorf("postgres store: marshal litellm key tags: %w", err)
	}

	// Resolve the hash + display prefix. When a plaintext secret is available
	// we derive both; otherwise we trust the caller-supplied overrides (the
	// remote instance does not hand back plaintext secrets).
	hash := strings.TrimSpace(hashOverride)
	prefix := strings.TrimSpace(prefixOverride)
	if secret != "" {
		hash = HashSecret(secret)
		prefix = prefixOf(secret)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("postgres store: begin litellm key upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, key_alias, key_hash, key_prefix, status, user_id,
			key_spend, tags, metadata, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11, NOW())
		ON CONFLICT (id) DO UPDATE SET
			name         = EXCLUDED.name,
			key_alias    = EXCLUDED.key_alias,
			key_hash     = EXCLUDED.key_hash,
			key_prefix   = EXCLUDED.key_prefix,
			status       = EXCLUDED.status,
			user_id      = EXCLUDED.user_id,
			key_spend    = EXCLUDED.key_spend,
			tags         = EXCLUDED.tags,
			metadata     = EXCLUDED.metadata,
			expires_at   = EXCLUDED.expires_at,
			updated_at   = NOW()
	`, s.keysTable),
		key.ID, displayName, nullableString(key.KeyAlias), hash, prefix, key.Status, nullableString(key.UserID),
		key.Spend, string(tagsJSON), string(metaJSON), key.ExpiresAt,
	); err != nil {
		return nil, fmt.Errorf("postgres store: upsert litellm key: %w", err)
	}

	if policy != nil {
		policy.APIKeyID = key.ID
		if err = upsertLiteLLMPolicyTx(ctx, tx, s.policiesTable, *policy); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("postgres store: commit litellm key upsert: %w", err)
	}
	created, _, err := s.LookupByID(ctx, key.ID)
	if err != nil {
		created = &key
	}
	return created, nil
}

// LookupByID returns the Manage-LiteLLM key (and its policy, if any) by its ID.
func (s *LiteLLMKeyStore) LookupByID(ctx context.Context, id string) (*LiteLLMKey, *LiteLLMPolicy, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.key_spend, k.tags, k.metadata, k.created_at, k.updated_at, k.expires_at, k.last_used_at,
		       p.rpm_limit, p.tpm_limit, p.budget_usd, COALESCE(p.budget_duration, ''),
		       p.max_parallel_requests, p.allowed_models, p.blocked_models, p.aliases,
		       p.allowed_ips, p.blocked_ips, p.model_routes, p.updated_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		LEFT JOIN %s p ON p.api_key_id = k.id
		WHERE k.id = $1
	`, s.keysTable, s.internalUsersTable, s.policiesTable), id)
	key, policy, err := scanLiteLLMKeyRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrLiteLLMKeyNotFound
		}
		return nil, nil, err
	}
	return key, policy, nil
}

func scanLiteLLMKeyRow(row *sql.Row) (*LiteLLMKey, *LiteLLMPolicy, error) {
	var (
		key             LiteLLMKey
		tagsJSON        []byte
		metadataJSON    []byte
		rpmLimit        sql.NullInt64
		tpmLimit        sql.NullInt64
		budgetUSD       sql.NullFloat64
		budgetDuration  string
		maxParallel     sql.NullInt64
		allowedModels   []byte
		blockedModels   []byte
		aliasesJSON     []byte
		allowedIPs      []byte
		blockedIPs      []byte
		modelRoutes     []byte
		policyUpdatedAt sql.NullTime
	)
	if err := row.Scan(
		&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
		&key.UserID, &key.UserAlias, &key.UserEmail,
		&key.Spend, &tagsJSON, &metadataJSON, &key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt,
		&rpmLimit, &tpmLimit, &budgetUSD, &budgetDuration, &maxParallel,
		&allowedModels, &blockedModels, &aliasesJSON,
		&allowedIPs, &blockedIPs, &modelRoutes, &policyUpdatedAt,
	); err != nil {
		return nil, nil, err
	}
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &key.Tags)
	}
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &key.Metadata)
	}
	if key.Metadata == nil {
		key.Metadata = map[string]any{}
	}

	var policy *LiteLLMPolicy
	if policyUpdatedAt.Valid {
		p := LiteLLMPolicy{APIKeyID: key.ID, UpdatedAt: policyUpdatedAt.Time, BudgetDuration: budgetDuration}
		if rpmLimit.Valid {
			v := int(rpmLimit.Int64)
			p.RPMLimit = &v
		}
		if tpmLimit.Valid {
			v := int(tpmLimit.Int64)
			p.TPMLimit = &v
		}
		if budgetUSD.Valid {
			v := budgetUSD.Float64
			p.BudgetUSD = &v
		}
		if maxParallel.Valid {
			v := int(maxParallel.Int64)
			p.MaxParallelRequests = &v
		}
		p.AllowedModels = decodeStringArray(allowedModels)
		p.BlockedModels = decodeStringArray(blockedModels)
		if len(aliasesJSON) > 0 {
			_ = json.Unmarshal(aliasesJSON, &p.Aliases)
		}
		p.AllowedIPs = decodeStringArray(allowedIPs)
		p.BlockedIPs = decodeStringArray(blockedIPs)
		p.ModelRoutes = decodeModelRoutes(modelRoutes)
		policy = &p
	}
	return &key, policy, nil
}

// LiteLLMKeyListFilter captures the optional filter dimensions for ListPaged.
// Empty values are ignored (unfiltered). SortBy is one of "created_at"
// (default), "name", "last_used_at", "user_alias", "spend". SortOrder is
// "desc" (default) or "asc". Search is a case-insensitive substring match on
// name, key_alias, or key_prefix.
type LiteLLMKeyListFilter struct {
	Status    string
	UserID    string
	Search    string
	SortBy    string
	SortOrder string
}

// ListPaged returns a page of Manage-LiteLLM keys (with owner alias/email
// joined) plus the total row count (for pager UI math). Page is 1-indexed;
// pageSize must be > 0. The user_id filter is applied natively in SQL so the
// total reflects every key matching the user, not just the visible page.
func (s *LiteLLMKeyStore) ListPaged(ctx context.Context, page, pageSize int, f LiteLLMKeyListFilter) ([]*LiteLLMKey, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var (
		where []string
		args  []any
	)
	addFilter := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.Status != "" {
		addFilter("k.status = $%d", f.Status)
	}
	if f.UserID != "" {
		addFilter("k.user_id = $%d", f.UserID)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		// Bind three separate positional placeholders (one per column) so the
		// LIKE pattern is matched against name, key_alias, and key_prefix.
		args = append(args, like, like, like)
		n := len(args)
		where = append(where, fmt.Sprintf(
			"(LOWER(k.name) LIKE LOWER($%d) OR LOWER(k.key_alias) LIKE LOWER($%d) OR LOWER(k.key_prefix) LIKE LOWER($%d))",
			n-2, n-1, n,
		))
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s k%s`, s.keysTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count litellm keys: %w", err)
	}

	sortCol := "k.created_at"
	sortDir := "DESC"
	switch strings.ToLower(f.SortBy) {
	case "name":
		sortCol = "k.name"
	case "last_used_at":
		sortCol = "k.last_used_at"
	case "user_alias":
		sortCol = "u.user_alias"
	case "spend":
		sortCol = "k.key_spend"
	case "created_at", "":
		sortCol = "k.created_at"
	}
	if strings.EqualFold(f.SortOrder, "asc") {
		sortDir = "ASC"
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.key_spend, k.tags, k.metadata, k.created_at, k.updated_at, k.expires_at, k.last_used_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id%s
		ORDER BY %s %s NULLS LAST
		LIMIT $%d OFFSET $%d
	`, s.keysTable, s.internalUsersTable, whereClause, sortCol, sortDir, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list litellm keys: %w", err)
	}
	defer rows.Close()

	keys := make([]*LiteLLMKey, 0, pageSize)
	for rows.Next() {
		var (
			key      LiteLLMKey
			tagsJSON []byte
			metaJSON []byte
		)
		if err = rows.Scan(&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
			&key.UserID, &key.UserAlias, &key.UserEmail,
			&key.Spend, &tagsJSON, &metaJSON, &key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan litellm key row: %w", err)
		}
		if len(tagsJSON) > 0 {
			_ = json.Unmarshal(tagsJSON, &key.Tags)
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &key.Metadata)
		}
		if key.Metadata == nil {
			key.Metadata = map[string]any{}
		}
		keys = append(keys, &key)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate litellm keys: %w", err)
	}
	// Attach policies for the visible page so the dashboard renders Budget and
	// RPM/TPM without a per-key round-trip. Keys without a policy row stay nil.
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	pols, err := s.policiesForKeys(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for _, k := range keys {
		k.Policy = pols[k.ID]
	}
	return keys, total, nil
}

// ListAll returns every Manage-LiteLLM API key (no pagination). It backs the
// "sync to NixLLM" key migration, which needs the full source set so the
// runtime api_keys table can mirror the external LiteLLM keys. KeyHash is
// preserved so migrated keys keep working against the proxy's hash lookup.
func (s *LiteLLMKeyStore) ListAll(ctx context.Context) ([]LiteLLMKey, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.key_spend, k.tags, k.metadata, k.created_at, k.updated_at, k.expires_at, k.last_used_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		ORDER BY k.created_at ASC
	`, s.keysTable, s.internalUsersTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list all litellm keys: %w", err)
	}
	defer rows.Close()
	keys := make([]LiteLLMKey, 0)
	for rows.Next() {
		var (
			key      LiteLLMKey
			tagsJSON []byte
			metaJSON []byte
		)
		if err = rows.Scan(&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
			&key.UserID, &key.UserAlias, &key.UserEmail,
			&key.Spend, &tagsJSON, &metaJSON, &key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan litellm key row: %w", err)
		}
		if len(tagsJSON) > 0 {
			_ = json.Unmarshal(tagsJSON, &key.Tags)
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &key.Metadata)
		}
		if key.Metadata == nil {
			key.Metadata = map[string]any{}
		}
		keys = append(keys, key)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate litellm keys: %w", err)
	}
	return keys, nil
}

// LookupByHash returns the Manage-LiteLLM key matching the supplied SHA-256
// secret hash. It is the litellm_api_keys analogue of APIKeyStore.LookupByHash
// and is used to attribute external spend-log rows to a key (and its alias)
// even when the runtime api_keys table has not been migrated yet. ErrLiteLLMKeyNotFound
// is returned when no row matches.
func (s *LiteLLMKeyStore) LookupByHash(ctx context.Context, hash string) (*LiteLLMKey, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.key_spend, k.tags, k.metadata, k.created_at, k.updated_at, k.expires_at, k.last_used_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		WHERE k.key_hash = $1
	`, s.keysTable, s.internalUsersTable), hash)
	var (
		key      LiteLLMKey
		tagsJSON []byte
		metaJSON []byte
	)
	if err := row.Scan(&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
		&key.UserID, &key.UserAlias, &key.UserEmail,
		&key.Spend, &tagsJSON, &metaJSON, &key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrLiteLLMKeyNotFound
		}
		return nil, err
	}
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &key.Tags)
	}
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &key.Metadata)
	}
	if key.Metadata == nil {
		key.Metadata = map[string]any{}
	}
	return &key, nil
}

// ListAllPolicies returns every Manage-LiteLLM key policy keyed by api_key_id.
// Used by the "sync to NixLLM" key migration to avoid an N+1 policy lookup per
// key. Keys without a policy row are simply absent from the map.
func (s *LiteLLMKeyStore) ListAllPolicies(ctx context.Context) (map[string]*LiteLLMPolicy, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, rpm_limit, tpm_limit, budget_usd, COALESCE(budget_duration, ''),
		       max_parallel_requests, allowed_models, blocked_models, aliases,
		       allowed_ips, blocked_ips, model_routes, updated_at
		FROM %s
	`, s.policiesTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list all litellm key policies: %w", err)
	}
	defer rows.Close()
	return scanLiteLLMPolicyRows(rows)
}

// policiesForKeys loads the litellm_key_policies rows for the supplied key ids
// (used by ListPaged so a page of keys carries its policies without an N+1
// per-key lookup). Returns a map keyed by api_key_id.
func (s *LiteLLMKeyStore) policiesForKeys(ctx context.Context, ids []string) (map[string]*LiteLLMPolicy, error) {
	if len(ids) == 0 {
		return map[string]*LiteLLMPolicy{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, rpm_limit, tpm_limit, budget_usd, COALESCE(budget_duration, ''),
		       max_parallel_requests, allowed_models, blocked_models, aliases,
		       allowed_ips, blocked_ips, model_routes, updated_at
		FROM %s WHERE api_key_id IN (%s)
	`, s.policiesTable, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list litellm key policies for page: %w", err)
	}
	defer rows.Close()
	return scanLiteLLMPolicyRows(rows)
}

// scanLiteLLMPolicyRows decodes litellm_key_policies rows into a map keyed by
// api_key_id. Shared by ListAllPolicies and policiesForKeys.
func scanLiteLLMPolicyRows(rows *sql.Rows) (map[string]*LiteLLMPolicy, error) {
	out := make(map[string]*LiteLLMPolicy)
	for rows.Next() {
		var (
			keyID           string
			rpmLimit        sql.NullInt64
			tpmLimit        sql.NullInt64
			budgetUSD       sql.NullFloat64
			budgetDuration  string
			maxParallel     sql.NullInt64
			allowedModels   []byte
			blockedModels   []byte
			aliasesJSON     []byte
			allowedIPs      []byte
			blockedIPs      []byte
			modelRoutes     []byte
			policyUpdatedAt time.Time
		)
		if err := rows.Scan(&keyID, &rpmLimit, &tpmLimit, &budgetUSD, &budgetDuration,
			&maxParallel, &allowedModels, &blockedModels, &aliasesJSON,
			&allowedIPs, &blockedIPs, &modelRoutes, &policyUpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan litellm key policy row: %w", err)
		}
		p := LiteLLMPolicy{APIKeyID: keyID, UpdatedAt: policyUpdatedAt, BudgetDuration: budgetDuration}
		if rpmLimit.Valid {
			v := int(rpmLimit.Int64)
			p.RPMLimit = &v
		}
		if tpmLimit.Valid {
			v := int(tpmLimit.Int64)
			p.TPMLimit = &v
		}
		if budgetUSD.Valid {
			v := budgetUSD.Float64
			p.BudgetUSD = &v
		}
		if maxParallel.Valid {
			v := int(maxParallel.Int64)
			p.MaxParallelRequests = &v
		}
		p.AllowedModels = decodeStringArray(allowedModels)
		p.BlockedModels = decodeStringArray(blockedModels)
		if len(aliasesJSON) > 0 {
			_ = json.Unmarshal(aliasesJSON, &p.Aliases)
		}
		p.AllowedIPs = decodeStringArray(allowedIPs)
		p.BlockedIPs = decodeStringArray(blockedIPs)
		p.ModelRoutes = decodeModelRoutes(modelRoutes)
		out[keyID] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate litellm key policies: %w", err)
	}
	return out, nil
}

// UpdatePolicy creates or replaces the policy attached to a Manage-LiteLLM key.
func (s *LiteLLMKeyStore) UpdatePolicy(ctx context.Context, id string, policy LiteLLMPolicy) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	policy.APIKeyID = id
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin litellm policy update tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = upsertLiteLLMPolicyTx(ctx, tx, s.policiesTable, policy); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET updated_at = NOW() WHERE id = $1`, s.keysTable,
	), id); err != nil {
		return fmt.Errorf("postgres store: bump litellm key updated_at: %w", err)
	}
	return tx.Commit()
}

func upsertLiteLLMPolicyTx(ctx context.Context, tx *sql.Tx, policiesTable string, policy LiteLLMPolicy) error {
	allowed, _ := json.Marshal(normalizeStringSlice(policy.AllowedModels))
	blocked, _ := json.Marshal(normalizeStringSlice(policy.BlockedModels))
	aliases := policy.Aliases
	if aliases == nil {
		aliases = map[string]string{}
	}
	aliasesJSON, err := json.Marshal(aliases)
	if err != nil {
		return fmt.Errorf("postgres store: marshal litellm policy aliases: %w", err)
	}
	allowedIPs, _ := json.Marshal(normalizeStringSlice(policy.AllowedIPs))
	blockedIPs, _ := json.Marshal(normalizeStringSlice(policy.BlockedIPs))
	routes, _ := json.Marshal(normalizeModelRoutes(policy.ModelRoutes))
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			api_key_id, rpm_limit, tpm_limit, budget_usd, budget_duration, max_parallel_requests,
			allowed_models, blocked_models, aliases, allowed_ips, blocked_ips, model_routes, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb, NOW())
		ON CONFLICT (api_key_id) DO UPDATE SET
			rpm_limit = EXCLUDED.rpm_limit,
			tpm_limit = EXCLUDED.tpm_limit,
			budget_usd = EXCLUDED.budget_usd,
			budget_duration = EXCLUDED.budget_duration,
			max_parallel_requests = EXCLUDED.max_parallel_requests,
			allowed_models = EXCLUDED.allowed_models,
			blocked_models = EXCLUDED.blocked_models,
			aliases = EXCLUDED.aliases,
			allowed_ips = EXCLUDED.allowed_ips,
			blocked_ips = EXCLUDED.blocked_ips,
			model_routes = EXCLUDED.model_routes,
			updated_at = NOW()
	`, policiesTable),
		policy.APIKeyID, policy.RPMLimit, policy.TPMLimit, policy.BudgetUSD,
		nullableString(policy.BudgetDuration), policy.MaxParallelRequests,
		string(allowed), string(blocked), string(aliasesJSON), string(allowedIPs), string(blockedIPs), string(routes),
	); err != nil {
		return fmt.Errorf("postgres store: upsert litellm policy: %w", err)
	}
	return nil
}

// UpdateStatus changes the lifecycle status of a Manage-LiteLLM key.
func (s *LiteLLMKeyStore) UpdateStatus(ctx context.Context, id, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET status = $1, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), status, id)
	if err != nil {
		return fmt.Errorf("postgres store: update litellm key status: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// UpdateMetadata replaces the metadata map of a Manage-LiteLLM key.
func (s *LiteLLMKeyStore) UpdateMetadata(ctx context.Context, id string, metadata map[string]any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	meta := metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("postgres store: marshal litellm key metadata: %w", err)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), string(metaJSON), id)
	if err != nil {
		return fmt.Errorf("postgres store: update litellm key metadata: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// UpdateTags replaces the tags list of a Manage-LiteLLM key.
func (s *LiteLLMKeyStore) UpdateTags(ctx context.Context, id string, tags []string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	tagsJSON, err := json.Marshal(normalizeStringSlice(tags))
	if err != nil {
		return fmt.Errorf("postgres store: marshal litellm key tags: %w", err)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET tags = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), string(tagsJSON), id)
	if err != nil {
		return fmt.Errorf("postgres store: update litellm key tags: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// UpdateExpiry sets (or clears, when expiresAt is nil) the expiry timestamp of
// a Manage-LiteLLM key.
func (s *LiteLLMKeyStore) UpdateExpiry(ctx context.Context, id string, expiresAt *time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET expires_at = $1, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), expiresAt, id)
	if err != nil {
		return fmt.Errorf("postgres store: update litellm key expiry: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// Rename changes the human-readable label of a Manage-LiteLLM key.
func (s *LiteLLMKeyStore) Rename(ctx context.Context, id, name string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET name = $1, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), trimOr(name, "unnamed"), id)
	if err != nil {
		return fmt.Errorf("postgres store: rename litellm key: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// UpdateAlias changes the non-secret key_alias label of a Manage-LiteLLM key.
// Pass an empty string to clear the alias.
func (s *LiteLLMKeyStore) UpdateAlias(ctx context.Context, id, alias string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET key_alias = $1, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), nullableString(alias), id)
	if err != nil {
		return fmt.Errorf("postgres store: update litellm key alias: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// UpdateUserID attaches (or detaches, when userID is empty) a Manage-LiteLLM
// key to a Manage-LiteLLM user.
func (s *LiteLLMKeyStore) UpdateUserID(ctx context.Context, id, userID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET user_id = $1, updated_at = NOW() WHERE id = $2`, s.keysTable,
	), nullableString(userID), id)
	if err != nil {
		return fmt.Errorf("postgres store: update litellm key user_id: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// Regenerate issues a new secret for the given Manage-LiteLLM key. The key ID,
// policy, metadata, and lifecycle status are preserved; only the hash changes.
// When secret is empty a fresh secret is auto-generated; when non-empty it is
// validated with validateSecret and used verbatim.
func (s *LiteLLMKeyStore) Regenerate(ctx context.Context, id, secret string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: litellm key store not initialized")
	}
	if secret == "" {
		var err error
		secret, err = GenerateSecret()
		if err != nil {
			return "", err
		}
	} else if err := validateSecret(secret); err != nil {
		return "", err
	}
	hash := HashSecret(secret)
	prefix := prefixOf(secret)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET key_hash = $1, key_prefix = $2, updated_at = NOW() WHERE id = $3`,
		s.keysTable,
	), hash, prefix, id)
	if err != nil {
		return "", fmt.Errorf("postgres store: regenerate litellm key: %w", err)
	}
	if err = assertLiteLLMRowsAffected(res, id, "litellm key"); err != nil {
		return "", err
	}
	return secret, nil
}

// Delete permanently removes a Manage-LiteLLM key and its policy (cascade).
func (s *LiteLLMKeyStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, s.keysTable,
	), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete litellm key: %w", err)
	}
	return assertLiteLLMRowsAffected(res, id, "litellm key")
}

// CountActive returns the number of Manage-LiteLLM keys currently in 'active'
// status.
func (s *LiteLLMKeyStore) CountActive(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: litellm key store not initialized")
	}
	var count int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE status = $1`, s.keysTable,
	), LiteLLMKeyStatusActive).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("postgres store: count active litellm keys: %w", err)
	}
	return count, nil
}

// assertLiteLLMRowsAffected returns ErrLiteLLMKeyNotFound when 0 rows were
// touched.
func assertLiteLLMRowsAffected(res sql.Result, id, label string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: %s rows affected: %w", label, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrLiteLLMKeyNotFound, id)
	}
	return nil
}
