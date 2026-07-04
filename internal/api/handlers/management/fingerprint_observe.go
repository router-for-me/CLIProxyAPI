package management

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/fpobserve"
)

// GetFingerprintObserve returns the current sampled outbound-fingerprint snapshot per
// account (from the executor's in-memory fpobserve store) plus whether the feature is on.
// It powers the fingerprint-observatory plugin page and any external viewer. The snapshot
// carries no PII — accounts are fnv tags, identifiers are shape/presence only.
func (h *Handler) GetFingerprintObserve(c *gin.Context) {
	enabled := h.cfg != nil && h.cfg.FingerprintObserve.Enabled
	c.JSON(http.StatusOK, gin.H{
		"enabled":  enabled,
		"accounts": fpobserve.Snapshot(),
	})
}
