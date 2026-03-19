package management

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Quota exceeded toggles
func (h *Handler) GetSwitchProject(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(200, gin.H{"switch-project": false})
		return
	}
	c.JSON(200, gin.H{"switch-project": cfg.QuotaExceeded.SwitchProject})
}
func (h *Handler) PutSwitchProject(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.QuotaExceeded.SwitchProject = v })
}

func (h *Handler) GetSwitchPreviewModel(c *gin.Context) {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		c.JSON(200, gin.H{"switch-preview-model": false})
		return
	}
	c.JSON(200, gin.H{"switch-preview-model": cfg.QuotaExceeded.SwitchPreviewModel})
}
func (h *Handler) PutSwitchPreviewModel(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.QuotaExceeded.SwitchPreviewModel = v })
}
