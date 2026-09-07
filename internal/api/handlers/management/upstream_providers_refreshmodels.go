package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// refreshModelsTimeout bounds the upstream /models fetch. Registered in
// AGENTS.md as an intentional timeout exception (management probe path).
const refreshModelsTimeout = 8 * time.Second

// refreshUpstreamProviderModelsRequest is the optional JSON body for POST
// .../refresh-models. A nil/zero EntryID picks the first active entry with
// a key, falling back to the row-level api_key.
type refreshUpstreamProviderModelsRequest struct {
	EntryID *int64 `json:"entry_id,omitempty"`
}

// upstreamModelsListResponse models the OpenAI-compatible GET /models body
// ({"object":"list","data":[{"id":...}]}). Only the id field is consumed.
type upstreamModelsListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// pickRefreshModelsKey resolves the bearer key for a models refresh: the
// requested entry, the first active keyed entry, then the row-level key.
func pickRefreshModelsKey(p *store.UpstreamProvider, entryID *int64) (string, string) {
	if entryID != nil && *entryID > 0 {
		for _, e := range p.APIKeyEntries {
			if e.ID == *entryID {
				return e.APIKey, "entry"
			}
		}
		return "", ""
	}
	for _, e := range p.APIKeyEntries {
		if !e.Disabled && e.APIKey != "" {
			return e.APIKey, "entry"
		}
	}
	if p.APIKey != "" {
		return p.APIKey, "row"
	}
	return "", ""
}

// RefreshUpstreamProviderModels handles POST
// /v0/management/upstream-providers/:id/refresh-models. Live-fetches the
// upstream /models list with the chosen credential and merges any new model
// ids into the row's catalog (merge-by-name: existing entries, including
// their wire formats, are never modified or removed). Upstream failures
// leave the catalog untouched.
func (h *Handler) RefreshUpstreamProviderModels(c *gin.Context) {
	id, errID := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if errID != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return
	}
	var body refreshUpstreamProviderModelsRequest
	_ = c.ShouldBindJSON(&body) // body is optional

	src, ok := h.upstreamProvidersStore(c)
	if !ok {
		return // 503 already written
	}
	row, errGet := src.Get(c.Request.Context(), id)
	if errGet != nil || row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	key, keySource := pickRefreshModelsKey(row, body.EntryID)
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no API key available on this provider row or its entries"})
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(row.BaseURL), "/")
	if baseURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider has no base_url configured"})
		return
	}

	fetchCtx, cancel := context.WithTimeout(c.Request.Context(), refreshModelsTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(fetchCtx, http.MethodGet, baseURL+"/models", nil)
	if errReq != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("build upstream request: %v", errReq)})
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	// Never log the key: only the URL and status are surfaced on failure.
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("upstream models fetch failed: %v", errDo)})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			// best-effort close; nothing actionable on a probe response
			_ = errClose
		}
	}()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("read upstream response: %v", errRead)})
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("upstream models fetch returned HTTP %d (%s source)", resp.StatusCode, keySource)})
		return
	}
	var parsed upstreamModelsListResponse
	if errParse := json.Unmarshal(raw, &parsed); errParse != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream returned a non-JSON models list"})
		return
	}
	existing := make(map[string]struct{}, len(row.Models))
	for _, m := range row.Models {
		existing[m.Name] = struct{}{}
	}
	added := 0
	for _, item := range parsed.Data {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		if _, dup := existing[name]; dup {
			continue
		}
		row.Models = append(row.Models, store.UpstreamProviderModel{Name: name})
		existing[name] = struct{}{}
		added++
	}
	if added > 0 {
		if _, errUpdate := src.Update(c.Request.Context(), *row); errUpdate != nil {
			h.upstreamProviderErrorResponse(c, errUpdate)
			return
		}
		h.syncCatalogFromUpstreamProviders(c.Request.Context())
		h.applyUpstreamProviders(c.Request.Context())
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "total": len(row.Models)})
}
