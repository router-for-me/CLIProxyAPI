package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotatraffic"
)

func (h *Handler) GetQuotaObservations(c *gin.Context) {
	allowed := map[string]bool{}
	providers := map[string]string{}
	if h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a != nil && !a.Disabled {
				allowed[a.Index] = true
				providers[a.Index] = a.Provider
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	items := make([]quotatraffic.Observation, 0)
	for _, item := range quotatraffic.Read(allowed) {
		// An auth index may survive a provider change. Do not report the old
		// provider's observations as quota for the replacement credential.
		if providers[item.AuthIndex] == item.Provider {
			items = append(items, item)
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
