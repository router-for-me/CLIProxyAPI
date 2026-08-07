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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// requireAutoRouters returns the AutoRouterStore. Returns false (after writing
// a 503 response) when the store is not wired — auto routers are a PG-only
// feature, mirroring model groups.
func (h *Handler) requireAutoRouters(c *gin.Context) (*store.AutoRouterStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	routers := h.pgAutoRouters
	h.mu.Unlock()
	if routers == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return routers, true
}

// pagedAutoRoutersResponse is the paginated list payload.
type pagedAutoRoutersResponse struct {
	Routers    []store.AutoRouter `json:"auto_routers"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	Total      int64              `json:"total"`
	TotalPages int                `json:"total_pages"`
}

// createAutoRouterRequest is the JSON payload for POST /auto-routers.
type createAutoRouterRequest struct {
	Name              string                   `json:"name"`
	ModelID           string                   `json:"model_id"`
	Description       string                   `json:"description,omitempty"`
	DisplayName       string                   `json:"display_name,omitempty"`
	Mappings          []store.TierMapping      `json:"mappings,omitempty"`
	Pricing           *store.AutoRouterPricing `json:"pricing,omitempty"`
	VisionBridgeModel string                   `json:"vision_bridge_model,omitempty"`
	Enabled           *bool                    `json:"enabled,omitempty"`
}

// updateAutoRouterRequest supports partial updates (mirrors the store update
// shape). Pointer-typed fields are applied only when non-nil.
type updateAutoRouterRequest struct {
	Name              *string                  `json:"name,omitempty"`
	ModelID           *string                  `json:"model_id,omitempty"`
	Description       *string                  `json:"description,omitempty"`
	DisplayName       *string                  `json:"display_name,omitempty"`
	Mappings          *[]store.TierMapping     `json:"mappings,omitempty"`
	Pricing           *store.AutoRouterPricing `json:"pricing,omitempty"`
	VisionBridgeModel *string                  `json:"vision_bridge_model,omitempty"`
	Enabled           *bool                    `json:"enabled,omitempty"`
}

// ListAutoRouters handles GET /v0/management/auto-routers.
//
// Query parameters:
//   - page       (default 1)
//   - page_size  (default 25, max 200)
//   - search     (optional substring match on name OR model_id)
//   - sort_by    "name" (default) | "model_id" | "created_at" | "updated_at"
//   - sort_order "asc" (default) | "desc"
func (h *Handler) ListAutoRouters(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
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
	f := store.AutoRouterListFilter{
		Search:    strings.TrimSpace(c.Query("search")),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "name"),
		SortOrder: c.DefaultQuery("sort_order", "asc"),
	}
	list, total, err := routers.ListPaged(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, pagedAutoRoutersResponse{
		Routers:    list,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// CreateAutoRouter handles POST /v0/management/auto-routers. Names and model
// ids must be unique; a conflict returns 409.
func (h *Handler) CreateAutoRouter(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	var req createAutoRouterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if msg := validateTierMappings(req.Mappings); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	r := store.AutoRouter{
		Name:              req.Name,
		ModelID:           req.ModelID,
		Description:       req.Description,
		DisplayName:       req.DisplayName,
		Mappings:          req.Mappings,
		Pricing:           req.Pricing,
		VisionBridgeModel: req.VisionBridgeModel,
		Enabled:           enabled,
	}
	created, err := routers.Create(c.Request.Context(), r)
	if err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	// Expose the router's model id as a selectable model (Models Group picker,
	// /v1/models, Models Catalog). Best-effort.
	h.syncAutoRouterCatalog(c.Request.Context(), &created)
	c.JSON(http.StatusCreated, gin.H{"auto_router": created})
}

// GetAutoRouter handles GET /v0/management/auto-routers/:id.
func (h *Handler) GetAutoRouter(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	r, err := routers.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"auto_router": r})
}

// UpdateAutoRouter handles PUT /v0/management/auto-routers/:id. All fields are
// optional; only the supplied fields are mutated.
func (h *Handler) UpdateAutoRouter(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	id := c.Param("id")
	// Fetch the pre-update router so we can re-register correctly when the
	// requestable model_id (the synthetic registry/catalog key) changes.
	before, errBefore := routers.Get(c.Request.Context(), id)
	if errBefore != nil {
		h.translateAutoRouterError(c, errBefore)
		return
	}
	var req updateAutoRouterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if req.Mappings != nil {
		if msg := validateTierMappings(*req.Mappings); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": msg}})
			return
		}
	}
	upd := store.AutoRouterUpdate{
		Name:              req.Name,
		ModelID:           req.ModelID,
		Description:       req.Description,
		DisplayName:       req.DisplayName,
		Mappings:          req.Mappings,
		Pricing:           req.Pricing,
		VisionBridgeModel: req.VisionBridgeModel,
		Enabled:           req.Enabled,
	}
	if err := routers.Update(c.Request.Context(), id, upd); err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	updated, err := routers.Get(c.Request.Context(), id)
	if err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	// Re-expose the (possibly changed) model id as a selectable model. If the
	// model_id changed, drop the old synthetic registration first so a stale id
	// does not linger in /v1/models or the picker.
	oldModelID := strings.TrimSpace(before.ModelID)
	newModelID := strings.TrimSpace(updated.ModelID)
	if newModelID != "" && newModelID != oldModelID {
		registry.UnregisterAutoRouterFromRegistry(oldModelID)
		if models := h.modelsStoreLocked(); models != nil {
			ctxTimed, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			if err := models.DeleteOne(ctxTimed, oldModelID, registry.AutoRouterProvider); err != nil && !errors.Is(err, store.ErrModelNotFound) {
				log.WithError(err).WithField("model", oldModelID).Warn("auto router: catalog delete old model failed")
			}
			cancel()
		}
	}
	h.syncAutoRouterCatalog(c.Request.Context(), &updated)
	c.JSON(http.StatusOK, gin.H{"auto_router": updated})
}

// DeleteAutoRouter handles DELETE /v0/management/auto-routers/:id.
func (h *Handler) DeleteAutoRouter(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	id := c.Param("id")
	// Capture the router (for its model id) before deletion so we can remove
	// its synthetic registrations afterwards.
	existing, errGet := routers.Get(c.Request.Context(), id)
	if errGet != nil {
		h.translateAutoRouterError(c, errGet)
		return
	}
	if err := routers.Delete(c.Request.Context(), id); err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	// Stop exposing the model id as a selectable model.
	h.unsyncAutoRouterCatalog(&existing)
	c.JSON(http.StatusOK, gin.H{"id": id, "deleted": true})
}

// translateAutoRouterError maps an AutoRouterStore error to the appropriate HTTP
// response.
func (h *Handler) translateAutoRouterError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrAutoRouterNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "auto router not found"}})
	case errors.Is(err, store.ErrAutoRouterNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"type": "conflict", "message": "auto router name already taken"}})
	case errors.Is(err, store.ErrAutoRouterModelIDTaken):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"type": "conflict", "message": "auto router model id already taken"}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
	}
}

// validateTierMappings ensures each mapping targets a known tier, contains no
// duplicate tiers, and carries a non-empty model when present. It also verifies
// that every routing priority entry references a pinned provider (mirroring
// ModelRoute validation in Models Group). Returns a human-readable message, or
// "" when valid.
func validateTierMappings(mappings []store.TierMapping) string {
	seen := map[string]bool{}
	for _, m := range mappings {
		tier := strings.ToLower(strings.TrimSpace(m.Tier))
		if tier == "" {
			return "tier_mappings: each mapping must specify a tier"
		}
		switch autorouter.Tier(tier) {
		case autorouter.TierSimple, autorouter.TierMedium, autorouter.TierComplex, autorouter.TierReasoning:
		default:
			return fmt.Sprintf("tier_mappings: unknown tier %q (expected simple/medium/complex/reasoning)", m.Tier)
		}
		if seen[tier] {
			return fmt.Sprintf("tier_mappings: duplicate tier %q", m.Tier)
		}
		seen[tier] = true
		if strings.TrimSpace(m.Model) == "" {
			return fmt.Sprintf("tier_mappings: tier %q must specify a target model", m.Tier)
		}
		// Every priority entry must target one of the pinned providers, so a
		// priority never silently applies to an unpinned provider.
		pinned := map[string]bool{}
		for _, p := range m.Providers {
			pinned[strings.ToLower(strings.TrimSpace(p))] = true
		}
		for _, pr := range m.Priorities {
			if !pinned[strings.ToLower(strings.TrimSpace(pr.Provider))] {
				return fmt.Sprintf("tier_mappings: tier %q priority references %q which is not a pinned provider", m.Tier, pr.Provider)
			}
		}
	}
	return ""
}

// autoRouterCatalogModel builds the models_catalog StoredModel that exposes an
// Auto Router's model_id as a normal, always-available model. It carries
// UserDefined=true so periodic catalog auto-syncs (protectUserDefined) never
// clobber or delete the row, mirroring dashboard-created model entries.
func autoRouterCatalogModel(r *store.AutoRouter) store.StoredModel {
	display := strings.TrimSpace(r.DisplayName)
	if display == "" {
		display = r.Name
	}
	return store.StoredModel{
		ID:               strings.TrimSpace(r.ModelID),
		Provider:         registry.AutoRouterProvider,
		OfficialProvider: registry.AutoRouterProvider,
		OwnedBy:          registry.AutoRouterProvider,
		Type:             registry.AutoRouterProvider,
		Object:           "model",
		DisplayName:      display,
		UserDefined:      true,
	}
}

// modelsStoreLocked returns the current ModelsStore without holding the handler
// lock across calls (callers should not rely on it being nil when PG is on).
func (h *Handler) modelsStoreLocked() *store.ModelsStore {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pgModels
}

// syncAutoRouterCatalog makes an Auto Router visible everywhere models are
// listed: it registers the model_id in the in-memory registry (so /v1/models,
// the Models Group picker, and the catalog's live view include it) and upserts
// a user-defined models_catalog row (so non-live/distinct catalog views include
// it too). Best-effort: failures are logged, never fail the router operation.
func (h *Handler) syncAutoRouterCatalog(ctx context.Context, r *store.AutoRouter) {
	if r == nil || strings.TrimSpace(r.ModelID) == "" {
		return
	}
	display := r.DisplayName
	if display == "" {
		display = r.Name
	}
	// Registry first (drives /v1/models + available/live pickers).
	registry.RegisterAutoRouterInRegistry(r.ModelID, display, 0)
	// Catalog row so distinct/catalog views also list it.
	if models := h.modelsStoreLocked(); models != nil {
		entry := autoRouterCatalogModel(r)
		entry.Created = time.Now().Unix()
		if err := models.UpsertOne(ctx, entry); err != nil {
			log.WithError(err).WithField("model", r.ModelID).Warn("auto router: catalog upsert failed")
		}
	}
}

// unsyncAutoRouterCatalog removes an Auto Router's model id from the in-memory
// registry and deletes its synthetic catalog row so it no longer appears as a
// selectable model. Provider-scoped delete leaves any other provider's row for
// the same id untouched.
func (h *Handler) unsyncAutoRouterCatalog(r *store.AutoRouter) {
	if r == nil || strings.TrimSpace(r.ModelID) == "" {
		return
	}
	registry.UnregisterAutoRouterFromRegistry(r.ModelID)
	if models := h.modelsStoreLocked(); models != nil {
		ctxTimed, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := models.DeleteOne(ctxTimed, r.ModelID, registry.AutoRouterProvider); err != nil && !errors.Is(err, store.ErrModelNotFound) {
			log.WithError(err).WithField("model", r.ModelID).Warn("auto router: catalog delete failed")
		}
	}
}

// autoRouterStoreLocked returns the current AutoRouterStore under the handler
// lock.
func (h *Handler) autoRouterStoreLocked() *store.AutoRouterStore {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pgAutoRouters
}

// RehydrateAutoRouters re-registers every enabled Auto Router's model id into
// the in-memory registry and models catalog so the routers survive a server
// restart (the registry is in-memory only and is rebuilt at boot). Called from
// server wiring after the stores are attached. Best-effort; failures are
// logged.
func (h *Handler) RehydrateAutoRouters(ctx context.Context) {
	routers := h.autoRouterStoreLocked()
	if routers == nil || ctx == nil {
		return
	}
	list, err := routers.ListAll(ctx)
	if err != nil {
		log.WithError(err).Warn("auto router: rehydrate list failed")
		return
	}
	for i := range list {
		if !list[i].Enabled {
			continue
		}
		h.syncAutoRouterCatalog(ctx, &list[i])
	}
}
