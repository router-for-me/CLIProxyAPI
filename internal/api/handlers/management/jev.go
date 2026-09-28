package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
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
// non-empty = rotate and seal.
type jevSettingsRequest struct {
	Enabled *bool   `json:"enabled,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
	Model   *string `json:"model,omitempty"`
}

// PutJevSettings handles PUT /v0/management/jev/settings.
//
// Partial update: omitted fields keep their stored value. A key rotated here is
// sealed at rest and takes effect for new classifications; because the gate's
// client is built once at startup, an already-running server keeps using the
// previously loaded key until it restarts.
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
	updated, errPut := s.Upsert(ctx, current, req.APIKey)
	if errPut != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errPut.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": updated})
}
