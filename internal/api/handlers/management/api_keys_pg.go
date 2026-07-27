package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

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

// requireMgmtTokens returns false (after writing a 503 response) when the
// management-token PG store is not wired. Callers should early-return when
// this returns false. Mirrors requireUsers/requirePG.
func (h *Handler) requireMgmtTokens(c *gin.Context) (*store.ManagementTokenStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	tokens := h.pgMgmtTokens
	h.mu.Unlock()
	if tokens == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return tokens, true
}

// pgCreateKeyRequest is the JSON payload for POST /api-keys-pg.
//
// UserID is now REQUIRED (LiteLLM workflow): every API key is owned by an
// internal user. Budget/RPM enforcement still runs on the per-key policy,
// with a fallback to the internal user's max_budget when the per-key cap is
// unset (see policy.Check). Pass an empty value at your own risk — the
// handler returns 400 when unset.
type pgCreateKeyRequest struct {
	Name      string         `json:"name"`
	Alias     string         `json:"alias,omitempty"`
	Secret    string         `json:"secret,omitempty"` // optional; auto-generated when empty
	UserID    string         `json:"user_id"`          // REQUIRED — owner of the key
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
//   - page       (default 1)
//   - page_size  (default 25, max 200)
//   - status     (optional, e.g. "active")
//   - user_id    (optional, filter to keys owned by this internal user)
//   - search     (optional, case-insensitive substring on name/alias/prefix)
//   - sort_by    "created_at" (default) | "name" | "last_used_at" | "user_alias"
//   - sort_order "desc" (default) | "asc"
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
	f := store.APIKeyListFilter{
		Status:    strings.TrimSpace(c.Query("status")),
		UserID:    strings.TrimSpace(c.Query("user_id")),
		Search:    strings.TrimSpace(c.Query("search")),
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	keys, total, err := apiKeys.ListPagedFiltered(c.Request.Context(), page, pageSize, f)
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
//
// Workflow (LiteLLM-style): a key is meaningless without an owning Internal
// User, so the request body MUST include user_id pointing at a row in the
// internal_users table. Budget/RPM enforcement runs on the per-key policy;
// when the key's policy leaves a budget cap unset, enforcement falls back to
// the user's max_budget (the per-user window accumulates spend in any case,
// see policy.Consume).
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
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": "user_id is required: every API key must be owned by an Internal User",
		}})
		return
	}
	// Validate the owner exists; surface a 404 (with a helpful hint) when the
	// caller picked a stale id. Skipped when the UserStore is wired separately
	// and the lookup fails — that surfaces as 500 below.
	h.mu.Lock()
	users := h.pgUsers
	h.mu.Unlock()
	if users == nil {
		h.pgNotConfigured(c)
		return
	}
	if _, err := users.Get(c.Request.Context(), userID); err != nil {
		if errors.Is(err, store.ErrInternalUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"type":    "not_found",
				"message": "internal user not found: create the user before assigning keys to it",
			}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// When a model_group_id is supplied on create (carried inside
	// req.Policy.ModelGroupID), validate existence so the caller cannot
	// persist a dangling id at key-creation time. The dedicated
	// /model-groups/:id/attach endpoint is the canonical attach path; this
	// check keeps POST /api-keys-pg honest when the dashboard sends both
	// fields at once.
	if req.Policy != nil && req.Policy.ModelGroupID != nil && strings.TrimSpace(*req.Policy.ModelGroupID) != "" {
		groups, groupOK := h.requireModelGroups(c)
		if !groupOK {
			return
		}
		if _, err := groups.Get(c.Request.Context(), *req.Policy.ModelGroupID); err != nil {
			h.translateModelGroupError(c, err)
			return
		}
	}
	// Validate IP allowlist/blocklist entries up front so a malformed pattern
	// is rejected at write time rather than silently skipped at enforcement.
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
	key, secret, err := apiKeys.Create(c.Request.Context(), req.Name, req.Alias, req.Secret, req.ExpiresAt, req.Metadata, req.Policy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Stamp the owner assignment. The Create path keeps the secret/routine
	// unchanged; user_id is written via a dedicated UPDATE so the schema-
	// migration column is populated atomically with the new key.
	keyID := key.ID
	if err := apiKeys.UpdateUserID(c.Request.Context(), keyID, userID); err != nil {
		// Roll back: drop the orphaned key rather than presenting a half-
		// assigned row. Best-effort — log the cleanup failure.
		if delErr := apiKeys.Delete(c.Request.Context(), keyID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", keyID).
				Warn("management: failed to roll back orphaned key after user_id attach failure")
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Re-read so the response carries the freshly-stamped user_id (Create's
	// returned *APIKey predates the assignment). On failure, roll the key
	// back so we never present an inconsistent half-assigned row.
	reloaded, reloadedPolicy, err := apiKeys.LookupByID(c.Request.Context(), keyID)
	if err != nil {
		if delErr := apiKeys.Delete(c.Request.Context(), keyID); delErr != nil {
			log.WithError(delErr).WithField("api_key_id", keyID).
				Warn("management: failed to roll back orphaned key after re-read failure")
		}
		h.translateKeyError(c, err)
		return
	}
	key = reloaded
	respPolicy := req.Policy
	if respPolicy == nil {
		respPolicy = reloadedPolicy
	}
	// Invalidate policy cache so the new key is immediately enforceable.
	if svc := *policySvc; svc != nil {
		_ = svc.InvalidateKey(c.Request.Context(), secret)
	}
	c.JSON(http.StatusCreated, pgKeyResponse{APIKey: key, Secret: secret, Policy: respPolicy})
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
// name, expiry, alias, and model_group_id). Pointer-typed fields are applied
// only when non-nil.
type pgPatchKeyRequest struct {
	Status      *string         `json:"status,omitempty"`
	Metadata    *map[string]any `json:"metadata,omitempty"`
	Name        *string         `json:"name,omitempty"`
	Alias       *string         `json:"alias,omitempty"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"` // nil clears the expiry
	ClearExpiry *bool           `json:"clear_expiry,omitempty"`
	// ModelGroupID attaches (non-empty) or detaches (empty) the key's policy
	// to a model group in a single PATCH call. When attached, the group's
	// allowed/blocked lists and per-model routes OVERRIDE the policy's own
	// fields at enforcement time (group = source of truth). Existence is
	// validated against the model_groups table. Detaching (empty string)
	// clears the policy's model_group_id so its own fields apply again.
	ModelGroupID *string `json:"model_group_id,omitempty"`
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
	if req.Alias != nil {
		if err := apiKeys.UpdateAlias(ctx, id, *req.Alias); err != nil {
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
	// model_group_id attachment via PATCH. Attach (non-empty) validates the
	// group exists; detach (empty) clears the policy's model_group_id. When
	// the key has no policy row yet, attaching materializes an empty policy
	// carrying only the model_group_id (the group then becomes the source of
	// truth for allowed/blocked/routes at enforcement time). Detaching a
	// policy-less key is a no-op (still 200).
	if req.ModelGroupID != nil {
		groups, groupOK := h.requireModelGroups(c)
		if !groupOK {
			return
		}
		gid := strings.TrimSpace(*req.ModelGroupID)
		if gid != "" {
			if _, err := groups.Get(ctx, gid); err != nil {
				h.translateModelGroupError(c, err)
				return
			}
		}
		_, existingPolicy, errLookup := apiKeys.LookupByID(ctx, id)
		if errLookup != nil {
			h.translateKeyError(c, errLookup)
			return
		}
		if existingPolicy == nil {
			// No policy row yet. Only attach when a group id was supplied;
			// detaching a policy-less key is a no-op (idempotent).
			if gid == "" {
				// Nothing to clear.
			} else {
				empty := store.Policy{APIKeyID: id, ModelGroupID: &gid}
				if err := apiKeys.UpdatePolicy(ctx, id, empty); err != nil {
					h.translateKeyError(c, err)
					return
				}
			}
		} else if err := groups.SetAttachment(ctx, id, gid); err != nil {
			h.translateModelGroupError(c, err)
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

// validateModelRoutes checks that each model route targets a model permitted by
// the policy's AllowedModels (exact or wildcard match), has no duplicate route
// models, and has at least one provider. Returns a non-empty human-readable
// error message when validation fails, or "" when the routes are valid.
func validateModelRoutes(p store.Policy) string {
	return validateModelRoutesFor(p.AllowedModels, p.ModelRoutes)
}

// validateModelRoutesFor is the parameterized form used by model-groups CRUD
// (where there is no Policy carrier) and by PUT /policy when a model_group_id
// is attached (the group fills model_routes at enforcement time, so the policy
// payload may legitimately carry no routes of its own).
func validateModelRoutesFor(allowedModels []string, routes []store.ModelRoute) string {
	if len(routes) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		model := strings.TrimSpace(r.Model)
		if model == "" {
			return "model_routes: entry with empty model is not allowed"
		}
		if len(r.Providers) == 0 {
			return fmt.Sprintf("model_routes: route for %q must list at least one provider", model)
		}
		key := strings.ToLower(model)
		if _, dup := seen[key]; dup {
			return fmt.Sprintf("model_routes: duplicate route for model %q", model)
		}
		seen[key] = struct{}{}
		if !policy.ModelCoveredByAllowed(allowedModels, model) {
			return fmt.Sprintf("model_routes: model %q is not in allowed_models", model)
		}
	}
	return ""
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
	// Validate IP allowlist/blocklist entries up front so a malformed pattern
	// is rejected at write time rather than silently skipped at enforcement.
	if msg := policy.ValidateIPPatterns(p.AllowedIPs); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "allowed_ips: " + msg}})
		return
	}
	if msg := policy.ValidateIPPatterns(p.BlockedIPs); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "blocked_ips: " + msg}})
		return
	}
	// When a model_group_id is attached, the group becomes the source of
	// truth for allowed_models / blocked_models / model_routes at enforcement
	// time (see policy.enforce.go). The per-entity routes are then redundant —
	// validate them only when the policy carries explicit routes; an empty
	// routes list with a group attached is legitimate (the dashboard clears
	// the entity routes when switching to group mode).
	if msg := validateModelRoutesFor(p.AllowedModels, p.ModelRoutes); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	// When attaching a group, validate existence so callers cannot persist a
	// dangling model_group_id via the policy endpoint. The dedicated
	// /model-groups/:id/attach endpoint is the canonical path; this check
	// keeps PUT /policy honest when the dashboard sends both fields at once.
	if p.ModelGroupID != nil && *p.ModelGroupID != "" {
		groups, groupOK := h.requireModelGroups(c)
		if !groupOK {
			return
		}
		if _, err := groups.Get(c.Request.Context(), *p.ModelGroupID); err != nil {
			h.translateModelGroupError(c, err)
			return
		}
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
