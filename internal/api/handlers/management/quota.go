package management

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Quota exceeded toggles
func (h *Handler) GetSwitchProject(c *gin.Context) {
	c.JSON(200, gin.H{"switch-project": h.cfg().QuotaExceeded.SwitchProject})
}
func (h *Handler) PutSwitchProject(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.QuotaExceeded.SwitchProject = v })
}

func (h *Handler) GetSwitchPreviewModel(c *gin.Context) {
	c.JSON(200, gin.H{"switch-preview-model": h.cfg().QuotaExceeded.SwitchPreviewModel})
}
func (h *Handler) PutSwitchPreviewModel(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.QuotaExceeded.SwitchPreviewModel = v })
}
