package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexreset"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

// CodexResetPolicy manages opt-in consent without exposing tokens or the KV journal.
func (h *Handler) CodexResetPolicy(c *gin.Context) {
	if cfg := h.configSnapshot(); cfg == nil || cfg.Home.Enabled {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "automatic resets require standalone mode"})
		return
	}
	key := codexreset.AccountKey(h.authByIndex(c.Query("auth_index")))
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a Codex credential with matching immutable user and account identities is required"})
		return
	}
	store, enabled, errStore := kvstore.CurrentKVClient()
	if errStore != nil || !enabled || store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "persistent KV storage is required for automatic resets"})
		return
	}
	state, raw, exists, errRead := codexreset.Read(c.Request.Context(), store, key)
	if errRead != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "reset journal unavailable"})
		return
	}
	if c.Request.Method == http.MethodPut {
		var body struct {
			AccountKey          string `json:"account_key"`
			Enabled             *bool  `json:"enabled"`
			BeforeExpirySeconds int    `json:"before_expiry_seconds"`
		}
		if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Enabled == nil || body.BeforeExpirySeconds < 60 || body.BeforeExpirySeconds > 86400 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "enabled and before_expiry_seconds (60..86400) are required"})
			return
		}
		if body.AccountKey != key {
			c.JSON(http.StatusConflict, gin.H{"error": "credential identity changed; reload before changing consent"})
			return
		}
		state.Enabled, state.BeforeExpirySeconds = *body.Enabled, body.BeforeExpirySeconds
		if state.Enabled && state.Result == "disabled" {
			state.Result = "waiting"
		}
		if saved, errSave := codexreset.Save(c.Request.Context(), store, state, raw, exists); errSave != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "reset policy was not saved"})
			return
		} else if !saved {
			c.JSON(http.StatusConflict, gin.H{"error": "reset policy changed; reload before retrying"})
			return
		}
	}
	c.JSON(http.StatusOK, state)
}
