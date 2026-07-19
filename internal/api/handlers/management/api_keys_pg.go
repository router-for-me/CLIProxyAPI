package management

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// pgNotConfigured is the canonical response when a management route requires
// the PG backend but it is not wired. Returning 503 (rather than 404) makes
// the absence discoverable: callers know the route exists but is disabled
// in the current deployment.
func (h *Handler) pgNotConfigured(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": gin.H{
			"type":    "pg_store_not_configured",
			"message": "PostgreSQL store is not configured. Set PGSTORE_DSN to enable this route.",
		},
	})
}

// requirePG returns false (after writing a 503 response) when the PG store
// is not wired. Callers should early-return when this returns false.
func (h *Handler) requirePG(c *gin.Context) (*store.APIKeyStore, *store.UsageStore, *store.ModelsStore, *policy.PolicyService, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, nil, false
	}
	h.mu.Lock()
	apiKeys := h.pgAPIKeys
	usage := h.pgUsage
	models := h.pgModels
	policySvc := h.policySvc
	h.mu.Unlock()
	if apiKeys == nil || usage == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, nil, false
	}
	svcPtr := &policySvc
	return apiKeys, usage, models, svcPtr, true
}

// pgCreateKeyRequest is the JSON payload for POST /api-keys-pg.
type pgCreateKeyRequest struct {
	Name      string         `json:"name"`
	Secret    string         `json:"secret,omitempty"` // optional; auto-generated when empty
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Policy    *store.Policy  `json:"policy,omitempty"`
}

// pgKeyResponse is the canonical single-key response. The plaintext secret
// is only included in the response of POST /api-keys-pg and POST /regenerate;
// other endpoints omit it.
type pgKeyResponse struct {
	*store.APIKey
	Secret string        `json:"secret,omitempty"`
	Policy *store.Policy `json:"policy,omitempty"`
}

// pgListKeysResponse returns all keys (without hashes or plaintext secrets).
type pgListKeysResponse struct {
	Keys []*store.APIKey `json:"api_keys"`
}

// pgPagedKeysResponse is the paginated variant of pgListKeysResponse.
// Total reflects the row count after the same status filter (when applied),
// so the caller can render a full pager without a second round-trip.
type pgPagedKeysResponse struct {
	Keys       []*store.APIKey `json:"api_keys"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	Total      int64           `json:"total"`
	TotalPages int             `json:"total_pages"`
}

// ListPGAPIKeys handles GET /v0/management/api-keys-pg.
//
// Query parameters:
//   - page      (default 1)
//   - page_size (default 25, max 200)
//   - status    (optional, e.g. "active")
//
// When no page is supplied, defaults are applied. Unbounded listing is
// intentionally not supported to bound memory on busy deployments.
func (h *Handler) ListPGAPIKeys(c *gin.Context) {
	apiKeys, _, _, _, ok := h.requirePG(c)
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
	keys, total, err := apiKeys.ListPaged(c.Request.Context(), page, pageSize, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, pgPagedKeysResponse{
		Keys:       keys,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreatePGAPIKey handles POST /v0/management/api-keys-pg.
func (h *Handler) CreatePGAPIKey(c *gin.Context) {
	apiKeys, _, _, policySvc, ok := h.requirePG(c)
	if !ok {
		return
	}
	var req pgCreateKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if req.Name == "" {
		req.Name = "unnamed"
	}
	key, secret, err := apiKeys.Create(c.Request.Context(), req.Name, req.Secret, req.ExpiresAt, req.Metadata, req.Policy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Invalidate policy cache so the new key is immediately enforceable.
	if svc := *policySvc; svc != nil {
		_ = svc.InvalidateKey(c.Request.Context(), secret)
	}
	c.JSON(http.StatusCreated, pgKeyResponse{APIKey: key, Secret: secret, Policy: req.Policy})
}

// GetPGAPIKey handles GET /v0/management/api-keys-pg/:id.
func (h *Handler) GetPGAPIKey(c *gin.Context) {
	apiKeys, _, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	key, pol, err := apiKeys.LookupByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "API key not found"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, pgKeyResponse{APIKey: key, Policy: pol})
}

// pgPatchKeyRequest supports partial updates on a key (status, metadata,
// name, expiry). Pointer-typed fields are applied only when non-nil.
type pgPatchKeyRequest struct {
	Status      *string         `json:"status,omitempty"`
	Metadata    *map[string]any `json:"metadata,omitempty"`
	Name        *string         `json:"name,omitempty"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"` // nil clears the expiry
	ClearExpiry *bool           `json:"clear_expiry,omitempty"`
}

// PatchPGAPIKey handles PATCH /v0/management/api-keys-pg/:id.
func (h *Handler) PatchPGAPIKey(c *gin.Context) {
	apiKeys, _, _, policySvc, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req pgPatchKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	ctx := c.Request.Context()
	if req.Name != nil {
		if err := apiKeys.Rename(ctx, id, *req.Name); err != nil {
			h.translateKeyError(c, err)
			return
		}
	}
	if req.Status != nil {
		if err := apiKeys.UpdateStatus(ctx, id, *req.Status); err != nil {
			h.translateKeyError(c, err)
			return
		}
	}
	if req.Metadata != nil {
		if err := apiKeys.UpdateMetadata(ctx, id, *req.Metadata); err != nil {
			h.translateKeyError(c, err)
			return
		}
	}
	if req.ClearExpiry != nil && *req.ClearExpiry {
		if err := apiKeys.UpdateExpiry(ctx, id, nil); err != nil {
			h.translateKeyError(c, err)
			return
		}
	} else if req.ExpiresAt != nil {
		if err := apiKeys.UpdateExpiry(ctx, id, req.ExpiresAt); err != nil {
			h.translateKeyError(c, err)
			return
		}
	}
	// Any change to the key invalidates the cached snapshot. We pass an
	// empty principal here because the patch did not change the secret —
	// but InvalidateAll() is the safe option when principal is unknown.
	if svc := *policySvc; svc != nil {
		svc.InvalidateAll()
	}
	key, pol, err := apiKeys.LookupByID(ctx, id)
	if err != nil {
		h.translateKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, pgKeyResponse{APIKey: key, Policy: pol})
}

// PutPGAPIKeyPolicy handles PUT /v0/management/api-keys-pg/:id/policy.
func (h *Handler) PutPGAPIKeyPolicy(c *gin.Context) {
	apiKeys, _, _, policySvc, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var p store.Policy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if err := apiKeys.UpdatePolicy(c.Request.Context(), id, p); err != nil {
		h.translateKeyError(c, err)
		return
	}
	if svc := *policySvc; svc != nil {
		svc.InvalidateAll()
	}
	c.JSON(http.StatusOK, gin.H{"policy": p, "api_key_id": id})
}

// RegeneratePGAPIKey handles POST /v0/management/api-keys-pg/:id/regenerate.
func (h *Handler) RegeneratePGAPIKey(c *gin.Context) {
	apiKeys, _, _, policySvc, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	secret, err := apiKeys.Regenerate(c.Request.Context(), id)
	if err != nil {
		h.translateKeyError(c, err)
		return
	}
	// Invalidate the entire cache: the old secret will no longer match,
	// but the new one was never cached, so the next request resolves fresh.
	if svc := *policySvc; svc != nil {
		svc.InvalidateAll()
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "secret": secret})
}

// DeletePGAPIKey handles DELETE /v0/management/api-keys-pg/:id.
func (h *Handler) DeletePGAPIKey(c *gin.Context) {
	apiKeys, _, _, policySvc, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := apiKeys.Delete(c.Request.Context(), id); err != nil {
		h.translateKeyError(c, err)
		return
	}
	if svc := *policySvc; svc != nil {
		svc.InvalidateAll()
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// translateKeyError maps a store error to the appropriate HTTP response.
func (h *Handler) translateKeyError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrAPIKeyNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "API key not found"}})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
}

// CancelableCtx is a small helper for handlers that need a request-scoped
// context with a timeout for downstream lookups. Exported so other PG
// handlers can reuse it without duplicating the boilerplate.
func CancelableCtx(c *gin.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), timeout)
}

// totalPages ceil-divides total by pageSize, returning at least 1 so callers
// never receive a zero-page count for non-empty result sets.
func totalPages(total int64, pageSize int) int {
	if pageSize <= 0 {
		return 1
	}
	pages := int(total / int64(pageSize))
	if total%int64(pageSize) != 0 {
		pages++
	}
	if pages < 1 && total > 0 {
		pages = 1
	}
	return pages
}
