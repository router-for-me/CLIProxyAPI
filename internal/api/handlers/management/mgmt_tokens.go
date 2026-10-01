package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// mgmtTokenResponse is the canonical single-token response. The plaintext
// secret is only included in the response of POST /api-tokens and POST
// /regenerate; other endpoints omit it.
type mgmtTokenResponse struct {
	*store.ManagementToken
	Secret string                       `json:"secret,omitempty"`
	Policy *store.ManagementTokenPolicy `json:"policy,omitempty"`
}

// mgmtPagedTokensResponse is the paginated list payload.
type mgmtPagedTokensResponse struct {
	Tokens     []*store.ManagementToken `json:"api_tokens"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"page_size"`
	Total      int64                    `json:"total"`
	TotalPages int                      `json:"total_pages"`
}

// mgmtPagedAuditResponse is the paginated audit-log payload.
type mgmtPagedAuditResponse struct {
	Entries    []*store.ManagementAuditLog `json:"entries"`
	Page       int                         `json:"page"`
	PageSize   int                         `json:"page_size"`
	Total      int64                       `json:"total"`
	TotalPages int                         `json:"total_pages"`
}

// mgmtCreateTokenRequest is the JSON payload for POST /api-tokens.
//
// Scope defaults to "read" when empty. Policy is optional; when omitted the
// token has no per-endpoint or rate limits (subject only to the IP ban + the
// global management secret's allow-remote gate).
type mgmtCreateTokenRequest struct {
	Name                   string                       `json:"name"`
	Scope                  string                       `json:"scope"`
	DefaultUserID          string                       `json:"default_user_id,omitempty"`
	DefaultUserIDEndpoints []string                     `json:"default_user_id_endpoints,omitempty"`
	ExpiresAt              *time.Time                   `json:"expires_at,omitempty"`
	Metadata               map[string]any               `json:"metadata,omitempty"`
	Policy                 *store.ManagementTokenPolicy `json:"policy,omitempty"`
}

// mgmtPatchTokenRequest supports partial updates on a token. Pointer-typed
// fields are applied only when non-nil.
type mgmtPatchTokenRequest struct {
	Name                   *string         `json:"name,omitempty"`
	Status                 *string         `json:"status,omitempty"`
	Scope                  *string         `json:"scope,omitempty"`
	DefaultUserID          *string         `json:"default_user_id,omitempty"`
	DefaultUserIDEndpoints *[]string       `json:"default_user_id_endpoints,omitempty"`
	Metadata               *map[string]any `json:"metadata,omitempty"`
	ExpiresAt              *time.Time      `json:"expires_at,omitempty"` // nil clears the expiry
	ClearExpiry            *bool           `json:"clear_expiry,omitempty"`
}

// validateDefaultUserConfig enforces the invariants tying the default-user
// fallback to write scope. It returns a human-readable reason when the
// configuration is invalid, or "" when valid. The default-user fallback is
// opt-in: both fields empty is always valid.
func validateDefaultUserConfig(scope, defaultUserID string, endpoints []string) string {
	if strings.TrimSpace(defaultUserID) == "" && len(normalizeMgmtEndpoints(endpoints)) == 0 {
		return ""
	}
	if scope != store.MgmtTokenScopeWrite {
		return "default_user_id and default_user_id_endpoints require scope 'write'"
	}
	if strings.TrimSpace(defaultUserID) == "" {
		return "default_user_id_endpoints requires default_user_id"
	}
	for _, ep := range endpoints {
		ep = strings.TrimSpace(ep)
		if ep == "" {
			continue
		}
		if !strings.HasPrefix(ep, "/") {
			return "default_user_id_endpoints entries must be management paths starting with '/'"
		}
	}
	return ""
}

// normalizeMgmtEndpoints trims entries and drops blanks so validation and
// persistence agree on what counts as an empty allow-list.
func normalizeMgmtEndpoints(endpoints []string) []string {
	out := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep = strings.TrimSpace(ep); ep != "" {
			out = append(out, ep)
		}
	}
	return out
}

// validateDefaultUserExists verifies the referenced Internal User exists before
// persisting the fallback. The default user id is always an internal_users id;
// Manage-LiteLLM users (litellm_internal_users) are intentionally not consulted.
// The check is skipped (accepted) when the PG internal-user store is not wired,
// so the feature degrades gracefully on deployments that only carry management
// tokens.
func (h *Handler) validateDefaultUserExists(c *gin.Context, defaultUserID string) bool {
	userID := strings.TrimSpace(defaultUserID)
	if userID == "" {
		return true
	}
	h.mu.Lock()
	users := h.pgUsers
	h.mu.Unlock()
	if users == nil {
		return true
	}
	if _, err := users.Get(c.Request.Context(), userID); err != nil {
		if errors.Is(err, store.ErrInternalUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"type":    "not_found",
				"message": "default_user_id references an unknown internal user",
			}})
			return false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return false
	}
	return true
}

// ListAPITokens handles GET /v0/management/api-tokens.
//
// Query parameters:
//   - page       (default 1)
//   - page_size  (default 25, max 200)
//   - status     (optional, e.g. "active")
//   - scope      (optional, "read" | "write")
//   - search     (optional substring match on name)
//   - sort_by    "created_at" (default) | "name" | "last_used_at"
//   - sort_order "desc" (default) | "asc"
func (h *Handler) ListAPITokens(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
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
	status := strings.TrimSpace(c.Query("status"))
	scope := strings.TrimSpace(c.Query("scope"))
	search := strings.TrimSpace(c.Query("search"))
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	list, total, err := tokens.ListPaged(c.Request.Context(), page, pageSize, status, scope, search, sortBy, sortOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, mgmtPagedTokensResponse{
		Tokens:     list,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreateAPIToken handles POST /v0/management/api-tokens.
func (h *Handler) CreateAPIToken(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	var req mgmtCreateTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if req.Name == "" {
		req.Name = "unnamed"
	}
	if req.Scope == "" {
		req.Scope = store.MgmtTokenScopeRead
	}
	if req.Scope != store.MgmtTokenScopeRead && req.Scope != store.MgmtTokenScopeWrite {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": "scope must be 'read' or 'write'",
		}})
		return
	}
	defaultUserID := strings.TrimSpace(req.DefaultUserID)
	defaultEndpoints := normalizeMgmtEndpoints(req.DefaultUserIDEndpoints)
	if msg := validateDefaultUserConfig(req.Scope, defaultUserID, defaultEndpoints); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	if !h.validateDefaultUserExists(c, defaultUserID) {
		return
	}
	created, secret, err := tokens.Create(c.Request.Context(), req.Name, req.Scope, defaultUserID, defaultEndpoints, req.ExpiresAt, req.Metadata, req.Policy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Freshly created tokens are not in the cache yet, so no invalidation needed.
	c.JSON(http.StatusCreated, mgmtTokenResponse{ManagementToken: created, Secret: secret, Policy: req.Policy})
}

// GetAPIToken handles GET /v0/management/api-tokens/:id.
func (h *Handler) GetAPIToken(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	id := c.Param("id")
	tok, pol, err := tokens.LookupByID(c.Request.Context(), id)
	if err != nil {
		h.translateTokenError(c, err)
		return
	}
	c.JSON(http.StatusOK, mgmtTokenResponse{ManagementToken: tok, Policy: pol})
}

// PatchAPIToken handles PATCH /v0/management/api-tokens/:id.
func (h *Handler) PatchAPIToken(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req mgmtPatchTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	ctx := c.Request.Context()
	current, _, err := tokens.LookupByID(ctx, id)
	if err != nil {
		h.translateTokenError(c, err)
		return
	}

	newScope := current.Scope
	if req.Scope != nil {
		if *req.Scope != store.MgmtTokenScopeRead && *req.Scope != store.MgmtTokenScopeWrite {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type":    "invalid_request",
				"message": "scope must be 'read' or 'write'",
			}})
			return
		}
		newScope = *req.Scope
	}
	newDefaultUserID := current.DefaultUserID
	if req.DefaultUserID != nil {
		newDefaultUserID = strings.TrimSpace(*req.DefaultUserID)
	}
	newDefaultEndpoints := current.DefaultUserIDEndpoints
	if req.DefaultUserIDEndpoints != nil {
		newDefaultEndpoints = normalizeMgmtEndpoints(*req.DefaultUserIDEndpoints)
	}
	// A read-scope token cannot carry a default-user fallback: downgrading to
	// read clears it automatically so the invariant always holds.
	if newScope == store.MgmtTokenScopeRead {
		newDefaultUserID, newDefaultEndpoints = "", nil
	}
	if msg := validateDefaultUserConfig(newScope, newDefaultUserID, newDefaultEndpoints); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	if !h.validateDefaultUserExists(c, newDefaultUserID) {
		return
	}

	if req.Name != nil {
		if err := tokens.Rename(ctx, id, *req.Name); err != nil {
			h.translateTokenError(c, err)
			return
		}
	}
	if req.Scope != nil {
		if err := tokens.UpdateScope(ctx, id, *req.Scope); err != nil {
			h.translateTokenError(c, err)
			return
		}
	}
	if req.DefaultUserID != nil || req.DefaultUserIDEndpoints != nil || req.Scope != nil {
		if err := tokens.UpdateDefaultUserID(ctx, id, newDefaultUserID, newDefaultEndpoints); err != nil {
			h.translateTokenError(c, err)
			return
		}
	}
	if req.Status != nil {
		if err := tokens.UpdateStatus(ctx, id, *req.Status); err != nil {
			h.translateTokenError(c, err)
			return
		}
	}
	if req.Metadata != nil {
		if err := tokens.UpdateMetadata(ctx, id, *req.Metadata); err != nil {
			h.translateTokenError(c, err)
			return
		}
	}
	if req.ClearExpiry != nil && *req.ClearExpiry {
		if err := tokens.UpdateExpiry(ctx, id, nil); err != nil {
			h.translateTokenError(c, err)
			return
		}
	} else if req.ExpiresAt != nil {
		if err := tokens.UpdateExpiry(ctx, id, req.ExpiresAt); err != nil {
			h.translateTokenError(c, err)
			return
		}
	}
	// Any change invalidates the cached token snapshot.
	InvalidateAllMgmtTokens()
	tok, pol, err := tokens.LookupByID(ctx, id)
	if err != nil {
		h.translateTokenError(c, err)
		return
	}
	c.JSON(http.StatusOK, mgmtTokenResponse{ManagementToken: tok, Policy: pol})
}

// PutAPITokenPolicy handles PUT /v0/management/api-tokens/:id/policy.
func (h *Handler) PutAPITokenPolicy(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var p store.ManagementTokenPolicy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if err := tokens.UpdatePolicy(c.Request.Context(), id, p); err != nil {
		h.translateTokenError(c, err)
		return
	}
	InvalidateAllMgmtTokens()
	c.JSON(http.StatusOK, gin.H{"policy": p, "token_id": id})
}

// RegenerateAPIToken handles POST /v0/management/api-tokens/:id/regenerate.
func (h *Handler) RegenerateAPIToken(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	id := c.Param("id")
	secret, err := tokens.Regenerate(c.Request.Context(), id)
	if err != nil {
		h.translateTokenError(c, err)
		return
	}
	// Invalidate the entire cache: the old secret will no longer match, but
	// the new one was never cached, so the next request resolves fresh.
	InvalidateAllMgmtTokens()
	c.JSON(http.StatusOK, gin.H{"id": id, "secret": secret})
}

// DeleteAPIToken handles DELETE /v0/management/api-tokens/:id.
func (h *Handler) DeleteAPIToken(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := tokens.Delete(c.Request.Context(), id); err != nil {
		h.translateTokenError(c, err)
		return
	}
	InvalidateAllMgmtTokens()
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// ListAPITokenAuditLog handles GET /v0/management/api-tokens/audit-log.
//
// Query parameters:
//   - page       (default 1)
//   - page_size  (default 25, max 200)
//   - token_id   (optional, filter to a single token)
//   - method     (optional, e.g. "GET")
//   - path       (optional, substring match on path)
//   - errors_only (optional "1"/"true" → only 4xx/5xx responses)
//   - from       (optional, RFC3339)
//   - to         (optional, RFC3339)
func (h *Handler) ListAPITokenAuditLog(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
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
	var f store.MgmtAuditFilter
	f.TokenID = strings.TrimSpace(c.Query("token_id"))
	f.Method = strings.TrimSpace(c.Query("method"))
	f.Path = strings.TrimSpace(c.Query("path"))
	if v := strings.TrimSpace(c.Query("errors_only")); v == "1" || strings.EqualFold(v, "true") {
		t := true
		f.IsError = &t
	}
	if v := strings.TrimSpace(c.Query("from")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := strings.TrimSpace(c.Query("to")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	entries, total, err := tokens.ListAuditPaged(c.Request.Context(), f, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, mgmtPagedAuditResponse{
		Entries:    entries,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// GetAPITokenAuditEntry handles GET /v0/management/api-tokens/audit-log/:id.
func (h *Handler) GetAPITokenAuditEntry(c *gin.Context) {
	tokens, ok := h.requireMgmtTokens(c)
	if !ok {
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "audit log id must be a positive integer"}})
		return
	}
	entry, err := tokens.GetAudit(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrManagementAuditLogNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "audit log entry not found"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entry": entry})
}

// translateTokenError maps a store error to the appropriate HTTP response.
func (h *Handler) translateTokenError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrManagementTokenNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "management token not found"}})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
}
