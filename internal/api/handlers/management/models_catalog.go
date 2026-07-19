package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pricingsource"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ListModelsCatalog handles GET /v0/management/models-catalog.
//
// Query parameters:
//   - page                (default 1, 1-indexed)
//   - page_size           (default 25, max 200)
//   - provider            (optional, scopes both page and count to the
//     upstream/proxy provider column)
//   - official_provider   (optional, scopes both page and count to the
//     official provider column)
//   - available_only      (default false): when "1" or "true", filters the
//     persisted catalog down to the IDs the active in-memory registry reports
//     as currently available (across openai/claude/gemini handler types).
//     This is what the dashboard should call to show "live" models only.
func (h *Handler) ListModelsCatalog(c *gin.Context) {
	_, _, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	provider := c.Query("provider")
	officialProvider := c.Query("official_provider")
	availableOnly := boolFromQuery(c.Query("available_only"))

	var idFilter []string
	if availableOnly {
		idFilter = registry.GetGlobalRegistry().AvailableModelIDList()
		// If the in-memory registry has no live models (cold start with no
		// clients registered), we still want a valid response: return an
		// empty page with total=0 rather than 0 rows matched-after-zero.
		if len(idFilter) == 0 {
			c.JSON(http.StatusOK, pgModelsCatalogResponse{
				Models:     []any{},
				Page:       page,
				PageSize:   pageSize,
				Total:      0,
				TotalPages: 0,
			})
			return
		}
	}

	stored, total, err := models.SelectAllPaged(c.Request.Context(), page, pageSize, provider, officialProvider, idFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	out := make([]any, 0, len(stored))
	for i := range stored {
		out = append(out, stored[i])
	}
	c.JSON(http.StatusOK, pgModelsCatalogResponse{
		Models:     out,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// pgModelsCatalogResponse is the paginated list response for the catalog.
type pgModelsCatalogResponse struct {
	Models     []any `json:"models"`
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// GetModelsCatalogCount handles GET /v0/management/models-catalog/count.
func (h *Handler) GetModelsCatalogCount(c *gin.Context) {
	_, _, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	count, err := models.Count(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}

func boolFromQuery(v string) bool {
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES":
		return true
	}
	return false
}

// SyncModelsFromV1 handles POST /v0/management/models-catalog/sync-from-v1.
//
// The endpoint mirrors the live caller-facing /v1/models response into the
// models_catalog PostgreSQL table. Authentication for the inner /v1/models
// call uses the first available caller API key, resolved in this order:
//
//  1. The first active PG-managed key in api_keys (cannot use its plaintext
//     because only the hash is persisted — fall through to step 2).
//  2. The first entry of cfg.APIKeys (ie. the `api-keys:` list from the
//     YAML config_store row, mirrored to file via PostgresStore.syncConfig).
//
// The caller key is used purely as an auth transport to exercise the same
// /v1/models pipeline a real caller hits (AuthMiddleware → policyMiddleware
// → unifiedModelsHandler → registry.GetAvailableModels). The returned model
// set is then upserted into models_catalog via the ModelsStore so pricing
// rows can be attached.
//
// Body parameters (all optional JSON):
//   - caller_key: override the auto-picked key. Use when the operator wants
//     to sync with a key that has a specific policy (e.g. unrestricted).
func (h *Handler) SyncModelsFromV1(c *gin.Context) {
	apiKeys, _, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}

	var body struct {
		CallerKey string `json:"caller_key,omitempty"`
	}
	// Body is optional — ignored when the request has none.
	_ = c.ShouldBindJSON(&body)

	callerKey := strings.TrimSpace(body.CallerKey)
	if callerKey == "" {
		picked, err := h.pickAutoCallerKey(c, apiKeys)
		if err != nil {
			c.JSON(http.StatusPreconditionFailed, gin.H{"error": gin.H{
				"type":    "no_caller_key",
				"message": err.Error(),
			}})
			return
		}
		callerKey = picked
	}

	// Probe the live /v1/models pipeline. We hit the same Gin engine that
	// mounts this handler so the request honors route-group middleware
	// (AuthMiddleware + PolicyMiddleware) exactly as a real caller would.
	resp, err := h.callV1Models(c, callerKey)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type":    "v1_models_probe_failed",
			"message": err.Error(),
		}})
		return
	}
	defer resp.Body.Close()

	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type":    "v1_models_decode_failed",
			"message": "failed to decode /v1/models response: " + err.Error(),
		}})
		return
	}
	if len(parsed.Data) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"synced":            0,
			"caller_key_source": "auto",
			"message":           "/v1/models returned no models — registry has no live clients",
		})
		return
	}

	// Convert the OpenAI-shape rows to store.StoredModel and upsert. We use
	// the OwnedBy field as provider when present (registry populates it
	// from model definitions); otherwise fall back to "unknown".
	stored := make([]store.StoredModel, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		provider := strings.TrimSpace(m.OwnedBy)
		if provider == "" {
			provider = "unknown"
		}
		stored = append(stored, store.StoredModel{
			ID:               m.ID,
			Provider:         provider,
			OfficialProvider: strOrDefault(m.OwnedBy, provider),
			Object:           strOrDefault(m.Object, "model"),
			Created:          m.Created,
			OwnedBy:          strOrDefault(m.OwnedBy, provider),
			Type:             provider,
		})
	}
	if err := models.UpsertModels(c.Request.Context(), stored); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "upsert failed: " + err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"synced":            len(stored),
		"caller_key_source": "auto",
		"caller_key_prefix": prefixOnly(callerKey),
	})
}

// pickAutoCallerKey returns the first plaintext caller API key the server
// can use to authenticate GET /v1/models.
//
// PG-managed keys are only stored as SHA-256 hashes — we can never recover
// their plaintext to send as a Bearer token. So this method falls through to
// the legacy `cfg.APIKeys` list (the YAML `api-keys:` field, sourced from
// config_store on hot-reload), which DOES contain plaintext keys. When the
// caller wants to sync via a PG-managed key, they must explicitly POST the
// secret via the caller_key body field.
func (h *Handler) pickAutoCallerKey(_ *gin.Context, _ *store.APIKeyStore) (string, error) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	if cfg == nil {
		return "", errNoCallerKey
	}
	for _, k := range cfg.APIKeys {
		if trimmed := strings.TrimSpace(k); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", errNoCallerKey
}

// callV1Models executes an internal GET /v1/models request against this
// server's own Gin engine so no network round-trip is needed and no listen
// port must be guessed. The request carries the caller's Bearer token,
// letting AuthMiddleware + pg-access provider resolve the key as a real
// caller would.
func (h *Handler) callV1Models(c *gin.Context, callerKey string) (*http.Response, error) {
	if h.v1ModelsHandler == nil {
		return nil, errNoCallerKey
	}
	path := "/v1/models"
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+callerKey)

	rec := newV1ModelsRecorder()
	h.v1ModelsHandler.ServeHTTP(rec, req)
	return rec.toHTTPResponse(), nil
}

// errNoCallerKey signals that no caller API key is available to authenticate
// the /v1/models probe. Returned when neither PG-managed keys (hashes only)
// nor the legacy config_store APIKeys list contains a usable plaintext.
var errNoCallerKey = errors.New("no caller API key available: PG-managed keys are hashed (use the caller_key body field to pass a plaintext secret), and cfg.APIKeys is empty")

// strOrDefault returns value when non-empty, otherwise def.
func strOrDefault(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}

// prefixOnly returns the first 8 characters of the caller key for log/display
// purposes. Never enough to reconstruct the secret, just enough to identify
// which key was used.
func prefixOnly(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:8] + "…"
}

// newV1ModelsRecorder constructs an http.ResponseWriter-like buffer that
// captures the inner /v1/models response without hitting the network.
func newV1ModelsRecorder() *v1Recorder {
	return &v1Recorder{
		header: http.Header{},
		body:   &strings.Builder{},
		status: http.StatusOK,
	}
}

// v1Recorder is a minimal http.ResponseWriter implementation used to serve
// an inner /v1/models call directly through the Gin engine. It does not
// need full-feature parity — gin only calls WriteHeader, Write, and Header
// for plain JSON responses.
type v1Recorder struct {
	header http.Header
	body   *strings.Builder
	status int
}

func (r *v1Recorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}
func (r *v1Recorder) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *v1Recorder) WriteHeader(statusCode int)  { r.status = statusCode }

// toHTTPResponse rebuilds the synthetic response as an *http.Response so
// the caller can treat it identically to a real network probe.
func (r *v1Recorder) toHTTPResponse() *http.Response {
	return &http.Response{
		StatusCode: r.status,
		Header:     r.header,
		Body:       io.NopCloser(strings.NewReader(r.body.String())),
	}
}

// pricingSuggestion represents one published price for a model from a
// particular source. The dashboard renders these as side-by-side candidates
// so the operator can pick which to accept.
type pricingSuggestion struct {
	Source           string  `json:"source"`
	InputPer1M       float64 `json:"input_per_1m_usd"`
	OutputPer1M      float64 `json:"output_per_1m_usd"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd"`
	Notes            string  `json:"notes,omitempty"`
}

// pricingSuggestionRow is the per-model entry the preview endpoint returns.
type pricingSuggestionRow struct {
	ModelID          string              `json:"model_id"`
	Provider         string              `json:"provider,omitempty"`
	OfficialProvider string              `json:"official_provider,omitempty"`
	CurrentPricing   *store.Pricing      `json:"current_pricing,omitempty"`
	Suggestions      []pricingSuggestion `json:"suggestions"`
}

// SyncPricingPreview handles POST /v0/management/models-catalog/sync-pricing-preview.
//
// Returns one row per catalog model containing:
//   - current_pricing: the existing model_pricing row (nil when none set yet).
//   - suggestions: every published price the bundled catalog knows for the
//     model's id, possibly from multiple sources (openai-official,
//     openrouter, cloudflare, etc) so the operator can compare and pick.
//
// The call is read-only: no model_pricing row is written. Apply the picks
// with sync-pricing-apply.
func (h *Handler) SyncPricingPreview(c *gin.Context) {
	_, usage, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	// Stream-fetch the whole catalog. Catalog size is bounded by the embedded
	// / synced set, so pulling into memory is acceptable; for very large
	// catalogs the caller could pair this with a provider filter (TODO).
	allModels, err := selectAllStreamed(ctx, models)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to load catalog: " + err.Error(),
		}})
		return
	}

	// Collect model IDs and bulk-match against the pricing source index.
	ids := make([]string, 0, len(allModels))
	for _, m := range allModels {
		ids = append(ids, m.ID)
	}
	matches := pricingsource.MatchAll(ids)
	currentByID, err := loadCurrentPricing(ctx, usage, ids)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to load current pricing: " + err.Error(),
		}})
		return
	}

	rows := make([]pricingSuggestionRow, 0, len(allModels))
	for _, m := range allModels {
		suggestions := toPricingSuggestions(matches[m.ID])
		row := pricingSuggestionRow{
			ModelID:          m.ID,
			Provider:         m.Provider,
			OfficialProvider: m.OfficialProvider,
			CurrentPricing:   currentByID[m.ID],
			Suggestions:      suggestions,
		}
		rows = append(rows, row)
	}
	c.JSON(http.StatusOK, gin.H{
		"rows":    rows,
		"sources": pricingsource.Sources(),
	})
}

// PricingApplySelection is one model's chosen pricing source. The operator
// selects which source's numbers to adopt (or "manual" for hand-edited values
// passed inline).
type PricingApplySelection struct {
	ModelID          string  `json:"model_id"`
	Source           string  `json:"source"`
	InputPer1M       float64 `json:"input_per_1m_usd,omitempty"`
	OutputPer1M      float64 `json:"output_per_1m_usd,omitempty"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd,omitempty"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd,omitempty"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd,omitempty"`
}

// SyncPricingApply handles POST /v0/management/models-catalog/sync-pricing-apply.
//
// Body:
//
//	{ "selections": [ { model_id, source }, ... ] }
//
// Each selection picks one source's published prices for that model and
// writes them into model_pricing. Source = "manual" lets the caller send
// inline numeric overrides (useful for ad-hoc adjustments not present in
// the bundled catalog).
//
// Returns the count of rows updated and the per-model outcome.
func (h *Handler) SyncPricingApply(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	var body struct {
		Selections []PricingApplySelection `json:"selections"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": err.Error(),
		}})
		return
	}
	if len(body.Selections) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type":    "invalid_request",
			"message": "selections is required and must be non-empty",
		}})
		return
	}

	// Each selection is resolved to a Pricing via the catalog index so the
	// operator can refer to any source by name. Pre-compute the bulk lookup
	// so the loop below is a cheap JOIN.
	modelIDs := make([]string, 0, len(body.Selections))
	for _, sel := range body.Selections {
		modelIDs = append(modelIDs, sel.ModelID)
	}
	matches := pricingsource.MatchAll(modelIDs)

	ctx := c.Request.Context()
	applied := 0
	outcomes := make([]map[string]any, 0, len(body.Selections))
	for _, sel := range body.Selections {
		p, ok := resolveSelection(sel, matches[sel.ModelID])
		if !ok {
			outcomes = append(outcomes, map[string]any{
				"model_id": sel.ModelID,
				"status":   "no_suggestion",
				"reason":   "no matching suggestion for source " + sel.Source,
			})
			continue
		}
		if err := usage.UpsertPricing(ctx, p); err != nil {
			outcomes = append(outcomes, map[string]any{
				"model_id": sel.ModelID,
				"status":   "error",
				"reason":   err.Error(),
			})
			continue
		}
		applied++
		outcomes = append(outcomes, map[string]any{
			"model_id": sel.ModelID,
			"status":   "applied",
			"source":   sel.Source,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"applied":   applied,
		"requested": len(body.Selections),
		"results":   outcomes,
	})
}

// resolveSelection translates an operator pick into a concrete Pricing row.
// When source == "manual", the inline numeric fields are used as-is. When the
// source matches a catalog entry, that entry's prices are adopted.
func resolveSelection(sel PricingApplySelection, suggestions []pricingsource.SuggestedPrice) (store.Pricing, bool) {
	if sel.Source == "manual" {
		return store.Pricing{
			ID:               sel.ModelID,
			InputPer1M:       sel.InputPer1M,
			OutputPer1M:      sel.OutputPer1M,
			CachedInputPer1M: sel.CachedInputPer1M,
			CachedReadPer1M:  sel.CachedReadPer1M,
			ReasoningPer1M:   sel.ReasoningPer1M,
		}, true
	}
	for _, s := range suggestions {
		if s.Source == sel.Source {
			return store.Pricing{
				ID:               s.Model,
				InputPer1M:       s.InputPer1M,
				OutputPer1M:      s.OutputPer1M,
				CachedInputPer1M: s.CachedInputPer1M,
				CachedReadPer1M:  s.CachedReadPer1M,
				ReasoningPer1M:   s.ReasoningPer1M,
			}, true
		}
	}
	return store.Pricing{}, false
}

// toPricingSuggestions converts the pricingsource return type into the
// JSON-friendly wire shape surfaced by the management API.
func toPricingSuggestions(in []pricingsource.SuggestedPrice) []pricingSuggestion {
	if len(in) == 0 {
		return nil
	}
	out := make([]pricingSuggestion, 0, len(in))
	for _, s := range in {
		out = append(out, pricingSuggestion{
			Source:           s.Source,
			InputPer1M:       s.InputPer1M,
			OutputPer1M:      s.OutputPer1M,
			CachedInputPer1M: s.CachedInputPer1M,
			CachedReadPer1M:  s.CachedReadPer1M,
			ReasoningPer1M:   s.ReasoningPer1M,
			Notes:            s.Notes,
		})
	}
	return out
}

// selectAllStreamed pulls every catalog row by paginating through
// SelectAllPaged. We use a generous page size to keep round-trips low even
// for thousand-row catalogs.
func selectAllStreamed(ctx context.Context, models *store.ModelsStore) ([]store.StoredModel, error) {
	var out []store.StoredModel
	const batch = 500
	page := 1
	for {
		rows, _, err := models.SelectAllPaged(ctx, page, batch, "", "", nil)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		out = append(out, rows...)
		if len(rows) < batch {
			break
		}
		page++
	}
	return out, nil
}

// loadCurrentPricing fetches the existing model_pricing row for every model
// id, returning a map keyed by id. Missing rows are simply absent from the
// map (operators distinguishing "no price" from "zero price" via the UI).
func loadCurrentPricing(ctx context.Context, usage *store.UsageStore, ids []string) (map[string]*store.Pricing, error) {
	out := make(map[string]*store.Pricing, len(ids))
	for _, id := range ids {
		p, err := usage.GetPricing(ctx, id)
		if err != nil {
			return nil, err
		}
		if p.ID == "" {
			continue
		}
		// All-zero rows are functionally "no price set"; surface them as
		// missing so the dashboard shows a "Add" call-to-action rather than
		// a "0.00 / 0.00 / 0.00" panel.
		if p.InputPer1M == 0 && p.OutputPer1M == 0 && p.CachedInputPer1M == 0 &&
			p.CachedReadPer1M == 0 && p.ReasoningPer1M == 0 {
			continue
		}
		copy := p
		out[id] = &copy
	}
	return out, nil
}

// ModelEntryRequest is the body shape for PUT /models-catalog/entry/:id/:provider.
// All fields except id+provider (which come from the URL) are accepted.
// Pointer-typed optional fields stay nil when omitted.
type ModelEntryRequest struct {
	Object                     string            `json:"object"`
	Created                    int64             `json:"created"`
	OwnedBy                    string            `json:"owned_by"`
	Type                       string            `json:"type"`
	OfficialProvider           string            `json:"official_provider"`
	DisplayName                string            `json:"display_name"`
	Name                       string            `json:"name"`
	Version                    string            `json:"version"`
	Description                string            `json:"description"`
	InputTokenLimit            int               `json:"input_token_limit"`
	OutputTokenLimit           int               `json:"output_token_limit"`
	SupportedGenerationMethods []string          `json:"supported_generation_methods"`
	ContextLength              int               `json:"context_length"`
	MaxCompletionTokens        int               `json:"max_completion_tokens"`
	SupportedParameters        []string          `json:"supported_parameters"`
	InputModalities            []string          `json:"input_modalities"`
	OutputModalities           []string          `json:"output_modalities"`
	SupportsWebSearch          bool              `json:"supports_web_search"`
	Thinking                   map[string]any    `json:"thinking"`
	OverrideHeader             map[string]string `json:"override_header"`
	UserDefined                bool              `json:"user_defined"`
}

// GetModelEntry handles GET /v0/management/models-catalog/entry/:id/:provider.
// Returns the single catalog row identified by (id, provider).
func (h *Handler) GetModelEntry(c *gin.Context) {
	_, _, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	model, err := models.SelectOne(c.Request.Context(), c.Param("id"), c.Param("provider"))
	if err != nil {
		translateModelError(c, err)
		return
	}
	c.JSON(http.StatusOK, model)
}

// PutModelEntry handles PUT /v0/management/models-catalog/entry/:id/:provider.
// Inserts or replaces a single model row. Use this for ad-hoc edits from
// the dashboard: rename display fields, adjust context length, add a new
// user-defined model that is not present in the upstream registry, etc.
func (h *Handler) PutModelEntry(c *gin.Context) {
	_, _, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	provider := c.Param("provider")
	var req ModelEntryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	entry := store.StoredModel{
		ID:                         id,
		Provider:                   provider,
		OfficialProvider:           req.OfficialProvider,
		Object:                     strOrDefault(req.Object, "model"),
		Created:                    req.Created,
		OwnedBy:                    strOrDefault(req.OwnedBy, provider),
		Type:                       strOrDefault(req.Type, provider),
		DisplayName:                req.DisplayName,
		Name:                       req.Name,
		Version:                    req.Version,
		Description:                req.Description,
		InputTokenLimit:            req.InputTokenLimit,
		OutputTokenLimit:           req.OutputTokenLimit,
		SupportedGenerationMethods: req.SupportedGenerationMethods,
		ContextLength:              req.ContextLength,
		MaxCompletionTokens:        req.MaxCompletionTokens,
		SupportedParameters:        req.SupportedParameters,
		InputModalities:            req.InputModalities,
		OutputModalities:           req.OutputModalities,
		SupportsWebSearch:          req.SupportsWebSearch,
		Thinking:                   req.Thinking,
		OverrideHeader:             req.OverrideHeader,
		UserDefined:                req.UserDefined,
	}
	if err := models.UpsertOne(c.Request.Context(), entry); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Re-read so the caller sees the canonical stored shape (with created
	// timestamps and defaults filled by the database).
	stored, err := models.SelectOne(c.Request.Context(), id, provider)
	if err != nil {
		c.JSON(http.StatusCreated, gin.H{"model": entry, "id": id, "provider": provider})
		return
	}
	c.JSON(http.StatusOK, stored)
}

// DeleteModelEntry handles DELETE /v0/management/models-catalog/entry/:id/:provider.
// Removes a single model row. Useful for pruning stale entries that are no
// longer reported by /v1/models and that the operator does not want to keep.
func (h *Handler) DeleteModelEntry(c *gin.Context) {
	_, _, models, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if err := models.DeleteOne(c.Request.Context(), c.Param("id"), c.Param("provider")); err != nil {
		translateModelError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": c.Param("id"), "provider": c.Param("provider"), "deleted": true})
}

// translateModelError maps a store error to the right HTTP response.
func translateModelError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrModelNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "model not found"}})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
}
