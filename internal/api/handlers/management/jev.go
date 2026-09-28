package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// jevSettingsStore is the surface the Jev settings handlers need. The real
// implementation is *store.JevStore; this interface lets tests inject a fake
// and keeps the handler's dependency surface tight (mirrors quotaRepo).
type jevSettingsStore interface {
	Get(ctx context.Context) (store.JevSettings, error)
	Upsert(ctx context.Context, set store.JevSettings, apiKey *string) (store.JevSettings, error)
}

// requireJev resolves the PG-backed Jev settings store or aborts with 503 when
// PG is not configured (mirrors requireAlerts / requireModelHealth).
func (h *Handler) requireJev(c *gin.Context) (jevSettingsStore, bool) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "PostgreSQL store is not configured",
		}})
		return nil, false
	}
	h.mu.Lock()
	s := h.pgJev
	h.mu.Unlock()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "Jev AI settings are not enabled (PostgreSQL store is not configured)",
		}})
		return nil, false
	}
	return s, true
}

// GetJevSettings handles GET /v0/management/jev/settings.
//
// The plaintext API key is never returned — only api_key_set and a masked
// api_key_prefix — so the response is safe to render and to log.
func (h *Handler) GetJevSettings(c *gin.Context) {
	s, ok := h.requireJev(c)
	if !ok {
		return
	}
	settings, errGet := s.Get(c.Request.Context())
	if errGet != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errGet.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

// jevSettingsRequest is the JSON body accepted by PutJevSettings. Pointer-typed
// fields are applied only when present, so a client can toggle the feature
// without resending the key. api_key semantics: nil = keep, "" = clear,
// non-empty = rotate and seal. base_url semantics: nil = keep, "" = reset to
// the public TypeSafe endpoint, non-empty = point at that API root.
type jevSettingsRequest struct {
	Enabled *bool   `json:"enabled,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
	Model   *string `json:"model,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
}

// PutJevSettings handles PUT /v0/management/jev/settings.
//
// Partial update: omitted fields keep their stored value. Endpoint changes (key
// or base URL) are sealed at rest where applicable and, when a rotator is
// wired, handed to the live classifier client so they take effect immediately
// rather than on the next restart.
func (h *Handler) PutJevSettings(c *gin.Context) {
	s, ok := h.requireJev(c)
	if !ok {
		return
	}
	var req jevSettingsRequest
	if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errBind.Error()})
		return
	}
	ctx := c.Request.Context()
	current, errGet := s.Get(ctx)
	if errGet != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errGet.Error()})
		return
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.Model != nil {
		if model := strings.TrimSpace(*req.Model); model != "" {
			current.Model = model
		}
	}
	if req.BaseURL != nil {
		// An empty value is meaningful: it resets to the public endpoint rather
		// than leaving the previous override in place. clampJevSettings fills in
		// the default, so the stored value is never blank.
		current.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	updated, errPut := s.Upsert(ctx, current, req.APIKey)
	if errPut != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errPut.Error()})
		return
	}
	// Endpoint changes must reach the live client: the gate's client is built
	// once at startup, so without this a saved key or base URL would only apply
	// after a restart. The key is read back from the store rather than reused
	// from req.APIKey so the clear case ("" → empty) takes the same path; the
	// base URL comes from the saved settings so a blank input resolves to the
	// default the store actually holds.
	if req.APIKey != nil || req.BaseURL != nil {
		h.rotateJevConfig(c, s, updated.BaseURL)
	}
	c.JSON(http.StatusOK, gin.H{"settings": updated})
}

// rotateJevConfig hands the stored key and the saved base URL to the live
// classifier client. It is best-effort: a failure to read the key back is
// logged and swallowed because the settings write itself already succeeded, and
// the next restart picks the values up regardless.
func (h *Handler) rotateJevConfig(c *gin.Context, s jevSettingsStore, baseURL string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	rotator := h.jevRotator
	h.mu.Unlock()
	if rotator == nil {
		return
	}
	key := ""
	if ks, ok := s.(interface {
		APIKey(context.Context) (string, error)
	}); ok {
		plain, errKey := ks.APIKey(c.Request.Context())
		if errKey != nil {
			log.WithError(errKey).Warn("management: jev api key rotation skipped; could not read the stored key")
			return
		}
		key = plain
	}
	rotator.SetAPIKey(key)
	rotator.SetBaseURL(baseURL)
}
