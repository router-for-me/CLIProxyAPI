package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// Management token lifecycle statuses. Mirrors the APIKey statuses so the
// dashboard and policy layer can reuse the same vocabulary.
const (
	MgmtTokenStatusActive  = "active"
	MgmtTokenStatusRevoked = "revoked"
	MgmtTokenStatusExpired = "expired"
)

// MgmtTokenScope gates which HTTP verbs a token may use.
//
//   - "read"  → GET only
//   - "write" → all verbs (GET + mutations)
const (
	MgmtTokenScopeRead  = "read"
	MgmtTokenScopeWrite = "write"
)

// MgmtSecretPrefix marks generated management-token secrets so they can be
// distinguished from client-facing API key secrets (sk-). The prefix itself
// does not authorize anything: the SHA-256 hash is the only persisted form and
// the plaintext is returned exactly once at creation/regeneration time.
const MgmtSecretPrefix = "sk-mgmt-"

// MgmtTokenPrefixLen mirrors APIKeyPrefixLen: the number of body characters
// retained for display. Insufficient to reconstruct the secret.
const MgmtTokenPrefixLen = 8

// ErrManagementTokenNotFound is returned when no management token matches the
// supplied identifier.
var ErrManagementTokenNotFound = errors.New("postgres store: management token not found")

// ManagementToken mirrors a row in the management_tokens table. It gates
// access to the /v0/management REST API surface. The plaintext secret is never
// persisted: only KeyHash (SHA-256) is stored. KeyPrefix exposes the first
// characters of the secret for display in management UIs.
type ManagementToken struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	KeyHash    string         `json:"-"`
	KeyPrefix  string         `json:"key_prefix"`
	Status     string         `json:"status"`
	Scope      string         `json:"scope"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	// DefaultUserID is the Internal User id used as the owner when a request to
	// one of DefaultUserIDEndpoints omits user_id. Only meaningful for
	// write-scope tokens; read-scope tokens must leave it empty.
	DefaultUserID string `json:"default_user_id,omitempty"`
	// DefaultUserIDEndpoints is the opt-in allow-list of management paths that
	// may use DefaultUserID. An empty list disables the fallback entirely.
	// Entries are absolute paths ("/v0/management/api-keys-pg"), optionally
	// ending in "*" for a prefix match.
	DefaultUserIDEndpoints []string `json:"default_user_id_endpoints,omitempty"`
}

// ManagementTokenPolicy captures the limits enforced on a management token.
// Pointer-typed scalar fields distinguish "unset / unlimited" (nil) from
// explicit zero values, mirroring store.Policy. AllowedEndpoints /
// BlockedEndpoints hold glob patterns like "GET /usage-stats/*" matched
// against "<METHOD> <path>".
type ManagementTokenPolicy struct {
	TokenID             string   `json:"token_id"`
	RPMLimit            *int     `json:"rpm_limit,omitempty"`
	MaxParallelRequests *int     `json:"max_parallel_requests,omitempty"`
	HourlyRateLimit     *int     `json:"hourly_rate_limit,omitempty"`
	AllowedEndpoints    []string `json:"allowed_endpoints,omitempty"`
	BlockedEndpoints    []string `json:"blocked_endpoints,omitempty"`
	// AllowedIPs / BlockedIPs restrict which source IP addresses may use the
	// token. Entries are single IPs ("10.0.0.5") or CIDR ranges
	// ("10.0.0.0/8", "2001:db8::/32"). BlockedIPs takes precedence: a match
	// denies the request even when AllowedIPs would also match. When
	// AllowedIPs is non-empty, the client IP must match at least one entry;
	// an empty AllowedIPs means "all IPs allowed" (subject to BlockedIPs).
	AllowedIPs []string  `json:"allowed_ips,omitempty"`
	BlockedIPs []string  `json:"blocked_ips,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ManagementAuditLog mirrors a row in the management_audit_log table. It
// records every management API call made with a management token: the request
// audit (who/what/when/latency), the request/response bodies for mutations,
// and the error message (if any) for 4xx/5xx responses. Bodies are sealed at
// rest via the Sealer when PGSTORE_ENCRYPTION_KEY is configured; otherwise
// they are persisted in plaintext (legacy-readable, forward-encrypting).
type ManagementAuditLog struct {
	ID           int64     `json:"id"`
	TokenID      string    `json:"token_id,omitempty"`
	TokenName    string    `json:"token_name,omitempty"`
	ActorIP      string    `json:"actor_ip,omitempty"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	StatusCode   int       `json:"status_code"`
	LatencyMs    int64     `json:"latency_ms"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	IsError      bool      `json:"is_error"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// ManagementTokenStore provides CRUD operations for management API tokens,
// their policies, and the audit log of calls made with them. It is backed by
// the same *sql.DB connection as PostgresStore.
type ManagementTokenStore struct {
	db            *sql.DB
	tokensTable   string
	policiesTable string
	auditTable    string
	sealer        *Sealer
}

// NewManagementTokenStore builds a ManagementTokenStore that reuses the
// PostgresStore connection and table names. Returns nil if the parent store
// is nil so callers can feature-detect the absence of the PG backend with a
// nil check (mirrors NewAPIKeyStore).
func NewManagementTokenStore(parent *PostgresStore) *ManagementTokenStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.WithError(err).Warn("postgres store: management audit log encryption disabled due to key error")
		sealer = nil
	}
	return &ManagementTokenStore{
		db:            parent.DB(),
		tokensTable:   parent.ManagementTokensTable(),
		policiesTable: parent.ManagementTokenPoliciesTable(),
		auditTable:    parent.ManagementAuditLogTable(),
		sealer:        sealer,
	}
}

// SetSealer overrides the in-memory sealer. Used by tests that construct a
// ManagementTokenStore directly without going through NewPostgresStore.
func (s *ManagementTokenStore) SetSealer(sealer *Sealer) {
	if s == nil {
		return
	}
	s.sealer = sealer
}

// Sealer exposes the configured sealer (may be nil when encryption is
// disabled). Reused by the audit middleware to seal bodies before persisting.
func (s *ManagementTokenStore) Sealer() *Sealer {
	if s == nil {
		return nil
	}
	return s.sealer
}

// generateMgmtSecret produces a new opaque plaintext management-token secret.
// The caller is the only party that ever sees the plaintext: the hashed form
// is what gets persisted (see HashSecret, shared with API keys).
func generateMgmtSecret() (string, error) {
	body, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}
	return MgmtSecretPrefix + body, nil
}

// generateRandomHex returns n cryptographically-secure random bytes hex-encoded.
func generateRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("postgres store: generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// mgmtPrefixOf returns the displayable prefix of a management secret (the
// first MgmtTokenPrefixLen characters of the body, without the prefix marker).
func mgmtPrefixOf(secret string) string {
	body := secret
	if len(body) > len(MgmtSecretPrefix) && body[:len(MgmtSecretPrefix)] == MgmtSecretPrefix {
		body = body[len(MgmtSecretPrefix):]
	}
	if len(body) > MgmtTokenPrefixLen {
		body = body[:MgmtTokenPrefixLen]
	}
	return body
}

// normalizeMgmtPatterns trims each entry and drops blanks so persisted
// default-user endpoint allow-lists never carry whitespace-only patterns. It
// always returns a non-nil slice so the JSONB column stores [] rather than NULL.
func normalizeMgmtPatterns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Create inserts a new management token and (optionally) its policy in a single
// transaction. It returns the freshly generated plaintext secret; the caller is
// responsible for surfacing it to the user exactly once as the secret is never
// recoverable from the database. Scope defaults to "read" when empty.
func (s *ManagementTokenStore) Create(ctx context.Context, name, scope, defaultUserID string, defaultUserIDEndpoints []string, expiresAt *time.Time, metadata map[string]any, policy *ManagementTokenPolicy) (*ManagementToken, string, error) {
	if s == nil || s.db == nil {
		return nil, "", fmt.Errorf("postgres store: management token store not initialized")
	}
	id := uuid.NewString()
	secret, err := generateMgmtSecret()
	if err != nil {
		return nil, "", err
	}
	hash := HashSecret(secret)
	prefix := mgmtPrefixOf(secret)
	displayName := trimOr(name, "unnamed")
	if scope == "" {
		scope = MgmtTokenScopeRead
	}
	defaultUserID = strings.TrimSpace(defaultUserID)
	endpoints := normalizeMgmtPatterns(defaultUserIDEndpoints)
	endpointsJSON, err := json.Marshal(endpoints)
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: marshal management token default user endpoints: %w", err)
	}
	meta := metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: marshal management token metadata: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: begin management token create tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, key_hash, key_prefix, status, scope,
		                default_user_id, default_user_id_endpoints, expires_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10::jsonb)
	`, s.tokensTable), id, displayName, hash, prefix, MgmtTokenStatusActive, scope,
		defaultUserID, string(endpointsJSON), expiresAt, string(metaJSON)); err != nil {
		return nil, "", fmt.Errorf("postgres store: insert management token: %w", err)
	}

	if policy != nil {
		policy.TokenID = id
		if err = upsertMgmtPolicyTx(ctx, tx, s.policiesTable, *policy); err != nil {
			return nil, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("postgres store: commit management token: %w", err)
	}

	created, _, err := s.LookupByID(ctx, id)
	if err != nil {
		// Best-effort reconstruction when the row is not yet visible.
		created = &ManagementToken{
			ID: id, Name: displayName, KeyHash: hash, KeyPrefix: prefix,
			Status: MgmtTokenStatusActive, Scope: scope, ExpiresAt: expiresAt, Metadata: meta,
			DefaultUserID: defaultUserID, DefaultUserIDEndpoints: endpoints,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	return created, secret, nil
}

// LookupByHash returns the management token and its policy matching the supplied
// SHA-256 secret hash. ErrManagementTokenNotFound is returned when no row matches.
func (s *ManagementTokenStore) LookupByHash(ctx context.Context, hash string) (*ManagementToken, *ManagementTokenPolicy, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("postgres store: management token store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT t.id, t.name, t.key_hash, t.key_prefix, t.status, t.scope,
		       t.default_user_id, t.default_user_id_endpoints,
		       t.created_at, t.updated_at, t.expires_at, t.last_used_at, t.metadata,
		       p.rpm_limit, p.max_parallel_requests, p.hourly_rate_limit,
		       p.allowed_endpoints, p.blocked_endpoints,
		       p.allowed_ips, p.blocked_ips,
		       p.updated_at
		FROM %s t
		LEFT JOIN %s p ON p.token_id = t.id
		WHERE t.key_hash = $1
	`, s.tokensTable, s.policiesTable), hash)
	token, policy, err := scanManagementTokenRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrManagementTokenNotFound
		}
		return nil, nil, err
	}
	return token, policy, nil
}

// LookupByID returns the management token (and its policy, if any) by its ID.
func (s *ManagementTokenStore) LookupByID(ctx context.Context, id string) (*ManagementToken, *ManagementTokenPolicy, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("postgres store: management token store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT t.id, t.name, t.key_hash, t.key_prefix, t.status, t.scope,
		       t.default_user_id, t.default_user_id_endpoints,
		       t.created_at, t.updated_at, t.expires_at, t.last_used_at, t.metadata,
		       p.rpm_limit, p.max_parallel_requests, p.hourly_rate_limit,
		       p.allowed_endpoints, p.blocked_endpoints,
		       p.allowed_ips, p.blocked_ips,
		       p.updated_at
		FROM %s t
		LEFT JOIN %s p ON p.token_id = t.id
		WHERE t.id = $1
	`, s.tokensTable, s.policiesTable), id)
	token, policy, err := scanManagementTokenRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrManagementTokenNotFound
		}
		return nil, nil, err
	}
	return token, policy, nil
}

func scanManagementTokenRow(row *sql.Row) (*ManagementToken, *ManagementTokenPolicy, error) {
	var (
		token            ManagementToken
		metadata         []byte
		defaultEndpoints []byte
		rpmLimit         sql.NullInt64
		maxParallel      sql.NullInt64
		hourlyRateLimit  sql.NullInt64
		allowedEndpoints []byte
		blockedEndpoints []byte
		allowedIPs       []byte
		blockedIPs       []byte
		policyUpdatedAt  sql.NullTime
	)
	if err := row.Scan(
		&token.ID, &token.Name, &token.KeyHash, &token.KeyPrefix, &token.Status, &token.Scope,
		&token.DefaultUserID, &defaultEndpoints,
		&token.CreatedAt, &token.UpdatedAt, &token.ExpiresAt, &token.LastUsedAt, &metadata,
		&rpmLimit, &maxParallel, &hourlyRateLimit,
		&allowedEndpoints, &blockedEndpoints,
		&allowedIPs, &blockedIPs,
		&policyUpdatedAt,
	); err != nil {
		return nil, nil, err
	}
	token.DefaultUserIDEndpoints = decodeStringArray(defaultEndpoints)
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &token.Metadata)
	}
	if token.Metadata == nil {
		token.Metadata = map[string]any{}
	}

	var policy *ManagementTokenPolicy
	if policyUpdatedAt.Valid {
		p := ManagementTokenPolicy{TokenID: token.ID, UpdatedAt: policyUpdatedAt.Time}
		if rpmLimit.Valid {
			v := int(rpmLimit.Int64)
			p.RPMLimit = &v
		}
		if maxParallel.Valid {
			v := int(maxParallel.Int64)
			p.MaxParallelRequests = &v
		}
		if hourlyRateLimit.Valid {
			v := int(hourlyRateLimit.Int64)
			p.HourlyRateLimit = &v
		}
		p.AllowedEndpoints = decodeStringArray(allowedEndpoints)
		p.BlockedEndpoints = decodeStringArray(blockedEndpoints)
		p.AllowedIPs = decodeStringArray(allowedIPs)
		p.BlockedIPs = decodeStringArray(blockedIPs)
		policy = &p
	}
	return &token, policy, nil
}

// ListPaged returns a page of management tokens plus the total row count (for
// pager UI math). Page is 1-indexed; pageSize must be > 0. Empty filter
// values are ignored. Search is a case-insensitive substring match on name.
// SortBy is one of "created_at" (default) | "name" | "last_used_at";
// SortOrder is "desc" (default) | "asc".
func (s *ManagementTokenStore) ListPaged(ctx context.Context, page, pageSize int, status, scope, search, sortBy, sortOrder string) ([]*ManagementToken, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: management token store not initialized")
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
	addFilter := func(clause string, val string) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if status != "" {
		addFilter("t.status = $%d", status)
	}
	if scope != "" {
		addFilter("t.scope = $%d", scope)
	}
	if search != "" {
		addFilter("LOWER(t.name) LIKE LOWER($%d)", "%"+search+"%")
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s t%s`, s.tokensTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count management tokens (paged): %w", err)
	}

	// Sort: whitelist columns to avoid SQL injection from user input.
	sortCol := "t.created_at"
	switch strings.ToLower(sortBy) {
	case "name":
		sortCol = "t.name"
	case "last_used_at":
		sortCol = "t.last_used_at"
	case "created_at", "":
		sortCol = "t.created_at"
	}
	sortDir := "DESC"
	if strings.EqualFold(sortOrder, "asc") {
		sortDir = "ASC"
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT t.id, t.name, t.key_hash, t.key_prefix, t.status, t.scope,
		       t.created_at, t.updated_at, t.expires_at, t.last_used_at, t.metadata
		FROM %s t%s
		ORDER BY %s %s NULLS LAST
		LIMIT $%d OFFSET $%d
	`, s.tokensTable, whereClause, sortCol, sortDir, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list management tokens (paged): %w", err)
	}
	defer rows.Close()

	tokens := make([]*ManagementToken, 0, pageSize)
	for rows.Next() {
		var (
			token    ManagementToken
			metadata []byte
		)
		if err = rows.Scan(&token.ID, &token.Name, &token.KeyHash, &token.KeyPrefix, &token.Status, &token.Scope,
			&token.CreatedAt, &token.UpdatedAt, &token.ExpiresAt, &token.LastUsedAt, &metadata); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan management token row (paged): %w", err)
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &token.Metadata)
		}
		if token.Metadata == nil {
			token.Metadata = map[string]any{}
		}
		tokens = append(tokens, &token)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate management tokens (paged): %w", err)
	}
	return tokens, total, nil
}

// UpdatePolicy creates or replaces the policy attached to token_id.
func (s *ManagementTokenStore) UpdatePolicy(ctx context.Context, id string, policy ManagementTokenPolicy) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	policy.TokenID = id
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin update management policy tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = upsertMgmtPolicyTx(ctx, tx, s.policiesTable, policy); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET updated_at = NOW() WHERE id = $1`, s.tokensTable,
	), id); err != nil {
		return fmt.Errorf("postgres store: bump management token updated_at: %w", err)
	}
	return tx.Commit()
}

func upsertMgmtPolicyTx(ctx context.Context, tx *sql.Tx, policiesTable string, policy ManagementTokenPolicy) error {
	allowed, _ := json.Marshal(normalizeStringSlice(policy.AllowedEndpoints))
	blocked, _ := json.Marshal(normalizeStringSlice(policy.BlockedEndpoints))
	allowedIPs, _ := json.Marshal(normalizeStringSlice(policy.AllowedIPs))
	blockedIPs, _ := json.Marshal(normalizeStringSlice(policy.BlockedIPs))
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			token_id, rpm_limit, max_parallel_requests, hourly_rate_limit,
			allowed_endpoints, blocked_endpoints,
			allowed_ips, blocked_ips,
			updated_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, NOW())
		ON CONFLICT (token_id) DO UPDATE SET
			rpm_limit = EXCLUDED.rpm_limit,
			max_parallel_requests = EXCLUDED.max_parallel_requests,
			hourly_rate_limit = EXCLUDED.hourly_rate_limit,
			allowed_endpoints = EXCLUDED.allowed_endpoints,
			blocked_endpoints = EXCLUDED.blocked_endpoints,
			allowed_ips = EXCLUDED.allowed_ips,
			blocked_ips = EXCLUDED.blocked_ips,
			updated_at = NOW()
	`, policiesTable),
		policy.TokenID, policy.RPMLimit, policy.MaxParallelRequests, policy.HourlyRateLimit,
		string(allowed), string(blocked),
		string(allowedIPs), string(blockedIPs),
	); err != nil {
		return fmt.Errorf("postgres store: upsert management token policy: %w", err)
	}
	return nil
}

// UpdateStatus changes the lifecycle status of a management token.
func (s *ManagementTokenStore) UpdateStatus(ctx context.Context, id, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET status = $1, updated_at = NOW() WHERE id = $2`, s.tokensTable,
	), status, id)
	if err != nil {
		return fmt.Errorf("postgres store: update management token status: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// UpdateScope changes the read/write scope of a management token.
func (s *ManagementTokenStore) UpdateScope(ctx context.Context, id, scope string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET scope = $1, updated_at = NOW() WHERE id = $2`, s.tokensTable,
	), scope, id)
	if err != nil {
		return fmt.Errorf("postgres store: update management token scope: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// UpdateDefaultUserID replaces the token's default-user fallback configuration:
// the owner id used when an allowed endpoint omits user_id and the allow-list of
// endpoints it applies to. Passing an empty user id and nil endpoints disables
// the fallback. The token must already have write scope (enforced by the
// caller); this method only persists the values.
func (s *ManagementTokenStore) UpdateDefaultUserID(ctx context.Context, id, defaultUserID string, endpoints []string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	endpointsJSON, err := json.Marshal(normalizeMgmtPatterns(endpoints))
	if err != nil {
		return fmt.Errorf("postgres store: marshal management token default user endpoints: %w", err)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET default_user_id = $1, default_user_id_endpoints = $2::jsonb, updated_at = NOW() WHERE id = $3`,
		s.tokensTable,
	), strings.TrimSpace(defaultUserID), string(endpointsJSON), id)
	if err != nil {
		return fmt.Errorf("postgres store: update management token default user id: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// UpdateExpiry sets (or clears, when expiresAt is nil) the expiry timestamp.
func (s *ManagementTokenStore) UpdateExpiry(ctx context.Context, id string, expiresAt *time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET expires_at = $1, updated_at = NOW() WHERE id = $2`, s.tokensTable,
	), expiresAt, id)
	if err != nil {
		return fmt.Errorf("postgres store: update management token expiry: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// Rename changes the human-readable label of a management token.
func (s *ManagementTokenStore) Rename(ctx context.Context, id, name string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET name = $1, updated_at = NOW() WHERE id = $2`, s.tokensTable,
	), trimOr(name, "unnamed"), id)
	if err != nil {
		return fmt.Errorf("postgres store: rename management token: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// UpdateMetadata replaces metadata for a management token.
func (s *ManagementTokenStore) UpdateMetadata(ctx context.Context, id string, metadata map[string]any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	meta := metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("postgres store: marshal management token metadata: %w", err)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.tokensTable,
	), string(metaJSON), id)
	if err != nil {
		return fmt.Errorf("postgres store: update management token metadata: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// Regenerate issues a new secret for the given token ID. The token ID, policy,
// scope, metadata, and lifecycle status are preserved; only the hash changes.
func (s *ManagementTokenStore) Regenerate(ctx context.Context, id string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: management token store not initialized")
	}
	secret, err := generateMgmtSecret()
	if err != nil {
		return "", err
	}
	hash := HashSecret(secret)
	prefix := mgmtPrefixOf(secret)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET key_hash = $1, key_prefix = $2, updated_at = NOW() WHERE id = $3`,
		s.tokensTable,
	), hash, prefix, id)
	if err != nil {
		return "", fmt.Errorf("postgres store: regenerate management token: %w", err)
	}
	if err = assertMgmtRowsAffected(res, id); err != nil {
		return "", err
	}
	return secret, nil
}

// Delete permanently removes the management token and its policy (cascade).
func (s *ManagementTokenStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management token store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, s.tokensTable,
	), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete management token: %w", err)
	}
	return assertMgmtRowsAffected(res, id)
}

// TouchLastUsed records the current time as the last_used_at timestamp. It is
// invoked from the request hot-path and is best-effort; errors are logged but
// not surfaced so callers can keep operating.
func (s *ManagementTokenStore) TouchLastUsed(ctx context.Context, id string) {
	if s == nil || s.db == nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET last_used_at = NOW() WHERE id = $1`, s.tokensTable,
	), id); err != nil {
		log.WithError(err).Debug("postgres store: touch management token last_used_at failed")
	}
}

// InsertAudit persists a single audit-log row. RequestBody and ResponseBody
// are sealed at rest via the Sealer when PGSTORE_ENCRYPTION_KEY is configured.
// Bodies are truncated to maxAuditBodyBytes before sealing to bound row size.
// The call is fire-and-forget from the audit middleware; errors are surfaced
// to the caller so the middleware can log them without blocking the response.
func (s *ManagementTokenStore) InsertAudit(ctx context.Context, e ManagementAuditLog) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management audit store not initialized")
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	reqBody := truncateAuditBody(e.RequestBody)
	resBody := truncateAuditBody(e.ResponseBody)
	if s.sealer != nil && s.sealer.Enabled() {
		var err error
		if reqBody, err = s.sealer.Seal(reqBody); err != nil {
			log.WithError(err).Debug("postgres store: seal audit request body failed; storing plaintext")
		}
		if resBody, err = s.sealer.Seal(resBody); err != nil {
			log.WithError(err).Debug("postgres store: seal audit response body failed; storing plaintext")
		}
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (token_id, actor_ip, method, path, status_code, latency_ms,
		               request_body, response_body, error_message, is_error, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, s.auditTable),
		nullableString(e.TokenID), nullableString(e.ActorIP), e.Method, e.Path,
		e.StatusCode, e.LatencyMs,
		nullableString(reqBody), nullableString(resBody),
		nullableString(e.ErrorMessage), e.IsError, e.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert management audit log: %w", err)
	}
	return nil
}

// maxAuditBodyBytes caps the size of request/response bodies persisted to the
// audit log so a single oversized payload cannot bloat the table unbounded.
const maxAuditBodyBytes = 64 * 1024

func truncateAuditBody(body string) string {
	if len(body) > maxAuditBodyBytes {
		return body[:maxAuditBodyBytes] + "…[truncated]"
	}
	return body
}

// MgmtAuditFilter captures the optional filter dimensions accepted by
// ListAuditPaged. Zero values (and empty strings) are ignored.
type MgmtAuditFilter struct {
	TokenID string
	Method  string
	Path    string
	IsError *bool
	From    time.Time
	To      time.Time
}

// ListAuditPaged returns a page of audit-log entries plus the total row count.
// RequestBody and ResponseBody are unsealed (decrypted) when a Sealer is
// configured; legacy plaintext rows pass through unchanged.
func (s *ManagementTokenStore) ListAuditPaged(ctx context.Context, f MgmtAuditFilter, page, pageSize int) ([]*ManagementAuditLog, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: management audit store not initialized")
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
	if f.TokenID != "" {
		addFilter("a.token_id = $%d", f.TokenID)
	}
	if f.Method != "" {
		addFilter("a.method = $%d", f.Method)
	}
	if f.Path != "" {
		addFilter("a.path LIKE $%d", "%"+f.Path+"%")
	}
	if f.IsError != nil {
		addFilter("a.is_error = $%d", *f.IsError)
	}
	if !f.From.IsZero() {
		addFilter("a.occurred_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		addFilter("a.occurred_at <= $%d", f.To)
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s a%s`, s.auditTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count management audit log (paged): %w", err)
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT a.id, a.token_id, COALESCE(t.name, ''), a.actor_ip, a.method, a.path,
		       a.status_code, a.latency_ms, a.request_body, a.response_body,
		       a.error_message, a.is_error, a.occurred_at
		FROM %s a
		LEFT JOIN %s t ON t.id = a.token_id%s
		ORDER BY a.occurred_at DESC
		LIMIT $%d OFFSET $%d
	`, s.auditTable, s.tokensTable, whereClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list management audit log (paged): %w", err)
	}
	defer rows.Close()

	entries := make([]*ManagementAuditLog, 0, pageSize)
	for rows.Next() {
		var (
			e            ManagementAuditLog
			reqBody      sql.NullString
			resBody      sql.NullString
			errorMessage sql.NullString
			actorIP      sql.NullString
			tokenID      sql.NullString
		)
		if err = rows.Scan(&e.ID, &tokenID, &e.TokenName, &actorIP, &e.Method, &e.Path,
			&e.StatusCode, &e.LatencyMs, &reqBody, &resBody,
			&errorMessage, &e.IsError, &e.OccurredAt); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan management audit row (paged): %w", err)
		}
		e.TokenID = tokenID.String
		e.ActorIP = actorIP.String
		e.RequestBody = unsealAuditBody(s.sealer, reqBody.String)
		e.ResponseBody = unsealAuditBody(s.sealer, resBody.String)
		e.ErrorMessage = errorMessage.String
		entries = append(entries, &e)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate management audit log (paged): %w", err)
	}
	return entries, total, nil
}

// GetAudit returns a single audit-log entry by ID. Bodies are unsealed.
func (s *ManagementTokenStore) GetAudit(ctx context.Context, id int64) (*ManagementAuditLog, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: management audit store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT a.id, a.token_id, COALESCE(t.name, ''), a.actor_ip, a.method, a.path,
		       a.status_code, a.latency_ms, a.request_body, a.response_body,
		       a.error_message, a.is_error, a.occurred_at
		FROM %s a
		LEFT JOIN %s t ON t.id = a.token_id
		WHERE a.id = $1
	`, s.auditTable, s.tokensTable), id)
	var (
		e            ManagementAuditLog
		reqBody      sql.NullString
		resBody      sql.NullString
		errorMessage sql.NullString
		actorIP      sql.NullString
		tokenID      sql.NullString
	)
	if err := row.Scan(&e.ID, &tokenID, &e.TokenName, &actorIP, &e.Method, &e.Path,
		&e.StatusCode, &e.LatencyMs, &reqBody, &resBody,
		&errorMessage, &e.IsError, &e.OccurredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrManagementAuditLogNotFound
		}
		return nil, fmt.Errorf("postgres store: get management audit log: %w", err)
	}
	e.TokenID = tokenID.String
	e.ActorIP = actorIP.String
	e.RequestBody = unsealAuditBody(s.sealer, reqBody.String)
	e.ResponseBody = unsealAuditBody(s.sealer, resBody.String)
	e.ErrorMessage = errorMessage.String
	return &e, nil
}

// ErrManagementAuditLogNotFound is returned when no audit-log row matches the
// supplied identifier.
var ErrManagementAuditLogNotFound = errors.New("postgres store: management audit log not found")

// unsealAuditBody decrypts a sealed body via the Sealer. Tolerates legacy
// plaintext payloads (those lacking the "v1:" prefix) by returning them
// unchanged so deployments adopting a passphrase after the fact don't break
// reads of pre-existing rows.
func unsealAuditBody(sealer *Sealer, payload string) string {
	if payload == "" || sealer == nil {
		return payload
	}
	plain, err := sealer.Open(payload)
	if err != nil {
		// Leave the (likely legacy) payload intact rather than dropping it.
		return payload
	}
	return plain
}

func assertMgmtRowsAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: management token rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrManagementTokenNotFound, id)
	}
	return nil
}
