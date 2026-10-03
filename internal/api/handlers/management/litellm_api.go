package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// atoiDefault parses s as an int, returning def when s is empty or unparsable.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// requireLiteLLMRuntime returns the runtime PG stores backing the compat
// /litellm routes, plus false (after writing a 503 response) when they are not
// wired. Mirrors requirePG / requireUsers. Guard before every compat handler.
func (h *Handler) requireLiteLLMRuntime(c *gin.Context) (*store.UserStore, *store.APIKeyStore, *store.UsageStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, false
	}
	h.mu.Lock()
	users, keys, usage := h.pgUsers, h.pgAPIKeys, h.pgUsage
	h.mu.Unlock()
	if users == nil || keys == nil || usage == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, false
	}
	return users, keys, usage, true
}

// litellmCompatError is the shared error envelope for the compat /litellm
// routes, mirroring LiteLLM's {"error": {"type", "message"}} shape.
func litellmCompatError(c *gin.Context, status int, typ, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"type": typ, "message": msg}})
}

// translateLiteLLMKeyCompatError maps a runtime APIKeyStore error onto the
// LiteLLM compat envelope: a missing key becomes a 404 not_found, any other
// error becomes a 500 internal_error. Each store mutation returns
// store.ErrAPIKeyNotFound (via assertRowsAffected) when the key does not exist,
// so routing that through here keeps the not-found case a 404 instead of a 500.
func translateLiteLLMKeyCompatError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrAPIKeyNotFound) {
		litellmCompatError(c, http.StatusNotFound, "not_found", "key not found")
		return
	}
	litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
}

// translateLiteLLMUserCompatError maps a runtime UserStore error onto the
// LiteLLM compat envelope: a missing user becomes a 404 not_found, any other
// error becomes a 500 internal_error.
func translateLiteLLMUserCompatError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrInternalUserNotFound) {
		litellmCompatError(c, http.StatusNotFound, "not_found", "user not found")
		return
	}
	litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
}

// resolveLiteLLMKeyID resolves the runtime APIKey for a LiteLLM key identifier:
// a plaintext "sk-…" secret (hashed and looked up), a bare sha256 hash, an
// internal key id, or a key_alias. This is the equivalent of LiteLLM's
// _hash_token_if_needed + VerificationTokenRepository lookup.
func (h *Handler) resolveLiteLLMKeyID(c *gin.Context, keys *store.APIKeyStore, key string) (*store.APIKey, *store.Policy, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return nil, nil, false
	}
	// Resolve a plaintext secret or an already-hashed sha256 hex digest. A bare
	// 64-char hex string is already the hash (LiteLLM's _hash_token_if_needed
	// behavior) and must not be hashed again; an "sk-…" secret is hashed once.
	if isHex64(key) {
		k, pol, err := keys.LookupByHash(c.Request.Context(), key)
		if err == nil {
			return k, pol, true
		}
		if !errors.Is(err, store.ErrAPIKeyNotFound) {
			litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
			return nil, nil, false
		}
		// A bare hash may also be the internal row id — fall through to the id
		// lookup only when the hash lookup found nothing.
	} else if strings.HasPrefix(key, "sk-") {
		k, pol, err := keys.LookupByHash(c.Request.Context(), store.HashSecret(key))
		if err == nil {
			return k, pol, true
		}
		if !errors.Is(err, store.ErrAPIKeyNotFound) {
			litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
			return nil, nil, false
		}
		// An "sk-…" secret that matches no hash is definitively unknown; do not
		// fall through to id/alias resolution.
		litellmCompatError(c, http.StatusNotFound, "not_found", "key not found")
		return nil, nil, false
	}
	// Internal row id, or key_alias.
	if k, pol, err := keys.LookupByID(c.Request.Context(), key); err == nil {
		return k, pol, true
	} else if !errors.Is(err, store.ErrAPIKeyNotFound) {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return nil, nil, false
	}
	if k, pol, err := keys.LookupByAlias(c.Request.Context(), key); err == nil {
		return k, pol, true
	} else if errors.Is(err, store.ErrAmbiguousAlias) {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key_alias is ambiguous; multiple keys share it")
		return nil, nil, false
	} else if !errors.Is(err, store.ErrAPIKeyNotFound) {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return nil, nil, false
	}
	litellmCompatError(c, http.StatusNotFound, "not_found", "key not found")
	return nil, nil, false
}

// isHex64 reports whether s is a 64-char lowercase hex string (a sha256 hash).
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// parseDurationToExpiry parses a LiteLLM duration token ("30s", "30m", "30h",
// "30d", "1mo", or "-1" for never) and returns the future expiry time, or nil
// when duration is empty. An unparseable token yields an error, mirroring
// LiteLLM's validate_budget_duration / duration handling.
func parseDurationToExpiry(duration string, from time.Time) (*time.Time, error) {
	d := strings.TrimSpace(duration)
	if d == "" || d == "-1" {
		return nil, nil
	}
	unit := d[len(d)-1]
	numStr := d[:len(d)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return nil, errors.New("invalid duration: " + d)
	}
	var exp time.Time
	switch unit {
	case 's':
		exp = from.Add(time.Duration(n) * time.Second)
	case 'm':
		exp = from.Add(time.Duration(n) * time.Minute)
	case 'h':
		exp = from.Add(time.Duration(n) * time.Hour)
	case 'd':
		exp = from.Add(time.Duration(n) * 24 * time.Hour)
	case 'o': // "1mo" — approximate as 30 days
		exp = from.AddDate(0, n, 0)
	default:
		return nil, errors.New("invalid duration: " + d)
	}
	return &exp, nil
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// liteLLMCompatUserNewRequest is the JSON payload for POST /litellm/user/new.
// It mirrors LiteLLM's NewUserRequest field names. auto_create_key defaults to
// true (LiteLLM's default) so the response carries a generated key.
type liteLLMCompatUserNewRequest struct {
	UserID              string         `json:"user_id"`
	UserAlias           string         `json:"user_alias"`
	UserEmail           string         `json:"user_email"`
	UserRole            string         `json:"user_role"`
	Models              []string       `json:"models"`
	Metadata            map[string]any `json:"metadata"`
	MaxBudget           *float64       `json:"max_budget"`
	BudgetDuration      string         `json:"budget_duration"`
	RPMLimit            *int64         `json:"rpm_limit"`
	TPMLimit            *int64         `json:"tpm_limit"`
	MaxParallelRequests *int           `json:"max_parallel_requests"`
	KeyAlias            string         `json:"key_alias"`
	Duration            string         `json:"duration"`
	AutoCreateKey       *bool          `json:"auto_create_key"`
}

// liteLLMCompatUserResponse is the typed /litellm/user/new response. Typed
// struct + omitempty so unset optional fields are omitted from the JSON,
// matching LiteLLM's Optional-model / OpenAPI parity. Pointer fields (and
// omitempty on strings) mean user_alias="" or max_budget=nil are omitted.
type liteLLMCompatUserResponse struct {
	UserID              string         `json:"user_id"`
	UserAlias           string         `json:"user_alias,omitempty"`
	UserEmail           string         `json:"user_email,omitempty"`
	UserRole            string         `json:"user_role"`
	Models              []string       `json:"models,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	MaxBudget           *float64       `json:"max_budget,omitempty"`
	BudgetDuration      string         `json:"budget_duration,omitempty"`
	BudgetResetAt       *time.Time     `json:"budget_reset_at,omitempty"`
	RPMLimit            *int64         `json:"rpm_limit,omitempty"`
	TPMLimit            *int64         `json:"tpm_limit,omitempty"`
	MaxParallelRequests *int           `json:"max_parallel_requests,omitempty"`
	Spend               float64        `json:"spend"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	// key + token_id are populated only when auto_create_key is true (the
	// default), matching LiteLLM's /user/new returning the generated key.
	Key     string `json:"key,omitempty"`
	TokenID string `json:"token_id,omitempty"`
}

// internalUserToCompat maps an internal user to LiteLLM's /user/new response
// field names (snake_case) rather than NixLLM's internal casing.
func internalUserToCompat(u store.InternalUser) liteLLMCompatUserResponse {
	return liteLLMCompatUserResponse{
		UserID:              u.ID,
		UserAlias:           u.UserAlias,
		UserEmail:           u.UserEmail,
		UserRole:            u.UserRole,
		Models:              u.Models,
		Metadata:            u.Metadata,
		MaxBudget:           u.MaxBudget,
		BudgetDuration:      u.BudgetDuration,
		BudgetResetAt:       u.BudgetResetAt,
		RPMLimit:            u.RPMLimit,
		TPMLimit:            u.TPMLimit,
		MaxParallelRequests: u.MaxParallelRequests,
		Spend:               u.Spend,
		CreatedAt:           u.CreatedAt,
		UpdatedAt:           u.UpdatedAt,
	}
}

// CreateLiteLLMUserCompat is the runtime-backed POST /litellm/user/new handler.
// It binds a LiteLLM-shaped request and creates an internal user, returning the
// created user with LiteLLM field names. When auto_create_key is true (the
// default), it also generates a runtime API key owned by the user and returns
// the plaintext secret in `key` plus the row id in `token_id`.
func (h *Handler) CreateLiteLLMUserCompat(c *gin.Context) {
	users, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatUserNewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	u, err := users.Create(c.Request.Context(), store.InternalUser{
		ID:                  req.UserID,
		UserAlias:           req.UserAlias,
		UserEmail:           req.UserEmail,
		UserRole:            req.UserRole,
		Models:              req.Models,
		Metadata:            req.Metadata,
		MaxBudget:           req.MaxBudget,
		BudgetDuration:      req.BudgetDuration,
		RPMLimit:            req.RPMLimit,
		TPMLimit:            req.TPMLimit,
		MaxParallelRequests: req.MaxParallelRequests,
	})
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	resp := internalUserToCompat(u)

	autoCreate := true
	if req.AutoCreateKey != nil {
		autoCreate = *req.AutoCreateKey
	}
	if autoCreate {
		pol := store.Policy{
			AllowedModels:    req.Models,
			BudgetMonthlyUSD: req.MaxBudget,
		}
		var expires *time.Time
		if exp, err := parseDurationToExpiry(req.Duration, time.Now()); err != nil {
			litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		} else {
			expires = exp
		}
		key, secret, err := keys.Create(c.Request.Context(), "", req.KeyAlias, "", expires, req.Metadata, &pol)
		if err != nil {
			litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if err := keys.UpdateUserID(c.Request.Context(), key.ID, u.ID); err != nil {
			litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		resp.Key = secret
		resp.TokenID = key.ID
	}
	c.JSON(http.StatusOK, resp)
}

// ListLiteLLMUsersCompat is the runtime-backed GET /litellm/user/list handler.
// It returns a paginated page of internal users with LiteLLM field names and
// UserListResponse's required fields (users/total/page/page_size/total_pages).
// Query params: role, user_ids, user_email, search, page, page_size, sort_by,
// sort_order (default asc), organization_ids, team, sso_user_ids.
func (h *Handler) ListLiteLLMUsersCompat(c *gin.Context) {
	users, _, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	page := atoiDefault(c.Query("page"), 1)
	pageSize := atoiDefault(c.Query("page_size"), 25)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	sortBy := c.Query("sort_by")
	if sortBy == "" {
		sortBy = "spend"
	}
	// LiteLLM's /user/list defaults sort_order to "asc".
	sortOrder := c.Query("sort_order")
	if sortOrder == "" {
		sortOrder = "asc"
	}
	filter := store.ListFilter{
		Role:      c.Query("role"),
		Search:    c.Query("search"),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}
	// user_email narrows via the search path (alias OR email match) when
	// provided; the other spec params (user_ids/sso_user_ids/team/
	// organization_ids) have no runtime-store equivalent and are accepted but
	// not applied, matching the runtime store's capabilities.
	if email := strings.TrimSpace(c.Query("user_email")); email != "" {
		filter.Search = email
	}
	list, total, err := users.ListWithSpend(c.Request.Context(), filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	compatUsers := make([]liteLLMCompatUserResponse, 0, len(list))
	for _, u := range list {
		compatUsers = append(compatUsers, internalUserToCompat(u))
	}
	c.JSON(http.StatusOK, gin.H{
		"users":       compatUsers,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages(total, pageSize),
	})
}

// GetLiteLLMUserCompat is the runtime-backed GET /litellm/user/info handler.
// It returns LiteLLM's UserInfoResponse shape: {user_id, user_info, keys,
// teams}. user_info carries the user's LiteLLM fields; keys is a list of the
// user's API keys; teams is always [] (no team model on the runtime store).
func (h *Handler) GetLiteLLMUserCompat(c *gin.Context) {
	users, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	userID := c.Query("user_id")
	if userID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "user_id is required")
		return
	}
	u, err := users.Get(c.Request.Context(), userID)
	if err != nil {
		translateLiteLLMUserCompatError(c, err)
		return
	}
	keyList, _, err := keys.ListPagedFiltered(c.Request.Context(), 1, 200, store.APIKeyListFilter{UserID: userID})
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	userKeys := make([]liteLLMCompatKeyResponse, 0, len(keyList))
	for _, k := range keyList {
		userKeys = append(userKeys, keyToCompat(k, nil))
	}
	c.JSON(http.StatusOK, gin.H{
		"user_id":   u.ID,
		"user_info": internalUserToCompat(u),
		"keys":      userKeys,
		"teams":     []any{},
	})
}

// liteLLMCompatUserUpdateRequest is the JSON payload for POST /litellm/user/update.
// All optional fields are pointers so an unset field is left untouched by the
// store update. Mirrors LiteLLM's UpdateUserRequest field names.
type liteLLMCompatUserUpdateRequest struct {
	UserID              string          `json:"user_id"`
	UserAlias           *string         `json:"user_alias"`
	UserEmail           *string         `json:"user_email"`
	UserRole            *string         `json:"user_role"`
	Models              *[]string       `json:"models"`
	Metadata            *map[string]any `json:"metadata"`
	MaxBudget           *float64        `json:"max_budget"`
	BudgetDuration      *string         `json:"budget_duration"`
	RPMLimit            *int64          `json:"rpm_limit"`
	TPMLimit            *int64          `json:"tpm_limit"`
	MaxParallelRequests *int            `json:"max_parallel_requests"`
	KeyAlias            *string         `json:"key_alias"`
	Duration            *string         `json:"duration"`
}

// UpdateLiteLLMUserCompat is the runtime-backed POST /litellm/user/update handler.
// user_id may be delivered in the JSON body (preferred) or the user_id query
// param. Returns the updated user with LiteLLM field names.
func (h *Handler) UpdateLiteLLMUserCompat(c *gin.Context) {
	users, _, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatUserUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	userID := req.UserID
	if userID == "" {
		userID = c.Query("user_id")
	}
	if userID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "user_id is required")
		return
	}
	upd := store.InternalUserUpdate{
		UserAlias:           req.UserAlias,
		UserEmail:           req.UserEmail,
		UserRole:            req.UserRole,
		Models:              req.Models,
		Metadata:            req.Metadata,
		MaxBudget:           req.MaxBudget,
		BudgetDuration:      req.BudgetDuration,
		RPMLimit:            req.RPMLimit,
		TPMLimit:            req.TPMLimit,
		MaxParallelRequests: req.MaxParallelRequests,
	}
	if err := users.Update(c.Request.Context(), userID, upd); err != nil {
		translateLiteLLMUserCompatError(c, err)
		return
	}
	// Policy cache must be invalidated so the next request re-reads the user.
	h.invalidatePolicyCache()
	u, err := users.Get(c.Request.Context(), userID)
	if err != nil {
		translateLiteLLMUserCompatError(c, err)
		return
	}
	c.JSON(http.StatusOK, internalUserToCompat(u))
}

// DeleteLiteLLMUserCompat is the runtime-backed POST /litellm/user/delete handler.
// The body is LiteLLM's DeleteUserRequest {"user_ids": [...]}; it deletes each
// user (and its associated runtime keys) and returns the number of users
// deleted, matching LiteLLM's deleted_users count.
func (h *Handler) DeleteLiteLLMUserCompat(c *gin.Context) {
	users, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.UserIDs) == 0 {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "user_ids is required")
		return
	}
	// Delete each user's keys first (the runtime FK is enforced at the store
	// level, and LiteLLM also deletes associated keys), then the users.
	deleted := 0
	for _, userID := range req.UserIDs {
		keyList, _, err := keys.ListPagedFiltered(c.Request.Context(), 1, 200, store.APIKeyListFilter{UserID: userID})
		if err != nil {
			litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		for _, k := range keyList {
			if err := keys.Delete(c.Request.Context(), k.ID); err != nil && !errors.Is(err, store.ErrAPIKeyNotFound) {
				litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
		}
		if err := users.Delete(c.Request.Context(), userID); err != nil {
			if errors.Is(err, store.ErrInternalUserNotFound) {
				continue // a missing user is not an error in a batch delete
			}
			litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		deleted++
	}
	// Policy cache must be invalidated so the next request re-reads the snapshot.
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, deleted)
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

// liteLLMCompatKeyGenerateRequest is the JSON payload for POST /litellm/key/generate.
// It mirrors LiteLLM's GenerateKeyRequest field names; the runtime maps the
// supported subset onto a store.Policy / APIKey. tpm_limit / budget_duration
// are accepted (mapped where the runtime supports them) so spec-parity callers
// are not rejected.
type liteLLMCompatKeyGenerateRequest struct {
	UserID              string         `json:"user_id"`
	Models              []string       `json:"models"`
	Metadata            map[string]any `json:"metadata"`
	MaxBudget           *float64       `json:"max_budget"`
	BudgetDuration      string         `json:"budget_duration"`
	RPMLimit            *int           `json:"rpm_limit"`
	TPMLimit            *int           `json:"tpm_limit"`
	MaxParallelRequests *int           `json:"max_parallel_requests"`
	KeyAlias            string         `json:"key_alias"`
	Alias               string         `json:"alias"`
	KeyName             string         `json:"key_name"`
	Name                string         `json:"name"`
	Duration            string         `json:"duration"`
	Expires             *time.Time     `json:"expires"`
}

// liteLLMCompatKeyResponse is the typed /litellm/key/generate + /litellm/key/info
// response. Typed struct + omitempty so unset optional fields are omitted from
// the JSON, matching LiteLLM's Optional-model / wire parity. Secret is only
// populated on generate — LiteLLM returns the plaintext key once at creation
// and omits it on read.
type liteLLMCompatKeyResponse struct {
	Key        string         `json:"key"`
	UserID     string         `json:"user_id,omitempty"`
	KeyName    string         `json:"key_name,omitempty"`
	KeyAlias   string         `json:"key_alias,omitempty"`
	Models     []string       `json:"models,omitempty"`
	MaxBudget  *float64       `json:"max_budget,omitempty"`
	Spend      float64        `json:"spend"`
	CreatedAt  *time.Time     `json:"created_at,omitempty"`
	UpdatedAt  *time.Time     `json:"updated_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Expires    *time.Time     `json:"expires,omitempty"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
	TokenID    string         `json:"token_id,omitempty"`
	Status     string         `json:"status,omitempty"`
	Secret     string         `json:"secret,omitempty"`
}

// keyToCompat maps a runtime APIKey + Policy to LiteLLM's /key response field
// names (snake_case). spend is not tracked on the runtime key row, so it is
// surfaced as 0. models / max_budget come from the policy.
func keyToCompat(k *store.APIKey, pol *store.Policy) liteLLMCompatKeyResponse {
	var models []string
	var maxBudget *float64
	if pol != nil {
		models = pol.AllowedModels
		maxBudget = pol.BudgetMonthlyUSD
	}
	var createdAt *time.Time
	if !k.CreatedAt.IsZero() {
		createdAt = &k.CreatedAt
	}
	var updatedAt *time.Time
	if !k.UpdatedAt.IsZero() {
		updatedAt = &k.UpdatedAt
	}
	return liteLLMCompatKeyResponse{
		Key:        k.ID,
		UserID:     k.UserID,
		KeyName:    k.Name,
		KeyAlias:   k.KeyAlias,
		Models:     models,
		MaxBudget:  maxBudget,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
		Metadata:   k.Metadata,
		ExpiresAt:  k.ExpiresAt,
		Expires:    k.ExpiresAt,
		LastUsedAt: k.LastUsedAt,
		TokenID:    k.ID,
		Status:     k.Status,
	}
}

// GenerateLiteLLMKeyCompat is the runtime-backed POST /litellm/key/generate
// handler. It creates a runtime API key owned by an internal user and returns
// the plaintext secret exactly once (LiteLLM's generate contract) in `key`,
// plus the row id in `token_id`.
func (h *Handler) GenerateLiteLLMKeyCompat(c *gin.Context) {
	users, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatKeyGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// The runtime Policy has no tpm_limit or budget_duration columns. Accept
	// them for spec-parity but note the limitation is surfaced via the policy
	// row's supported subset. (LiteLLM itself accepts them on generate.)
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		// Fall back to the management token's default user id when its opt-in
		// endpoint allow-list covers this request.
		userID = defaultUserIDFromContext(c)
	}
	if userID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "user_id is required")
		return
	}
	// Validate the owner exists; surface a 404 when the caller picked a stale id.
	if _, err := users.Get(c.Request.Context(), userID); err != nil {
		translateLiteLLMUserCompatError(c, err)
		return
	}
	var expires *time.Time
	if req.Expires != nil {
		expires = req.Expires
	} else if req.Duration != "" {
		exp, err := parseDurationToExpiry(req.Duration, time.Now())
		if err != nil {
			litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		expires = exp
	}
	pol := store.Policy{
		AllowedModels:       req.Models,
		RPMLimit:            req.RPMLimit,
		BudgetMonthlyUSD:    req.MaxBudget,
		MaxParallelRequests: req.MaxParallelRequests,
	}
	name := req.KeyName
	if name == "" {
		name = req.Name
	}
	alias := req.KeyAlias
	if alias == "" {
		alias = req.Alias
	}
	key, secret, err := keys.Create(c.Request.Context(), name, alias, "", expires, req.Metadata, &pol)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Stamp the owner assignment.
	keyID := key.ID
	if err := keys.UpdateUserID(c.Request.Context(), keyID, userID); err != nil {
		// Roll back: drop the orphaned key rather than presenting a half-assigned row.
		if delErr := keys.Delete(c.Request.Context(), keyID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", keyID).
				Warn("management: failed to roll back orphaned litellm compat key after user_id attach failure")
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Re-read so the response carries the freshly-stamped user_id and policy.
	reloaded, reloadedPolicy, err := keys.LookupByID(c.Request.Context(), keyID)
	if err != nil {
		if delErr := keys.Delete(c.Request.Context(), keyID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", keyID).
				Warn("management: failed to roll back orphaned litellm compat key after re-read failure")
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	resp := keyToCompat(reloaded, reloadedPolicy)
	resp.Key = secret
	resp.Secret = secret
	c.JSON(http.StatusOK, resp)
}

// GetLiteLLMKeyCompat is the runtime-backed GET /litellm/key/info handler. The
// key is read from the key query param (LiteLLM's convention): a plaintext
// secret, a sha256 hash, an internal key id, or a key_alias all resolve. The
// secret is NOT returned on read — LiteLLM omits it. Returns the LiteLLM
// {"key": <identifier>, "info": {…}} envelope.
func (h *Handler) GetLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	keyID := strings.TrimSpace(c.Query("key"))
	if keyID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	key, pol, ok := h.resolveLiteLLMKeyID(c, keys, keyID)
	if !ok {
		return
	}
	info := keyToCompat(key, pol)
	c.JSON(http.StatusOK, gin.H{
		"key":  keyID,
		"info": info,
	})
}

// ListLiteLLMKeysCompat is the runtime-backed GET /litellm/key/list handler. It
// returns a paginated page of runtime API keys with LiteLLM field names and
// KeyListResponseObject's required fields (keys/total_count/current_page/
// total_pages). Query params: page, size (default 10, max 100), user_id,
// status, search, sort_by, sort_order (default desc), key_alias.
func (h *Handler) ListLiteLLMKeysCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	page := atoiDefault(c.Query("page"), 1)
	size := atoiDefault(c.Query("size"), 10)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	if size > 100 {
		size = 100
	}
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	filter := store.APIKeyListFilter{
		Status:    strings.TrimSpace(c.Query("status")),
		UserID:    strings.TrimSpace(c.Query("user_id")),
		Search:    strings.TrimSpace(c.Query("search")),
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}
	// key_alias narrows via the search path (name/alias/key_prefix substring).
	if alias := strings.TrimSpace(c.Query("key_alias")); alias != "" {
		filter.Search = alias
	}
	list, total, err := keys.ListPagedFiltered(c.Request.Context(), page, size, filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	compat := make([]liteLLMCompatKeyResponse, 0, len(list))
	for _, k := range list {
		compat = append(compat, keyToCompat(k, nil))
	}
	c.JSON(http.StatusOK, gin.H{
		"keys":         compat,
		"total_count":  total,
		"current_page": page,
		"total_pages":  totalPages(total, size),
	})
}

// liteLLMCompatKeyUpdateRequest is the JSON payload for POST /litellm/key/update.
// key (the key id, plaintext secret, hash, or key_alias) identifies the target;
// all optional fields are pointers so an unset field is left untouched by the
// store update. Mirrors LiteLLM's UpdateKeyRequest identifier handling.
type liteLLMCompatKeyUpdateRequest struct {
	Key       string          `json:"key"`
	KeyAlias  *string         `json:"key_alias"`
	Name      *string         `json:"name"`
	Alias     *string         `json:"alias"`
	Status    *string         `json:"status"`
	Metadata  *map[string]any `json:"metadata"`
	Models    *[]string       `json:"models"`
	MaxBudget *float64        `json:"max_budget"`
	Duration  *string         `json:"duration"`
	Expires   *time.Time      `json:"expires"`
}

// UpdateLiteLLMKeyCompat is the runtime-backed POST /litellm/key/update handler.
// It applies the non-nil optional fields and returns the updated key with
// LiteLLM field names.
func (h *Handler) UpdateLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatKeyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	keyID := strings.TrimSpace(req.Key)
	if keyID == "" && req.KeyAlias != nil {
		keyID = *req.KeyAlias
	}
	if keyID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	// Resolve the identifier to the internal row id so mutations apply to the
	// right row and the response echoes LiteLLM's identifier.
	key, _, ok := h.resolveLiteLLMKeyID(c, keys, keyID)
	if !ok {
		return
	}
	id := key.ID
	if req.Name != nil {
		if err := keys.Rename(c.Request.Context(), id, *req.Name); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Alias != nil {
		if err := keys.UpdateAlias(c.Request.Context(), id, *req.Alias); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.KeyAlias != nil {
		if err := keys.UpdateAlias(c.Request.Context(), id, *req.KeyAlias); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Status != nil {
		if err := keys.UpdateStatus(c.Request.Context(), id, *req.Status); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Metadata != nil {
		if err := keys.UpdateMetadata(c.Request.Context(), id, *req.Metadata); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Expires != nil {
		if err := keys.UpdateExpiry(c.Request.Context(), id, req.Expires); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	key, pol, err := keys.LookupByID(c.Request.Context(), id)
	if err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	// Policy cache must be invalidated so the next request re-reads the key.
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, keyToCompat(key, pol))
}

// RegenerateLiteLLMKeyCompat is the runtime-backed POST /litellm/key/regenerate
// handler. The key is read from the key query param (LiteLLM's convention). It
// rotates the key's secret and returns the new plaintext secret exactly once
// (LiteLLM's regenerate contract) as a GenerateKeyResponse-shaped object.
func (h *Handler) RegenerateLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	keyID := strings.TrimSpace(c.Query("key"))
	if keyID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	key, _, ok := h.resolveLiteLLMKeyID(c, keys, keyID)
	if !ok {
		return
	}
	id := key.ID
	newSecret, err := keys.Regenerate(c.Request.Context(), id, "")
	if err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	// Policy cache must be invalidated so the rotated key's old (keyid, hash)
	// snapshot does not keep the stale secret resolving until the cache TTL.
	h.invalidatePolicyCache()
	reloaded, pol, err := keys.LookupByID(c.Request.Context(), id)
	if err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	resp := keyToCompat(reloaded, pol)
	resp.Key = newSecret
	resp.Secret = newSecret
	c.JSON(http.StatusOK, resp)
}

// DeleteLiteLLMKeyCompat is the runtime-backed POST /litellm/key/delete handler.
// The body is LiteLLM's KeyRequest {"keys": [...]} or {"key_aliases": [...]}.
// It deletes each key and returns {"deleted_keys": [...]} (the identifiers
// echoed back), matching LiteLLM's contract.
func (h *Handler) DeleteLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req struct {
		Keys       []string `json:"keys"`
		KeyAliases []string `json:"key_aliases"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var identifiers []string
	if len(req.Keys) > 0 {
		identifiers = req.Keys
	} else if len(req.KeyAliases) > 0 {
		identifiers = req.KeyAliases
	}
	if len(identifiers) == 0 {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "keys or key_aliases is required")
		return
	}
	deleted := make([]string, 0, len(identifiers))
	for _, ident := range identifiers {
		key, _, ok := h.resolveLiteLLMKeyID(c, keys, ident)
		if !ok {
			return
		}
		if err := keys.Delete(c.Request.Context(), key.ID); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
		deleted = append(deleted, ident)
	}
	// Policy cache must be invalidated so the next request re-reads the snapshot.
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"deleted_keys": deleted})
}

// ---------------------------------------------------------------------------
// Spend
// ---------------------------------------------------------------------------

// liteLLMCompatSpendLogResponse is the LiteLLM spend-log shape returned by
// GET /litellm/spend/logs. Optional fields are omitempty to keep the payload
// tight, mirroring the user/key compat responses.
type liteLLMCompatSpendLogResponse struct {
	RequestID        string  `json:"request_id,omitempty"`
	APIKey           string  `json:"api_key,omitempty"`
	Model            string  `json:"model,omitempty"`
	APIBase          string  `json:"api_base,omitempty"`
	CallType         string  `json:"call_type,omitempty"`
	Spend            float64 `json:"spend"`
	TotalTokens      int64   `json:"total_tokens"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheTokens      int64   `json:"cache_tokens,omitempty"`
	StartTime        string  `json:"startTime,omitempty"`
	EndTime          string  `json:"endTime,omitempty"`
	User             string  `json:"user,omitempty"`
	Status           int     `json:"status,omitempty"`
	RequesterIP      string  `json:"requester_ip_address,omitempty"`
}

// spendLogToCompat maps a UsageEventRow onto the LiteLLM spend-log shape.
// The row carries KeyAlias rather than the raw API key, and the user/principal
// is never returned to API callers, so the user field is left empty for now.
func spendLogToCompat(r store.UsageEventRow) liteLLMCompatSpendLogResponse {
	status := http.StatusOK
	if r.Failed {
		status = r.FailStatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
	}
	end := r.RequestedAt
	return liteLLMCompatSpendLogResponse{
		RequestID:        r.RequestID,
		APIKey:           r.KeyAlias,
		Model:            r.Model,
		APIBase:          r.Endpoint,
		CallType:         "litellm_completion",
		Spend:            r.CostUSD,
		TotalTokens:      r.TotalTokens,
		PromptTokens:     r.InputTokens,
		CompletionTokens: r.OutputTokens,
		CacheTokens:      r.CachedTokens,
		StartTime:        r.RequestedAt.Format(time.RFC3339),
		EndTime:          end.Format(time.RFC3339),
		User:             "",
		Status:           status,
		RequesterIP:      r.ClientIP,
	}
}

// ListLiteLLMSpendLogsCompat handles GET /litellm/spend/logs, returning the
// spend-log rows as a DIRECT array (LiteLLM's contract), not a paginated
// envelope. Query params: api_key, user_id, request_id, start_date, end_date,
// summarize (accepted; no summarization is applied).
func (h *Handler) ListLiteLLMSpendLogsCompat(c *gin.Context) {
	_, _, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	filter := store.UsageFilter{
		UserID:    c.Query("user_id"),
		Model:     c.Query("model"),
		RequestID: c.Query("request_id"),
	}
	if ks := strings.TrimSpace(c.Query("api_key")); ks != "" {
		// Accept the same value the endpoint returns as api_key (the key_alias,
		// falling back to the key name) as well as the raw internal key id. The
		// store matches either against usage_events.api_key_id or the projected
		// non-secret label.
		filter.KeyLabel = ks
	}
	if from, err := time.Parse(time.RFC3339, c.Query("start_date")); err == nil {
		filter.From = from
	} else if c.Query("start_date") != "" {
		if d, err := time.Parse("2006-01-02", c.Query("start_date")); err == nil {
			filter.From = d
		}
	}
	if to, err := time.Parse(time.RFC3339, c.Query("end_date")); err == nil {
		filter.To = to
	} else if c.Query("end_date") != "" {
		if d, err := time.Parse("2006-01-02", c.Query("end_date")); err == nil {
			filter.To = d.Add(24 * time.Hour)
		}
	}
	// LiteLLM's /spend/logs caps at 10,000 rows ordered by startTime desc.
	rows, _, err := usage.SelectEvents(c.Request.Context(), filter, 1, 10000)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	data := make([]liteLLMCompatSpendLogResponse, 0, len(rows))
	for _, r := range rows {
		data = append(data, spendLogToCompat(r))
	}
	c.JSON(http.StatusOK, data)
}

// liteLLMCompatSpendReportRow is one row of the spend report endpoints
// (/global/spend/report, /key/spend/report, /user/spend/report). It mirrors
// LiteLLM's per-api_key spend report shape: total cost/tokens plus a
// per-model breakdown.
type liteLLMCompatSpendReportRow struct {
	APIKey            string                          `json:"api_key"`
	TotalCost         float64                         `json:"total_cost"`
	TotalInputTokens  int64                           `json:"total_input_tokens"`
	TotalOutputTokens int64                           `json:"total_output_tokens"`
	ModelDetails      []liteLLMCompatSpendModelDetail `json:"model_details"`
}

// liteLLMCompatSpendModelDetail is one per-model entry inside a spend report
// row's model_details array.
type liteLLMCompatSpendModelDetail struct {
	Model             string  `json:"model"`
	TotalCost         float64 `json:"total_cost"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
}

// litellmSpendModelTotals accumulates one model's cost/token totals while
// building a spend report row.
type litellmSpendModelTotals struct {
	cost   float64
	input  int64
	output int64
}

// collectSpendReportRows aggregates a page of usage event rows into the
// LiteLLM spend-report shape, keyed by the api key that produced the spend.
// It is computed in Go from SelectEvents because the store's SQL aggregation
// only returns the principal alias (not the internal key id), and a single
// event row already carries both (APIKeyID + KeyAlias). Rows whose key is
// missing are grouped under an empty api_key, matching an empty-key event.
func collectSpendReportRows(events []store.UsageEventRow) []liteLLMCompatSpendReportRow {
	byKey := map[string]map[string]*litellmSpendModelTotals{}
	keyOrder := []string{}
	modelOrder := map[string][]string{}
	for _, ev := range events {
		key := ev.APIKeyID
		models, ok := byKey[key]
		if !ok {
			models = map[string]*litellmSpendModelTotals{}
			byKey[key] = models
			keyOrder = append(keyOrder, key)
		}
		totals, ok := models[ev.Model]
		if !ok {
			totals = &litellmSpendModelTotals{}
			models[ev.Model] = totals
			modelOrder[key] = append(modelOrder[key], ev.Model)
		}
		totals.cost += ev.CostUSD
		totals.input += ev.InputTokens
		totals.output += ev.OutputTokens
	}
	out := make([]liteLLMCompatSpendReportRow, 0, len(keyOrder))
	for _, key := range keyOrder {
		row := liteLLMCompatSpendReportRow{
			APIKey:       key,
			ModelDetails: make([]liteLLMCompatSpendModelDetail, 0, len(byKey[key])),
		}
		for _, model := range modelOrder[key] {
			totals := byKey[key][model]
			row.TotalCost += totals.cost
			row.TotalInputTokens += totals.input
			row.TotalOutputTokens += totals.output
			row.ModelDetails = append(row.ModelDetails, liteLLMCompatSpendModelDetail{
				Model:             model,
				TotalCost:         totals.cost,
				TotalInputTokens:  totals.input,
				TotalOutputTokens: totals.output,
			})
		}
		out = append(out, row)
	}
	return out
}

// litellmSpendDateFilter parses the start_date/end_date query params (both
// date-only, LiteLLM's convention for the spend reports) into a UsageFilter
// time window. An end_date is inclusive of its whole day.
func litellmSpendDateFilter(c *gin.Context) store.UsageFilter {
	filter := store.UsageFilter{}
	if from, err := time.Parse("2006-01-02", c.Query("start_date")); err == nil {
		filter.From = from
	}
	if to, err := time.Parse("2006-01-02", c.Query("end_date")); err == nil {
		filter.To = to.Add(24 * time.Hour)
	}
	return filter
}

// GetLiteLLMGlobalSpendReport handles GET /litellm/global/spend/report,
// returning the LiteLLM /global/spend/report shape: one row per api key with a
// per-model breakdown. start_date and end_date are both required (LiteLLM
// 400s otherwise). When api_key or internal_user_id is provided the report is
// scoped to that entity; group_by=customer/team are accepted but not applied.
func (h *Handler) GetLiteLLMGlobalSpendReport(c *gin.Context) {
	_, keys, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	if c.Query("start_date") == "" || c.Query("end_date") == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "start_date and end_date are required")
		return
	}
	filter := litellmSpendDateFilter(c)
	if ks := c.Query("api_key"); ks != "" {
		key, _, ok := h.resolveLiteLLMKeyID(c, keys, ks)
		if !ok {
			return
		}
		filter.APIKeyID = key.ID
	}
	if uid := c.Query("internal_user_id"); uid != "" {
		filter.UserID = uid
	}
	if tid := c.Query("team_id"); tid != "" {
		filter.UserID = tid
	}
	// The store's SQL aggregation can't key rows by internal key id (it only
	// surfaces the principal alias), so page through events and aggregate in Go.
	events, err := litellmSpendEvents(c, usage, filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, collectSpendReportRows(events))
}

// GetLiteLLMKeySpendReport handles GET /litellm/key/spend/report, returning the
// spend for one key (or all keys when api_key is omitted) in the spend-report
// shape. The api_key param accepts a plaintext secret, hash, id, or alias.
func (h *Handler) GetLiteLLMKeySpendReport(c *gin.Context) {
	_, keys, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	filter := litellmSpendDateFilter(c)
	if ks := c.Query("api_key"); ks != "" {
		key, _, ok := h.resolveLiteLLMKeyID(c, keys, ks)
		if !ok {
			return
		}
		filter.APIKeyID = key.ID
	}
	events, err := litellmSpendEvents(c, usage, filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, collectSpendReportRows(events))
}

// GetLiteLLMUserSpendReport handles GET /litellm/user/spend/report, returning
// the spend for one user (or all users when internal_user_id is omitted) in
// the spend-report shape.
func (h *Handler) GetLiteLLMUserSpendReport(c *gin.Context) {
	_, _, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	filter := litellmSpendDateFilter(c)
	if uid := c.Query("internal_user_id"); uid != "" {
		filter.UserID = uid
	}
	events, err := litellmSpendEvents(c, usage, filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, collectSpendReportRows(events))
}

// litellmSpendEvents pages through every usage event matching filter (up to
// 10,000 rows, matching LiteLLM's /spend/logs cap) so the spend-report rows
// can be aggregated in Go. SelectEvents caps a single page at 200 rows, hence
// the paging loop.
func litellmSpendEvents(c *gin.Context, usage *store.UsageStore, filter store.UsageFilter) ([]store.UsageEventRow, error) {
	const pageSize = 200
	const maxEvents = 10000
	var all []store.UsageEventRow
	for page := 1; ; page++ {
		rows, _, err := usage.SelectEvents(c.Request.Context(), filter, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) < pageSize || len(all) >= maxEvents {
			break
		}
	}
	if len(all) > maxEvents {
		all = all[:maxEvents]
	}
	return all, nil
}

// GetLiteLLMSpendTags handles GET /litellm/spend/tags, returning the per-tag
// spend aggregation. The runtime usage_events table has no request_tags column,
// so this returns an empty array (the LiteLLM shape) rather than erroring.
func (h *Handler) GetLiteLLMSpendTags(c *gin.Context) {
	if _, _, _, ok := h.requireLiteLLMRuntime(c); !ok {
		return
	}
	c.JSON(http.StatusOK, []any{})
}
