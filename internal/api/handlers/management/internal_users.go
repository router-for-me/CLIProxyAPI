// Package management routing equivalence:
//
// This file implements the LiteLLM /user/* equivalence class under the
// /v0/management/internal-users/* route prefix. The path stays /internal-users
// for backward compatibility with existing CLI tooling, but the response
// shapes and behavior mirror LiteLLM's internal_user_endpoints.py:
//
//	POST   /internal-users                          ↔ POST /user/new         (auto_create_key default true)
//	GET    /internal-users                          ↔ GET  /user/list
//	GET    /internal-users/leaderboard              ↔ GET  /spend/users         (spend-ranked)
//	POST   /internal-users/reconcile-all            ↔ (NixLLM-specific; no LiteLLM equivalent)
//	GET    /internal-users/:id                       ↔ GET  /user/info
//	PATCH  /internal-users/:id                      ↔ POST /user/update
//	DELETE /internal-users/:id                      ↔ POST /user/delete
//	POST   /internal-users/:id/reset-spend          ↔ POST /global/spend/reset  (scoped)
//	POST   /internal-users/:id/reconcile-spend      ↔ (NixLLM-specific; post Consume drift recovery)
//	GET    /internal-users/:id/keys                 ↔ keyed enumeration in /user/info
//	POST   /internal-users/:id/keys/:keyId/attach   ↔ POST /key/generate         (attach mode)
//	GET    /internal-users/:id/totals               ↔ /global/activity KPIs scoped to the user
//	GET    /internal-users/:id/timeseries           ↔ /global/activity daily_data scoped
//	GET    /internal-users/:id/top                  ↔ /global/activity/model scoped
//	GET    /internal-users/:id/events               ↔ /spend/logs?user_id=…
//	GET    /internal-users/:id/windows              ↔ budget windows (per-user view, NixLLM-specific)
//	GET    /internal-users/:id/model-spend         ↔ /user/daily/activity      (on-the-fly SELECT)
//	GET    /internal-users/:id/models               ← config + realized: allowed_models + model_max_budgets + on-the-fly SELECT
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

// requireUsers returns the UserStore (and the api_key store, since the two
// frequently co-operate for the "attach key to user" endpoint). Returns
// false (after writing a 503 response) when the UserStore is not wired.
func (h *Handler) requireUsers(c *gin.Context) (*store.UserStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	users := h.pgUsers
	h.mu.Unlock()
	if users == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return users, true
}

// internalUserResponse is the canonical single-user payload.
type internalUserResponse struct {
	*store.InternalUser
}

// pagedUsersResponse is the paginated list payload.
type pagedUsersResponse struct {
	Users      []store.InternalUser `json:"users"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"page_size"`
	Total      int64                `json:"total"`
	TotalPages int                  `json:"total_pages"`
}

// createUserRequest is the JSON payload for POST /internal-users.
//
// MaxParallelRequests is the LiteLLM-equivalent per-user in-flight cap.
// AutoCreateKey defaults to true (LiteLLM workflow): the request returns the
// freshly-minted user AND a one-shot API key secret bound to that user. Pass
// auto_create_key=false to opt out and only persist the user row.
type createUserRequest struct {
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

// patchUserRequest supports partial updates. Pointer-typed fields are applied
// only when non-nil; pass an explicit empty string to clear scalar fields.
type patchUserRequest struct {
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

// ListInternalUsers handles GET /v0/management/internal-users.
//
// Query parameters:
//   - page       (default 1)
//   - page_size  (default 25, max 200)
//   - role       (optional filter, e.g. "internal_user")
//   - search     (optional substring match on alias OR email)
//   - sort_by    "spend" (default) | "created_at" | "user_alias"
//   - sort_order "desc" (default) | "asc"
func (h *Handler) ListInternalUsers(c *gin.Context) {
	users, ok := h.requireUsers(c)
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
	f := store.ListFilter{
		Role:      strings.TrimSpace(c.Query("role")),
		Search:    strings.TrimSpace(c.Query("search")),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "spend"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	list, total, err := users.ListWithSpend(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, pagedUsersResponse{
		Users:      list,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreateInternalUser handles POST /v0/management/internal-users.
//
// LiteLLM workflow: when auto_create_key != false (default true), the
// endpoint also provisions a default API key bound to the new user and
// returns the plaintext secret ONCE in the response body. The caller is
// responsible for surfacing it to the operator; the dashboard does so.
func (h *Handler) CreateInternalUser(c *gin.Context) {
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	apiKeys, _, _, policySvc, pgOK := h.requirePG(c)
	if !pgOK {
		return
	}
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	u := store.InternalUser{
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
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
	// Auto-provision an API key for this user (owner = created.ID). The key
	// gets a default name derived from the user alias (or "default-key"),
	// unlimited per-key policy (caps unset → fallback to user-level caps),
	// and is immediately enforceable.
	keyName := strings.TrimSpace(created.UserAlias)
	if keyName == "" {
		keyName = "default-key"
	} else if !strings.HasSuffix(keyName, "-key") {
		keyName = keyName + "-key"
	}
	key, secret, errKey := apiKeys.Create(c.Request.Context(), keyName, "", "", nil, nil, nil)
	if errKey != nil {
		// Best-effort: keep the user row, surface key-provisioning failure
		// without losing the entity. The operator can attach a key later.
		log.WithError(errKey).WithField("user_id", created.ID).
			Warn("management: auto-create-key provisioning failed; user persisted without key")
		c.JSON(http.StatusCreated, gin.H{
			"user":              created,
			"auto_create_error": errKey.Error(),
		})
		return
	}
	if err := apiKeys.UpdateUserID(c.Request.Context(), key.ID, created.ID); err != nil {
		// Roll back the orphaned key so the user has no half-attached entry.
		if delErr := apiKeys.Delete(c.Request.Context(), key.ID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", key.ID).
				Warn("management: failed to roll back orphaned auto-created key")
		}
		log.WithError(err).WithField("user_id", created.ID).
			Warn("management: auto-create-key attach failed; user persisted without key")
		c.JSON(http.StatusCreated, gin.H{
			"user":              created,
			"auto_create_error": err.Error(),
		})
		return
	}
	// Re-read so the response carries the freshly-stamped user_id. `pol` is
	// the per-key policy, included for dashboard symmetry with /api-keys-pg.
	reloadedKey, pol, reReadErr := apiKeys.LookupByID(c.Request.Context(), key.ID)
	if reReadErr == nil {
		key = reloadedKey
	}
	if svc := *policySvc; svc != nil {
		_ = svc.InvalidateKey(c.Request.Context(), secret)
	}
	c.JSON(http.StatusCreated, gin.H{
		"user":    created,
		"api_key": key,
		"policy":  pol,
		"secret":  secret,
	})
}

// GetInternalUser handles GET /v0/management/internal-users/:id.
func (h *Handler) GetInternalUser(c *gin.Context) {
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	id := c.Param("id")
	u, err := users.Get(c.Request.Context(), id)
	if err != nil {
		h.translateUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, internalUserResponse{InternalUser: &u})
}

// PatchInternalUser handles PATCH /v0/management/internal-users/:id.
func (h *Handler) PatchInternalUser(c *gin.Context) {
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req patchUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
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
	if err := users.Update(c.Request.Context(), id, upd); err != nil {
		h.translateUserError(c, err)
		return
	}
	// Policy cache must be invalidated so the next request re-reads the user.
	h.invalidatePolicyCache()
	u, err := users.Get(c.Request.Context(), id)
	if err != nil {
		h.translateUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, internalUserResponse{InternalUser: &u})
}

// DeleteInternalUser handles DELETE /v0/management/internal-users/:id.
func (h *Handler) DeleteInternalUser(c *gin.Context) {
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := users.Delete(c.Request.Context(), id); err != nil {
		h.translateUserError(c, err)
		return
	}
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// ResetInternalUserSpend handles POST /v0/management/internal-users/:id/reset-spend.
func (h *Handler) ResetInternalUserSpend(c *gin.Context) {
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := users.ResetSpend(c.Request.Context(), id); err != nil {
		h.translateUserError(c, err)
		return
	}
	u, err := users.Get(c.Request.Context(), id)
	if err != nil {
		h.translateUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, internalUserResponse{InternalUser: &u})
}

// AttachKeyToUser handles POST /v0/management/internal-users/:id/keys/:keyId/attach.
// It sets api_keys.user_id = :id (the supplied internal user). Detach by
// passing an empty value via the dedicated detach route.
func (h *Handler) AttachKeyToUser(c *gin.Context) {
	apiKeys, _, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	// requireUsers for the existence-confirmation; the actual mutation is
	// on api_keys (handled by APIKeyStore.UpdateUserID). requireUsers writes
	// a 503 when the UserStore is not configured.
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	id := c.Param("id")
	keyID := c.Param("keyId")
	if err := apiKeys.UpdateUserID(c.Request.Context(), keyID, id); err != nil {
		h.translateKeyError(c, err)
		return
	}
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"user_id": id, "api_key_id": keyID, "attached": true})
}

// DetachKeyFromUser handles DELETE /v0/management/internal-users/:id/keys/:keyId.
// It clears api_keys.user_id for the supplied key.
func (h *Handler) DetachKeyFromUser(c *gin.Context) {
	apiKeys, _, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	keyID := c.Param("keyId")
	if err := apiKeys.UpdateUserID(c.Request.Context(), keyID, ""); err != nil {
		h.translateKeyError(c, err)
		return
	}
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"api_key_id": keyID, "attached": false})
}

// ListInternalUserKeys handles GET /v0/management/internal-users/:id/keys.
// Returns all api_keys whose user_id matches :id.
func (h *Handler) ListInternalUserKeys(c *gin.Context) {
	apiKeys, _, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if _, ok := h.requireUsers(c); !ok {
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
	status := strings.TrimSpace(c.Query("status"))
	keys, _, err := apiKeys.ListPaged(c.Request.Context(), page, pageSize, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Filter in Go to user_id == id since ListPaged does not support a
	// user_id filter natively; pages are bounded so this is acceptable.
	out := make([]*store.APIKey, 0, len(keys))
	for _, k := range keys {
		if k != nil && k.UserID == id {
			out = append(out, k)
		}
	}
	totalMatching := int64(len(out))
	c.JSON(http.StatusOK, gin.H{
		"api_keys":    out,
		"page":        page,
		"page_size":   pageSize,
		"total":       totalMatching,
		"total_pages": totalPages(totalMatching, pageSize),
	})
}

// translateUserError maps a UserStore error to the appropriate HTTP response.
func (h *Handler) translateUserError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrInternalUserNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "internal user not found"}})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
}

// invalidatePolicyCache is a best-effort call to clear the policy service's
// in-memory snapshot cache, so mutations impact the next request immediately.
func (h *Handler) invalidatePolicyCache() {
	if h == nil {
		return
	}
	h.mu.Lock()
	svc := h.policySvc
	h.mu.Unlock()
	if svc != nil {
		svc.InvalidateAll()
	}
}

// ReconcileInternalUserSpend handles POST /v0/management/internal-users/:id/reconcile-spend.
//
// Recomputes the user's running spend from usage_events and updates
// internal_users.spend inside a single transaction. Used to recover from
// best-effort IncrementSpend failures (transient PG downtime during Consume).
// Optional from/to query bounds scope the SUM so a windowed reconciliation
// can re-arm spend after a partial outage.
func (h *Handler) ReconcileInternalUserSpend(c *gin.Context) {
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	users := h.pgUsers
	userID := c.Param("id")
	var from, to time.Time
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	res, err := users.ReconcileSpend(c.Request.Context(), userID, from, to)
	if err != nil {
		h.translateUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": res})
}

// ReconcileAllSpend handles POST /v0/management/internal-users/reconcile-all?since=RFC3339.
//
// Bulk reconciliation across all users that have usage_events newer than
// `since`. When `since` is omitted, the entire history is recomputed for
// every user (slow — operators should scope the call). Returns one result
// per affected user.
func (h *Handler) ReconcileAllSpend(c *gin.Context) {
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	users := h.pgUsers
	var since time.Time
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	results, err := users.ReconcileAll(c.Request.Context(), since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "since": since, "count": len(results)})
}
