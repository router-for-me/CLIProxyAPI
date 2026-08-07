package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// GlobalModelRow is one catalog row backing a model id, projected for the
// Global Model editor. It carries only the fields the operator needs to see
// which providers are affected by a global edit (identity + the per-row
// provider/official_provider/display_name). The full attributes come from the
// canonical row in the parent GlobalModelResponse.
type GlobalModelRow struct {
	Provider         string `json:"provider"`
	OfficialProvider string `json:"official_provider"`
	OwnedBy          string `json:"owned_by"`
	Type             string `json:"type"`
	DisplayName      string `json:"display_name"`
	UserDefined      bool   `json:"user_defined"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

// GlobalModelResponse is the merged/distinct view the Global Model editor
// loads on open. Canonical attributes are seeded from the first matching row
// (ordered by provider), pricing is the existing global model_pricing row
// (nil when unset), and rows lists every catalog row sharing the model id so
// the operator can preview who will be affected by a global edit.
type GlobalModelResponse struct {
	ModelID   string            `json:"model_id"`
	Live      bool              `json:"live"`
	Pricing   *store.Pricing    `json:"pricing,omitempty"`
	Canonical store.StoredModel `json:"canonical"`
	Rows      []GlobalModelRow  `json:"rows"`
	ProviderN int               `json:"provider_count"`
	// Routing is the per-model-id global routing override (pinned providers +
	// strategy + priorities), present (non-nil) when the operator has set one.
	Routing *store.ModelRoute `json:"routing,omitempty"`
}

// GlobalModelAttributesRequest is the operator-editable attribute set for the
// Global Model editor. Pointer fields are nil when the operator did not toggle
// "apply globally" for that field, so PutGlobalModel omits them from the
// UpdateByID patch (an omitted field is never nullified). The shape mirrors
// ModelEntryRequest but is restricted to the attributes that make sense to fan
// out across providers (pricing is separate, handled via the Pricing field).
type GlobalModelAttributesRequest struct {
	OfficialProvider    *string   `json:"official_provider,omitempty"`
	DisplayName         *string   `json:"display_name,omitempty"`
	Description         *string   `json:"description,omitempty"`
	ContextLength       *int      `json:"context_length,omitempty"`
	MaxCompletionTokens *int      `json:"max_completion_tokens,omitempty"`
	InputTokenLimit     *int      `json:"input_token_limit,omitempty"`
	OutputTokenLimit    *int      `json:"output_token_limit,omitempty"`
	InputModalities     *[]string `json:"input_modalities,omitempty"`
	OutputModalities    *[]string `json:"output_modalities,omitempty"`
	// UserDefined is the per-row "user-defined" flag. The auto-sync path
	// writes it as false, so a model id fanned out across multiple upstream
	// providers can end up with one row marked user_defined and the rest not.
	// Surfacing it here lets the dashboard's "Apply globally" checkbox flip
	// every row sharing the model id in one PUT, instead of forcing the
	// operator to edit each (id, provider) row individually.
	UserDefined *bool `json:"user_defined,omitempty"`
}

// GlobalModelRoutingRequest is the operator-editable routing override for the
// Global Model editor. It pins the model id to a subset of upstream providers,
// mirroring a Models Group ModelRoute (the request-time handler applies the same
// intersect/order/stash mechanics). Per-model caps (RPM/budget/discount) are
// policy concepts and are not part of a Global route.
type GlobalModelRoutingRequest struct {
	Providers  []string                 `json:"providers"`
	Strategy   string                   `json:"strategy,omitempty"`
	Priorities []store.ProviderPriority `json:"priorities,omitempty"`
}

// GlobalModelPutRequest is the body for PUT /models-catalog/global/:id. Either
// Attributes, Routing, or Pricing (or any combination) may be present. When a
// section is omitted it is left untouched.
type GlobalModelPutRequest struct {
	Attributes *GlobalModelAttributesRequest `json:"attributes,omitempty"`
	Routing    *GlobalModelRoutingRequest    `json:"routing,omitempty"`
	Pricing    *store.Pricing                `json:"pricing,omitempty"`
}

// GlobalModelResult is the PUT response: how many catalog rows had attributes
// updated, whether the (global) pricing row was written, and whether a global
// routing override was set (or cleared).
type GlobalModelResult struct {
	ModelID      string `json:"model_id"`
	RowsUpdated  int64  `json:"rows_updated"`
	PricingSet   bool   `json:"pricing_set"`
	RouteSet     bool   `json:"route_set"`
	RouteCleared bool   `json:"route_cleared,omitempty"`
}

// GetGlobalModel handles GET /v0/management/models-catalog/global/:id.
//
// Returns the merged view for a model id: every catalog row that shares the id
// (across providers), the canonical attributes (from the first row ordered by
// provider), and the existing pricing (already global per model id). The
// dashboard's Global Model editor uses this to seed the form and preview the
// affected providers. Returns 404 when no catalog row matches the id.
func (h *Handler) GetGlobalModel(c *gin.Context) {
	_, usage, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "id path parameter is required"}})
		return
	}
	rows, err := models.SelectByIDAllProviders(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "no catalog rows match this model id"}})
		return
	}
	// Canonical attributes come from the first row (SelectByIDAllProviders
	// orders by provider). This is the representative form the operator edits.
	canonical := rows[0]

	pricing, err := usage.GetPricing(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	var pricingPtr *store.Pricing
	if pricing.ID != "" && !isZeroPricing(pricing) {
		cp := pricing
		pricingPtr = &cp
	}

	liveIDs := liveAvailableIDsSet()
	resp := GlobalModelResponse{
		ModelID:   canonical.ID,
		Live:      liveIDs[strings.ToLower(canonical.ID)],
		Pricing:   pricingPtr,
		Canonical: canonical,
		Rows:      make([]GlobalModelRow, 0, len(rows)),
		ProviderN: len(rows),
		Routing:   models.GlobalModelRoute(c.Request.Context(), id),
	}
	for _, r := range rows {
		updatedAt := ""
		if !r.UpdatedAt.IsZero() {
			updatedAt = r.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		resp.Rows = append(resp.Rows, GlobalModelRow{
			Provider:         r.Provider,
			OfficialProvider: r.OfficialProvider,
			OwnedBy:          r.OwnedBy,
			Type:             r.Type,
			DisplayName:      r.DisplayName,
			UserDefined:      r.UserDefined,
			UpdatedAt:        updatedAt,
		})
	}
	c.JSON(http.StatusOK, resp)
}

// PutGlobalModel handles PUT /v0/management/models-catalog/global/:id.
//
// Body: GlobalModelPutRequest. Fan-out semantics:
//   - Attributes (when present): one UPDATE ... WHERE LOWER(id) = LOWER($1)
//     applies every non-nil attribute to every catalog row sharing the model
//     id, across providers. Omitted (nil) fields are left untouched.
//   - Pricing (when present): a single UpsertPricing write (model_pricing is
//     keyed by model id only, so this is inherently global across providers).
//
// Returns the count of rows updated and whether pricing was set. Best-effort
// on the attributes: a pricing write still succeeds even when no rows matched
// the id (the operator may be pre-setting pricing for a model the catalog has
// not yet ingested). 400 when neither section is present.
func (h *Handler) PutGlobalModel(c *gin.Context) {
	_, usage, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "id path parameter is required"}})
		return
	}
	var body GlobalModelPutRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	if body.Attributes == nil && body.Pricing == nil && body.Routing == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "at least one of attributes, routing or pricing must be present"}})
		return
	}

	ctx := c.Request.Context()
	result := GlobalModelResult{ModelID: id}

	if body.Attributes != nil {
		patch := globalAttributesToPatch(body.Attributes)
		n, err := models.UpdateByID(ctx, id, patch)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		result.RowsUpdated = n
	}

	if body.Routing != nil {
		route := store.GlobalModelRouteUpsert{
			Providers:  body.Routing.Providers,
			Strategy:   body.Routing.Strategy,
			Priorities: body.Routing.Priorities,
		}
		// A Global route is one-per-model-id, so "no providers" clears it (the
		// routing section cannot be left partially applied like attributes can).
		// Normalize first so the RouteSet result reflects the stored route.
		normalized := route.Normalize()
		clear := len(normalized.Providers) == 0
		if err := models.UpsertGlobalModelRoute(ctx, id, route, clear); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		result.RouteSet = !clear
		result.RouteCleared = clear
	}

	if body.Pricing != nil {
		p := *body.Pricing
		p.ID = id
		if err := usage.UpsertPricing(ctx, p); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		result.PricingSet = true
	}
	c.JSON(http.StatusOK, result)
}

// globalAttributesToPatch converts the JSON request shape into the
// store.GlobalModelPatch. The request fields are already pointers, so they
// pass through verbatim — omitted/nil fields stay nil and are skipped by
// UpdateByID. This indirection exists so the wire type can evolve (extra
// validation, server-side coercion) without touching the store API.
func globalAttributesToPatch(a *GlobalModelAttributesRequest) store.GlobalModelPatch {
	if a == nil {
		return store.GlobalModelPatch{}
	}
	return store.GlobalModelPatch{
		OfficialProvider:    a.OfficialProvider,
		DisplayName:         a.DisplayName,
		Description:         a.Description,
		ContextLength:       a.ContextLength,
		MaxCompletionTokens: a.MaxCompletionTokens,
		InputTokenLimit:     a.InputTokenLimit,
		OutputTokenLimit:    a.OutputTokenLimit,
		InputModalities:     a.InputModalities,
		OutputModalities:    a.OutputModalities,
		UserDefined:         a.UserDefined,
	}
}

// isZeroPricing mirrors the "all-zero means no price set" convention used by
// loadPricingForRows / loadCurrentPricing so a zero-value row is surfaced as
// missing (nil) rather than "$0.00 / $0.00".
func isZeroPricing(p store.Pricing) bool {
	return p.InputPer1M == 0 && p.OutputPer1M == 0 && p.CachedInputPer1M == 0 &&
		p.CachedReadPer1M == 0 && p.ReasoningPer1M == 0
}
