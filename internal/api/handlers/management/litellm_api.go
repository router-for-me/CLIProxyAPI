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

// liteLLMCompatUserNewRequest is the JSON payload for POST /litellm/user/new.
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
}

// CreateLiteLLMUserCompat is the runtime-backed POST /litellm/user/new handler.
// It binds a LiteLLM-shaped request and creates an internal user, returning the
// created user with LiteLLM field names.
func (h *Handler) CreateLiteLLMUserCompat(c *gin.Context) {
	users, _, _, ok := h.requireLiteLLMRuntime(c)
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
	c.JSON(http.StatusCreated, internalUserToCompat(u))
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

// ListLiteLLMUsersCompat is the runtime-backed GET /litellm/user/list handler.
// It returns a paginated page of internal users with LiteLLM field names. Query
// params: role, search, page, page_size, sort_by (default spend), sort_order
// (default desc).
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
	sortOrder := c.Query("sort_order")
	if sortOrder == "" {
		sortOrder = "desc"
	}
	list, total, err := users.ListWithSpend(c.Request.Context(), store.ListFilter{
		Role:      c.Query("role"),
		Search:    c.Query("search"),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    sortBy,
		SortOrder: sortOrder,
	})
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	compatUsers := make([]liteLLMCompatUserResponse, 0, len(list))
	for _, u := range list {
		compatUsers = append(compatUsers, internalUserToCompat(u))
	}
	c.JSON(http.StatusOK, gin.H{
		"users":     compatUsers,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetLiteLLMUserCompat is the runtime-backed GET /litellm/user/info handler. The
// user id is read from the user_id query param (LiteLLM's convention).
func (h *Handler) GetLiteLLMUserCompat(c *gin.Context) {
	users, _, _, ok := h.requireLiteLLMRuntime(c)
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
		if errors.Is(err, store.ErrInternalUserNotFound) {
			litellmCompatError(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, internalUserToCompat(u))
}

// liteLLMCompatUserUpdateRequest is the JSON payload for POST /litellm/user/update.
// All optional fields are pointers so an unset field is left untouched by the
// store update.
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
		if errors.Is(err, store.ErrInternalUserNotFound) {
			litellmCompatError(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Policy cache must be invalidated so the next request re-reads the user.
	h.invalidatePolicyCache()
	u, err := users.Get(c.Request.Context(), userID)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, internalUserToCompat(u))
}

// DeleteLiteLLMUserCompat is the runtime-backed POST /litellm/user/delete handler.
// user_id may be delivered in the JSON body (preferred) or the user_id query
// param. Returns {"deleted": true} on success.
func (h *Handler) DeleteLiteLLMUserCompat(c *gin.Context) {
	users, _, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	userID := ""
	if c.Request.Body != nil {
		var req struct {
			UserID string `json:"user_id"`
		}
		if err := c.ShouldBindJSON(&req); err == nil && req.UserID != "" {
			userID = req.UserID
		}
	}
	if userID == "" {
		userID = c.Query("user_id")
	}
	if userID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "user_id is required")
		return
	}
	if err := users.Delete(c.Request.Context(), userID); err != nil {
		if errors.Is(err, store.ErrInternalUserNotFound) {
			litellmCompatError(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Policy cache must be invalidated so the next request re-reads the snapshot.
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"id": userID, "deleted": true})
}

// liteLLMCompatKeyGenerateRequest is the JSON payload for POST /litellm/key/generate.
// The LiteLLM key owner is user_id (REQUIRED); the remaining fields map onto a
// runtime store.Policy / APIKey.
type liteLLMCompatKeyGenerateRequest struct {
	UserID         string         `json:"user_id"`
	Models         []string       `json:"models"`
	Metadata       map[string]any `json:"metadata"`
	MaxBudget      *float64       `json:"max_budget"`
	BudgetDuration string         `json:"budget_duration"`
	RPMLimit       *int           `json:"rpm_limit"`
	TPMLimit       *int           `json:"tpm_limit"`
	Alias          string         `json:"alias"`
	Name           string         `json:"name"`
}

// liteLLMCompatKeyResponse is the typed /litellm/key/generate + /litellm/key/info
// response. Typed struct + omitempty so unset optional fields are omitted from
// the JSON, matching LiteLLM's Optional-model / wire parity (mirrors
// liteLLMCompatUserResponse). Secret is only populated on generate — LiteLLM
// returns the plaintext key once at creation and omits it on read.
type liteLLMCompatKeyResponse struct {
	Key        string         `json:"key"`
	UserID     string         `json:"user_id"`
	Secret     string         `json:"secret,omitempty"`
	KeyAlias   string         `json:"key_alias,omitempty"`
	Models     []string       `json:"models,omitempty"`
	MaxBudget  *float64       `json:"max_budget,omitempty"`
	Spend      float64        `json:"spend"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
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
	return liteLLMCompatKeyResponse{
		Key:        k.ID,
		UserID:     k.UserID,
		KeyAlias:   k.KeyAlias,
		Models:     models,
		MaxBudget:  maxBudget,
		CreatedAt:  k.CreatedAt,
		UpdatedAt:  k.UpdatedAt,
		Metadata:   k.Metadata,
		ExpiresAt:  k.ExpiresAt,
		LastUsedAt: k.LastUsedAt,
	}
}

// GenerateLiteLLMKeyCompat is the runtime-backed POST /litellm/key/generate
// handler. It creates a runtime API key owned by an internal user and returns
// the plaintext secret exactly once (LiteLLM's generate contract).
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
	// The runtime Policy has no tpm_limit or budget_duration columns, so these
	// fields cannot be honored. Reject them explicitly rather than silently
	// dropping the client's intent behind a misleading 200.
	if req.TPMLimit != nil || req.BudgetDuration != "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "tpm_limit / budget_duration are not supported on key generate")
		return
	}
	if req.UserID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "user_id is required")
		return
	}
	// Validate the owner exists; surface a 404 when the caller picked a stale id.
	if _, err := users.Get(c.Request.Context(), req.UserID); err != nil {
		if errors.Is(err, store.ErrInternalUserNotFound) {
			litellmCompatError(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	pol := store.Policy{
		AllowedModels:    req.Models,
		RPMLimit:         req.RPMLimit,
		BudgetMonthlyUSD: req.MaxBudget,
	}
	key, secret, err := keys.Create(c.Request.Context(), req.Name, req.Alias, "", nil, req.Metadata, &pol)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Stamp the owner assignment.
	keyID := key.ID
	if err := keys.UpdateUserID(c.Request.Context(), keyID, req.UserID); err != nil {
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
	resp.Secret = secret
	c.JSON(http.StatusOK, resp)
}

// GetLiteLLMKeyCompat is the runtime-backed GET /litellm/key/info handler. The
// key id is read from the key query param (LiteLLM's convention). The secret is
// NOT returned on read — LiteLLM omits it.
func (h *Handler) GetLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	keyID := c.Query("key")
	if keyID == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	key, pol, err := keys.LookupByID(c.Request.Context(), keyID)
	if err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	c.JSON(http.StatusOK, keyToCompat(key, pol))
}

// ListLiteLLMKeysCompat is the runtime-backed GET /litellm/key/list handler. It
// returns a paginated page of runtime API keys with LiteLLM field names. Query
// params: user_id, status, search, page, page_size, sort_by (default
// created_at), sort_order (default desc). The array key is api_keys, matching
// LiteLLM's key/list contract.
func (h *Handler) ListLiteLLMKeysCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
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
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	filter := store.APIKeyListFilter{
		Status:    strings.TrimSpace(c.Query("status")),
		UserID:    strings.TrimSpace(c.Query("user_id")),
		Search:    strings.TrimSpace(c.Query("search")),
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}
	list, total, err := keys.ListPagedFiltered(c.Request.Context(), page, pageSize, filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	compat := make([]liteLLMCompatKeyResponse, 0, len(list))
	for _, k := range list {
		compat = append(compat, keyToCompat(k, nil))
	}
	c.JSON(http.StatusOK, gin.H{
		"api_keys":  compat,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// liteLLMCompatKeyUpdateRequest is the JSON payload for POST /litellm/key/update.
// key (the key id) is required; all optional fields are pointers so an unset
// field is left untouched by the store update.
type liteLLMCompatKeyUpdateRequest struct {
	Key      string          `json:"key"`
	Name     *string         `json:"name"`
	Alias    *string         `json:"alias"`
	Status   *string         `json:"status"`
	Metadata *map[string]any `json:"metadata"`
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
	if req.Key == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	if req.Name != nil {
		if err := keys.Rename(c.Request.Context(), req.Key, *req.Name); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Alias != nil {
		if err := keys.UpdateAlias(c.Request.Context(), req.Key, *req.Alias); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Status != nil {
		if err := keys.UpdateStatus(c.Request.Context(), req.Key, *req.Status); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	if req.Metadata != nil {
		if err := keys.UpdateMetadata(c.Request.Context(), req.Key, *req.Metadata); err != nil {
			translateLiteLLMKeyCompatError(c, err)
			return
		}
	}
	key, pol, err := keys.LookupByID(c.Request.Context(), req.Key)
	if err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	// Policy cache must be invalidated so the next request re-reads the key.
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, keyToCompat(key, pol))
}

// liteLLMCompatKeyIDRequest is the JSON payload for the key id-only mutations
// POST /litellm/key/regenerate and POST /litellm/key/delete.
type liteLLMCompatKeyIDRequest struct {
	Key string `json:"key"`
}

// RegenerateLiteLLMKeyCompat is the runtime-backed POST /litellm/key/regenerate
// handler. It rotates the key's secret and returns the new plaintext secret
// exactly once (LiteLLM's regenerate contract).
func (h *Handler) RegenerateLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatKeyIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Key == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	newSecret, err := keys.Regenerate(c.Request.Context(), req.Key, "")
	if err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": req.Key, "secret": newSecret})
}

// DeleteLiteLLMKeyCompat is the runtime-backed POST /litellm/key/delete handler.
// It deletes the key and returns {"deleted": true} on success.
func (h *Handler) DeleteLiteLLMKeyCompat(c *gin.Context) {
	_, keys, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatKeyIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Key == "" {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	if err := keys.Delete(c.Request.Context(), req.Key); err != nil {
		translateLiteLLMKeyCompatError(c, err)
		return
	}
	// Policy cache must be invalidated so the next request re-reads the snapshot.
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"key": req.Key, "deleted": true})
}

// liteLLMCompatSpendLogResponse is the LiteLLM spend-log shape returned by
// GET /litellm/spend/logs. Optional fields are omitempty to keep the payload
// tight, mirroring the user/key compat responses.
type liteLLMCompatSpendLogResponse struct {
	RequestID        string  `json:"request_id,omitempty"`
	APIKey           string  `json:"api_key,omitempty"`
	User             string  `json:"user,omitempty"`
	Model            string  `json:"model,omitempty"`
	Spend            float64 `json:"spend"`
	TotalTokens      int64   `json:"total_tokens"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheTokens      int64   `json:"cache_tokens,omitempty"`
	StartTime        string  `json:"startTime,omitempty"`
	EndTime          string  `json:"endTime,omitempty"`
	Status           int     `json:"status"`
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
		Spend:            r.CostUSD,
		TotalTokens:      r.TotalTokens,
		PromptTokens:     r.InputTokens,
		CompletionTokens: r.OutputTokens,
		CacheTokens:      r.CachedTokens,
		StartTime:        r.RequestedAt.Format(time.RFC3339),
		EndTime:          end.Format(time.RFC3339),
		Status:           status,
	}
}

// ListLiteLLMSpendLogsCompat handles GET /litellm/spend/logs, returning the
// paginated spend-log rows in LiteLLM's spend-log shape.
func (h *Handler) ListLiteLLMSpendLogsCompat(c *gin.Context) {
	_, _, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	filter := store.UsageFilter{
		UserID: c.Query("user_id"),
		Model:  c.Query("model"),
	}
	if ks := c.Query("api_key"); ks != "" {
		filter.APIKeyID = ks
	}
	if from, err := time.Parse(time.RFC3339, c.Query("start_date")); err == nil {
		filter.From = from
	}
	if to, err := time.Parse(time.RFC3339, c.Query("end_date")); err == nil {
		filter.To = to
	}
	page := atoiDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := atoiDefault(c.Query("page_size"), 25)
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	rows, total, err := usage.SelectEvents(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	data := make([]liteLLMCompatSpendLogResponse, 0, len(rows))
	for _, r := range rows {
		data = append(data, spendLogToCompat(r))
	}
	c.JSON(http.StatusOK, gin.H{
		"data":      data,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// liteLLMCompatSpendUserResponse is the LiteLLM spend-user shape returned by
// GET /litellm/spend/users.
type liteLLMCompatSpendUserResponse struct {
	UserID        string  `json:"user_id,omitempty"`
	TotalSpend    float64 `json:"total_spend"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens,omitempty"`
	InputTokens   int64   `json:"input_tokens,omitempty"`
	OutputTokens  int64   `json:"output_tokens,omitempty"`
	CacheTokens   int64   `json:"cache_tokens,omitempty"`
}

// ListLiteLLMSpendUsersCompat handles GET /litellm/spend/users, returning the
// per-user spend aggregation in LiteLLM's spend-user shape. Each aggregate row
// is grouped by user_id, so the bucket carries the user identifier.
func (h *Handler) ListLiteLLMSpendUsersCompat(c *gin.Context) {
	_, _, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	filter := store.UsageFilter{GroupBy: "user_id"}
	if from, err := time.Parse(time.RFC3339, c.Query("start_date")); err == nil {
		filter.From = from
	}
	if to, err := time.Parse(time.RFC3339, c.Query("end_date")); err == nil {
		filter.To = to
	}
	rows, err := usage.SelectAggregate(c.Request.Context(), filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	data := make([]liteLLMCompatSpendUserResponse, 0, len(rows))
	for _, a := range rows {
		data = append(data, liteLLMCompatSpendUserResponse{
			UserID:        a.Bucket,
			TotalSpend:    a.CostUSD,
			TotalRequests: a.RequestCount,
			TotalTokens:   a.TotalTokens,
			InputTokens:   a.InputTokens,
			OutputTokens:  a.OutputTokens,
			CacheTokens:   a.CachedTokens,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// GetLiteLLMGlobalSpendCompat handles GET /litellm/global/spend, returning the
// total spend across all usage in LiteLLM's global-spend shape.
func (h *Handler) GetLiteLLMGlobalSpendCompat(c *gin.Context) {
	_, _, usage, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	filter := store.UsageFilter{}
	if from, err := time.Parse(time.RFC3339, c.Query("start_date")); err == nil {
		filter.From = from
	}
	if to, err := time.Parse(time.RFC3339, c.Query("end_date")); err == nil {
		filter.To = to
	}
	totals, err := usage.SelectTotals(c.Request.Context(), filter)
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total_spend":    totals.CostUSD,
		"total_requests": totals.RequestCount,
	})
}
