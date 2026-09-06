package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// UpstreamProviderResponse is the dashboard-facing projection of a
// store.UpstreamProvider row. It embeds the row verbatim and adds a
// computed ProviderKey: the executor/registry identifier the proxy uses for
// auth selection (e.g. "openai-compatible-opencode", "claude"). The
// dashboard's per-model routing picker surfaces ProviderKey next to each
// upstream provider so an operator can pin a model to a provider even when
// no live auth has registered for it yet (the in-memory registry would
// otherwise hide such providers — see GetModelProviders).
type UpstreamProviderResponse struct {
	store.UpstreamProvider
	ProviderKey string `json:"provider_key"`
}

// toUpstreamProviderResponse wraps a store row with its computed provider key.
func toUpstreamProviderResponse(p store.UpstreamProvider) UpstreamProviderResponse {
	return UpstreamProviderResponse{
		UpstreamProvider: p,
		ProviderKey:      util.UpstreamProviderKey(p.ProviderType, p.Name, p.ID),
	}
}

// upstreamProvidersStore returns the PG-backed upstream provider store,
// writing a 503 response when PG is not configured.
func (h *Handler) upstreamProvidersStore(c *gin.Context) (store.UpstreamProviderStore, bool) {
	h.mu.Lock()
	s := h.pgUpstreamProviders
	h.mu.Unlock()
	if s == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return s, true
}

// upstreamProviderErrorResponse maps a store error to an HTTP response.
func (h *Handler) upstreamProviderErrorResponse(c *gin.Context, err error) {
	if errors.Is(err, store.ErrUpstreamProviderNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"type": "not_found", "message": "upstream provider not found",
		}})
		return
	}
	msg := err.Error()
	lowerMsg := strings.ToLower(msg)
	if strings.Contains(lowerMsg, "duplicate upstream provider api key entry name") ||
		strings.Contains(lowerMsg, "upstream_provider_entries_provider_name") {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"type": "conflict", "message": "an API key entry with that name already exists for this upstream provider",
		}})
		return
	}
	if strings.Contains(lowerMsg, "duplicate key") || strings.Contains(lowerMsg, "unique constraint") {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"type": "conflict", "message": "an upstream provider with that (provider_type, file_name) already exists",
		}})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request", "message": msg,
	}})
}

// validateUpstreamProviderRequest checks a decoded request body before it
// reaches the store. Unknown routing strategies are rejected here with the
// descriptive config error so the operator sees a fixable message instead of
// the strategy being silently dropped to "unset"; recognized aliases and
// canonical values pass through (toUpstreamProvider canonicalizes them).
func validateUpstreamProviderRequest(body *upstreamProviderReq) error {
	if raw := strings.TrimSpace(body.RoutingStrategy); raw != "" && config.NormalizePoolRoutingStrategy(raw) == "" {
		return config.ValidatePoolRoutingStrategy(raw)
	}
	return nil
}

// toUpstreamProvider maps a decoded JSON body to a store.UpstreamProvider,
// zeroing the id/timestamps (set by the DB on insert/update).
func toUpstreamProvider(body *upstreamProviderReq) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:            strings.TrimSpace(body.ProviderType),
		Name:                    strings.TrimSpace(body.Name),
		Priority:                body.Priority,
		RoutingStrategy:         config.NormalizePoolRoutingStrategy(body.RoutingStrategy),
		Disabled:                body.Disabled,
		Prefix:                  strings.TrimSpace(body.Prefix),
		APIKey:                  strings.TrimSpace(body.APIKey),
		BaseURL:                 strings.TrimSpace(body.BaseURL),
		ProxyURL:                strings.TrimSpace(body.ProxyURL),
		ProxyPoolID:             body.ProxyPoolID,
		Label:                   strings.TrimSpace(body.Label),
		Email:                   strings.TrimSpace(body.Email),
		FileName:                strings.TrimSpace(body.FileName),
		SourceBackend:           strings.TrimSpace(body.SourceBackend),
		Websockets:              body.Websockets,
		RebuildMidSystemMessage: body.RebuildMidSystemMessage,
		ExperimentalCCHSigning:  body.ExperimentalCCHSigning,
		CloakMode:               strings.TrimSpace(body.CloakMode),
		CloakStrictMode:         body.CloakStrictMode,
		CloakSensitiveWords:     body.CloakSensitiveWords,
		TokenAccessToken:        strings.TrimSpace(body.TokenAccessToken),
		TokenRefreshToken:       strings.TrimSpace(body.TokenRefreshToken),
		TokenTokenType:          strings.TrimSpace(body.TokenTokenType),
		TokenScope:              strings.TrimSpace(body.TokenScope),
		ExtraConfig:             body.ExtraConfig,
		Headers:                 body.Headers,
		ExcludedModels:          body.ExcludedModels,
	}
	if body.CloakCacheUserID != nil {
		v := *body.CloakCacheUserID
		p.CloakCacheUserID = &v
	}
	if body.TokenExpired != nil {
		v := *body.TokenExpired
		p.TokenExpired = &v
	}
	if body.TokenExpiry != "" {
		if t, ok := parseRFC3339(body.TokenExpiry); ok {
			p.TokenExpiry = &t
		}
	}
	// Convert request model entries to store structs.
	for _, m := range body.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{
			Name:             m.Name,
			Alias:            m.Alias,
			DisplayName:      m.DisplayName,
			ForceMapping:     m.ForceMapping,
			Fork:             m.Fork,
			Image:            m.Image,
			InputModalities:  m.InputModalities,
			OutputModalities: m.OutputModalities,
			Thinking:         m.Thinking,
		})
	}
	// Convert api-key entry requests to store structs. The entry id is
	// round-tripped so the PG store can preserve the persisted child row.
	// Name and api_key are passed through unchanged; the store normalizes
	// them and rejects duplicates/identity conflicts without leaking any
	// secret material.
	for _, e := range body.APIKeyEntries {
		entry := store.UpstreamProviderAPIKey{
			ID:          e.ID,
			Name:        e.Name,
			APIKey:      e.APIKey,
			ProxyURL:    e.ProxyURL,
			ProxyPoolID: e.ProxyPoolID,
			Disabled:    e.Disabled,
		}
		if e.Weight != nil {
			w := *e.Weight
			entry.Weight = &w
		}
		if e.Priority != nil {
			v := *e.Priority
			entry.Priority = &v
		}
		p.APIKeyEntries = append(p.APIKeyEntries, entry)
	}
	return p
}

// ListUpstreamProviders handles GET /v0/management/upstream-providers.
//
// Returns every normalized upstream provider row with its child collections
// (models, headers, excluded_models, api_key_entries) joined in. Each
// api_key_entries element is the persisted child row including its id and
// name, so the dashboard can round-trip entry identity without re-deriving
// it from array position.
func (h *Handler) ListUpstreamProviders(c *gin.Context) {
	srcs, ok := h.upstreamProvidersStore(c)
	if !ok {
		return
	}
	if v := c.Query("provider_type"); v != "" {
		// Optional filter by provider_type.
		all, err := srcs.List(c.Request.Context())
		if err != nil {
			h.upstreamProviderErrorResponse(c, err)
			return
		}
		filtered := make([]UpstreamProviderResponse, 0, len(all))
		for _, p := range all {
			if p.ProviderType == v {
				filtered = append(filtered, toUpstreamProviderResponse(p))
			}
		}
		c.JSON(http.StatusOK, gin.H{"providers": filtered})
		return
	}
	rows, err := srcs.List(c.Request.Context())
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	out := make([]UpstreamProviderResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, toUpstreamProviderResponse(p))
	}
	c.JSON(http.StatusOK, gin.H{"providers": out})
}

// GetUpstreamProvider handles GET /v0/management/upstream-providers/:id.
func (h *Handler) GetUpstreamProvider(c *gin.Context) {
	srcs, ok := h.upstreamProvidersStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	p, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	c.JSON(http.StatusOK, toUpstreamProviderResponse(*p))
}

// CreateUpstreamProvider handles POST /v0/management/upstream-providers.
//
// Body: upstreamProviderReq. The response echoes the created row with its id.
// After creation the config.yaml provider sections + auth-dir JSON files are
// re-rendered from the table and the in-memory clients are reloaded.
func (h *Handler) CreateUpstreamProvider(c *gin.Context) {
	srcs, ok := h.upstreamProvidersStore(c)
	if !ok {
		return
	}
	var body upstreamProviderReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if err := validateUpstreamProviderRequest(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	p := toUpstreamProvider(&body)
	created, err := srcs.Create(c.Request.Context(), p)
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	// Mirror the provider's model definitions into models_catalog
	// synchronously so the Models Catalog reflects the new models
	// immediately, rather than after the async config-reload → registry →
	// PGSync hops. Best-effort: a catalog sync miss never blocks the save.
	h.syncCatalogFromUpstreamProviders(c.Request.Context())
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusCreated, toUpstreamProviderResponse(*created))
}

// UpdateUpstreamProvider handles PUT /v0/management/upstream-providers/:id.
//
// Body: upstreamProviderReq. All mutable fields are replaced; API-key child
// rows are synchronized by the submitted id (existing ids are updated in
// place, omitted ids are deleted, and zero ids are inserted). After update
// the artifacts are re-rendered and the clients reloaded. When the upstream
// name/identifier changed, persisted models_catalog rows are repointed from
// the old name to the new name so the Models Catalog does not keep a phantom
// Upstream Provider entry under the old name (catalog rows for OpenAI-
// compatible providers key their provider column off the upstream name
// verbatim, via OwnedBy).
func (h *Handler) UpdateUpstreamProvider(c *gin.Context) {
	srcs, ok := h.upstreamProvidersStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	var body upstreamProviderReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if err := validateUpstreamProviderRequest(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	// Capture the pre-update name so a rename can repoint the models_catalog
	// rows that key their provider column off this upstream's name (OpenAI-
	// compatible catalog rows use OwnedBy = upstream name verbatim). Built-in
	// providers (claude / gemini / ...) have their catalog keyed by a fixed
	// owner string ("anthropic", "google"), not the upstream name, so the
	// rename is a no-op when nothing matches — safe to run unconditionally.
	existing, err := srcs.Get(c.Request.Context(), id)
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	oldName := strings.TrimSpace(existing.Name)

	p := toUpstreamProvider(&body)
	p.ID = id
	updated, err := srcs.Update(c.Request.Context(), p)
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}

	// If the upstream name changed, repoint persisted models_catalog rows so
	// the old name doesn't linger as a phantom Upstream Provider entry in
	// the Models Catalog. Best-effort: a catalog sync miss never blocks the
	// upstream save (the row is already updated); only the catalog display
	// stays stale until the next sync.
	newName := strings.TrimSpace(updated.Name)
	if oldName != "" && newName != "" && oldName != newName {
		_, _, models, _, pgOK := h.requirePG(c)
		if pgOK && models != nil {
			if rerr := models.RenameProvider(c.Request.Context(), oldName, newName); rerr != nil {
				log.WithError(rerr).
					WithField("old_name", oldName).
					WithField("new_name", newName).
					Warn("upstream-providers: models_catalog rename did not complete; catalog may show a stale provider until next sync")
			}
		}
	}

	// Mirror the provider's (possibly renamed) model definitions into
	// models_catalog synchronously so the Models Catalog reflects the
	// updated models immediately, rather than after the async config-reload
	// → registry → PGSync hops. Best-effort: a catalog sync miss never
	// blocks the save.
	h.syncCatalogFromUpstreamProviders(c.Request.Context())
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, toUpstreamProviderResponse(*updated))
}

// DeleteUpstreamProvider handles DELETE /v0/management/upstream-providers/:id.
//
// Idempotent. After deletion the artifacts are re-rendered and the clients
// reloaded so the provider is dropped from routing.
func (h *Handler) DeleteUpstreamProvider(c *gin.Context) {
	srcs, ok := h.upstreamProvidersStore(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	if err := srcs.Delete(c.Request.Context(), id); err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
