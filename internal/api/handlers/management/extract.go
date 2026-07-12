package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/extractor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// PostExtractAuth scans known local credential sources and writes auth JSON
// files into the configured auth directory. It returns the list of providers
// for which credentials were discovered.
func (h *Handler) PostExtractAuth(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "handler not configured"})
		return
	}
	authDir, err := util.ResolveAuthDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	found, err := extractor.ExtractAll(authDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": found})
}
