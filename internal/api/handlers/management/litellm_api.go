package management

import (
	"errors"
	"net/http"
	"strconv"
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
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			litellmCompatError(c, http.StatusNotFound, "not_found", "key not found")
			return
		}
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, keyToCompat(key, pol))
}
