package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

// API key lifecycle statuses.
const (
	APIKeyStatusActive   = "active"
	APIKeyStatusDisabled = "disabled"
	APIKeyStatusRevoked  = "revoked"
	APIKeyStatusExpired  = "expired"
)

// APIKeyPrefixLen is the number of characters retained (after the optional sk-
// style suffix is stripped) for display purposes. Never enough to reconstruct
// the secret.
const APIKeyPrefixLen = 8

// SecretPrefix is the human-readable prefix attached to every generated secret
// so callers can identify these keys as proxy-managed.
const SecretPrefix = "sk-"

// ErrAPIKeyNotFound is returned when no API key matches the supplied identifier.
var ErrAPIKeyNotFound = errors.New("postgres store: api key not found")

// ErrInvalidSecret is returned when a caller-supplied secret fails validation
// (e.g. too short). It lets HTTP handlers map secret-validation failures to
// 400 responses rather than the default 500.
var ErrInvalidSecret = errors.New("postgres store: invalid api key secret")

// ErrAmbiguousAlias is returned by LookupByAlias when more than one API key
// shares the same key_alias (case-insensitive). The caller must not guess which
// row was meant; the import-by-alias flow reports the row as "ambiguous".
var ErrAmbiguousAlias = errors.New("postgres store: api key alias is ambiguous")

// APIKey mirrors a row in the api_keys table. The plaintext secret is never
// persisted: only KeyHash (SHA-256) is stored. KeyPrefix exposes the first
// characters of the secret for display in management UIs. KeyAlias is an
// operator-supplied, non-secret human label used for filtering/display in
// usage stats (the sealed api_key_principal column cannot be queried for
// filtering).
type APIKey struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	KeyAlias   string         `json:"key_alias,omitempty"`
	KeyHash    string         `json:"-"`
	KeyPrefix  string         `json:"key_prefix"`
	Status     string         `json:"status"`
	UserID     string         `json:"user_id,omitempty"`
	UserAlias  string         `json:"user_alias,omitempty"`
	UserEmail  string         `json:"user_email,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// Policy captures the limits enforced on an API key. Pointer-typed scalar
// fields distinguish "unset / unlimited" (nil) from explicit zero values.
type Policy struct {
	APIKeyID            string   `json:"api_key_id"`
	RPMLimit            *int     `json:"rpm_limit,omitempty"`
	HourlyRateLimit     *int     `json:"hourly_rate_limit,omitempty"`
	BudgetHourlyUSD     *float64 `json:"budget_hourly_usd,omitempty"`
	BudgetWeeklyUSD     *float64 `json:"budget_weekly_usd,omitempty"`
	BudgetMonthlyUSD    *float64 `json:"budget_monthly_usd,omitempty"`
	MaxParallelRequests *int     `json:"max_parallel_requests,omitempty"`
	AllowedModels       []string `json:"allowed_models,omitempty"`
	BlockedModels       []string `json:"blocked_models,omitempty"`
	// ModelRoutes optionally pins specific allowed model IDs to a subset of
	// upstream providers. When a route is present for the requested model, the
	// request is confined to those providers only (no failover to other
	// registry providers). A model in ModelRoutes must also be permitted by
	// AllowedModels (exact or wildcard). Wildcard tokens cannot be routed.
	ModelRoutes []ModelRoute `json:"model_routes,omitempty"`
	// ModelGroupID optionally attaches this policy to a model_groups row.
	// When set, the group's AllowedModels / BlockedModels / ModelRoutes
	// OVERRIDE the policy's own fields at enforcement time (group is the
	// source of truth). The group's routes are honored here because routes
	// are an API-key-policy concept; an internal-user-attached group ignores
	// the group's routes. Empty/nil = no group attached (entity fields apply
	// as before). Nullable; validated for existence at attach time.
	ModelGroupID *string `json:"model_group_id,omitempty"`
	// ModelRPMLimits / ModelBudgetLimits carry the attached model group's
	// per-model caps when (and only when) a model group is attached. They are
	// populated by the policy service's group-override snapshot path and are
	// never persisted on the policy row itself (they surface via the group).
	// Keyed by model id. A missing key = unlimited.
	ModelRPMLimits    map[string]int     `json:"model_rpm_limits,omitempty"`
	ModelBudgetLimits map[string]float64 `json:"model_budget_limits,omitempty"`
	// DiscountPct / ModelDiscountPcts carry the attached model group's
	// discount when (and only when) a model group is attached. DiscountPct is
	// the group-level default; ModelDiscountPcts is the per-model override map
	// keyed by model id (a present key takes precedence over the default).
	// Populated by the group-override snapshot path; never persisted on the
	// policy row. Applied to cost_usd at the flusher (events) and in Consume
	// (budget windows) so both stay in sync.
	DiscountPct       *float64           `json:"discount_pct,omitempty"`
	ModelDiscountPcts map[string]float64 `json:"model_discount_pcts,omitempty"`
	// AllowedIPs / BlockedIPs restrict which source IP addresses may use the
	// API key. Entries are single IPs ("10.0.0.5") or CIDR ranges
	// ("10.0.0.0/8", "2001:db8::/32"). BlockedIPs takes precedence: a match
	// denies the request even when AllowedIPs would also match. When
	// AllowedIPs is non-empty, the client IP must match at least one entry;
	// an empty AllowedIPs means "all IPs allowed" (subject to BlockedIPs).
	AllowedIPs []string `json:"allowed_ips,omitempty"`
	BlockedIPs []string `json:"blocked_ips,omitempty"`
	// StoreRequestBodies permits persisting this key's request/response
	// bodies (privacy opt-in). Capture happens when either the serving
	// provider's toggle or this key's toggle is ON. Default false.
	StoreRequestBodies bool      `json:"store_request_bodies"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ModelRoute pins a single model ID to a set of upstream providers. Requests
// for Model are confined to the listed Providers (case-insensitive match).
//
// Strategy optionally overrides the credential-selection mode for this model:
//   - ""          (default) inherits the global routing.strategy (round-robin).
//   - "priority"  pins to the highest-priority provider until it is
//     exhausted/cooldown, then descends to the next. Priorities
//     order Providers (highest number = primary).
//   - "failover"  starts at the highest-priority provider and relies on the
//     conductor's inner loop to walk to the next provider on a
//     non-invalid upstream error (5xx/429/cooldown).
//
// Priorities is optional and only Providers present in Providers are
// honored; unlisted providers default to priority 0.
type ModelRoute struct {
	Model      string             `json:"model"`
	Providers  []string           `json:"providers"`
	Strategy   string             `json:"strategy,omitempty"`
	Priorities []ProviderPriority `json:"priorities,omitempty"`
	// RPMLimit optionally caps requests-per-minute for this model when the
	// route belongs to a model group (group-attached policy). nil or <= 0
	// means unlimited for this model. Enforced by the policy service via an
	// in-memory per-(key, model) sliding window.
	RPMLimit *int `json:"rpm_limit,omitempty"`
	// MaxBudgetUSD optionally caps the total lifetime spend for this model
	// when the route belongs to a model group (group-attached policy).
	// nil or <= 0 means unlimited. Enforced against the SUM(cost_usd) of
	// the key's usage_events rows for this model (HTTP 402 once exceeded).
	MaxBudgetUSD *float64 `json:"max_budget_usd,omitempty"`
	// DiscountPct optionally applies a discount percentage (0-100) to the
	// computed cost_usd of requests for this model when the route belongs to a
	// model group. nil or <= 0 means no discount (full price). Takes
	// precedence over the group-level DiscountPct default when set.
	DiscountPct *float64 `json:"discount_pct,omitempty"`
}

// ProviderPriority assigns a priority weight to a single provider within a
// ModelRoute. Higher numbers are preferred (served first). Only providers
// also listed in ModelRoute.Providers are considered.
type ProviderPriority struct {
	Provider string `json:"provider"`
	Priority int    `json:"priority"`
}

// APIKeyStore provides CRUD operations for client-facing API keys and their
// policies. It is backed by the same *sql.DB connection as PostgresStore.
type APIKeyStore struct {
	db                 *sql.DB
	apiKeysTable       string
	policiesTable      string
	internalUsersTable string
}

// NewAPIKeyStore builds an APIKeyStore that reuses the PostgresStore connection
// and table names. Returns nil if the parent store is nil so callers can
// feature-detect the absence of the PG backend with a nil check.
func NewAPIKeyStore(parent *PostgresStore) *APIKeyStore {
	if parent == nil {
		return nil
	}
	return &APIKeyStore{
		db:                 parent.DB(),
		apiKeysTable:       parent.APIKeysTable(),
		policiesTable:      parent.PoliciesTable(),
		internalUsersTable: parent.InternalUsersTable(),
	}
}

// HashSecret returns the SHA-256 hex digest of a plaintext API key secret.
// This digest is the only representation persisted to the database.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SecretPrefixOf returns the displayable prefix of a secret (exported wrapper
// around prefixOf so sibling packages like the management handlers can render a
// masked prefix without importing unexported helpers).
func SecretPrefixOf(secret string) string {
	return prefixOf(secret)
}

// prefixOf returns the displayable prefix of a secret. The prefix exposes the
// first APIKeyPrefixLen characters of the secret body (without the SecretPrefix
// marker), which is insufficient to reconstruct the secret.
func prefixOf(secret string) string {
	body := secret
	if len(body) > len(SecretPrefix) && body[:len(SecretPrefix)] == SecretPrefix {
		body = body[len(SecretPrefix):]
	}
	if len(body) > APIKeyPrefixLen {
		body = body[:APIKeyPrefixLen]
	}
	return body
}

// GenerateSecret produces a new opaque plaintext secret. The caller is the only
// party that ever sees the plaintext: the hashed form is what gets persisted.
func GenerateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("postgres store: generate secret: %w", err)
	}
	return SecretPrefix + hex.EncodeToString(buf), nil
}

// Create inserts a new API key and (optionally) its policy in a single
// transaction. It returns the freshly generated plaintext secret; the caller is
// responsible for surfacing it to the user exactly once as the secret is never
// recoverable from the database. The alias is an optional non-secret label
// stored alongside the key for filtering/display in usage stats. To attach the
// key to an internal user owner, call UpdateUserID after creation.
func (s *APIKeyStore) Create(ctx context.Context, name string, alias string, secret string, expiresAt *time.Time, metadata map[string]any, policy *Policy) (*APIKey, string, error) {
	if s == nil || s.db == nil {
		return nil, "", fmt.Errorf("postgres store: api key store not initialized")
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
		return nil, "", fmt.Errorf("postgres store: marshal metadata: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("postgres store: begin api key create tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, key_alias, key_hash, key_prefix, status, expires_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
	`, s.apiKeysTable), id, displayName, nullableString(alias), hash, prefix, APIKeyStatusActive, expiresAt, string(metaJSON)); err != nil {
		return nil, "", fmt.Errorf("postgres store: insert api key: %w", err)
	}

	if policy != nil {
		policy.APIKeyID = id
		if err = upsertPolicyTx(ctx, tx, s.policiesTable, *policy); err != nil {
			return nil, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("postgres store: commit api key: %w", err)
	}

	created, _, err := s.LookupByHash(ctx, hash)
	if err != nil {
		// Best-effort reconstruction when the row is not yet visible.
		created = &APIKey{
			ID: id, Name: displayName, KeyAlias: alias, KeyHash: hash, KeyPrefix: prefix,
			Status: APIKeyStatusActive, ExpiresAt: expiresAt, Metadata: meta,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	return created, secret, nil
}

// Upsert inserts or updates an API key row by id, preserving the supplied
// key_hash/key_prefix/status/user_id/expires_at so the "sync to NixLLM" key
// migration is re-sync-stable and migrated secrets keep working (auth hashes
// the presented secret and compares against key_hash). The policy, when
// non-nil, is upserted alongside. Unlike Create it never generates a secret:
// it carries an existing credential's identity over verbatim.
func (s *APIKeyStore) Upsert(ctx context.Context, k APIKey, policy *Policy) (APIKey, error) {
	if s == nil || s.db == nil {
		return APIKey{}, fmt.Errorf("postgres store: api key store not initialized")
	}
	if k.ID == "" || k.KeyHash == "" {
		return APIKey{}, fmt.Errorf("postgres store: upsert api key requires id and key_hash")
	}
	displayName := trimOr(k.Name, "unnamed")
	meta := k.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return APIKey{}, fmt.Errorf("postgres store: marshal metadata: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, fmt.Errorf("postgres store: begin api key upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, name, key_alias, key_hash, key_prefix, status, user_id, expires_at, last_used_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)
		ON CONFLICT (id) DO UPDATE SET
			name        = EXCLUDED.name,
			key_alias   = EXCLUDED.key_alias,
			key_hash    = EXCLUDED.key_hash,
			key_prefix  = EXCLUDED.key_prefix,
			status      = EXCLUDED.status,
			user_id     = EXCLUDED.user_id,
			expires_at  = EXCLUDED.expires_at,
			last_used_at = EXCLUDED.last_used_at,
			metadata    = EXCLUDED.metadata,
			updated_at  = NOW()
	`, s.apiKeysTable),
		k.ID, displayName, nullableString(k.KeyAlias), k.KeyHash, k.KeyPrefix, k.Status,
		nullableString(k.UserID), k.ExpiresAt, k.LastUsedAt, string(metaJSON)); err != nil {
		return APIKey{}, fmt.Errorf("postgres store: upsert api key: %w", err)
	}

	if policy != nil {
		policy.APIKeyID = k.ID
		if err = upsertPolicyTx(ctx, tx, s.policiesTable, *policy); err != nil {
			return APIKey{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return APIKey{}, fmt.Errorf("postgres store: commit api key upsert: %w", err)
	}
	created, _, err := s.LookupByID(ctx, k.ID)
	if err != nil {
		// Best-effort reconstruction when the row is not yet visible.
		created = &APIKey{
			ID: k.ID, Name: displayName, KeyAlias: k.KeyAlias, KeyHash: k.KeyHash, KeyPrefix: k.KeyPrefix,
			Status: k.Status, UserID: k.UserID, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, Metadata: meta,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	return *created, nil
}

// liteLLMPolicyToRuntimePolicy converts a Manage-LiteLLM key policy into the
// runtime Policy shape. Most limits map 1:1; the runtime has no key-level
// tpm_limit or alias map, and LiteLLM's single budget_usd + budget_duration is
// enforced only when it maps exactly onto a runtime window (7d → weekly,
// 30d → monthly). Budgets with a duration that has no precise runtime
// counterpart (1d/24h/12h/8h, or no duration at all) are NOT enforced at
// runtime — the raw values are preserved in api_keys.metadata by
// ImportLiteLLMKeys so the operator keeps full visibility.
func liteLLMPolicyToRuntimePolicy(src LiteLLMPolicy) Policy {
	dst := Policy{
		RPMLimit:            intToIntPtr(src.RPMLimit),
		MaxParallelRequests: intToIntPtr(src.MaxParallelRequests),
		AllowedModels:       normalizeStringSlice(src.AllowedModels),
		BlockedModels:       normalizeStringSlice(src.BlockedModels),
		AllowedIPs:          normalizeStringSlice(src.AllowedIPs),
		BlockedIPs:          normalizeStringSlice(src.BlockedIPs),
		ModelRoutes:         normalizeModelRoutes(src.ModelRoutes),
	}
	if src.BudgetUSD != nil && *src.BudgetUSD > 0 {
		v := *src.BudgetUSD
		switch src.BudgetDuration {
		case BudgetDuration7d:
			dst.BudgetWeeklyUSD = &v
		case BudgetDuration30d:
			dst.BudgetMonthlyUSD = &v
		}
	}
	return dst
}

func intToIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

// ImportLiteLLMKeys upserts Manage-LiteLLM API keys into the runtime api_keys
// table (with their policies) — the key phase of the "sync to NixLLM" full
// migration. KeyHash is carried over so existing client secrets keep working
// against the proxy's hash lookup. LiteLLM tags have no runtime column, so
// they are folded into metadata["litellm_tags"]. Rows that cannot be imported
// are skipped and counted; a single bad row never aborts the pass.
func (s *APIKeyStore) ImportLiteLLMKeys(ctx context.Context, src []LiteLLMKey, policies map[string]*LiteLLMPolicy) (imported, skipped int, err error) {
	if s == nil || s.db == nil {
		return 0, 0, fmt.Errorf("postgres store: api key store not initialized")
	}
	for i := range src {
		k := src[i]
		meta := k.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		if len(k.Tags) > 0 {
			meta["litellm_tags"] = k.Tags
		}
		runtimeKey := APIKey{
			ID:         k.ID,
			Name:       k.Name,
			KeyAlias:   k.KeyAlias,
			KeyHash:    k.KeyHash,
			KeyPrefix:  k.KeyPrefix,
			Status:     k.Status,
			UserID:     k.UserID,
			ExpiresAt:  k.ExpiresAt,
			LastUsedAt: k.LastUsedAt,
			Metadata:   meta,
		}
		var pol *Policy
		if policies != nil {
			if srcPol, ok := policies[k.ID]; ok && srcPol != nil {
				p := liteLLMPolicyToRuntimePolicy(*srcPol)
				pol = &p
				// Preserve the raw LiteLLM budget for budgets that are not
				// enforced at runtime (no precisely-mappable window), so the
				// operator can still see the original cap + duration.
				if srcPol.BudgetUSD != nil {
					meta["litellm_budget_usd"] = *srcPol.BudgetUSD
				}
				if srcPol.BudgetDuration != "" {
					meta["litellm_budget_duration"] = srcPol.BudgetDuration
				}
			}
		}
		if _, errUps := s.Upsert(ctx, runtimeKey, pol); errUps != nil {
			skipped++
			log.WithError(errUps).WithField("key_id", k.ID).
				Warn("management: sync to nixllm: skipping un-importable key")
			continue
		}
		imported++
	}
	return imported, skipped, nil
}

func validateSecret(secret string) error {
	if len(secret) < 16 {
		return fmt.Errorf("%w: api key secret too short (min 16 chars)", ErrInvalidSecret)
	}
	return nil
}

func trimOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// LookupByHash returns the API key and its policy matching the supplied
// SHA-256 secret hash. ErrAPIKeyNotFound is returned when no row matches.
func (s *APIKeyStore) LookupByHash(ctx context.Context, hash string) (*APIKey, *Policy, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("postgres store: api key store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.created_at, k.updated_at, k.expires_at, k.last_used_at, k.metadata,
		       p.rpm_limit, p.hourly_rate_limit, p.budget_hourly_usd, p.budget_weekly_usd,
		       p.budget_monthly_usd, p.max_parallel_requests,
		       p.allowed_models, p.blocked_models, p.model_routes, p.model_group_id,
		       p.allowed_ips, p.blocked_ips, p.store_request_bodies, p.updated_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		LEFT JOIN %s p ON p.api_key_id = k.id
		WHERE k.key_hash = $1
	`, s.apiKeysTable, s.internalUsersTable, s.policiesTable), hash)
	key, policy, err := scanAPIKeyRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrAPIKeyNotFound
		}
		return nil, nil, err
	}
	return key, policy, nil
}

// LookupByID returns the API key (and its policy, if any) by its ID.
func (s *APIKeyStore) LookupByID(ctx context.Context, id string) (*APIKey, *Policy, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("postgres store: api key store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.created_at, k.updated_at, k.expires_at, k.last_used_at, k.metadata,
		       p.rpm_limit, p.hourly_rate_limit, p.budget_hourly_usd, p.budget_weekly_usd,
		       p.budget_monthly_usd, p.max_parallel_requests,
		       p.allowed_models, p.blocked_models, p.model_routes, p.model_group_id,
		       p.allowed_ips, p.blocked_ips, p.store_request_bodies, p.updated_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		LEFT JOIN %s p ON p.api_key_id = k.id
		WHERE k.id = $1
	`, s.apiKeysTable, s.internalUsersTable, s.policiesTable), id)
	key, policy, err := scanAPIKeyRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrAPIKeyNotFound
		}
		return nil, nil, err
	}
	return key, policy, nil
}

// LookupByAlias returns the API key (and its policy, if any) whose key_alias
// matches alias case-insensitively. The alias is not unique in the schema, so
// when more than one row shares the alias ErrAmbiguousAlias is returned and the
// caller must treat the match as unresolved. ErrAPIKeyNotFound is returned when
// no row matches. A nil/empty alias never matches.
func (s *APIKeyStore) LookupByAlias(ctx context.Context, alias string) (*APIKey, *Policy, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("postgres store: api key store not initialized")
	}
	if strings.TrimSpace(alias) == "" {
		return nil, nil, ErrAPIKeyNotFound
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.created_at, k.updated_at, k.expires_at, k.last_used_at, k.metadata,
		       p.rpm_limit, p.hourly_rate_limit, p.budget_hourly_usd, p.budget_weekly_usd,
		       p.budget_monthly_usd, p.max_parallel_requests,
		       p.allowed_models, p.blocked_models, p.model_routes, p.model_group_id,
		       p.allowed_ips, p.blocked_ips, p.store_request_bodies, p.updated_at
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		LEFT JOIN %s p ON p.api_key_id = k.id
		WHERE LOWER(k.key_alias) = LOWER($1)
		ORDER BY k.created_at ASC
	`, s.apiKeysTable, s.internalUsersTable, s.policiesTable), alias)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres store: lookup api key by alias: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Iterate rows; fully decode each (key, policy) exactly like scanAPIKeyRow
	// does so the returned *Policy is populated correctly. Alias is not unique,
	// so we must collect every match before deciding how to resolve it.
	var keys []*APIKey
	var policies []*Policy
	for rows.Next() {
		key, policy, err := scanAPIKeyRows(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("postgres store: scan api key row by alias: %w", err)
		}
		keys = append(keys, key)
		policies = append(policies, policy)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("postgres store: iterate api key rows by alias: %w", err)
	}
	switch len(keys) {
	case 0:
		return nil, nil, ErrAPIKeyNotFound
	case 1:
		return keys[0], policies[0], nil
	default:
		return nil, nil, ErrAmbiguousAlias
	}
}

// scanAPIKeyRows decodes one row from a *sql.Rows into an APIKey and its
// optional Policy, mirroring scanAPIKeyRow's column layout and policy decode.
func scanAPIKeyRows(row *sql.Rows) (*APIKey, *Policy, error) {
	var (
		key             APIKey
		metadata        []byte
		rpmLimit        sql.NullInt64
		hourlyRateLimit sql.NullInt64
		budgetHourly    sql.NullFloat64
		budgetWeekly    sql.NullFloat64
		budgetMonthly   sql.NullFloat64
		maxParallel     sql.NullInt64
		allowedModels   []byte
		blockedModels   []byte
		modelRoutes     []byte
		modelGroupID    sql.NullString
		allowedIPs      []byte
		blockedIPs      []byte
		policyUpdatedAt sql.NullTime
		storeBodies     sql.NullBool
	)
	if err := row.Scan(
		&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
		&key.UserID,
		&key.UserAlias, &key.UserEmail,
		&key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt, &metadata,
		&rpmLimit, &hourlyRateLimit, &budgetHourly, &budgetWeekly, &budgetMonthly,
		&maxParallel,
		&allowedModels, &blockedModels, &modelRoutes, &modelGroupID,
		&allowedIPs, &blockedIPs, &policyUpdatedAt, &storeBodies,
	); err != nil {
		return nil, nil, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &key.Metadata)
	}
	if key.Metadata == nil {
		key.Metadata = map[string]any{}
	}

	var policy *Policy
	if policyUpdatedAt.Valid {
		p := Policy{APIKeyID: key.ID, UpdatedAt: policyUpdatedAt.Time}
		if rpmLimit.Valid {
			v := int(rpmLimit.Int64)
			p.RPMLimit = &v
		}
		if hourlyRateLimit.Valid {
			v := int(hourlyRateLimit.Int64)
			p.HourlyRateLimit = &v
		}
		if budgetHourly.Valid {
			v := budgetHourly.Float64
			p.BudgetHourlyUSD = &v
		}
		if budgetWeekly.Valid {
			v := budgetWeekly.Float64
			p.BudgetWeeklyUSD = &v
		}
		if budgetMonthly.Valid {
			v := budgetMonthly.Float64
			p.BudgetMonthlyUSD = &v
		}
		if maxParallel.Valid {
			v := int(maxParallel.Int64)
			p.MaxParallelRequests = &v
		}
		p.AllowedModels = decodeStringArray(allowedModels)
		p.BlockedModels = decodeStringArray(blockedModels)
		p.ModelRoutes = decodeModelRoutes(modelRoutes)
		if modelGroupID.Valid && modelGroupID.String != "" {
			id := modelGroupID.String
			p.ModelGroupID = &id
		}
		p.AllowedIPs = decodeStringArray(allowedIPs)
		p.BlockedIPs = decodeStringArray(blockedIPs)
		p.StoreRequestBodies = storeBodies.Valid && storeBodies.Bool
		policy = &p
	}
	return &key, policy, nil
}

func scanAPIKeyRow(row *sql.Row) (*APIKey, *Policy, error) {
	var (
		key             APIKey
		metadata        []byte
		rpmLimit        sql.NullInt64
		hourlyRateLimit sql.NullInt64
		budgetHourly    sql.NullFloat64
		budgetWeekly    sql.NullFloat64
		budgetMonthly   sql.NullFloat64
		maxParallel     sql.NullInt64
		allowedModels   []byte
		blockedModels   []byte
		modelRoutes     []byte
		modelGroupID    sql.NullString
		allowedIPs      []byte
		blockedIPs      []byte
		policyUpdatedAt sql.NullTime
		storeBodies     sql.NullBool
	)
	if err := row.Scan(
		&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
		&key.UserID,
		&key.UserAlias, &key.UserEmail,
		&key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt, &metadata,
		&rpmLimit, &hourlyRateLimit, &budgetHourly, &budgetWeekly, &budgetMonthly,
		&maxParallel,
		&allowedModels, &blockedModels, &modelRoutes, &modelGroupID,
		&allowedIPs, &blockedIPs, &policyUpdatedAt, &storeBodies,
	); err != nil {
		return nil, nil, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &key.Metadata)
	}
	if key.Metadata == nil {
		key.Metadata = map[string]any{}
	}

	var policy *Policy
	if policyUpdatedAt.Valid {
		p := Policy{APIKeyID: key.ID, UpdatedAt: policyUpdatedAt.Time}
		if rpmLimit.Valid {
			v := int(rpmLimit.Int64)
			p.RPMLimit = &v
		}
		if hourlyRateLimit.Valid {
			v := int(hourlyRateLimit.Int64)
			p.HourlyRateLimit = &v
		}
		if budgetHourly.Valid {
			v := budgetHourly.Float64
			p.BudgetHourlyUSD = &v
		}
		if budgetWeekly.Valid {
			v := budgetWeekly.Float64
			p.BudgetWeeklyUSD = &v
		}
		if budgetMonthly.Valid {
			v := budgetMonthly.Float64
			p.BudgetMonthlyUSD = &v
		}
		if maxParallel.Valid {
			v := int(maxParallel.Int64)
			p.MaxParallelRequests = &v
		}
		p.AllowedModels = decodeStringArray(allowedModels)
		p.BlockedModels = decodeStringArray(blockedModels)
		p.ModelRoutes = decodeModelRoutes(modelRoutes)
		if modelGroupID.Valid && modelGroupID.String != "" {
			id := modelGroupID.String
			p.ModelGroupID = &id
		}
		p.AllowedIPs = decodeStringArray(allowedIPs)
		p.BlockedIPs = decodeStringArray(blockedIPs)
		p.StoreRequestBodies = storeBodies.Valid && storeBodies.Bool
		policy = &p
	}
	return &key, policy, nil
}

func decodeStringArray(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// List returns all API keys ordered by creation time (newest first). Policies
// are not loaded here; use LookupByID to fetch the policy for a specific key.
func (s *APIKeyStore) List(ctx context.Context) ([]*APIKey, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: api key store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.created_at, k.updated_at, k.expires_at, k.last_used_at, k.metadata
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		ORDER BY k.created_at DESC
	`, s.apiKeysTable, s.internalUsersTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list api keys: %w", err)
	}
	defer rows.Close()

	keys := make([]*APIKey, 0, 32)
	for rows.Next() {
		var (
			key      APIKey
			metadata []byte
		)
		if err = rows.Scan(&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
			&key.UserID,
			&key.UserAlias, &key.UserEmail,
			&key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt, &metadata); err != nil {
			return nil, fmt.Errorf("postgres store: scan api key row: %w", err)
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &key.Metadata)
		}
		if key.Metadata == nil {
			key.Metadata = map[string]any{}
		}
		keys = append(keys, &key)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate api keys: %w", err)
	}
	return keys, nil
}

// ListPaged returns a page of API keys plus the total row count (for pager UI
// math). Page is 1-indexed; pageSize must be > 0. When statusFilter is
// non-empty (e.g. "active"), results are restricted to that status and the
// total reflects the same filter.
func (s *APIKeyStore) ListPaged(ctx context.Context, page, pageSize int, statusFilter string) ([]*APIKey, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: api key store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 25
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, s.apiKeysTable)
	countArgs := []any{}
	if statusFilter != "" {
		countQuery += " WHERE status = $1"
		countArgs = append(countArgs, statusFilter)
	}
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count api keys (paged): %w", err)
	}

	listQuery := fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_hash, k.key_prefix, k.status,
		       COALESCE(k.user_id, ''),
		       COALESCE(u.user_alias, ''), COALESCE(u.user_email, ''),
		       k.created_at, k.updated_at, k.expires_at, k.last_used_at, k.metadata
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
	`, s.apiKeysTable, s.internalUsersTable)
	listArgs := []any{}
	if statusFilter != "" {
		listArgs = append(listArgs, statusFilter)
		listQuery += fmt.Sprintf(" WHERE k.status = $%d", len(listArgs))
	}
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery += fmt.Sprintf(" ORDER BY k.created_at DESC LIMIT $%d OFFSET $%d", len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list api keys (paged): %w", err)
	}
	defer rows.Close()

	keys := make([]*APIKey, 0, pageSize)
	for rows.Next() {
		var (
			key      APIKey
			metadata []byte
		)
		if err = rows.Scan(&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
			&key.UserID,
			&key.UserAlias, &key.UserEmail,
			&key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt, &metadata); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan api key row (paged): %w", err)
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &key.Metadata)
		}
		if key.Metadata == nil {
			key.Metadata = map[string]any{}
		}
		keys = append(keys, &key)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate api keys (paged): %w", err)
	}
	return keys, total, nil
}

// APIKeyBudgetCaps is the per-key budget projection returned by
// ListBudgetCaps. It pairs an active API key with its optional per-window
// budget caps (nil = no cap for that window) plus the resolved key/user name
// so the alert detector can produce a readable alert without extra joins.
type APIKeyBudgetCaps struct {
	APIKeyID         string    `json:"api_key_id"`
	Name             string    `json:"name"`
	KeyAlias         string    `json:"key_alias"`
	KeyPrefix        string    `json:"key_prefix"`
	UserID           string    `json:"user_id"`
	UserAlias        string    `json:"user_alias"`
	CreatedAt        time.Time `json:"created_at"`
	BudgetHourlyUSD  *float64  `json:"budget_hourly_usd"`
	BudgetWeeklyUSD  *float64  `json:"budget_weekly_usd"`
	BudgetMonthlyUSD *float64  `json:"budget_monthly_usd"`
}

// ListBudgetCaps returns every active API key that carries at least one budget
// cap (hourly/weekly/monthly), paired with its policy caps. Only active keys
// are returned because only they can be enforced (a disabled/revoked key's
// budget is moot). It is the drill source for the "API key max spend" alert
// detector: the detector resolves each window's current spend from
// usage_windows and compares against these caps. nil policy rows (key with no
// budget policy) are skipped.
func (s *APIKeyStore) ListBudgetCaps(ctx context.Context) ([]APIKeyBudgetCaps, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: api key store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT k.id, k.name, COALESCE(k.key_alias, ''), k.key_prefix,
		       COALESCE(k.user_id, ''), COALESCE(u.user_alias, ''),
		       k.created_at, p.budget_hourly_usd, p.budget_weekly_usd, p.budget_monthly_usd
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id
		JOIN %s p ON p.api_key_id = k.id
		WHERE k.status = 'active'
		  AND (p.budget_hourly_usd > 0 OR p.budget_weekly_usd > 0 OR p.budget_monthly_usd > 0)`, s.apiKeysTable, s.internalUsersTable, s.policiesTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list api key budget caps: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close api key budget caps rows failed")
		}
	}()
	out := make([]APIKeyBudgetCaps, 0)
	for rows.Next() {
		var (
			c                   APIKeyBudgetCaps
			hourly, weekly, mon sql.NullFloat64
		)
		if err := rows.Scan(&c.APIKeyID, &c.Name, &c.KeyAlias, &c.KeyPrefix,
			&c.UserID, &c.UserAlias, &c.CreatedAt, &hourly, &weekly, &mon); err != nil {
			return nil, fmt.Errorf("postgres store: scan api key budget caps row: %w", err)
		}
		if hourly.Valid {
			v := hourly.Float64
			c.BudgetHourlyUSD = &v
		}
		if weekly.Valid {
			v := weekly.Float64
			c.BudgetWeeklyUSD = &v
		}
		if mon.Valid {
			v := mon.Float64
			c.BudgetMonthlyUSD = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdatePolicy creates or replaces the policy attached to api_key_id.
func (s *APIKeyStore) UpdatePolicy(ctx context.Context, id string, policy Policy) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	policy.APIKeyID = id
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: begin update policy tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = upsertPolicyTx(ctx, tx, s.policiesTable, policy); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET updated_at = NOW() WHERE id = $1`, s.apiKeysTable,
	), id); err != nil {
		return fmt.Errorf("postgres store: bump api key updated_at: %w", err)
	}
	return tx.Commit()
}

func upsertPolicyTx(ctx context.Context, tx *sql.Tx, policiesTable string, policy Policy) error {
	allowed, _ := json.Marshal(normalizeStringSlice(policy.AllowedModels))
	blocked, _ := json.Marshal(normalizeStringSlice(policy.BlockedModels))
	routes, _ := json.Marshal(normalizeModelRoutes(policy.ModelRoutes))
	allowedIPs, _ := json.Marshal(normalizeStringSlice(policy.AllowedIPs))
	blockedIPs, _ := json.Marshal(normalizeStringSlice(policy.BlockedIPs))
	// model_group_id: a non-empty id attaches the policy to a group (group
	// becomes the source of truth at enforcement time). A nil pointer means
	// "leave unchanged"; an empty pointer ("" via the dashboard clearing the
	// group) means detach. We model both as nullable so UPDATE can clear it.
	var modelGroupIDArg any
	if policy.ModelGroupID != nil {
		modelGroupIDArg = nullableString(*policy.ModelGroupID)
	} else {
		// Preserve previous value when caller omitted the field. The dashboard
		// always sends model_group_id (possibly empty), so this preserve-path
		// only matters when the policy is upserted internally (e.g. on key
		// creation with no group). We default to NULL because a freshly created
		// key has no prior group; PreserveCurrent would need a SELECT anyway.
		modelGroupIDArg = nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			api_key_id, rpm_limit, hourly_rate_limit,
			budget_hourly_usd, budget_weekly_usd, budget_monthly_usd,
			max_parallel_requests,
			allowed_models, blocked_models, model_routes, model_group_id,
			allowed_ips, blocked_ips, store_request_bodies, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12::jsonb, $13::jsonb, $14, NOW())
		ON CONFLICT (api_key_id) DO UPDATE SET
			rpm_limit = EXCLUDED.rpm_limit,
			hourly_rate_limit = EXCLUDED.hourly_rate_limit,
			budget_hourly_usd = EXCLUDED.budget_hourly_usd,
			budget_weekly_usd = EXCLUDED.budget_weekly_usd,
			budget_monthly_usd = EXCLUDED.budget_monthly_usd,
			max_parallel_requests = EXCLUDED.max_parallel_requests,
			allowed_models = EXCLUDED.allowed_models,
			blocked_models = EXCLUDED.blocked_models,
			model_routes = EXCLUDED.model_routes,
			model_group_id = EXCLUDED.model_group_id,
			allowed_ips = EXCLUDED.allowed_ips,
			blocked_ips = EXCLUDED.blocked_ips,
			store_request_bodies = EXCLUDED.store_request_bodies,
			updated_at = NOW()
	`, policiesTable),
		policy.APIKeyID, policy.RPMLimit, policy.HourlyRateLimit,
		policy.BudgetHourlyUSD, policy.BudgetWeeklyUSD, policy.BudgetMonthlyUSD,
		policy.MaxParallelRequests,
		string(allowed), string(blocked), string(routes), modelGroupIDArg,
		string(allowedIPs), string(blockedIPs), policy.StoreRequestBodies,
	); err != nil {
		return fmt.Errorf("postgres store: upsert policy: %w", err)
	}
	return nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// normalizeModelRoutes trims whitespace and drops routes with an empty model.
// Routes that carry only per-model caps (no pinned providers) are kept — they
// are how a model group persists RPM/budget caps without pinning a provider.
// It returns a non-nil slice (empty when input is empty) so the persisted
// jsonb column is [] rather than NULL. Strategy is normalized (lowercased,
// trimmed); Priorities entries referencing providers not in the route's
// allowlist are dropped so persisted priorities stay consistent.
func normalizeModelRoutes(routes []ModelRoute) []ModelRoute {
	if len(routes) == 0 {
		return []ModelRoute{}
	}
	out := make([]ModelRoute, 0, len(routes))
	for _, r := range routes {
		model := strings.TrimSpace(r.Model)
		if model == "" {
			continue
		}
		providers := make([]string, 0, len(r.Providers))
		providerSet := make(map[string]struct{}, len(r.Providers))
		for _, p := range r.Providers {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			key := strings.ToLower(p)
			if _, dup := providerSet[key]; dup {
				continue
			}
			providerSet[key] = struct{}{}
			providers = append(providers, p)
		}
		hasCaps := (r.RPMLimit != nil && *r.RPMLimit > 0) || (r.MaxBudgetUSD != nil && *r.MaxBudgetUSD > 0) ||
			(r.DiscountPct != nil && *r.DiscountPct > 0)
		if len(providers) == 0 && !hasCaps {
			continue
		}
		strategy := strings.ToLower(strings.TrimSpace(r.Strategy))
		var priorities []ProviderPriority
		for _, pr := range r.Priorities {
			provider := strings.TrimSpace(pr.Provider)
			if provider == "" {
				continue
			}
			if _, ok := providerSet[strings.ToLower(provider)]; !ok {
				continue
			}
			priorities = append(priorities, ProviderPriority{Provider: provider, Priority: pr.Priority})
		}
		out = append(out, ModelRoute{
			Model:        model,
			Providers:    providers,
			Strategy:     strategy,
			Priorities:   priorities,
			RPMLimit:     r.RPMLimit,
			MaxBudgetUSD: r.MaxBudgetUSD,
			DiscountPct:  r.DiscountPct,
		})
	}
	return out
}

// decodeModelRoutes unmarshals the model_routes jsonb column. Returns nil when
// the column is empty/null/[] so zero-value semantics ("no routes") apply.
func decodeModelRoutes(raw []byte) []ModelRoute {
	if len(raw) == 0 {
		return nil
	}
	var out []ModelRoute
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UpdateStatus changes the lifecycle status of an API key.
func (s *APIKeyStore) UpdateStatus(ctx context.Context, id, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET status = $1, updated_at = NOW() WHERE id = $2`, s.apiKeysTable,
	), status, id)
	if err != nil {
		return fmt.Errorf("postgres store: update api key status: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// UpdateMetadata merges metadata for an API key.
func (s *APIKeyStore) UpdateMetadata(ctx context.Context, id string, metadata map[string]any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	meta := metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("postgres store: marshal metadata: %w", err)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET metadata = $1::jsonb, updated_at = NOW() WHERE id = $2`, s.apiKeysTable,
	), string(metaJSON), id)
	if err != nil {
		return fmt.Errorf("postgres store: update api key metadata: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// UpdateExpiry sets (or clears, when expiresAt is nil) the expiry timestamp.
func (s *APIKeyStore) UpdateExpiry(ctx context.Context, id string, expiresAt *time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET expires_at = $1, updated_at = NOW() WHERE id = $2`, s.apiKeysTable,
	), expiresAt, id)
	if err != nil {
		return fmt.Errorf("postgres store: update api key expiry: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// Rename changes the human-readable label of an API key.
func (s *APIKeyStore) Rename(ctx context.Context, id, name string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET name = $1, updated_at = NOW() WHERE id = $2`, s.apiKeysTable,
	), trimOr(name, "unnamed"), id)
	if err != nil {
		return fmt.Errorf("postgres store: rename api key: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// UpdateAlias changes the non-secret key_alias label used for filtering and
// display in usage stats. Pass an empty string to clear the alias.
func (s *APIKeyStore) UpdateAlias(ctx context.Context, id, alias string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET key_alias = $1, updated_at = NOW() WHERE id = $2`, s.apiKeysTable,
	), nullableString(alias), id)
	if err != nil {
		return fmt.Errorf("postgres store: update api key alias: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// UpdateUserID attaches (or detaches, when userID is empty) an API key to an
// internal user. The association drives per-user budgeting, attribution in
// usage_events (stamped by the usage flusher), and the Internal Users
// dashboard.
func (s *APIKeyStore) UpdateUserID(ctx context.Context, id, userID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET user_id = $1, updated_at = NOW() WHERE id = $2`, s.apiKeysTable,
	), nullableString(userID), id)
	if err != nil {
		return fmt.Errorf("postgres store: update api key user_id: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// Regenerate issues a new secret for the given key ID. The key ID, policy,
// metadata, and lifecycle status are preserved; only the hash changes.
//
// When secret is empty a fresh secret is auto-generated via GenerateSecret
// (the historical behavior). When secret is non-empty it is validated with
// validateSecret and used verbatim — this lets callers rotate to a chosen
// custom string. As with Create, uniqueness of the secret is the caller's
// responsibility: no collision pre-check is performed, and only the SHA-256
// hash is persisted. The key_hash column carries a UNIQUE constraint in the
// schema, so a duplicate custom secret fails the UPDATE with a unique-violation
// error (SQLSTATE 23505) rather than silently shadowing another key; callers
// relying on the hash should tolerate that error.
func (s *APIKeyStore) Regenerate(ctx context.Context, id, secret string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: api key store not initialized")
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
		s.apiKeysTable,
	), hash, prefix, id)
	if err != nil {
		return "", fmt.Errorf("postgres store: regenerate api key: %w", err)
	}
	if err = assertRowsAffected(res, id, "api key"); err != nil {
		return "", err
	}
	return secret, nil
}

// Delete permanently removes the API key and its policy (cascade).
func (s *APIKeyStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: api key store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, s.apiKeysTable,
	), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete api key: %w", err)
	}
	return assertRowsAffected(res, id, "api key")
}

// TouchLastUsed records the current time as the last_used_at timestamp. It is
// invoked from the request hot-path and is best-effort; errors are logged but
// not surfaced so callers can keep operating.
func (s *APIKeyStore) TouchLastUsed(ctx context.Context, id string) {
	if s == nil || s.db == nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET last_used_at = NOW() WHERE id = $1`, s.apiKeysTable,
	), id); err != nil {
		log.WithError(err).Debug("postgres store: touch last_used_at failed")
	}
}

// CountActive returns the number of keys currently in 'active' status.
func (s *APIKeyStore) CountActive(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: api key store not initialized")
	}
	var count int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE status = $1`, s.apiKeysTable,
	), APIKeyStatusActive).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("postgres store: count active api keys: %w", err)
	}
	return count, nil
}

// assertRowsAffected returns ErrAPIKeyNotFound when 0 rows were touched.
func assertRowsAffected(res sql.Result, id, label string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: %s rows affected: %w", label, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", ErrAPIKeyNotFound, id)
	}
	return nil
}

// APIKeyListFilter captures the optional filter dimensions for ListPagedFiltered.
// Empty values are ignored (unfiltered). SortBy is one of "created_at"
// (default), "name", "last_used_at", "user_alias". SortOrder is "desc"
// (default) or "asc". Search is a case-insensitive substring match on name,
// key_alias, key_prefix, id, or the owning user's alias/email.
type APIKeyListFilter struct {
	Status    string
	UserID    string
	Search    string
	SortBy    string
	SortOrder string
}

// ListPagedFiltered is the filtered/sorted variant of ListPaged. It accepts a
// full filter struct so the management API can expose search, user_id, and
// sort dimensions without overloading the ListPaged signature.
func (s *APIKeyStore) ListPagedFiltered(ctx context.Context, page, pageSize int, f APIKeyListFilter) ([]*APIKey, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: api key store not initialized")
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
		// Bind one placeholder per searchable column so the LIKE pattern is
		// matched against name, key_alias, key_prefix, id, and the owning
		// user's alias/email (mirrors the dashboard's keyword search).
		args = append(args, like, like, like, like, like, like)
		n := len(args)
		where = append(where, fmt.Sprintf(
			"(LOWER(k.name) LIKE LOWER($%d) OR LOWER(k.key_alias) LIKE LOWER($%d) OR LOWER(k.key_prefix) LIKE LOWER($%d) OR LOWER(k.id) LIKE LOWER($%d) OR LOWER(u.user_alias) LIKE LOWER($%d) OR LOWER(u.user_email) LIKE LOWER($%d))",
			n-5, n-4, n-3, n-2, n-1, n,
		))
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s k LEFT JOIN %s u ON u.id = k.user_id%s`,
		s.apiKeysTable, s.internalUsersTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count api keys (filtered): %w", err)
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
		       k.created_at, k.updated_at, k.expires_at, k.last_used_at, k.metadata
		FROM %s k
		LEFT JOIN %s u ON u.id = k.user_id%s
		ORDER BY %s %s NULLS LAST
		LIMIT $%d OFFSET $%d
	`, s.apiKeysTable, s.internalUsersTable, whereClause, sortCol, sortDir, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list api keys (filtered): %w", err)
	}
	defer rows.Close()

	keys := make([]*APIKey, 0, pageSize)
	for rows.Next() {
		var (
			key      APIKey
			metadata []byte
		)
		if err = rows.Scan(&key.ID, &key.Name, &key.KeyAlias, &key.KeyHash, &key.KeyPrefix, &key.Status,
			&key.UserID,
			&key.UserAlias, &key.UserEmail,
			&key.CreatedAt, &key.UpdatedAt, &key.ExpiresAt, &key.LastUsedAt, &metadata); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan api key row (filtered): %w", err)
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &key.Metadata)
		}
		if key.Metadata == nil {
			key.Metadata = map[string]any{}
		}
		keys = append(keys, &key)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate api keys (filtered): %w", err)
	}
	return keys, total, nil
}
