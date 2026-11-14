package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// opencode-api-key: []OpenCodeKey
func (h *Handler) GetOpenCodeKeys(c *gin.Context) {
	c.JSON(200, gin.H{"opencode-api-key": h.openCodeKeysWithAuthIndex()})
}

func (h *Handler) PutOpenCodeKeys(c *gin.Context) {
	h.putCodexStyleKeys(c, "opencode-api-key", &h.cfg.OpenCodeKey, h.cfg.SanitizeOpenCodeKeys)
}

func (h *Handler) PatchOpenCodeKey(c *gin.Context) {
	h.patchCodexStyleKey(c, "opencode-api-key", &h.cfg.OpenCodeKey, h.cfg.SanitizeOpenCodeKeys)
}

func (h *Handler) DeleteOpenCodeKey(c *gin.Context) {
	h.deleteCodexStyleKey(c, "opencode-api-key", &h.cfg.OpenCodeKey, h.cfg.SanitizeOpenCodeKeys)
}

// opencode-go-api-key: []OpenCodeGoKey
func (h *Handler) GetOpenCodeGoKeys(c *gin.Context) {
	c.JSON(200, gin.H{"opencode-go-api-key": h.openCodeGoKeysWithAuthIndex()})
}

func (h *Handler) PutOpenCodeGoKeys(c *gin.Context) {
	h.putCodexStyleKeys(c, "opencode-go-api-key", &h.cfg.OpenCodeGoKey, h.cfg.SanitizeOpenCodeGoKeys)
}

func (h *Handler) PatchOpenCodeGoKey(c *gin.Context) {
	h.patchCodexStyleKey(c, "opencode-go-api-key", &h.cfg.OpenCodeGoKey, h.cfg.SanitizeOpenCodeGoKeys)
}

func (h *Handler) DeleteOpenCodeGoKey(c *gin.Context) {
	h.deleteCodexStyleKey(c, "opencode-go-api-key", &h.cfg.OpenCodeGoKey, h.cfg.SanitizeOpenCodeGoKeys)
}

func (h *Handler) putCodexStyleKeys(c *gin.Context, name string, dst *[]config.CodexKey, sanitize func()) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.CodexKey
	if errUnmarshal := json.Unmarshal(data, &arr); errUnmarshal != nil {
		var obj struct {
			Items []config.CodexKey `json:"items"`
		}
		if errObject := json.Unmarshal(data, &obj); errObject != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	filtered := make([]config.CodexKey, 0, len(arr))
	for i := range arr {
		entry := arr[i]
		normalizeCodexKey(&entry)
		if entry.BaseURL == "" {
			continue
		}
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("%s[%d].weight", name, i), entry.Weight) {
			return
		}
		filtered = append(filtered, entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	*dst = filtered
	sanitize()
	h.persistLocked(c)
}

func (h *Handler) patchCodexStyleKey(c *gin.Context, name string, dst *[]config.CodexKey, sanitize func()) {
	type codexStyleKeyPatch struct {
		APIKey              *string                          `json:"api-key"`
		Priority            *int                             `json:"priority"`
		Weight              json.RawMessage                  `json:"weight"`
		Prefix              *string                          `json:"prefix"`
		BaseURL             *string                          `json:"base-url"`
		Websockets          *bool                            `json:"websockets"`
		ProxyURL            *string                          `json:"proxy-url"`
		Models              *[]config.CodexModel             `json:"models"`
		Headers             *map[string]string               `json:"headers"`
		ExcludedModels      *[]string                        `json:"excluded-models"`
		DisableCooling      json.RawMessage                  `json:"disable-cooling"`
		RequestRetry        *int                             `json:"request-retry"`
		RequestScopedErrors *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
	}
	var body struct {
		Index *int                `json:"index"`
		Match *string             `json:"match"`
		Value *codexStyleKeyPatch `json:"value"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(*dst) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range *dst {
			if (*dst)[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := (*dst)[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
	}
	if body.Value.Priority != nil {
		entry.Priority = *body.Value.Priority
	}
	if len(body.Value.Weight) > 0 {
		weight, errWeight := parseCredentialWeightPatch(body.Value.Weight)
		if errWeight != nil {
			c.JSON(400, gin.H{"error": errWeight.Error()})
			return
		}
		entry.Weight = weight
	}
	if body.Value.Prefix != nil {
		entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
	}
	if body.Value.BaseURL != nil {
		trimmed := strings.TrimSpace(*body.Value.BaseURL)
		if trimmed == "" {
			*dst = append((*dst)[:targetIndex], (*dst)[targetIndex+1:]...)
			sanitize()
			h.persistLocked(c)
			return
		}
		entry.BaseURL = trimmed
	}
	if body.Value.Websockets != nil {
		entry.Websockets = *body.Value.Websockets
	}
	if body.Value.ProxyURL != nil {
		entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.CodexModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.ExcludedModels != nil {
		entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
	}
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), (*body.Value.RequestScopedErrors)...)
	}
	normalizeCodexKey(&entry)
	(*dst)[targetIndex] = entry
	sanitize()
	h.persistLocked(c)
}

func (h *Handler) deleteCodexStyleKey(c *gin.Context, name string, dst *[]config.CodexKey, sanitize func()) {
	_ = name
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.CodexKey, 0, len(*dst))
			for _, entry := range *dst {
				if strings.TrimSpace(entry.APIKey) == val && strings.TrimSpace(entry.BaseURL) == base {
					continue
				}
				out = append(out, entry)
			}
			*dst = out
			sanitize()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range *dst {
			if strings.TrimSpace((*dst)[i].APIKey) == val {
				matchCount++
				if matchIndex == -1 {
					matchIndex = i
				}
			}
		}
		if matchCount > 1 {
			c.JSON(400, gin.H{"error": "multiple items match api-key; base-url is required"})
			return
		}
		if matchIndex != -1 {
			*dst = append((*dst)[:matchIndex], (*dst)[matchIndex+1:]...)
		}
		sanitize()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, errScan := fmt.Sscanf(idxStr, "%d", &idx)
		if errScan == nil && idx >= 0 && idx < len(*dst) {
			*dst = append((*dst)[:idx], (*dst)[idx+1:]...)
			sanitize()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}
