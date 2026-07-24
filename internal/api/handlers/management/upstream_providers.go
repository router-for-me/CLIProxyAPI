package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

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
	if strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint") {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"type": "conflict", "message": "an upstream provider with that (provider_type, file_name) already exists",
		}})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request", "message": msg,
	}})
}

// toUpstreamProvider maps a decoded JSON body to a store.UpstreamProvider,
// zeroing the id/timestamps (set by the DB on insert/update).
func toUpstreamProvider(body *upstreamProviderReq) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:            strings.TrimSpace(body.ProviderType),
		Name:                    strings.TrimSpace(body.Name),
		Priority:                body.Priority,
		Disabled:                body.Disabled,
		Prefix:                  strings.TrimSpace(body.Prefix),
		APIKey:                  strings.TrimSpace(body.APIKey),
		BaseURL:                 strings.TrimSpace(body.BaseURL),
		ProxyURL:                strings.TrimSpace(body.ProxyURL),
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
	// Convert api-key entry requests to store structs.
	for _, e := range body.APIKeyEntries {
		p.APIKeyEntries = append(p.APIKeyEntries, store.UpstreamProviderAPIKey{
			APIKey:   e.APIKey,
			ProxyURL: e.ProxyURL,
		})
	}
	return p
}

// ListUpstreamProviders handles GET /v0/management/upstream-providers.
//
// Returns every normalized upstream provider row with its child collections
// (models, headers, excluded_models, api_key_entries) joined in.
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
		filtered := make([]store.UpstreamProvider, 0, len(all))
		for _, p := range all {
			if p.ProviderType == v {
				filtered = append(filtered, p)
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
	c.JSON(http.StatusOK, gin.H{"providers": rows})
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
	c.JSON(http.StatusOK, p)
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
	p := toUpstreamProvider(&body)
	created, err := srcs.Create(c.Request.Context(), p)
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusCreated, created)
}

// UpdateUpstreamProvider handles PUT /v0/management/upstream-providers/:id.
//
// Body: upstreamProviderReq. All mutable fields are replaced. After update
// the artifacts are re-rendered and the clients reloaded.
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
	p := toUpstreamProvider(&body)
	p.ID = id
	updated, err := srcs.Update(c.Request.Context(), p)
	if err != nil {
		h.upstreamProviderErrorResponse(c, err)
		return
	}
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, updated)
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
