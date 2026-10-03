package management

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
)

// GetOpenAICompatQuota serves the per-API-key quota snapshot written by
// bin/sync_providers.js (quota.json, sits next to config.yaml). It covers
// provider keys polled out-of-band (OpenRouter per-key credits/free-tier
// counters), which are not OAuth auth files and therefore never show up in
// the auth-file quota flow. Raw passthrough: the file is already the JSON
// contract the Management Center quota page renders.
func (h *Handler) GetOpenAICompatQuota(c *gin.Context) {
	path := filepath.Join(filepath.Dir(h.configFilePath), "quota.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"providers": []any{}, "note": "quota.json not written yet"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed", "message": err.Error()})
		return
	}
	var payload gin.H
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "parse_failed", "message": err.Error()})
		return
	}
	if payload == nil { // quota.json containing literal `null` unmarshals to a nil map
		payload = gin.H{}
	}
	payload["served_at"] = time.Now().UTC().Format(time.RFC3339)
	c.JSON(http.StatusOK, payload)
}
