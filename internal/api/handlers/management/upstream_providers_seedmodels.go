package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// openCodeGoSeedModels is the initial opencode-go model catalog, ported from
// OmniRoute's opencode-go registry. WireFormat "" means openai (the
// executor's default); "anthropic" routes that model through the Claude
// wire (/v1/messages).
var openCodeGoSeedModels = []store.UpstreamProviderModel{
	{Name: "glm-5.2"},
	{Name: "glm-5.1"},
	{Name: "glm-5"},
	{Name: "kimi-k2.7-code"},
	{Name: "kimi-k2.6"},
	{Name: "kimi-k2.5"},
	{Name: "kimi-k3"},
	{Name: "kimi-k3-max"},
	{Name: "mimo-v2.5-pro"},
	{Name: "mimo-v2.5"},
	{Name: "minimax-m2.7"},
	{Name: "minimax-m2.5"},
	{Name: "minimax-m3", WireFormat: "anthropic"},
	{Name: "qwen3.7-max", WireFormat: "anthropic"},
	{Name: "qwen3.7-plus", WireFormat: "anthropic"},
	{Name: "qwen3.6-plus-high", WireFormat: "anthropic"},
	{Name: "qwen3.6-plus-max", WireFormat: "anthropic"},
	{Name: "hy3"},
	{Name: "hy3-preview"},
	{Name: "muse-spark-1.2-contributor"},
	{Name: "grok-4.5"},
	{Name: "deepseek-v4-pro"},
	{Name: "deepseek-v4-flash"},
	{Name: "ox-alpha-free"},
}

// SeedUpstreamProviderModels handles POST
// /v0/management/upstream-providers/:id/seed-models. Idempotent: seed
// models already present (by exact name) are never duplicated or modified;
// only missing names are appended. Applies to any provider row so a
// catalog refresh works for pre-existing opencode-go rows too.
func (h *Handler) SeedUpstreamProviderModels(c *gin.Context) {
	id, errID := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if errID != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return
	}
	src, ok := h.upstreamProvidersStore(c)
	if !ok {
		return // upstreamProvidersStore already wrote the 503
	}
	row, errGet := src.Get(c.Request.Context(), id)
	if errGet != nil || row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	existing := make(map[string]struct{}, len(row.Models))
	for _, m := range row.Models {
		existing[m.Name] = struct{}{}
	}
	added := 0
	for _, seed := range openCodeGoSeedModels {
		if _, dup := existing[seed.Name]; dup {
			continue
		}
		row.Models = append(row.Models, seed)
		existing[seed.Name] = struct{}{}
		added++
	}
	if added == 0 {
		c.JSON(http.StatusOK, gin.H{"added": 0, "total": len(row.Models)})
		return
	}
	updated, errUpdate := src.Update(c.Request.Context(), *row)
	if errUpdate != nil {
		h.upstreamProviderErrorResponse(c, errUpdate)
		return
	}
	// Keep the models catalog and live routing artifacts in sync, same as
	// the create/update handlers.
	h.syncCatalogFromUpstreamProviders(c.Request.Context())
	h.applyUpstreamProviders(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"added": added, "total": len(updated.Models)})
}
