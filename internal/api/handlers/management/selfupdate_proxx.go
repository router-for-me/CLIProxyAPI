package management

// proxx: self-update endpoints (ADR 0007). The apply endpoint takes no URL or version: it only ever
// applies the latest release that verified against the embedded signing key.

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate"
)

// GetProxxUpdate returns the self-update status.
func (h *Handler) GetProxxUpdate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, selfupdate.GetStatus())
}

// PostProxxUpdateCheck checks for a new release and returns the status.
func (h *Handler) PostProxxUpdateCheck(c *gin.Context) {
	c.JSON(http.StatusOK, selfupdate.CheckNow(c.Request.Context()))
}

// PostProxxUpdateApply starts downloading, verifying and applying the latest release (202), or
// returns 409 when updates are unsupported here or there is nothing to apply.
func (h *Handler) PostProxxUpdateApply(c *gin.Context) {
	st, err := selfupdate.Apply(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, st)
}

// PostProxxUpdateCancel cancels a pending apply that has not started the restart yet.
func (h *Handler) PostProxxUpdateCancel(c *gin.Context) {
	st, err := selfupdate.Cancel()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, st)
}
