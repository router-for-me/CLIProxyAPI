package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Generic helpers for list[string]
func (h *Handler) putStringList(c *gin.Context, set func([]string), after func()) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []string
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	set(arr)
	if after != nil {
		after()
	}
	h.persist(c)
}

func (h *Handler) patchStringList(c *gin.Context, target *[]string, after func()) {
	var body struct {
		Old   *string `json:"old"`
		New   *string `json:"new"`
		Index *int    `json:"index"`
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	if body.Index != nil && body.Value != nil && *body.Index >= 0 && *body.Index < len(*target) {
		(*target)[*body.Index] = *body.Value
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	if body.Old != nil && body.New != nil {
		for i := range *target {
			if (*target)[i] == *body.Old {
				(*target)[i] = *body.New
				if after != nil {
					after()
				}
				h.persist(c)
				return
			}
		}
		*target = append(*target, *body.New)
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	c.JSON(400, gin.H{"error": "missing fields"})
}

func (h *Handler) deleteFromStringList(c *gin.Context, target *[]string, after func()) {
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(*target) {
			*target = append((*target)[:idx], (*target)[idx+1:]...)
			if after != nil {
				after()
			}
			h.persist(c)
			return
		}
	}
	if val := strings.TrimSpace(c.Query("value")); val != "" {
		out := make([]string, 0, len(*target))
		for _, v := range *target {
			if strings.TrimSpace(v) != val {
				out = append(out, v)
			}
		}
		*target = out
		if after != nil {
			after()
		}
		h.persist(c)
		return
	}
	c.JSON(400, gin.H{"error": "missing index or value"})
}

func sanitizeStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for i := range in {
		if trimmed := strings.TrimSpace(in[i]); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func geminiKeyStringsFromConfig(cfg *config.Config) []string {
	if cfg == nil || len(cfg.GeminiKey) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg.GeminiKey))
	for i := range cfg.GeminiKey {
		if key := strings.TrimSpace(cfg.GeminiKey[i].APIKey); key != "" {
			out = append(out, key)
		}
	}
	return out
}

func (h *Handler) applyLegacyKeys(keys []string) {
	if h == nil || h.cfg == nil {
		return
	}
	sanitized := sanitizeStringSlice(keys)
	existing := make(map[string]config.GeminiKey, len(h.cfg.GeminiKey))
	for _, entry := range h.cfg.GeminiKey {
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			existing[key] = entry
		}
	}
	newList := make([]config.GeminiKey, 0, len(sanitized))
	for _, key := range sanitized {
		if entry, ok := existing[key]; ok {
			newList = append(newList, entry)
		} else {
			newList = append(newList, config.GeminiKey{APIKey: key})
		}
	}
	h.cfg.GeminiKey = newList
	h.cfg.GlAPIKey = sanitized
	h.cfg.SanitizeGeminiKeys()
}

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) { c.JSON(200, gin.H{"api-keys": h.cfg.APIKeys}) }
func (h *Handler) PutAPIKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		h.cfg.APIKeys = append([]string(nil), v...)
		h.cfg.Access.Providers = nil
	}, nil)
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	h.patchStringList(c, &h.cfg.APIKeys, func() { h.cfg.Access.Providers = nil })
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.deleteFromStringList(c, &h.cfg.APIKeys, func() { h.cfg.Access.Providers = nil })
}

// generative-language-api-key
func (h *Handler) GetGlKeys(c *gin.Context) {
	c.JSON(200, gin.H{"generative-language-api-key": geminiKeyStringsFromConfig(h.cfg)})
}
func (h *Handler) PutGlKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		h.applyLegacyKeys(v)
	}, nil)
}
func (h *Handler) PatchGlKeys(c *gin.Context) {
	target := append([]string(nil), geminiKeyStringsFromConfig(h.cfg)...)
	h.patchStringList(c, &target, func() { h.applyLegacyKeys(target) })
}
func (h *Handler) DeleteGlKeys(c *gin.Context) {
	target := append([]string(nil), geminiKeyStringsFromConfig(h.cfg)...)
	h.deleteFromStringList(c, &target, func() { h.applyLegacyKeys(target) })
}

// gemini-api-key: []GeminiKey
func (h *Handler) GetGeminiKeys(c *gin.Context) {
	c.JSON(200, gin.H{"gemini-api-key": h.cfg.GeminiKey})
}
func (h *Handler) PutGeminiKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.GeminiKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.GeminiKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.cfg.GeminiKey = append([]config.GeminiKey(nil), arr...)
	h.cfg.SanitizeGeminiKeys()
	h.persist(c)
}
func (h *Handler) PatchGeminiKey(c *gin.Context) {
	var body struct {
		Index *int              `json:"index"`
		Match *string           `json:"match"`
		Value *config.GeminiKey `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	value := *body.Value
	value.APIKey = strings.TrimSpace(value.APIKey)
	value.BaseURL = strings.TrimSpace(value.BaseURL)
	value.ProxyURL = strings.TrimSpace(value.ProxyURL)
	if value.APIKey == "" {
		// Treat empty API key as delete.
		if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.GeminiKey) {
			h.cfg.GeminiKey = append(h.cfg.GeminiKey[:*body.Index], h.cfg.GeminiKey[*body.Index+1:]...)
			h.cfg.SanitizeGeminiKeys()
			h.persist(c)
			return
		}
		if body.Match != nil {
			match := strings.TrimSpace(*body.Match)
			if match != "" {
				out := make([]config.GeminiKey, 0, len(h.cfg.GeminiKey))
				removed := false
				for i := range h.cfg.GeminiKey {
					if !removed && h.cfg.GeminiKey[i].APIKey == match {
						removed = true
						continue
					}
					out = append(out, h.cfg.GeminiKey[i])
				}
				if removed {
					h.cfg.GeminiKey = out
					h.cfg.SanitizeGeminiKeys()
					h.persist(c)
					return
				}
			}
		}
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.GeminiKey) {
		h.cfg.GeminiKey[*body.Index] = value
		h.cfg.SanitizeGeminiKeys()
		h.persist(c)
		return
	}
	if body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.GeminiKey {
			if h.cfg.GeminiKey[i].APIKey == match {
				h.cfg.GeminiKey[i] = value
				h.cfg.SanitizeGeminiKeys()
				h.persist(c)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteGeminiKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		out := make([]config.GeminiKey, 0, len(h.cfg.GeminiKey))
		for _, v := range h.cfg.GeminiKey {
			if v.APIKey != val {
				out = append(out, v)
			}
		}
		if len(out) != len(h.cfg.GeminiKey) {
			h.cfg.GeminiKey = out
			h.cfg.SanitizeGeminiKeys()
			h.persist(c)
		} else {
			c.JSON(404, gin.H{"error": "item not found"})
		}
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.GeminiKey) {
			h.cfg.GeminiKey = append(h.cfg.GeminiKey[:idx], h.cfg.GeminiKey[idx+1:]...)
			h.cfg.SanitizeGeminiKeys()
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// claude-api-key: []ClaudeKey
func (h *Handler) GetClaudeKeys(c *gin.Context) {
	c.JSON(200, gin.H{"claude-api-key": h.cfg.ClaudeKey})
}
func (h *Handler) PutClaudeKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.ClaudeKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.ClaudeKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	for i := range arr {
		normalizeClaudeKey(&arr[i])
	}
	h.cfg.ClaudeKey = arr
	h.cfg.SanitizeClaudeKeys()
	h.persist(c)
}
func (h *Handler) PatchClaudeKey(c *gin.Context) {
	var body struct {
		Index *int              `json:"index"`
		Match *string           `json:"match"`
		Value *config.ClaudeKey `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	value := *body.Value
	normalizeClaudeKey(&value)
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.ClaudeKey) {
		h.cfg.ClaudeKey[*body.Index] = value
		h.cfg.SanitizeClaudeKeys()
		h.persist(c)
		return
	}
	if body.Match != nil {
		for i := range h.cfg.ClaudeKey {
			if h.cfg.ClaudeKey[i].APIKey == *body.Match {
				h.cfg.ClaudeKey[i] = value
				h.cfg.SanitizeClaudeKeys()
				h.persist(c)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteClaudeKey(c *gin.Context) {
	if val := c.Query("api-key"); val != "" {
		out := make([]config.ClaudeKey, 0, len(h.cfg.ClaudeKey))
		for _, v := range h.cfg.ClaudeKey {
			if v.APIKey != val {
				out = append(out, v)
			}
		}
		h.cfg.ClaudeKey = out
		h.cfg.SanitizeClaudeKeys()
		h.persist(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.ClaudeKey) {
			h.cfg.ClaudeKey = append(h.cfg.ClaudeKey[:idx], h.cfg.ClaudeKey[idx+1:]...)
			h.cfg.SanitizeClaudeKeys()
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// openai-compatibility: []OpenAICompatibility
func (h *Handler) GetOpenAICompat(c *gin.Context) {
	c.JSON(200, gin.H{"openai-compatibility": normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)})
}
func (h *Handler) PutOpenAICompat(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.OpenAICompatibility
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.OpenAICompatibility `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	arr = migrateLegacyOpenAICompatibilityKeys(arr)
	// Filter out providers with empty base-url -> remove provider entirely
	filtered := make([]config.OpenAICompatibility, 0, len(arr))
	for i := range arr {
		if strings.TrimSpace(arr[i].BaseURL) != "" {
			filtered = append(filtered, arr[i])
		}
	}
	h.cfg.OpenAICompatibility = migrateLegacyOpenAICompatibilityKeys(filtered)
	h.cfg.SanitizeOpenAICompatibility()
	h.persist(c)
}
func (h *Handler) PatchOpenAICompat(c *gin.Context) {
	var body struct {
		Name  *string                     `json:"name"`
		Index *int                        `json:"index"`
		Value *config.OpenAICompatibility `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.cfg.OpenAICompatibility = migrateLegacyOpenAICompatibilityKeys(h.cfg.OpenAICompatibility)
	normalizeOpenAICompatibilityEntry(body.Value)
	// If base-url becomes empty, delete the provider instead of updating
	if strings.TrimSpace(body.Value.BaseURL) == "" {
		if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpenAICompatibility) {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:*body.Index], h.cfg.OpenAICompatibility[*body.Index+1:]...)
			h.cfg.SanitizeOpenAICompatibility()
			h.persist(c)
			return
		}
		if body.Name != nil {
			out := make([]config.OpenAICompatibility, 0, len(h.cfg.OpenAICompatibility))
			removed := false
			for i := range h.cfg.OpenAICompatibility {
				if !removed && h.cfg.OpenAICompatibility[i].Name == *body.Name {
					removed = true
					continue
				}
				out = append(out, h.cfg.OpenAICompatibility[i])
			}
			if removed {
				h.cfg.OpenAICompatibility = out
				h.cfg.SanitizeOpenAICompatibility()
				h.persist(c)
				return
			}
		}
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpenAICompatibility) {
		h.cfg.OpenAICompatibility[*body.Index] = *body.Value
		h.cfg.SanitizeOpenAICompatibility()
		h.persist(c)
		return
	}
	if body.Name != nil {
		for i := range h.cfg.OpenAICompatibility {
			if h.cfg.OpenAICompatibility[i].Name == *body.Name {
				h.cfg.OpenAICompatibility[i] = *body.Value
				h.cfg.SanitizeOpenAICompatibility()
				h.persist(c)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteOpenAICompat(c *gin.Context) {
	if name := c.Query("name"); name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.OpenAICompatibility))
		for _, v := range h.cfg.OpenAICompatibility {
			if v.Name != name {
				out = append(out, v)
			}
		}
		h.cfg.OpenAICompatibility = out
		h.cfg.SanitizeOpenAICompatibility()
		h.persist(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.OpenAICompatibility) {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:idx], h.cfg.OpenAICompatibility[idx+1:]...)
			h.cfg.SanitizeOpenAICompatibility()
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing name or index"})
}

// codex-api-key: []CodexKey
func (h *Handler) GetCodexKeys(c *gin.Context) {
	c.JSON(200, gin.H{"codex-api-key": h.cfg.CodexKey})
}
func (h *Handler) PutCodexKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.CodexKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.CodexKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	// Filter out codex entries with empty base-url (treat as removed)
	filtered := make([]config.CodexKey, 0, len(arr))
	for i := range arr {
		entry := arr[i]
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = config.NormalizeHeaders(entry.Headers)
		if entry.BaseURL == "" {
			continue
		}
		filtered = append(filtered, entry)
	}
	h.cfg.CodexKey = filtered
	h.cfg.SanitizeCodexKeys()
	h.persist(c)
}
func (h *Handler) PatchCodexKey(c *gin.Context) {
	var body struct {
		Index *int             `json:"index"`
		Match *string          `json:"match"`
		Value *config.CodexKey `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	value := *body.Value
	value.APIKey = strings.TrimSpace(value.APIKey)
	value.BaseURL = strings.TrimSpace(value.BaseURL)
	value.ProxyURL = strings.TrimSpace(value.ProxyURL)
	value.Headers = config.NormalizeHeaders(value.Headers)
	// If base-url becomes empty, delete instead of update
	if value.BaseURL == "" {
		if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.CodexKey) {
			h.cfg.CodexKey = append(h.cfg.CodexKey[:*body.Index], h.cfg.CodexKey[*body.Index+1:]...)
			h.cfg.SanitizeCodexKeys()
			h.persist(c)
			return
		}
		if body.Match != nil {
			out := make([]config.CodexKey, 0, len(h.cfg.CodexKey))
			removed := false
			for i := range h.cfg.CodexKey {
				if !removed && h.cfg.CodexKey[i].APIKey == *body.Match {
					removed = true
					continue
				}
				out = append(out, h.cfg.CodexKey[i])
			}
			if removed {
				h.cfg.CodexKey = out
				h.cfg.SanitizeCodexKeys()
				h.persist(c)
				return
			}
		}
	} else {
		if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.CodexKey) {
			h.cfg.CodexKey[*body.Index] = value
			h.cfg.SanitizeCodexKeys()
			h.persist(c)
			return
		}
		if body.Match != nil {
			for i := range h.cfg.CodexKey {
				if h.cfg.CodexKey[i].APIKey == *body.Match {
					h.cfg.CodexKey[i] = value
					h.cfg.SanitizeCodexKeys()
					h.persist(c)
					return
				}
			}
		}
	}
	c.JSON(404, gin.H{"error": "item not found"})
}
func (h *Handler) DeleteCodexKey(c *gin.Context) {
	if val := c.Query("api-key"); val != "" {
		out := make([]config.CodexKey, 0, len(h.cfg.CodexKey))
		for _, v := range h.cfg.CodexKey {
			if v.APIKey != val {
				out = append(out, v)
			}
		}
		h.cfg.CodexKey = out
		h.cfg.SanitizeCodexKeys()
		h.persist(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.CodexKey) {
			h.cfg.CodexKey = append(h.cfg.CodexKey[:idx], h.cfg.CodexKey[idx+1:]...)
			h.cfg.SanitizeCodexKeys()
			h.persist(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

func normalizeOpenAICompatibilityEntry(entry *config.OpenAICompatibility) {
	if entry == nil {
		return
	}
	// Trim base-url; empty base-url indicates provider should be removed by sanitization
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	existing := make(map[string]struct{}, len(entry.APIKeyEntries))
	for i := range entry.APIKeyEntries {
		trimmed := strings.TrimSpace(entry.APIKeyEntries[i].APIKey)
		entry.APIKeyEntries[i].APIKey = trimmed
		if trimmed != "" {
			existing[trimmed] = struct{}{}
		}
	}
	if len(entry.APIKeys) == 0 {
		return
	}
	for _, legacyKey := range entry.APIKeys {
		trimmed := strings.TrimSpace(legacyKey)
		if trimmed == "" {
			continue
		}
		if _, ok := existing[trimmed]; ok {
			continue
		}
		entry.APIKeyEntries = append(entry.APIKeyEntries, config.OpenAICompatibilityAPIKey{APIKey: trimmed})
		existing[trimmed] = struct{}{}
	}
	entry.APIKeys = nil
}

func migrateLegacyOpenAICompatibilityKeys(entries []config.OpenAICompatibility) []config.OpenAICompatibility {
	for i := range entries {
		normalizeOpenAICompatibilityEntry(&entries[i])
	}
	return entries
}

func normalizedOpenAICompatibilityEntries(entries []config.OpenAICompatibility) []config.OpenAICompatibility {
	if len(entries) == 0 {
		return nil
	}
	out := make([]config.OpenAICompatibility, len(entries))
	for i := range entries {
		copyEntry := entries[i]
		if len(copyEntry.APIKeyEntries) > 0 {
			copyEntry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), copyEntry.APIKeyEntries...)
		}
		if len(copyEntry.APIKeys) > 0 {
			copyEntry.APIKeys = append([]string(nil), copyEntry.APIKeys...)
		}
		normalizeOpenAICompatibilityEntry(&copyEntry)
		out[i] = copyEntry
	}
	return out
}

func normalizeClaudeKey(entry *config.ClaudeKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	if len(entry.Models) == 0 {
		return
	}
	normalized := make([]config.ClaudeModel, 0, len(entry.Models))
	for i := range entry.Models {
		model := entry.Models[i]
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if model.Name == "" && model.Alias == "" {
			continue
		}
		normalized = append(normalized, model)
	}
	entry.Models = normalized
}
