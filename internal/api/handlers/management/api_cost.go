package management

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apicost"
)

const defaultAPICostLookback = 30 * 24 * time.Hour

// apiCostStore and apiCostPricing are the data sources; tests may replace them.
var (
	apiCostStore   = apicost.Default
	apiCostPricing = apicost.CurrentPricingStatus
)

// GetAPICost returns hourly estimated API list-price costs per model and
// credential, computed with ccusage's formula from LiteLLM prices. It is
// read-only and does not touch the usage queue.
func (h *Handler) GetAPICost(c *gin.Context) {
	now := time.Now()
	since := now.Add(-defaultAPICostLookback)
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		parsed, errParse := time.Parse(time.RFC3339, raw)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "since must be an RFC3339 timestamp"})
			return
		}
		since = parsed
	}
	buckets := apiCostStore().Buckets(since)

	authIDs := make(map[string]struct{})
	unpriced := make(map[string]int64)
	for _, bucket := range buckets {
		if bucket.AuthID != "" {
			authIDs[bucket.AuthID] = struct{}{}
		}
		if bucket.UnpricedRequests > 0 {
			unpriced[bucket.Model] += bucket.UnpricedRequests
		}
	}
	accounts := make([]gin.H, 0, len(authIDs))
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil {
				continue
			}
			if _, ok := authIDs[auth.ID]; !ok {
				continue
			}
			item := gin.H{
				"id":       auth.ID,
				"name":     authDisplayName(auth),
				"provider": strings.ToLower(strings.TrimSpace(auth.Provider)),
				"disabled": auth.Disabled,
			}
			if email := authEmail(auth); email != "" {
				item["email"] = email
			}
			accounts = append(accounts, item)
		}
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i]["id"].(string) < accounts[j]["id"].(string) })
	unpricedModels := make([]gin.H, 0, len(unpriced))
	for model, requests := range unpriced {
		unpricedModels = append(unpricedModels, gin.H{"model": model, "requests": requests})
	}
	sort.Slice(unpricedModels, func(i, j int) bool {
		return unpricedModels[i]["model"].(string) < unpricedModels[j]["model"].(string)
	})

	c.JSON(http.StatusOK, gin.H{
		"generated_at":    now.UTC(),
		"since":           since.UTC(),
		"retention_days":  int64(apicost.DefaultRetention / (24 * time.Hour)),
		"pricing":         apiCostPricing(),
		"buckets":         buckets,
		"accounts":        accounts,
		"unpriced_models": unpricedModels,
	})
}
