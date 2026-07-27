package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// requireModelGroups returns the ModelGroupStore. Returns false (after writing
// a 503 response) when the store is not wired — model groups are a PG-only
// feature, mirroring upstream-providers and internal-users.
func (h *Handler) requireModelGroups(c *gin.Context) (*store.ModelGroupStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	groups := h.pgModelGroups
	h.mu.Unlock()
	if groups == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return groups, true
}

// modelGroupResponse is the canonical single-group payload.
type modelGroupResponse struct {
	*store.ModelGroup
}

// pagedModelGroupsResponse is the paginated list payload.
type pagedModelGroupsResponse struct {
	Groups     []store.ModelGroup `json:"groups"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	Total      int64              `json:"total"`
	TotalPages int                `json:"total_pages"`
}

// createModelGroupRequest is the JSON payload for POST /model-groups.
type createModelGroupRequest struct {
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	AllowedModels []string           `json:"allowed_models"`
	BlockedModels []string           `json:"blocked_models"`
	ModelRoutes   []store.ModelRoute `json:"model_routes"`
	Metadata      map[string]any     `json:"metadata"`
}

// updateModelGroupRequest supports partial updates. Pointer-typed fields are
// applied only when non-nil; pass an explicit empty string/array to clear.
type updateModelGroupRequest struct {
	Name          *string             `json:"name"`
	Description   *string             `json:"description"`
	AllowedModels *[]string           `json:"allowed_models"`
	BlockedModels *[]string           `json:"blocked_models"`
	ModelRoutes   *[]store.ModelRoute `json:"model_routes"`
	Metadata      *map[string]any     `json:"metadata"`
}

// attachModelGroupRequest is the JSON body for POST /model-groups/:id/attach
// and POST /model-groups/:id/detach.
//
// api_key_id is the API key's id whose api_key_policies row will carry the
// model_group_id. Model groups apply ONLY to API-key policies (per-key model
// access overrides); Internal Users are not attachable.
type attachModelGroupRequest struct {
	APIKeyID string `json:"api_key_id"`
}

// ListModelGroups handles GET /v0/management/model-groups.
//
// Query parameters:
//   - page       (default 1)
//   - page_size  (default 25, max 200)
//   - search     (optional substring match on name OR description)
//   - sort_by    "name" (default) | "created_at" | "updated_at"
//   - sort_order "asc" (default) | "desc"
func (h *Handler) ListModelGroups(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
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
	f := store.ModelGroupListFilter{
		Search:    strings.TrimSpace(c.Query("search")),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "name"),
		SortOrder: c.DefaultQuery("sort_order", "asc"),
	}
	list, total, err := groups.ListPaged(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, pagedModelGroupsResponse{
		Groups:     list,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreateModelGroup handles POST /v0/management/model-groups. Names must be
// unique; a name that already exists returns 409.
func (h *Handler) CreateModelGroup(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
	if !ok {
		return
	}
	var req createModelGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	// Validate routes against the requested allowed list so a group cannot be
	// created that routes a model outside its grant.
	if msg := validateModelRoutesFor(req.AllowedModels, req.ModelRoutes); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	g := store.ModelGroup{
		Name:          req.Name,
		Description:   req.Description,
		AllowedModels: req.AllowedModels,
		BlockedModels: req.BlockedModels,
		ModelRoutes:   req.ModelRoutes,
		Metadata:      req.Metadata,
	}
	created, err := groups.Create(c.Request.Context(), g)
	if err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, modelGroupResponse{ModelGroup: &created})
}

// GetModelGroup handles GET /v0/management/model-groups/:id.
func (h *Handler) GetModelGroup(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
	if !ok {
		return
	}
	g, err := groups.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	attachments, _ := groups.ListAttachments(c.Request.Context(), g.ID)
	c.JSON(http.StatusOK, gin.H{"group": g, "attachments": attachments})
}

// UpdateModelGroup handles PUT /v0/management/model-groups/:id. All fields are
// optional; only the supplied fields are mutated.
func (h *Handler) UpdateModelGroup(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req updateModelGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	// Validate routes against the new allowed list when both are supplied in
	// the same payload. When only the allowed list is sent, existing routes
	// may now be out of bounds — enforcement tolerates this (a routed model
	// outside allowed is simply ignored at enforcement time), but we still
	// validate to give the operator early feedback.
	var allowedForValidation []string
	var routesForValidation []store.ModelRoute
	current, errCur := groups.Get(c.Request.Context(), id)
	if errCur != nil {
		h.translateModelGroupError(c, errCur)
		return
	}
	allowedForValidation = current.AllowedModels
	if req.AllowedModels != nil {
		allowedForValidation = *req.AllowedModels
	}
	routesForValidation = current.ModelRoutes
	if req.ModelRoutes != nil {
		routesForValidation = *req.ModelRoutes
	}
	if msg := validateModelRoutesFor(allowedForValidation, routesForValidation); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	upd := store.ModelGroupUpdate{
		Name:          req.Name,
		Description:   req.Description,
		AllowedModels: req.AllowedModels,
		BlockedModels: req.BlockedModels,
		ModelRoutes:   req.ModelRoutes,
		Metadata:      req.Metadata,
	}
	if err := groups.Update(c.Request.Context(), id, upd); err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	// Invalidate the policy cache: a group mutation affects every attached
	// API-key policy and internal user on their next request.
	h.invalidatePolicyCache()
	updated, err := groups.Get(c.Request.Context(), id)
	if err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, modelGroupResponse{ModelGroup: &updated})
}

// DeleteModelGroup handles DELETE /v0/management/model-groups/:id. Rejected
// with 409 while any api_key_policies still reference the group (operator
// must detach them first).
func (h *Handler) DeleteModelGroup(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := groups.Delete(c.Request.Context(), id); err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// AttachModelGroup handles POST /v0/management/model-groups/:id/attach. The
// group becomes the source of truth for the target API-key policy's model
// access (allowed/blocked lists + routes) at enforcement time. Model groups
// attach ONLY to API-key policies (Internal Users are intentionally not
// attachable).
func (h *Handler) AttachModelGroup(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req attachModelGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if strings.TrimSpace(req.APIKeyID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "api_key_id is required"}})
		return
	}
	if err := groups.SetAttachment(c.Request.Context(), req.APIKeyID, id); err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"group_id": id, "api_key_id": req.APIKeyID, "attached": true})
}

// DetachModelGroup handles POST /v0/management/model-groups/:id/detach.
// Clears the model_group_id on the target API-key policy so the policy's own
// allowed/blocked/routes fields apply again.
func (h *Handler) DetachModelGroup(c *gin.Context) {
	groups, ok := h.requireModelGroups(c)
	if !ok {
		return
	}
	id := c.Param("id")
	var req attachModelGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if strings.TrimSpace(req.APIKeyID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "api_key_id is required"}})
		return
	}
	// SetAttachment skips the group-existence check when groupID is empty
	// (detach), so the :id in the path is informational only here.
	_ = id
	if err := groups.SetAttachment(c.Request.Context(), req.APIKeyID, ""); err != nil {
		h.translateModelGroupError(c, err)
		return
	}
	h.invalidatePolicyCache()
	c.JSON(http.StatusOK, gin.H{"group_id": id, "api_key_id": req.APIKeyID, "detached": true})
}

// translateModelGroupError maps a ModelGroupStore error to the appropriate HTTP
// response.
func (h *Handler) translateModelGroupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrModelGroupNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "model group not found"}})
	case errors.Is(err, store.ErrModelGroupNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"type": "conflict", "message": "model group name already taken"}})
	case errors.Is(err, store.ErrModelGroupInUse):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"type": "in_use", "message": err.Error()}})
	case errors.Is(err, store.ErrAPIKeyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "api key not found"}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
	}
}
