package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Generic helpers for list[string] — clone-modify-persist-swap via applyConfigChange.

func (h *Handler) putStringList(c *gin.Context, set func(*config.Config, []string), after func(*config.Config)) {
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
	h.applyConfigChange(c, func(cfg *config.Config) {
		set(cfg, arr)
		if after != nil {
			after(cfg)
		}
	})
}

func (h *Handler) patchStringList(c *gin.Context, accessor func(*config.Config) *[]string, after func(*config.Config)) {
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
	if body.Index != nil && body.Value != nil {
		h.applyConfigChange(c, func(cfg *config.Config) {
			target := accessor(cfg)
			if *body.Index >= 0 && *body.Index < len(*target) {
				(*target)[*body.Index] = *body.Value
				if after != nil {
					after(cfg)
				}
			}
		})
		return
	}
	if body.Old != nil && body.New != nil {
		h.applyConfigChange(c, func(cfg *config.Config) {
			target := accessor(cfg)
			for i := range *target {
				if (*target)[i] == *body.Old {
					(*target)[i] = *body.New
					if after != nil {
						after(cfg)
					}
					return
				}
			}
			*target = append(*target, *body.New)
			if after != nil {
				after(cfg)
			}
		})
		return
	}
	c.JSON(400, gin.H{"error": "missing fields"})
}

func (h *Handler) deleteFromStringList(c *gin.Context, accessor func(*config.Config) *[]string, after func(*config.Config)) {
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				target := accessor(cfg)
				if idx >= 0 && idx < len(*target) {
					*target = append((*target)[:idx], (*target)[idx+1:]...)
					if after != nil {
						after(cfg)
					}
				}
			})
			return
		}
	}
	if val := strings.TrimSpace(c.Query("value")); val != "" {
		h.applyConfigChange(c, func(cfg *config.Config) {
			target := accessor(cfg)
			out := make([]string, 0, len(*target))
			for _, v := range *target {
				if strings.TrimSpace(v) != val {
					out = append(out, v)
				}
			}
			*target = out
			if after != nil {
				after(cfg)
			}
		})
		return
	}
	c.JSON(400, gin.H{"error": "missing index or value"})
}

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) { c.JSON(200, gin.H{"api-keys": h.cfg().APIKeys}) }
func (h *Handler) PutAPIKeys(c *gin.Context) {
	h.putStringList(c, func(cfg *config.Config, v []string) {
		cfg.APIKeys = append([]string(nil), v...)
	}, nil)
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	h.patchStringList(c, func(cfg *config.Config) *[]string { return &cfg.APIKeys }, nil)
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.deleteFromStringList(c, func(cfg *config.Config) *[]string { return &cfg.APIKeys }, nil)
}

// gemini-api-key: []GeminiKey
func (h *Handler) GetGeminiKeys(c *gin.Context) {
	c.JSON(200, gin.H{"gemini-api-key": h.geminiKeysWithAuthIndex()})
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
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.GeminiKey = append([]config.GeminiKey(nil), arr...)
		cfg.SanitizeGeminiKeys()
	})
}
func (h *Handler) PatchGeminiKey(c *gin.Context) {
	type geminiKeyPatch struct {
		APIKey         *string            `json:"api-key"`
		Prefix         *string            `json:"prefix"`
		BaseURL        *string            `json:"base-url"`
		ProxyURL       *string            `json:"proxy-url"`
		Headers        *map[string]string `json:"headers"`
		ExcludedModels *[]string          `json:"excluded-models"`
	}
	var body struct {
		Index *int            `json:"index"`
		Match *string         `json:"match"`
		Value *geminiKeyPatch `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	// Pre-resolve target on the current snapshot so 404 short-circuits before cloning.
	cur := h.cfg()
	if cur == nil {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(cur.GeminiKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		if match != "" {
			for i := range cur.GeminiKey {
				if cur.GeminiKey[i].APIKey == match {
					targetIndex = i
					break
				}
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		// Re-resolve under the writer lock in case the clone differs.
		idx := targetIndex
		if idx >= len(cfg.GeminiKey) {
			return
		}
		entry := cfg.GeminiKey[idx]
		if body.Value.APIKey != nil {
			trimmed := strings.TrimSpace(*body.Value.APIKey)
			if trimmed == "" {
				cfg.GeminiKey = append(cfg.GeminiKey[:idx], cfg.GeminiKey[idx+1:]...)
				cfg.SanitizeGeminiKeys()
				return
			}
			entry.APIKey = trimmed
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
		}
		if body.Value.ProxyURL != nil {
			entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
		}
		if body.Value.Headers != nil {
			entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
		}
		if body.Value.ExcludedModels != nil {
			entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
		}
		cfg.GeminiKey[idx] = entry
		cfg.SanitizeGeminiKeys()
	})
}

func (h *Handler) DeleteGeminiKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			h.applyConfigChange(c, func(cfg *config.Config) {
				out := make([]config.GeminiKey, 0, len(cfg.GeminiKey))
				for _, v := range cfg.GeminiKey {
					if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
						continue
					}
					out = append(out, v)
				}
				if len(out) != len(cfg.GeminiKey) {
					cfg.GeminiKey = out
					cfg.SanitizeGeminiKeys()
				}
			})
			return
		}

		// Resolve match on snapshot so 404/400 short-circuits before cloning.
		cur := h.cfg()
		if cur == nil {
			c.JSON(404, gin.H{"error": "item not found"})
			return
		}
		matchIndex := -1
		matchCount := 0
		for i := range cur.GeminiKey {
			if strings.TrimSpace(cur.GeminiKey[i].APIKey) == val {
				matchCount++
				if matchIndex == -1 {
					matchIndex = i
				}
			}
		}
		if matchCount == 0 {
			c.JSON(404, gin.H{"error": "item not found"})
			return
		}
		if matchCount > 1 {
			c.JSON(400, gin.H{"error": "multiple items match api-key; base-url is required"})
			return
		}
		h.applyConfigChange(c, func(cfg *config.Config) {
			if matchIndex < len(cfg.GeminiKey) {
				cfg.GeminiKey = append(cfg.GeminiKey[:matchIndex], cfg.GeminiKey[matchIndex+1:]...)
				cfg.SanitizeGeminiKeys()
			}
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				if idx >= 0 && idx < len(cfg.GeminiKey) {
					cfg.GeminiKey = append(cfg.GeminiKey[:idx], cfg.GeminiKey[idx+1:]...)
					cfg.SanitizeGeminiKeys()
				}
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// claude-api-key: []ClaudeKey
func (h *Handler) GetClaudeKeys(c *gin.Context) {
	c.JSON(200, gin.H{"claude-api-key": h.claudeKeysWithAuthIndex()})
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
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.ClaudeKey = arr
		cfg.SanitizeClaudeKeys()
	})
}
func (h *Handler) PatchClaudeKey(c *gin.Context) {
	type claudeKeyPatch struct {
		APIKey         *string               `json:"api-key"`
		Prefix         *string               `json:"prefix"`
		BaseURL        *string               `json:"base-url"`
		ProxyURL       *string               `json:"proxy-url"`
		Models         *[]config.ClaudeModel `json:"models"`
		Headers        *map[string]string    `json:"headers"`
		ExcludedModels *[]string             `json:"excluded-models"`
	}
	var body struct {
		Index *int            `json:"index"`
		Match *string         `json:"match"`
		Value *claudeKeyPatch `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	cur := h.cfg()
	if cur == nil {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(cur.ClaudeKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range cur.ClaudeKey {
			if cur.ClaudeKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		idx := targetIndex
		if idx >= len(cfg.ClaudeKey) {
			return
		}
		entry := cfg.ClaudeKey[idx]
		if body.Value.APIKey != nil {
			entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
		}
		if body.Value.ProxyURL != nil {
			entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
		}
		if body.Value.Models != nil {
			entry.Models = append([]config.ClaudeModel(nil), (*body.Value.Models)...)
		}
		if body.Value.Headers != nil {
			entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
		}
		if body.Value.ExcludedModels != nil {
			entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
		}
		normalizeClaudeKey(&entry)
		cfg.ClaudeKey[idx] = entry
		cfg.SanitizeClaudeKeys()
	})
}

func (h *Handler) DeleteClaudeKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			h.applyConfigChange(c, func(cfg *config.Config) {
				out := make([]config.ClaudeKey, 0, len(cfg.ClaudeKey))
				for _, v := range cfg.ClaudeKey {
					if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
						continue
					}
					out = append(out, v)
				}
				cfg.ClaudeKey = out
				cfg.SanitizeClaudeKeys()
			})
			return
		}

		cur := h.cfg()
		if cur == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				cfg.SanitizeClaudeKeys()
			})
			return
		}
		matchIndex := -1
		matchCount := 0
		for i := range cur.ClaudeKey {
			if strings.TrimSpace(cur.ClaudeKey[i].APIKey) == val {
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
		h.applyConfigChange(c, func(cfg *config.Config) {
			if matchIndex != -1 && matchIndex < len(cfg.ClaudeKey) {
				cfg.ClaudeKey = append(cfg.ClaudeKey[:matchIndex], cfg.ClaudeKey[matchIndex+1:]...)
			}
			cfg.SanitizeClaudeKeys()
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				if idx >= 0 && idx < len(cfg.ClaudeKey) {
					cfg.ClaudeKey = append(cfg.ClaudeKey[:idx], cfg.ClaudeKey[idx+1:]...)
					cfg.SanitizeClaudeKeys()
				}
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// openai-compatibility: []OpenAICompatibility
func (h *Handler) GetOpenAICompat(c *gin.Context) {
	c.JSON(200, gin.H{"openai-compatibility": h.openAICompatibilityWithAuthIndex()})
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
	filtered := make([]config.OpenAICompatibility, 0, len(arr))
	for i := range arr {
		normalizeOpenAICompatibilityEntry(&arr[i])
		if strings.TrimSpace(arr[i].BaseURL) != "" {
			filtered = append(filtered, arr[i])
		}
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.OpenAICompatibility = filtered
		cfg.SanitizeOpenAICompatibility()
	})
}
func (h *Handler) PatchOpenAICompat(c *gin.Context) {
	type openAICompatPatch struct {
		Name          *string                             `json:"name"`
		Prefix        *string                             `json:"prefix"`
		Disabled      *bool                               `json:"disabled"`
		BaseURL       *string                             `json:"base-url"`
		APIKeyEntries *[]config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		Models        *[]config.OpenAICompatibilityModel  `json:"models"`
		Headers       *map[string]string                  `json:"headers"`
	}
	var body struct {
		Name  *string            `json:"name"`
		Index *int               `json:"index"`
		Value *openAICompatPatch `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	cur := h.cfg()
	if cur == nil {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(cur.OpenAICompatibility) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Name != nil {
		match := strings.TrimSpace(*body.Name)
		for i := range cur.OpenAICompatibility {
			if cur.OpenAICompatibility[i].Name == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		idx := targetIndex
		if idx >= len(cfg.OpenAICompatibility) {
			return
		}
		entry := cfg.OpenAICompatibility[idx]
		if body.Value.Name != nil {
			entry.Name = strings.TrimSpace(*body.Value.Name)
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.Disabled != nil {
			entry.Disabled = *body.Value.Disabled
		}
		if body.Value.BaseURL != nil {
			trimmed := strings.TrimSpace(*body.Value.BaseURL)
			if trimmed == "" {
				cfg.OpenAICompatibility = append(cfg.OpenAICompatibility[:idx], cfg.OpenAICompatibility[idx+1:]...)
				cfg.SanitizeOpenAICompatibility()
				return
			}
			entry.BaseURL = trimmed
		}
		if body.Value.APIKeyEntries != nil {
			entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), (*body.Value.APIKeyEntries)...)
		}
		if body.Value.Models != nil {
			entry.Models = append([]config.OpenAICompatibilityModel(nil), (*body.Value.Models)...)
		}
		if body.Value.Headers != nil {
			entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
		}
		normalizeOpenAICompatibilityEntry(&entry)
		cfg.OpenAICompatibility[idx] = entry
		cfg.SanitizeOpenAICompatibility()
	})
}

func (h *Handler) DeleteOpenAICompat(c *gin.Context) {
	if name := c.Query("name"); name != "" {
		h.applyConfigChange(c, func(cfg *config.Config) {
			out := make([]config.OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
			for _, v := range cfg.OpenAICompatibility {
				if v.Name != name {
					out = append(out, v)
				}
			}
			cfg.OpenAICompatibility = out
			cfg.SanitizeOpenAICompatibility()
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				if idx >= 0 && idx < len(cfg.OpenAICompatibility) {
					cfg.OpenAICompatibility = append(cfg.OpenAICompatibility[:idx], cfg.OpenAICompatibility[idx+1:]...)
					cfg.SanitizeOpenAICompatibility()
				}
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing name or index"})
}

// vertex-api-key: []VertexCompatKey
func (h *Handler) GetVertexCompatKeys(c *gin.Context) {
	c.JSON(200, gin.H{"vertex-api-key": h.vertexCompatKeysWithAuthIndex()})
}
func (h *Handler) PutVertexCompatKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.VertexCompatKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.VertexCompatKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	for i := range arr {
		normalizeVertexCompatKey(&arr[i])
		if arr[i].APIKey == "" {
			c.JSON(400, gin.H{"error": fmt.Sprintf("vertex-api-key[%d].api-key is required", i)})
			return
		}
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.VertexCompatAPIKey = append([]config.VertexCompatKey(nil), arr...)
		cfg.SanitizeVertexCompatKeys()
	})
}
func (h *Handler) PatchVertexCompatKey(c *gin.Context) {
	type vertexCompatPatch struct {
		APIKey         *string                     `json:"api-key"`
		Prefix         *string                     `json:"prefix"`
		BaseURL        *string                     `json:"base-url"`
		ProxyURL       *string                     `json:"proxy-url"`
		Headers        *map[string]string          `json:"headers"`
		Models         *[]config.VertexCompatModel `json:"models"`
		ExcludedModels *[]string                   `json:"excluded-models"`
	}
	var body struct {
		Index *int               `json:"index"`
		Match *string            `json:"match"`
		Value *vertexCompatPatch `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	cur := h.cfg()
	if cur == nil {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(cur.VertexCompatAPIKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		if match != "" {
			for i := range cur.VertexCompatAPIKey {
				if cur.VertexCompatAPIKey[i].APIKey == match {
					targetIndex = i
					break
				}
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		idx := targetIndex
		if idx >= len(cfg.VertexCompatAPIKey) {
			return
		}
		entry := cfg.VertexCompatAPIKey[idx]
		if body.Value.APIKey != nil {
			trimmed := strings.TrimSpace(*body.Value.APIKey)
			if trimmed == "" {
				cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:idx], cfg.VertexCompatAPIKey[idx+1:]...)
				cfg.SanitizeVertexCompatKeys()
				return
			}
			entry.APIKey = trimmed
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			trimmed := strings.TrimSpace(*body.Value.BaseURL)
			if trimmed == "" {
				cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:idx], cfg.VertexCompatAPIKey[idx+1:]...)
				cfg.SanitizeVertexCompatKeys()
				return
			}
			entry.BaseURL = trimmed
		}
		if body.Value.ProxyURL != nil {
			entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
		}
		if body.Value.Headers != nil {
			entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
		}
		if body.Value.Models != nil {
			entry.Models = append([]config.VertexCompatModel(nil), (*body.Value.Models)...)
		}
		if body.Value.ExcludedModels != nil {
			entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
		}
		normalizeVertexCompatKey(&entry)
		cfg.VertexCompatAPIKey[idx] = entry
		cfg.SanitizeVertexCompatKeys()
	})
}

func (h *Handler) DeleteVertexCompatKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			h.applyConfigChange(c, func(cfg *config.Config) {
				out := make([]config.VertexCompatKey, 0, len(cfg.VertexCompatAPIKey))
				for _, v := range cfg.VertexCompatAPIKey {
					if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
						continue
					}
					out = append(out, v)
				}
				cfg.VertexCompatAPIKey = out
				cfg.SanitizeVertexCompatKeys()
			})
			return
		}

		cur := h.cfg()
		if cur == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				cfg.SanitizeVertexCompatKeys()
			})
			return
		}
		matchIndex := -1
		matchCount := 0
		for i := range cur.VertexCompatAPIKey {
			if strings.TrimSpace(cur.VertexCompatAPIKey[i].APIKey) == val {
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
		h.applyConfigChange(c, func(cfg *config.Config) {
			if matchIndex != -1 && matchIndex < len(cfg.VertexCompatAPIKey) {
				cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:matchIndex], cfg.VertexCompatAPIKey[matchIndex+1:]...)
			}
			cfg.SanitizeVertexCompatKeys()
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, errScan := fmt.Sscanf(idxStr, "%d", &idx); errScan == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				if idx >= 0 && idx < len(cfg.VertexCompatAPIKey) {
					cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:idx], cfg.VertexCompatAPIKey[idx+1:]...)
					cfg.SanitizeVertexCompatKeys()
				}
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// oauth-excluded-models: map[string][]string
func (h *Handler) GetOAuthExcludedModels(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-excluded-models": config.NormalizeOAuthExcludedModels(h.cfg().OAuthExcludedModels)})
}

func (h *Handler) PutOAuthExcludedModels(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string][]string
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(entries)
	})
}

func (h *Handler) PatchOAuthExcludedModels(c *gin.Context) {
	var body struct {
		Provider *string  `json:"provider"`
		Models   []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Provider == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(*body.Provider))
	if provider == "" {
		c.JSON(400, gin.H{"error": "invalid provider"})
		return
	}
	normalized := config.NormalizeExcludedModels(body.Models)
	if len(normalized) == 0 {
		// Removal path: 404 short-circuits before clone.
		cur := h.cfg()
		if cur == nil || cur.OAuthExcludedModels == nil {
			c.JSON(404, gin.H{"error": "provider not found"})
			return
		}
		if _, ok := cur.OAuthExcludedModels[provider]; !ok {
			c.JSON(404, gin.H{"error": "provider not found"})
			return
		}
		h.applyConfigChange(c, func(cfg *config.Config) {
			delete(cfg.OAuthExcludedModels, provider)
			if len(cfg.OAuthExcludedModels) == 0 {
				cfg.OAuthExcludedModels = nil
			}
		})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		if cfg.OAuthExcludedModels == nil {
			cfg.OAuthExcludedModels = make(map[string][]string)
		}
		cfg.OAuthExcludedModels[provider] = normalized
	})
}

func (h *Handler) DeleteOAuthExcludedModels(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		c.JSON(400, gin.H{"error": "missing provider"})
		return
	}
	cur := h.cfg()
	if cur == nil || cur.OAuthExcludedModels == nil {
		c.JSON(404, gin.H{"error": "provider not found"})
		return
	}
	if _, ok := cur.OAuthExcludedModels[provider]; !ok {
		c.JSON(404, gin.H{"error": "provider not found"})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		delete(cfg.OAuthExcludedModels, provider)
		if len(cfg.OAuthExcludedModels) == 0 {
			cfg.OAuthExcludedModels = nil
		}
	})
}

// oauth-model-alias: map[string][]OAuthModelAlias
func (h *Handler) GetOAuthModelAlias(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-model-alias": sanitizedOAuthModelAlias(h.cfg().OAuthModelAlias)})
}

func (h *Handler) PutOAuthModelAlias(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string][]config.OAuthModelAlias
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]config.OAuthModelAlias `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.OAuthModelAlias = sanitizedOAuthModelAlias(entries)
	})
}

func (h *Handler) PatchOAuthModelAlias(c *gin.Context) {
	var body struct {
		Provider *string                  `json:"provider"`
		Channel  *string                  `json:"channel"`
		Aliases  []config.OAuthModelAlias `json:"aliases"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	channelRaw := ""
	if body.Channel != nil {
		channelRaw = *body.Channel
	} else if body.Provider != nil {
		channelRaw = *body.Provider
	}
	channel := strings.ToLower(strings.TrimSpace(channelRaw))
	if channel == "" {
		c.JSON(400, gin.H{"error": "invalid channel"})
		return
	}

	normalizedMap := sanitizedOAuthModelAlias(map[string][]config.OAuthModelAlias{channel: body.Aliases})
	normalized := normalizedMap[channel]
	if len(normalized) == 0 {
		cur := h.cfg()
		if cur == nil || cur.OAuthModelAlias == nil {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		if _, ok := cur.OAuthModelAlias[channel]; !ok {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		h.applyConfigChange(c, func(cfg *config.Config) {
			delete(cfg.OAuthModelAlias, channel)
			if len(cfg.OAuthModelAlias) == 0 {
				cfg.OAuthModelAlias = nil
			}
		})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		if cfg.OAuthModelAlias == nil {
			cfg.OAuthModelAlias = make(map[string][]config.OAuthModelAlias)
		}
		cfg.OAuthModelAlias[channel] = normalized
	})
}

func (h *Handler) DeleteOAuthModelAlias(c *gin.Context) {
	channel := strings.ToLower(strings.TrimSpace(c.Query("channel")))
	if channel == "" {
		channel = strings.ToLower(strings.TrimSpace(c.Query("provider")))
	}
	if channel == "" {
		c.JSON(400, gin.H{"error": "missing channel"})
		return
	}
	cur := h.cfg()
	if cur == nil || cur.OAuthModelAlias == nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	if _, ok := cur.OAuthModelAlias[channel]; !ok {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		delete(cfg.OAuthModelAlias, channel)
		if len(cfg.OAuthModelAlias) == 0 {
			cfg.OAuthModelAlias = nil
		}
	})
}

// codex-api-key: []CodexKey
func (h *Handler) GetCodexKeys(c *gin.Context) {
	c.JSON(200, gin.H{"codex-api-key": h.codexKeysWithAuthIndex()})
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
		normalizeCodexKey(&entry)
		if entry.BaseURL == "" {
			continue
		}
		filtered = append(filtered, entry)
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.CodexKey = filtered
		cfg.SanitizeCodexKeys()
	})
}
func (h *Handler) PatchCodexKey(c *gin.Context) {
	type codexKeyPatch struct {
		APIKey         *string              `json:"api-key"`
		Prefix         *string              `json:"prefix"`
		BaseURL        *string              `json:"base-url"`
		ProxyURL       *string              `json:"proxy-url"`
		Models         *[]config.CodexModel `json:"models"`
		Headers        *map[string]string   `json:"headers"`
		ExcludedModels *[]string            `json:"excluded-models"`
	}
	var body struct {
		Index *int           `json:"index"`
		Match *string        `json:"match"`
		Value *codexKeyPatch `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	cur := h.cfg()
	if cur == nil {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(cur.CodexKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range cur.CodexKey {
			if cur.CodexKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		idx := targetIndex
		if idx >= len(cfg.CodexKey) {
			return
		}
		entry := cfg.CodexKey[idx]
		if body.Value.APIKey != nil {
			entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			trimmed := strings.TrimSpace(*body.Value.BaseURL)
			if trimmed == "" {
				cfg.CodexKey = append(cfg.CodexKey[:idx], cfg.CodexKey[idx+1:]...)
				cfg.SanitizeCodexKeys()
				return
			}
			entry.BaseURL = trimmed
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
		normalizeCodexKey(&entry)
		cfg.CodexKey[idx] = entry
		cfg.SanitizeCodexKeys()
	})
}

func (h *Handler) DeleteCodexKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			h.applyConfigChange(c, func(cfg *config.Config) {
				out := make([]config.CodexKey, 0, len(cfg.CodexKey))
				for _, v := range cfg.CodexKey {
					if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
						continue
					}
					out = append(out, v)
				}
				cfg.CodexKey = out
				cfg.SanitizeCodexKeys()
			})
			return
		}

		cur := h.cfg()
		if cur == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				cfg.SanitizeCodexKeys()
			})
			return
		}
		matchIndex := -1
		matchCount := 0
		for i := range cur.CodexKey {
			if strings.TrimSpace(cur.CodexKey[i].APIKey) == val {
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
		h.applyConfigChange(c, func(cfg *config.Config) {
			if matchIndex != -1 && matchIndex < len(cfg.CodexKey) {
				cfg.CodexKey = append(cfg.CodexKey[:matchIndex], cfg.CodexKey[matchIndex+1:]...)
			}
			cfg.SanitizeCodexKeys()
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil {
			h.applyConfigChange(c, func(cfg *config.Config) {
				if idx >= 0 && idx < len(cfg.CodexKey) {
					cfg.CodexKey = append(cfg.CodexKey[:idx], cfg.CodexKey[idx+1:]...)
					cfg.SanitizeCodexKeys()
				}
			})
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
	entry.ExcludedModels = config.NormalizeExcludedModels(entry.ExcludedModels)
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

func normalizeCodexKey(entry *config.CodexKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.ExcludedModels = config.NormalizeExcludedModels(entry.ExcludedModels)
	if len(entry.Models) == 0 {
		return
	}
	normalized := make([]config.CodexModel, 0, len(entry.Models))
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

func normalizeVertexCompatKey(entry *config.VertexCompatKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.ExcludedModels = config.NormalizeExcludedModels(entry.ExcludedModels)
	if len(entry.Models) == 0 {
		return
	}
	normalized := make([]config.VertexCompatModel, 0, len(entry.Models))
	for i := range entry.Models {
		model := entry.Models[i]
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if model.Name == "" || model.Alias == "" {
			continue
		}
		normalized = append(normalized, model)
	}
	entry.Models = normalized
}

func sanitizedOAuthModelAlias(entries map[string][]config.OAuthModelAlias) map[string][]config.OAuthModelAlias {
	if len(entries) == 0 {
		return nil
	}
	copied := make(map[string][]config.OAuthModelAlias, len(entries))
	for channel, aliases := range entries {
		if len(aliases) == 0 {
			continue
		}
		copied[channel] = append([]config.OAuthModelAlias(nil), aliases...)
	}
	if len(copied) == 0 {
		return nil
	}
	cfg := config.Config{OAuthModelAlias: copied}
	cfg.SanitizeOAuthModelAlias()
	if len(cfg.OAuthModelAlias) == 0 {
		return nil
	}
	return cfg.OAuthModelAlias
}

// GetAmpCode returns the complete ampcode configuration.
func (h *Handler) GetAmpCode(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"ampcode": config.AmpCode{}})
		return
	}
	c.JSON(200, gin.H{"ampcode": h.cfg().AmpCode})
}

// GetAmpUpstreamURL returns the ampcode upstream URL.
func (h *Handler) GetAmpUpstreamURL(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"upstream-url": ""})
		return
	}
	c.JSON(200, gin.H{"upstream-url": h.cfg().AmpCode.UpstreamURL})
}

// PutAmpUpstreamURL updates the ampcode upstream URL.
func (h *Handler) PutAmpUpstreamURL(c *gin.Context) {
	h.updateStringField(c, func(cfg *config.Config, v string) { cfg.AmpCode.UpstreamURL = strings.TrimSpace(v) })
}

// DeleteAmpUpstreamURL clears the ampcode upstream URL.
func (h *Handler) DeleteAmpUpstreamURL(c *gin.Context) {
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.AmpCode.UpstreamURL = ""
	})
}

// GetAmpUpstreamAPIKey returns the ampcode upstream API key.
func (h *Handler) GetAmpUpstreamAPIKey(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"upstream-api-key": ""})
		return
	}
	c.JSON(200, gin.H{"upstream-api-key": h.cfg().AmpCode.UpstreamAPIKey})
}

// PutAmpUpstreamAPIKey updates the ampcode upstream API key.
func (h *Handler) PutAmpUpstreamAPIKey(c *gin.Context) {
	h.updateStringField(c, func(cfg *config.Config, v string) { cfg.AmpCode.UpstreamAPIKey = strings.TrimSpace(v) })
}

// DeleteAmpUpstreamAPIKey clears the ampcode upstream API key.
func (h *Handler) DeleteAmpUpstreamAPIKey(c *gin.Context) {
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.AmpCode.UpstreamAPIKey = ""
	})
}

// GetAmpRestrictManagementToLocalhost returns the localhost restriction setting.
func (h *Handler) GetAmpRestrictManagementToLocalhost(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"restrict-management-to-localhost": true})
		return
	}
	c.JSON(200, gin.H{"restrict-management-to-localhost": h.cfg().AmpCode.RestrictManagementToLocalhost})
}

// PutAmpRestrictManagementToLocalhost updates the localhost restriction setting.
func (h *Handler) PutAmpRestrictManagementToLocalhost(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.AmpCode.RestrictManagementToLocalhost = v })
}

// GetAmpModelMappings returns the ampcode model mappings.
func (h *Handler) GetAmpModelMappings(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"model-mappings": []config.AmpModelMapping{}})
		return
	}
	c.JSON(200, gin.H{"model-mappings": h.cfg().AmpCode.ModelMappings})
}

// PutAmpModelMappings replaces all ampcode model mappings.
func (h *Handler) PutAmpModelMappings(c *gin.Context) {
	var body struct {
		Value []config.AmpModelMapping `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.AmpCode.ModelMappings = body.Value
	})
}

// PatchAmpModelMappings adds or updates model mappings.
func (h *Handler) PatchAmpModelMappings(c *gin.Context) {
	var body struct {
		Value []config.AmpModelMapping `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		existing := make(map[string]int)
		for i, m := range cfg.AmpCode.ModelMappings {
			existing[strings.TrimSpace(m.From)] = i
		}

		for _, newMapping := range body.Value {
			from := strings.TrimSpace(newMapping.From)
			if idx, ok := existing[from]; ok {
				cfg.AmpCode.ModelMappings[idx] = newMapping
			} else {
				cfg.AmpCode.ModelMappings = append(cfg.AmpCode.ModelMappings, newMapping)
				existing[from] = len(cfg.AmpCode.ModelMappings) - 1
			}
		}
	})
}

// DeleteAmpModelMappings removes specified model mappings by "from" field.
func (h *Handler) DeleteAmpModelMappings(c *gin.Context) {
	var body struct {
		Value []string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Value) == 0 {
		h.applyConfigChange(c, func(cfg *config.Config) {
			cfg.AmpCode.ModelMappings = nil
		})
		return
	}

	toRemove := make(map[string]bool)
	for _, from := range body.Value {
		toRemove[strings.TrimSpace(from)] = true
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		newMappings := make([]config.AmpModelMapping, 0, len(cfg.AmpCode.ModelMappings))
		for _, m := range cfg.AmpCode.ModelMappings {
			if !toRemove[strings.TrimSpace(m.From)] {
				newMappings = append(newMappings, m)
			}
		}
		cfg.AmpCode.ModelMappings = newMappings
	})
}

// GetAmpForceModelMappings returns whether model mappings are forced.
func (h *Handler) GetAmpForceModelMappings(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"force-model-mappings": false})
		return
	}
	c.JSON(200, gin.H{"force-model-mappings": h.cfg().AmpCode.ForceModelMappings})
}

// PutAmpForceModelMappings updates the force model mappings setting.
func (h *Handler) PutAmpForceModelMappings(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.AmpCode.ForceModelMappings = v })
}

// GetAmpUpstreamAPIKeys returns the ampcode upstream API keys mapping.
func (h *Handler) GetAmpUpstreamAPIKeys(c *gin.Context) {
	if h == nil || h.cfg() == nil {
		c.JSON(200, gin.H{"upstream-api-keys": []config.AmpUpstreamAPIKeyEntry{}})
		return
	}
	c.JSON(200, gin.H{"upstream-api-keys": h.cfg().AmpCode.UpstreamAPIKeys})
}

// PutAmpUpstreamAPIKeys replaces all ampcode upstream API keys mappings.
func (h *Handler) PutAmpUpstreamAPIKeys(c *gin.Context) {
	var body struct {
		Value []config.AmpUpstreamAPIKeyEntry `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	// Normalize entries: trim whitespace, filter empty
	normalized := normalizeAmpUpstreamAPIKeyEntries(body.Value)
	h.applyConfigChange(c, func(cfg *config.Config) {
		cfg.AmpCode.UpstreamAPIKeys = normalized
	})
}

// PatchAmpUpstreamAPIKeys adds or updates upstream API keys entries.
// Matching is done by upstream-api-key value.
func (h *Handler) PatchAmpUpstreamAPIKeys(c *gin.Context) {
	var body struct {
		Value []config.AmpUpstreamAPIKeyEntry `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		existing := make(map[string]int)
		for i, entry := range cfg.AmpCode.UpstreamAPIKeys {
			existing[strings.TrimSpace(entry.UpstreamAPIKey)] = i
		}

		for _, newEntry := range body.Value {
			upstreamKey := strings.TrimSpace(newEntry.UpstreamAPIKey)
			if upstreamKey == "" {
				continue
			}
			normalizedEntry := config.AmpUpstreamAPIKeyEntry{
				UpstreamAPIKey: upstreamKey,
				APIKeys:        normalizeAPIKeysList(newEntry.APIKeys),
			}
			if idx, ok := existing[upstreamKey]; ok {
				cfg.AmpCode.UpstreamAPIKeys[idx] = normalizedEntry
			} else {
				cfg.AmpCode.UpstreamAPIKeys = append(cfg.AmpCode.UpstreamAPIKeys, normalizedEntry)
				existing[upstreamKey] = len(cfg.AmpCode.UpstreamAPIKeys) - 1
			}
		}
	})
}

// DeleteAmpUpstreamAPIKeys removes specified upstream API keys entries.
// Body must be JSON: {"value": ["<upstream-api-key>", ...]}.
// If "value" is an empty array, clears all entries.
// If JSON is invalid or "value" is missing/null, returns 400 and does not persist any change.
func (h *Handler) DeleteAmpUpstreamAPIKeys(c *gin.Context) {
	var body struct {
		Value []string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	if body.Value == nil {
		c.JSON(400, gin.H{"error": "missing value"})
		return
	}

	// Empty array means clear all
	if len(body.Value) == 0 {
		h.applyConfigChange(c, func(cfg *config.Config) {
			cfg.AmpCode.UpstreamAPIKeys = nil
		})
		return
	}

	toRemove := make(map[string]bool)
	for _, key := range body.Value {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		toRemove[trimmed] = true
	}
	if len(toRemove) == 0 {
		c.JSON(400, gin.H{"error": "empty value"})
		return
	}

	h.applyConfigChange(c, func(cfg *config.Config) {
		newEntries := make([]config.AmpUpstreamAPIKeyEntry, 0, len(cfg.AmpCode.UpstreamAPIKeys))
		for _, entry := range cfg.AmpCode.UpstreamAPIKeys {
			if !toRemove[strings.TrimSpace(entry.UpstreamAPIKey)] {
				newEntries = append(newEntries, entry)
			}
		}
		cfg.AmpCode.UpstreamAPIKeys = newEntries
	})
}

// normalizeAmpUpstreamAPIKeyEntries normalizes a list of upstream API key entries.
func normalizeAmpUpstreamAPIKeyEntries(entries []config.AmpUpstreamAPIKeyEntry) []config.AmpUpstreamAPIKeyEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]config.AmpUpstreamAPIKeyEntry, 0, len(entries))
	for _, entry := range entries {
		upstreamKey := strings.TrimSpace(entry.UpstreamAPIKey)
		if upstreamKey == "" {
			continue
		}
		apiKeys := normalizeAPIKeysList(entry.APIKeys)
		out = append(out, config.AmpUpstreamAPIKeyEntry{
			UpstreamAPIKey: upstreamKey,
			APIKeys:        apiKeys,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeAPIKeysList trims and filters empty strings from a list of API keys.
func normalizeAPIKeysList(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
