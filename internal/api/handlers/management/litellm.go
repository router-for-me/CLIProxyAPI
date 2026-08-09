// Package management: Manage LiteLLM routes.
//
// This file implements the Manage LiteLLM feature under the
// /v0/management/litellm/* route prefix. It manages Internal Users and API
// Keys in the dedicated litellm_* tables, which are intentionally separate
// from the runtime tables (internal_users / api_keys / api_key_policies):
//
//	GET    /litellm/users        — list users (paginated, searchable, sortable)
//	POST   /litellm/users        — create user (auto_create_key default true)
//	GET    /litellm/users/:id    — get one user
//	PATCH  /litellm/users/:id    — partial update
//	DELETE /litellm/users/:id    — delete user (keys are detached, not deleted)
//	POST   /litellm/users/:id/reset-spend — zero a user's spend + re-arm window
//	GET    /litellm/users/:id/keys — list the user's keys (native SQL filter)
//	GET    /litellm/keys         — list keys (paginated, filtered, sortable)
//	POST   /litellm/keys         — create key (user_id required)
//	GET    /litellm/keys/:id     — get one key
//	PATCH  /litellm/keys/:id     — partial update
//	PUT    /litellm/keys/:id/policy — replace the full key policy
//	POST   /litellm/keys/:id/regenerate — rotate the secret (shown once)
//	DELETE /litellm/keys/:id     — delete key
//
// These routes return 503 when the PG store is not configured. Unlike the
// runtime internal-users / api-keys-pg routes, mutations here never touch the
// policy service cache: the litellm_* tables are management-only and the
// request-serving path never reads them.
package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// requireLiteLLM returns the Manage-LiteLLM user and key stores. Returns false
// (after writing a 503 response) when either store is not wired.
func (h *Handler) requireLiteLLM(c *gin.Context) (*store.LiteLLMUserStore, *store.LiteLLMKeyStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, nil, false
	}
	h.mu.Lock()
	users := h.litellmUsers
	keys := h.litellmKeys
	h.mu.Unlock()
	if users == nil || keys == nil {
		h.pgNotConfigured(c)
		return nil, nil, false
	}
	return users, keys, true
}

// liteLLMUserResponse is the canonical single-user payload.
type liteLLMUserResponse struct {
	*store.LiteLLMUser
}

// liteLLMPagedUsersResponse is the paginated user list payload.
type liteLLMPagedUsersResponse struct {
	Users      []store.LiteLLMUser `json:"users"`
	Page       int                 `json:"page"`
	PageSize   int                 `json:"page_size"`
	Total      int64               `json:"total"`
	TotalPages int                 `json:"total_pages"`
}

// liteLLMCreateUserRequest is the JSON payload for POST /litellm/users.
// AutoCreateKey defaults to true (LiteLLM workflow): the response returns the
// freshly-minted user AND a one-shot API key secret bound to that user.
type liteLLMCreateUserRequest struct {
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
	AutoCreateKey       *bool          `json:"auto_create_key,omitempty"`
}

// liteLLMPatchUserRequest supports partial user updates. Pointer-typed fields
// are applied only when non-nil; pass an explicit empty string to clear scalar
// fields.
type liteLLMPatchUserRequest struct {
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

// ListLiteLLMUsers handles GET /v0/management/litellm/users.
//
// Query parameters mirror ListInternalUsers: page, page_size, role, search
// (alias OR email), sort_by (spend | created_at | user_alias), sort_order.
func (h *Handler) ListLiteLLMUsers(c *gin.Context) {
	users, _, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	f := store.LiteLLMListFilter{
		Role:      strings.TrimSpace(c.Query("role")),
		Search:    strings.TrimSpace(c.Query("search")),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "spend"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	list, total, err := users.List(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, liteLLMPagedUsersResponse{
		Users:      list,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreateLiteLLMUser handles POST /v0/management/litellm/users.
//
// LiteLLM workflow: when auto_create_key != false (default true), the endpoint
// also provisions a default API key bound to the new user in the litellm_api_keys
// table and returns the plaintext secret ONCE in the response body. The caller
// is responsible for surfacing it to the operator; the dashboard does so.
func (h *Handler) CreateLiteLLMUser(c *gin.Context) {
	users, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	var req liteLLMCreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	u := store.LiteLLMUser{
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
	created, err := users.Create(c.Request.Context(), u)
	if err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	autoCreate := true
	if req.AutoCreateKey != nil {
		autoCreate = *req.AutoCreateKey
	}
	if !autoCreate {
		c.JSON(http.StatusCreated, gin.H{"user": created})
		return
	}
	// Auto-provision a key for this user (owner = created.ID) with unlimited
	// per-key policy — budget/RPM fall back to the user row, mirroring the
	// LiteLLM user→key hierarchy.
	keyName := strings.TrimSpace(created.UserAlias)
	if keyName == "" {
		keyName = "default-key"
	} else if !strings.HasSuffix(keyName, "-key") {
		keyName = keyName + "-key"
	}
	key, secret, errKey := keys.Create(c.Request.Context(), keyName, "", "", nil, nil, nil, nil)
	if errKey != nil {
		// Best-effort: keep the user row, surface key-provisioning failure
		// without losing the entity. The operator can attach a key later.
		log.WithError(errKey).WithField("user_id", created.ID).
			Warn("management: litellm auto-create-key provisioning failed; user persisted without key")
		c.JSON(http.StatusCreated, gin.H{
			"user":              created,
			"auto_create_error": errKey.Error(),
		})
		return
	}
	if err := keys.UpdateUserID(c.Request.Context(), key.ID, created.ID); err != nil {
		// Roll back the orphaned key so the user has no half-attached entry.
		if delErr := keys.Delete(c.Request.Context(), key.ID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", key.ID).
				Warn("management: failed to roll back orphaned litellm auto-created key")
		}
		log.WithError(err).WithField("user_id", created.ID).
			Warn("management: litellm auto-create-key attach failed; user persisted without key")
		c.JSON(http.StatusCreated, gin.H{
			"user":              created,
			"auto_create_error": err.Error(),
		})
		return
	}
	// Re-read so the response carries the freshly-stamped user_id.
	reloadedKey, pol, reReadErr := keys.LookupByID(c.Request.Context(), key.ID)
	if reReadErr == nil {
		key = reloadedKey
	}
	c.JSON(http.StatusCreated, gin.H{
		"user":    created,
		"api_key": key,
		"policy":  pol,
		"secret":  secret,
	})
}

// GetLiteLLMUser handles GET /v0/management/litellm/users/:id.
func (h *Handler) GetLiteLLMUser(c *gin.Context) {
	users, _, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	u, err := users.Get(c.Request.Context(), id)
	if err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, liteLLMUserResponse{LiteLLMUser: &u})
}

// PatchLiteLLMUser handles PATCH /v0/management/litellm/users/:id.
func (h *Handler) PatchLiteLLMUser(c *gin.Context) {
	users, _, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req liteLLMPatchUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	upd := store.LiteLLMUserUpdate{
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
	if err := users.Update(c.Request.Context(), id, upd); err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	u, err := users.Get(c.Request.Context(), id)
	if err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, liteLLMUserResponse{LiteLLMUser: &u})
}

// DeleteLiteLLMUser handles DELETE /v0/management/litellm/users/:id. Its keys
// are detached (user_id cleared) rather than deleted so an operator can
// reassign them.
func (h *Handler) DeleteLiteLLMUser(c *gin.Context) {
	users, _, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := users.Delete(c.Request.Context(), id); err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// ResetLiteLLMUserSpend handles POST /v0/management/litellm/users/:id/reset-spend.
func (h *Handler) ResetLiteLLMUserSpend(c *gin.Context) {
	users, _, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := users.ResetSpend(c.Request.Context(), id); err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	u, err := users.Get(c.Request.Context(), id)
	if err != nil {
		h.translateLiteLLMUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, liteLLMUserResponse{LiteLLMUser: &u})
}

// liteLLMPagedKeysResponse is the paginated key list payload.
type liteLLMPagedKeysResponse struct {
	Keys       []*store.LiteLLMKey `json:"api_keys"`
	Page       int                 `json:"page"`
	PageSize   int                 `json:"page_size"`
	Total      int64               `json:"total"`
	TotalPages int                 `json:"total_pages"`
}

// ListLiteLLMUserKeys handles GET /v0/management/litellm/users/:id/keys.
// The user_id filter is applied natively in SQL so total reflects every key
// matching the user (unlike the runtime ListInternalUserKeys in-Go filter).
func (h *Handler) ListLiteLLMUserKeys(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	f := store.LiteLLMKeyListFilter{
		Status:    strings.TrimSpace(c.Query("status")),
		UserID:    id,
		Search:    strings.TrimSpace(c.Query("search")),
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	keysList, total, err := keys.ListPaged(c.Request.Context(), page, pageSize, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, liteLLMPagedKeysResponse{
		Keys:       keysList,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// liteLLMCreateKeyRequest is the JSON payload for POST /litellm/keys.
// UserID is REQUIRED (LiteLLM workflow): every Manage-LiteLLM key is owned by
// a litellm_internal_users row. Policy carries the complete LiteLLM-style key
// limits (budget_usd + budget_duration, tpm_limit, aliases, tags).
type liteLLMCreateKeyRequest struct {
	Name      string               `json:"name"`
	Alias     string               `json:"alias,omitempty"`
	Secret    string               `json:"secret,omitempty"` // optional; auto-generated when empty
	UserID    string               `json:"user_id"`          // REQUIRED — owner of the key
	ExpiresAt *time.Time           `json:"expires_at,omitempty"`
	Metadata  map[string]any       `json:"metadata,omitempty"`
	Tags      []string             `json:"tags,omitempty"`
	Policy    *store.LiteLLMPolicy `json:"policy,omitempty"`
}

// liteLLMKeyResponse is the canonical single-key response. The plaintext
// secret is only included in the response of POST /litellm/keys and POST
// /litellm/keys/:id/regenerate; other endpoints omit it.
type liteLLMKeyResponse struct {
	*store.LiteLLMKey
	Secret string               `json:"secret,omitempty"`
	Policy *store.LiteLLMPolicy `json:"policy,omitempty"`
}

// ListLiteLLMKeys handles GET /v0/management/litellm/keys.
//
// Query parameters mirror ListPGAPIKeys: page, page_size, status, user_id,
// search (name/alias/prefix), sort_by (created_at | name | last_used_at |
// user_alias | spend), sort_order.
func (h *Handler) ListLiteLLMKeys(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	f := store.LiteLLMKeyListFilter{
		Status:    strings.TrimSpace(c.Query("status")),
		UserID:    strings.TrimSpace(c.Query("user_id")),
		Search:    strings.TrimSpace(c.Query("search")),
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	keysList, total, err := keys.ListPaged(c.Request.Context(), page, pageSize, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, liteLLMPagedKeysResponse{
		Keys:       keysList,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreateLiteLLMKey handles POST /v0/management/litellm/keys.
func (h *Handler) CreateLiteLLMKey(c *gin.Context) {
	users, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	var req liteLLMCreateKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if req.Name == "" {
		req.Name = "unnamed"
	}
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": "user_id is required: every Manage LiteLLM API key must be owned by an Internal User",
		}})
		return
	}
	// Validate the owner exists; surface a 404 (with a helpful hint) when the
	// caller picked a stale id.
	if _, err := users.Get(c.Request.Context(), userID); err != nil {
		if errors.Is(err, store.ErrLiteLLMUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"type":    "not_found",
				"message": "internal user not found: create the user before assigning keys to it",
			}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Validate IP allowlist/blocklist entries up front so a malformed pattern
	// is rejected at write time rather than silently skipped later.
	if req.Policy != nil {
		if msg := policy.ValidateIPPatterns(req.Policy.AllowedIPs); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "allowed_ips: " + msg}})
			return
		}
		if msg := policy.ValidateIPPatterns(req.Policy.BlockedIPs); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "blocked_ips: " + msg}})
			return
		}
	}
	createSecret := strings.TrimSpace(req.Secret)
	key, secret, err := keys.Create(c.Request.Context(), req.Name, req.Alias, createSecret, req.ExpiresAt, req.Metadata, req.Tags, req.Policy)
	if err != nil {
		if errors.Is(err, store.ErrInvalidSecret) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Stamp the owner assignment.
	keyID := key.ID
	if err := keys.UpdateUserID(c.Request.Context(), keyID, userID); err != nil {
		// Roll back: drop the orphaned key rather than presenting a half-
		// assigned row. Best-effort — log the cleanup failure.
		if delErr := keys.Delete(c.Request.Context(), keyID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", keyID).
				Warn("management: failed to roll back orphaned litellm key after user_id attach failure")
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Re-read so the response carries the freshly-stamped user_id.
	reloaded, reloadedPolicy, err := keys.LookupByID(c.Request.Context(), keyID)
	if err != nil {
		if delErr := keys.Delete(c.Request.Context(), keyID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", keyID).
				Warn("management: failed to roll back orphaned litellm key after re-read failure")
		}
		h.translateLiteLLMKeyError(c, err)
		return
	}
	key = reloaded
	respPolicy := req.Policy
	if respPolicy == nil {
		respPolicy = reloadedPolicy
	}
	c.JSON(http.StatusCreated, liteLLMKeyResponse{LiteLLMKey: key, Secret: secret, Policy: respPolicy})
}

// GetLiteLLMKey handles GET /v0/management/litellm/keys/:id.
func (h *Handler) GetLiteLLMKey(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	key, pol, err := keys.LookupByID(c.Request.Context(), id)
	if err != nil {
		h.translateLiteLLMKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, liteLLMKeyResponse{LiteLLMKey: key, Policy: pol})
}

// liteLLMPatchKeyRequest supports partial updates on a key (status, metadata,
// name, alias, expiry, tags). Pointer-typed fields are applied only when
// non-nil.
type liteLLMPatchKeyRequest struct {
	Status      *string         `json:"status,omitempty"`
	Metadata    *map[string]any `json:"metadata,omitempty"`
	Tags        *[]string       `json:"tags,omitempty"`
	Name        *string         `json:"name,omitempty"`
	Alias       *string         `json:"alias,omitempty"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"` // nil clears the expiry
	ClearExpiry *bool           `json:"clear_expiry,omitempty"`
}

// PatchLiteLLMKey handles PATCH /v0/management/litellm/keys/:id.
func (h *Handler) PatchLiteLLMKey(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req liteLLMPatchKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	ctx := c.Request.Context()
	if req.Name != nil {
		if err := keys.Rename(ctx, id, *req.Name); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	}
	if req.Alias != nil {
		if err := keys.UpdateAlias(ctx, id, *req.Alias); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	}
	if req.Status != nil {
		if err := keys.UpdateStatus(ctx, id, *req.Status); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	}
	if req.Metadata != nil {
		if err := keys.UpdateMetadata(ctx, id, *req.Metadata); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	}
	if req.Tags != nil {
		if err := keys.UpdateTags(ctx, id, *req.Tags); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	}
	if req.ClearExpiry != nil && *req.ClearExpiry {
		if err := keys.UpdateExpiry(ctx, id, nil); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	} else if req.ExpiresAt != nil {
		if err := keys.UpdateExpiry(ctx, id, req.ExpiresAt); err != nil {
			h.translateLiteLLMKeyError(c, err)
			return
		}
	}
	key, pol, err := keys.LookupByID(ctx, id)
	if err != nil {
		h.translateLiteLLMKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, liteLLMKeyResponse{LiteLLMKey: key, Policy: pol})
}

// PutLiteLLMKeyPolicy handles PUT /v0/management/litellm/keys/:id/policy.
// Replaces the full per-key policy.
func (h *Handler) PutLiteLLMKeyPolicy(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var p store.LiteLLMPolicy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if msg := policy.ValidateIPPatterns(p.AllowedIPs); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "allowed_ips: " + msg}})
		return
	}
	if msg := policy.ValidateIPPatterns(p.BlockedIPs); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "blocked_ips: " + msg}})
		return
	}
	if msg := validateModelRoutesFor(p.AllowedModels, p.ModelRoutes); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	if err := keys.UpdatePolicy(c.Request.Context(), id, p); err != nil {
		h.translateLiteLLMKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"policy": p, "api_key_id": id})
}

// RegenerateLiteLLMKey handles POST /v0/management/litellm/keys/:id/regenerate.
//
// The request body is optional. When omitted (or when secret is empty) the
// server auto-generates a fresh secret; when a JSON body with a non-empty
// "secret" is supplied it is validated (min 16 chars) and used verbatim.
func (h *Handler) RegenerateLiteLLMKey(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var secret string
	if c.Request.ContentLength > 0 {
		var req struct {
			Secret string `json:"secret"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
			return
		}
		secret = strings.TrimSpace(req.Secret)
	}
	newSecret, err := keys.Regenerate(c.Request.Context(), id, secret)
	if err != nil {
		if errors.Is(err, store.ErrInvalidSecret) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
			return
		}
		h.translateLiteLLMKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "secret": newSecret})
}

// DeleteLiteLLMKey handles DELETE /v0/management/litellm/keys/:id.
func (h *Handler) DeleteLiteLLMKey(c *gin.Context) {
	_, keys, ok := h.requireLiteLLM(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := keys.Delete(c.Request.Context(), id); err != nil {
		h.translateLiteLLMKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// translateLiteLLMUserError maps a LiteLLMUserStore error to the appropriate
// HTTP response.
func (h *Handler) translateLiteLLMUserError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrLiteLLMUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "internal user not found"}})
	case errors.Is(err, store.ErrLiteLLMUserEmailExists):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"type": "conflict", "message": "an internal user with that email already exists"}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
	}
}

// translateLiteLLMKeyError maps a LiteLLMKeyStore error to the appropriate HTTP
// response.
func (h *Handler) translateLiteLLMKeyError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrLiteLLMKeyNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "API key not found"}})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
}
