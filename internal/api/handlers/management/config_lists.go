package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func (h *Handler) configSnapshotOrEmpty() *config.Config {
	cfg, err := h.configSnapshot()
	if err != nil || cfg == nil {
		return &config.Config{}
	}
	return cfg
}

func decodeJSONItems[T any](c *gin.Context) ([]T, bool) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return nil, false
	}

	var items []T
	if err = json.Unmarshal(data, &items); err == nil {
		return items, true
	}

	var wrapped struct {
		Items []T `json:"items"`
	}
	if err = json.Unmarshal(data, &wrapped); err != nil || len(wrapped.Items) == 0 {
		c.JSON(400, gin.H{"error": "invalid body"})
		return nil, false
	}
	return wrapped.Items, true
}

func findIndexByIndexOrMatch[T any](items []T, index *int, match *string, matches func(T, string) bool) int {
	if index != nil && *index >= 0 && *index < len(items) {
		return *index
	}
	if match == nil {
		return -1
	}

	needle := strings.TrimSpace(*match)
	if needle == "" {
		return -1
	}
	for i := range items {
		if matches(items[i], needle) {
			return i
		}
	}
	return -1
}

func (h *Handler) ampCodeSnapshot() config.AmpCode {
	return h.configSnapshotOrEmpty().AmpCode
}

func (h *Handler) mutateAmpCode(c *gin.Context, mutate func(*config.AmpCode) error) {
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		return mutate(&cfg.AmpCode)
	})
}

// Generic helpers for list[string]
func (h *Handler) putStringList(c *gin.Context, set func(*config.Config, []string), after func(*config.Config)) {
	arr, ok := decodeJSONItems[string](c)
	if !ok {
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		set(cfg, arr)
		if after != nil {
			after(cfg)
		}
		return nil
	})
}

func (h *Handler) patchStringList(c *gin.Context, target func(*config.Config) *[]string, after func(*config.Config)) {
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		items := target(cfg)
		if body.Index != nil && body.Value != nil && *body.Index >= 0 && *body.Index < len(*items) {
			(*items)[*body.Index] = *body.Value
			if after != nil {
				after(cfg)
			}
			return nil
		}
		if body.Old != nil && body.New != nil {
			for i := range *items {
				if (*items)[i] == *body.Old {
					(*items)[i] = *body.New
					if after != nil {
						after(cfg)
					}
					return nil
				}
			}
			*items = append(*items, *body.New)
			if after != nil {
				after(cfg)
			}
			return nil
		}
		return fmt.Errorf("missing fields")
	})
}

func (h *Handler) deleteFromStringList(c *gin.Context, target func(*config.Config) *[]string, after func(*config.Config)) {
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil {
			h.applyConfigMutation(c, func(cfg *config.Config) error {
				items := target(cfg)
				if idx < 0 || idx >= len(*items) {
					return fmt.Errorf("missing index or value")
				}
				*items = append((*items)[:idx], (*items)[idx+1:]...)
				if after != nil {
					after(cfg)
				}
				return nil
			})
			return
		}
	}
	if val := strings.TrimSpace(c.Query("value")); val != "" {
		h.applyConfigMutation(c, func(cfg *config.Config) error {
			items := target(cfg)
			out := make([]string, 0, len(*items))
			for _, v := range *items {
				if strings.TrimSpace(v) != val {
					out = append(out, v)
				}
			}
			*items = out
			if after != nil {
				after(cfg)
			}
			return nil
		})
		return
	}
	c.JSON(400, gin.H{"error": "missing index or value"})
}

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"api-keys": cfg.APIKeys})
}
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
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"gemini-api-key": cfg.GeminiKey})
}
func (h *Handler) PutGeminiKeys(c *gin.Context) {
	arr, ok := decodeJSONItems[config.GeminiKey](c)
	if !ok {
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.GeminiKey = append([]config.GeminiKey(nil), arr...)
		cfg.SanitizeGeminiKeys()
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		targetIndex := findIndexByIndexOrMatch(cfg.GeminiKey, body.Index, body.Match, func(item config.GeminiKey, match string) bool {
			return item.APIKey == match
		})
		if targetIndex == -1 {
			return fmt.Errorf("item not found")
		}
		entry := cfg.GeminiKey[targetIndex]
		if body.Value.APIKey != nil {
			trimmed := strings.TrimSpace(*body.Value.APIKey)
			if trimmed == "" {
				cfg.GeminiKey = append(cfg.GeminiKey[:targetIndex], cfg.GeminiKey[targetIndex+1:]...)
				cfg.SanitizeGeminiKeys()
				return nil
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
		cfg.GeminiKey[targetIndex] = entry
		cfg.SanitizeGeminiKeys()
		return nil
	})
}

func (h *Handler) DeleteGeminiKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		h.applyConfigMutation(c, func(cfg *config.Config) error {
			out := make([]config.GeminiKey, 0, len(cfg.GeminiKey))
			for _, v := range cfg.GeminiKey {
				if v.APIKey != val {
					out = append(out, v)
				}
			}
			if len(out) == len(cfg.GeminiKey) {
				return fmt.Errorf("item not found")
			}
			cfg.GeminiKey = out
			cfg.SanitizeGeminiKeys()
			return nil
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil {
			h.applyConfigMutation(c, func(cfg *config.Config) error {
				if idx < 0 || idx >= len(cfg.GeminiKey) {
					return fmt.Errorf("missing api-key or index")
				}
				cfg.GeminiKey = append(cfg.GeminiKey[:idx], cfg.GeminiKey[idx+1:]...)
				cfg.SanitizeGeminiKeys()
				return nil
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// claude-api-key: []ClaudeKey
func (h *Handler) GetClaudeKeys(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"claude-api-key": cfg.ClaudeKey})
}
func (h *Handler) PutClaudeKeys(c *gin.Context) {
	arr, ok := decodeJSONItems[config.ClaudeKey](c)
	if !ok {
		return
	}
	for i := range arr {
		normalizeClaudeKey(&arr[i])
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.ClaudeKey = arr
		cfg.SanitizeClaudeKeys()
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		targetIndex := findIndexByIndexOrMatch(cfg.ClaudeKey, body.Index, body.Match, func(item config.ClaudeKey, match string) bool {
			return item.APIKey == match
		})
		if targetIndex == -1 {
			return fmt.Errorf("item not found")
		}
		entry := cfg.ClaudeKey[targetIndex]
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
		cfg.ClaudeKey[targetIndex] = entry
		cfg.SanitizeClaudeKeys()
		return nil
	})
}

func (h *Handler) DeleteClaudeKey(c *gin.Context) {
	if val := c.Query("api-key"); val != "" {
		h.applyConfigMutation(c, func(cfg *config.Config) error {
			out := make([]config.ClaudeKey, 0, len(cfg.ClaudeKey))
			for _, v := range cfg.ClaudeKey {
				if v.APIKey != val {
					out = append(out, v)
				}
			}
			cfg.ClaudeKey = out
			cfg.SanitizeClaudeKeys()
			return nil
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil {
			h.applyConfigMutation(c, func(cfg *config.Config) error {
				if idx < 0 || idx >= len(cfg.ClaudeKey) {
					return fmt.Errorf("missing api-key or index")
				}
				cfg.ClaudeKey = append(cfg.ClaudeKey[:idx], cfg.ClaudeKey[idx+1:]...)
				cfg.SanitizeClaudeKeys()
				return nil
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// openai-compatibility: []OpenAICompatibility
func (h *Handler) GetOpenAICompat(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"openai-compatibility": normalizedOpenAICompatibilityEntries(cfg.OpenAICompatibility)})
}
func (h *Handler) PutOpenAICompat(c *gin.Context) {
	arr, ok := decodeJSONItems[config.OpenAICompatibility](c)
	if !ok {
		return
	}
	filtered := make([]config.OpenAICompatibility, 0, len(arr))
	for i := range arr {
		normalizeOpenAICompatibilityEntry(&arr[i])
		if strings.TrimSpace(arr[i].BaseURL) != "" {
			filtered = append(filtered, arr[i])
		}
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.OpenAICompatibility = filtered
		cfg.SanitizeOpenAICompatibility()
		return nil
	})
}
func (h *Handler) PatchOpenAICompat(c *gin.Context) {
	type openAICompatPatch struct {
		Name          *string                             `json:"name"`
		Prefix        *string                             `json:"prefix"`
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		targetIndex := findIndexByIndexOrMatch(cfg.OpenAICompatibility, body.Index, body.Name, func(item config.OpenAICompatibility, match string) bool {
			return item.Name == match
		})
		if targetIndex == -1 {
			return fmt.Errorf("item not found")
		}
		entry := cfg.OpenAICompatibility[targetIndex]
		if body.Value.Name != nil {
			entry.Name = strings.TrimSpace(*body.Value.Name)
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			trimmed := strings.TrimSpace(*body.Value.BaseURL)
			if trimmed == "" {
				cfg.OpenAICompatibility = append(cfg.OpenAICompatibility[:targetIndex], cfg.OpenAICompatibility[targetIndex+1:]...)
				cfg.SanitizeOpenAICompatibility()
				return nil
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
		cfg.OpenAICompatibility[targetIndex] = entry
		cfg.SanitizeOpenAICompatibility()
		return nil
	})
}

func (h *Handler) DeleteOpenAICompat(c *gin.Context) {
	if name := c.Query("name"); name != "" {
		h.applyConfigMutation(c, func(cfg *config.Config) error {
			out := make([]config.OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
			for _, v := range cfg.OpenAICompatibility {
				if v.Name != name {
					out = append(out, v)
				}
			}
			cfg.OpenAICompatibility = out
			cfg.SanitizeOpenAICompatibility()
			return nil
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil {
			h.applyConfigMutation(c, func(cfg *config.Config) error {
				if idx < 0 || idx >= len(cfg.OpenAICompatibility) {
					return fmt.Errorf("missing name or index")
				}
				cfg.OpenAICompatibility = append(cfg.OpenAICompatibility[:idx], cfg.OpenAICompatibility[idx+1:]...)
				cfg.SanitizeOpenAICompatibility()
				return nil
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing name or index"})
}

// vertex-api-key: []VertexCompatKey
func (h *Handler) GetVertexCompatKeys(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"vertex-api-key": cfg.VertexCompatAPIKey})
}
func (h *Handler) PutVertexCompatKeys(c *gin.Context) {
	arr, ok := decodeJSONItems[config.VertexCompatKey](c)
	if !ok {
		return
	}
	for i := range arr {
		normalizeVertexCompatKey(&arr[i])
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.VertexCompatAPIKey = arr
		cfg.SanitizeVertexCompatKeys()
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		targetIndex := findIndexByIndexOrMatch(cfg.VertexCompatAPIKey, body.Index, body.Match, func(item config.VertexCompatKey, match string) bool {
			return item.APIKey == match
		})
		if targetIndex == -1 {
			return fmt.Errorf("item not found")
		}
		entry := cfg.VertexCompatAPIKey[targetIndex]
		if body.Value.APIKey != nil {
			trimmed := strings.TrimSpace(*body.Value.APIKey)
			if trimmed == "" {
				cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:targetIndex], cfg.VertexCompatAPIKey[targetIndex+1:]...)
				cfg.SanitizeVertexCompatKeys()
				return nil
			}
			entry.APIKey = trimmed
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			trimmed := strings.TrimSpace(*body.Value.BaseURL)
			if trimmed == "" {
				cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:targetIndex], cfg.VertexCompatAPIKey[targetIndex+1:]...)
				cfg.SanitizeVertexCompatKeys()
				return nil
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
		cfg.VertexCompatAPIKey[targetIndex] = entry
		cfg.SanitizeVertexCompatKeys()
		return nil
	})
}

func (h *Handler) DeleteVertexCompatKey(c *gin.Context) {
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		h.applyConfigMutation(c, func(cfg *config.Config) error {
			out := make([]config.VertexCompatKey, 0, len(cfg.VertexCompatAPIKey))
			for _, v := range cfg.VertexCompatAPIKey {
				if v.APIKey != val {
					out = append(out, v)
				}
			}
			cfg.VertexCompatAPIKey = out
			cfg.SanitizeVertexCompatKeys()
			return nil
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, errScan := fmt.Sscanf(idxStr, "%d", &idx)
		if errScan == nil {
			h.applyConfigMutation(c, func(cfg *config.Config) error {
				if idx < 0 || idx >= len(cfg.VertexCompatAPIKey) {
					return fmt.Errorf("missing api-key or index")
				}
				cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey[:idx], cfg.VertexCompatAPIKey[idx+1:]...)
				cfg.SanitizeVertexCompatKeys()
				return nil
			})
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// oauth-excluded-models: map[string][]string
func (h *Handler) GetOAuthExcludedModels(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"oauth-excluded-models": config.NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)})
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(entries)
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		if len(normalized) == 0 {
			if cfg.OAuthExcludedModels == nil {
				return fmt.Errorf("provider not found")
			}
			if _, ok := cfg.OAuthExcludedModels[provider]; !ok {
				return fmt.Errorf("provider not found")
			}
			delete(cfg.OAuthExcludedModels, provider)
			if len(cfg.OAuthExcludedModels) == 0 {
				cfg.OAuthExcludedModels = nil
			}
			return nil
		}
		if cfg.OAuthExcludedModels == nil {
			cfg.OAuthExcludedModels = make(map[string][]string)
		}
		cfg.OAuthExcludedModels[provider] = normalized
		return nil
	})
}

func (h *Handler) DeleteOAuthExcludedModels(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		c.JSON(400, gin.H{"error": "missing provider"})
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		if cfg.OAuthExcludedModels == nil {
			return fmt.Errorf("provider not found")
		}
		if _, ok := cfg.OAuthExcludedModels[provider]; !ok {
			return fmt.Errorf("provider not found")
		}
		delete(cfg.OAuthExcludedModels, provider)
		if len(cfg.OAuthExcludedModels) == 0 {
			cfg.OAuthExcludedModels = nil
		}
		return nil
	})
}

// oauth-model-alias: map[string][]OAuthModelAlias
func (h *Handler) GetOAuthModelAlias(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"oauth-model-alias": sanitizedOAuthModelAlias(cfg.OAuthModelAlias)})
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.OAuthModelAlias = sanitizedOAuthModelAlias(entries)
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		if len(normalized) == 0 {
			if cfg.OAuthModelAlias == nil {
				return fmt.Errorf("channel not found")
			}
			if _, ok := cfg.OAuthModelAlias[channel]; !ok {
				return fmt.Errorf("channel not found")
			}
			delete(cfg.OAuthModelAlias, channel)
			if len(cfg.OAuthModelAlias) == 0 {
				cfg.OAuthModelAlias = nil
			}
			return nil
		}
		if cfg.OAuthModelAlias == nil {
			cfg.OAuthModelAlias = make(map[string][]config.OAuthModelAlias)
		}
		cfg.OAuthModelAlias[channel] = normalized
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		if cfg.OAuthModelAlias == nil {
			return fmt.Errorf("channel not found")
		}
		if _, ok := cfg.OAuthModelAlias[channel]; !ok {
			return fmt.Errorf("channel not found")
		}
		delete(cfg.OAuthModelAlias, channel)
		if len(cfg.OAuthModelAlias) == 0 {
			cfg.OAuthModelAlias = nil
		}
		return nil
	})
}

// codex-api-key: []CodexKey
func (h *Handler) GetCodexKeys(c *gin.Context) {
	cfg := h.configSnapshotOrEmpty()
	c.JSON(200, gin.H{"codex-api-key": cfg.CodexKey})
}
func (h *Handler) PutCodexKeys(c *gin.Context) {
	arr, ok := decodeJSONItems[config.CodexKey](c)
	if !ok {
		return
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		cfg.CodexKey = filtered
		cfg.SanitizeCodexKeys()
		return nil
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
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		targetIndex := findIndexByIndexOrMatch(cfg.CodexKey, body.Index, body.Match, func(item config.CodexKey, match string) bool {
			return item.APIKey == match
		})
		if targetIndex == -1 {
			return fmt.Errorf("item not found")
		}
		entry := cfg.CodexKey[targetIndex]
		if body.Value.APIKey != nil {
			entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
		}
		if body.Value.Prefix != nil {
			entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
		}
		if body.Value.BaseURL != nil {
			trimmed := strings.TrimSpace(*body.Value.BaseURL)
			if trimmed == "" {
				cfg.CodexKey = append(cfg.CodexKey[:targetIndex], cfg.CodexKey[targetIndex+1:]...)
				cfg.SanitizeCodexKeys()
				return nil
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
		cfg.CodexKey[targetIndex] = entry
		cfg.SanitizeCodexKeys()
		return nil
	})
}

func (h *Handler) DeleteCodexKey(c *gin.Context) {
	if val := c.Query("api-key"); val != "" {
		h.applyConfigMutation(c, func(cfg *config.Config) error {
			out := make([]config.CodexKey, 0, len(cfg.CodexKey))
			for _, v := range cfg.CodexKey {
				if v.APIKey != val {
					out = append(out, v)
				}
			}
			cfg.CodexKey = out
			cfg.SanitizeCodexKeys()
			return nil
		})
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil {
			h.applyConfigMutation(c, func(cfg *config.Config) error {
				if idx < 0 || idx >= len(cfg.CodexKey) {
					return fmt.Errorf("missing api-key or index")
				}
				cfg.CodexKey = append(cfg.CodexKey[:idx], cfg.CodexKey[idx+1:]...)
				cfg.SanitizeCodexKeys()
				return nil
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
	c.JSON(200, gin.H{"ampcode": h.ampCodeSnapshot()})
}

// GetAmpUpstreamURL returns the ampcode upstream URL.
func (h *Handler) GetAmpUpstreamURL(c *gin.Context) {
	c.JSON(200, gin.H{"upstream-url": h.ampCodeSnapshot().UpstreamURL})
}

// PutAmpUpstreamURL updates the ampcode upstream URL.
func (h *Handler) PutAmpUpstreamURL(c *gin.Context) {
	h.updateStringField(c, func(cfg *config.Config, v string) { cfg.AmpCode.UpstreamURL = strings.TrimSpace(v) })
}

// DeleteAmpUpstreamURL clears the ampcode upstream URL.
func (h *Handler) DeleteAmpUpstreamURL(c *gin.Context) {
	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		amp.UpstreamURL = ""
		return nil
	})
}

// GetAmpUpstreamAPIKey returns the ampcode upstream API key.
func (h *Handler) GetAmpUpstreamAPIKey(c *gin.Context) {
	c.JSON(200, gin.H{"upstream-api-key": h.ampCodeSnapshot().UpstreamAPIKey})
}

// PutAmpUpstreamAPIKey updates the ampcode upstream API key.
func (h *Handler) PutAmpUpstreamAPIKey(c *gin.Context) {
	h.updateStringField(c, func(cfg *config.Config, v string) { cfg.AmpCode.UpstreamAPIKey = strings.TrimSpace(v) })
}

// DeleteAmpUpstreamAPIKey clears the ampcode upstream API key.
func (h *Handler) DeleteAmpUpstreamAPIKey(c *gin.Context) {
	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		amp.UpstreamAPIKey = ""
		return nil
	})
}

// GetAmpRestrictManagementToLocalhost returns the localhost restriction setting.
func (h *Handler) GetAmpRestrictManagementToLocalhost(c *gin.Context) {
	c.JSON(200, gin.H{"restrict-management-to-localhost": h.ampCodeSnapshot().RestrictManagementToLocalhost})
}

// PutAmpRestrictManagementToLocalhost updates the localhost restriction setting.
func (h *Handler) PutAmpRestrictManagementToLocalhost(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.AmpCode.RestrictManagementToLocalhost = v })
}

// GetAmpModelMappings returns the ampcode model mappings.
func (h *Handler) GetAmpModelMappings(c *gin.Context) {
	c.JSON(200, gin.H{"model-mappings": h.ampCodeSnapshot().ModelMappings})
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
	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		amp.ModelMappings = body.Value
		return nil
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

	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		existing := make(map[string]int)
		for i, m := range amp.ModelMappings {
			existing[strings.TrimSpace(m.From)] = i
		}
		for _, newMapping := range body.Value {
			from := strings.TrimSpace(newMapping.From)
			if idx, ok := existing[from]; ok {
				amp.ModelMappings[idx] = newMapping
			} else {
				amp.ModelMappings = append(amp.ModelMappings, newMapping)
				existing[from] = len(amp.ModelMappings) - 1
			}
		}
		return nil
	})
}

// DeleteAmpModelMappings removes specified model mappings by "from" field.
func (h *Handler) DeleteAmpModelMappings(c *gin.Context) {
	var body struct {
		Value []string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Value) == 0 {
		h.mutateAmpCode(c, func(amp *config.AmpCode) error {
			amp.ModelMappings = nil
			return nil
		})
		return
	}

	toRemove := make(map[string]bool)
	for _, from := range body.Value {
		toRemove[strings.TrimSpace(from)] = true
	}

	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		newMappings := make([]config.AmpModelMapping, 0, len(amp.ModelMappings))
		for _, m := range amp.ModelMappings {
			if !toRemove[strings.TrimSpace(m.From)] {
				newMappings = append(newMappings, m)
			}
		}
		amp.ModelMappings = newMappings
		return nil
	})
}

// GetAmpForceModelMappings returns whether model mappings are forced.
func (h *Handler) GetAmpForceModelMappings(c *gin.Context) {
	c.JSON(200, gin.H{"force-model-mappings": h.ampCodeSnapshot().ForceModelMappings})
}

// PutAmpForceModelMappings updates the force model mappings setting.
func (h *Handler) PutAmpForceModelMappings(c *gin.Context) {
	h.updateBoolField(c, func(cfg *config.Config, v bool) { cfg.AmpCode.ForceModelMappings = v })
}

// GetAmpUpstreamAPIKeys returns the ampcode upstream API keys mapping.
func (h *Handler) GetAmpUpstreamAPIKeys(c *gin.Context) {
	c.JSON(200, gin.H{"upstream-api-keys": h.ampCodeSnapshot().UpstreamAPIKeys})
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
	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		amp.UpstreamAPIKeys = normalized
		return nil
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

	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		existing := make(map[string]int)
		for i, entry := range amp.UpstreamAPIKeys {
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
				amp.UpstreamAPIKeys[idx] = normalizedEntry
			} else {
				amp.UpstreamAPIKeys = append(amp.UpstreamAPIKeys, normalizedEntry)
				existing[upstreamKey] = len(amp.UpstreamAPIKeys) - 1
			}
		}
		return nil
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
		h.mutateAmpCode(c, func(amp *config.AmpCode) error {
			amp.UpstreamAPIKeys = nil
			return nil
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

	h.mutateAmpCode(c, func(amp *config.AmpCode) error {
		newEntries := make([]config.AmpUpstreamAPIKeyEntry, 0, len(amp.UpstreamAPIKeys))
		for _, entry := range amp.UpstreamAPIKeys {
			if !toRemove[strings.TrimSpace(entry.UpstreamAPIKey)] {
				newEntries = append(newEntries, entry)
			}
		}
		amp.UpstreamAPIKeys = newEntries
		return nil
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
