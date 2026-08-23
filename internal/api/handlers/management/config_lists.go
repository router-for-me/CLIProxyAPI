package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func parseCredentialWeightPatch(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("weight is missing")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var weight int
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if errDecode := decoder.Decode(&weight); errDecode != nil {
		return nil, fmt.Errorf("weight must be an integer")
	}
	if errValidate := config.ValidateCredentialWeight(&weight); errValidate != nil {
		return nil, errValidate
	}
	return &weight, nil
}

func rejectInvalidCredentialWeight(c *gin.Context, field string, weight *int) bool {
	if errValidate := config.ValidateCredentialWeight(weight); errValidate != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("%s: %v", field, errValidate)})
		return true
	}
	return false
}

// rejectInvalidFingerprintProfile fails a write that carries a value the request
// path would silently ignore, so a typo surfaces here instead of as a warning
// behind every later request.
func rejectInvalidFingerprintProfile(c *gin.Context, field, profile string) bool {
	if errValidate := config.ValidateClaudeFingerprintProfile(profile); errValidate != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("%s: %v", field, errValidate)})
		return true
	}
	return false
}

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

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) { c.JSON(200, gin.H{"api-keys": h.cfg.APIKeys}) }
func (h *Handler) PutAPIKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		h.cfg.APIKeys = append([]string(nil), v...)
	}, nil)
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	h.patchStringList(c, &h.cfg.APIKeys, func() {})
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.deleteFromStringList(c, &h.cfg.APIKeys, func() {})
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
	for index := range arr {
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("gemini-api-key[%d].weight", index), arr[index].Weight) {
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.GeminiKey = append([]config.GeminiKey(nil), arr...)
	h.cfg.SanitizeGeminiKeys()
	h.persistLocked(c)
}
func (h *Handler) PatchGeminiKey(c *gin.Context) {
	type geminiKeyPatch struct {
		APIKey              *string                          `json:"api-key"`
		Weight              json.RawMessage                  `json:"weight"`
		Prefix              *string                          `json:"prefix"`
		BaseURL             *string                          `json:"base-url"`
		ProxyURL            *string                          `json:"proxy-url"`
		Headers             *map[string]string               `json:"headers"`
		ExcludedModels      *[]string                        `json:"excluded-models"`
		DisableCooling      json.RawMessage                  `json:"disable-cooling"`
		RequestRetry        *int                             `json:"request-retry"`
		RequestScopedErrors *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
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

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.GeminiKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		if match != "" {
			baseRaw, hasBase := c.GetQuery("base-url")
			base := strings.TrimSpace(baseRaw)
			matches := make([]int, 0, 1)
			for i := range h.cfg.GeminiKey {
				if strings.TrimSpace(h.cfg.GeminiKey[i].APIKey) != match {
					continue
				}
				if hasBase && strings.TrimSpace(h.cfg.GeminiKey[i].BaseURL) != base {
					continue
				}
				matches = append(matches, i)
			}
			if len(matches) > 1 {
				c.JSON(400, gin.H{"error": "multiple items match; index is required"})
				return
			}
			if len(matches) == 1 {
				targetIndex = matches[0]
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.GeminiKey[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
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
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	if entry.APIKey == "" && entry.BaseURL == "" {
		h.cfg.GeminiKey = append(h.cfg.GeminiKey[:targetIndex], h.cfg.GeminiKey[targetIndex+1:]...)
		h.cfg.SanitizeGeminiKeys()
		h.persistLocked(c)
		return
	}
	h.cfg.GeminiKey[targetIndex] = entry
	h.cfg.SanitizeGeminiKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteGeminiKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			matchIndex := -1
			matchCount := 0
			for i := range h.cfg.GeminiKey {
				if strings.TrimSpace(h.cfg.GeminiKey[i].APIKey) == val && strings.TrimSpace(h.cfg.GeminiKey[i].BaseURL) == base {
					matchIndex = i
					matchCount++
				}
			}
			if matchCount == 0 {
				c.JSON(404, gin.H{"error": "item not found"})
				return
			}
			if matchCount > 1 {
				c.JSON(400, gin.H{"error": "multiple items match; index is required"})
				return
			}
			h.cfg.GeminiKey = append(h.cfg.GeminiKey[:matchIndex], h.cfg.GeminiKey[matchIndex+1:]...)
			h.cfg.SanitizeGeminiKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.GeminiKey {
			if strings.TrimSpace(h.cfg.GeminiKey[i].APIKey) == val {
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
		h.cfg.GeminiKey = append(h.cfg.GeminiKey[:matchIndex], h.cfg.GeminiKey[matchIndex+1:]...)
		h.cfg.SanitizeGeminiKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.GeminiKey) {
			h.cfg.GeminiKey = append(h.cfg.GeminiKey[:idx], h.cfg.GeminiKey[idx+1:]...)
			h.cfg.SanitizeGeminiKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// interactions-api-key: []GeminiKey
func (h *Handler) GetInteractionsKeys(c *gin.Context) {
	c.JSON(200, gin.H{"interactions-api-key": h.interactionsKeysWithAuthIndex()})
}
func (h *Handler) PutInteractionsKeys(c *gin.Context) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.GeminiKey
	errUnmarshal := json.Unmarshal(data, &arr)
	if errUnmarshal != nil {
		var obj struct {
			Items []config.GeminiKey `json:"items"`
		}
		errObjUnmarshal := json.Unmarshal(data, &obj)
		if errObjUnmarshal != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	for index := range arr {
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("interactions-api-key[%d].weight", index), arr[index].Weight) {
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.InteractionsKey = append([]config.GeminiKey(nil), arr...)
	h.cfg.SanitizeInteractionsKeys()
	h.persistLocked(c)
}
func (h *Handler) PatchInteractionsKey(c *gin.Context) {
	type geminiKeyPatch struct {
		APIKey              *string                          `json:"api-key"`
		Weight              json.RawMessage                  `json:"weight"`
		Prefix              *string                          `json:"prefix"`
		BaseURL             *string                          `json:"base-url"`
		ProxyURL            *string                          `json:"proxy-url"`
		Headers             *map[string]string               `json:"headers"`
		ExcludedModels      *[]string                        `json:"excluded-models"`
		DisableCooling      json.RawMessage                  `json:"disable-cooling"`
		RequestRetry        *int                             `json:"request-retry"`
		RequestScopedErrors *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
	}
	var body struct {
		Index *int            `json:"index"`
		Match *string         `json:"match"`
		Value *geminiKeyPatch `json:"value"`
	}
	errBind := c.ShouldBindJSON(&body)
	if errBind != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.InteractionsKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		if match != "" {
			baseRaw, hasBase := c.GetQuery("base-url")
			base := strings.TrimSpace(baseRaw)
			matches := make([]int, 0, 1)
			for i := range h.cfg.InteractionsKey {
				if strings.TrimSpace(h.cfg.InteractionsKey[i].APIKey) != match {
					continue
				}
				if hasBase && strings.TrimSpace(h.cfg.InteractionsKey[i].BaseURL) != base {
					continue
				}
				matches = append(matches, i)
			}
			if len(matches) > 1 {
				c.JSON(400, gin.H{"error": "multiple items match; index is required"})
				return
			}
			if len(matches) == 1 {
				targetIndex = matches[0]
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.InteractionsKey[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
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
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	if entry.APIKey == "" && entry.BaseURL == "" {
		h.cfg.InteractionsKey = append(h.cfg.InteractionsKey[:targetIndex], h.cfg.InteractionsKey[targetIndex+1:]...)
		h.cfg.SanitizeInteractionsKeys()
		h.persistLocked(c)
		return
	}
	h.cfg.InteractionsKey[targetIndex] = entry
	h.cfg.SanitizeInteractionsKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteInteractionsKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			matchIndex := -1
			matchCount := 0
			for i := range h.cfg.InteractionsKey {
				if strings.TrimSpace(h.cfg.InteractionsKey[i].APIKey) == val && strings.TrimSpace(h.cfg.InteractionsKey[i].BaseURL) == base {
					matchIndex = i
					matchCount++
				}
			}
			if matchCount == 0 {
				c.JSON(404, gin.H{"error": "item not found"})
				return
			}
			if matchCount > 1 {
				c.JSON(400, gin.H{"error": "multiple items match; index is required"})
				return
			}
			h.cfg.InteractionsKey = append(h.cfg.InteractionsKey[:matchIndex], h.cfg.InteractionsKey[matchIndex+1:]...)
			h.cfg.SanitizeInteractionsKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.InteractionsKey {
			if strings.TrimSpace(h.cfg.InteractionsKey[i].APIKey) == val {
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
		h.cfg.InteractionsKey = append(h.cfg.InteractionsKey[:matchIndex], h.cfg.InteractionsKey[matchIndex+1:]...)
		h.cfg.SanitizeInteractionsKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, errScan := fmt.Sscanf(idxStr, "%d", &idx)
		if errScan == nil && idx >= 0 && idx < len(h.cfg.InteractionsKey) {
			h.cfg.InteractionsKey = append(h.cfg.InteractionsKey[:idx], h.cfg.InteractionsKey[idx+1:]...)
			h.cfg.SanitizeInteractionsKeys()
			h.persistLocked(c)
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
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("claude-api-key[%d].weight", i), arr[i].Weight) {
			return
		}
		if rejectInvalidFingerprintProfile(c, fmt.Sprintf("claude-api-key[%d].fingerprint-profile", i), arr[i].FingerprintProfile) {
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.ClaudeKey = arr
	h.cfg.SanitizeClaudeKeys()
	h.persistLocked(c)
}
func (h *Handler) PatchClaudeKey(c *gin.Context) {
	type claudeKeyPatch struct {
		APIKey                  *string                          `json:"api-key"`
		FingerprintProfile      *string                          `json:"fingerprint-profile"`
		Weight                  json.RawMessage                  `json:"weight"`
		Prefix                  *string                          `json:"prefix"`
		BaseURL                 *string                          `json:"base-url"`
		ProxyURL                *string                          `json:"proxy-url"`
		Models                  *[]config.ClaudeModel            `json:"models"`
		Headers                 *map[string]string               `json:"headers"`
		ExcludedModels          *[]string                        `json:"excluded-models"`
		RebuildMidSystemMessage *bool                            `json:"rebuild-mid-system-message"`
		DisableCooling          json.RawMessage                  `json:"disable-cooling"`
		RequestRetry            *int                             `json:"request-retry"`
		RequestScopedErrors     *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
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

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.ClaudeKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.ClaudeKey {
			if h.cfg.ClaudeKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.ClaudeKey[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
	}
	if body.Value.FingerprintProfile != nil {
		if rejectInvalidFingerprintProfile(c, "fingerprint-profile", *body.Value.FingerprintProfile) {
			return
		}
		entry.FingerprintProfile, _ = config.NormalizeClaudeFingerprintProfile(*body.Value.FingerprintProfile)
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
	if body.Value.RebuildMidSystemMessage != nil {
		entry.RebuildMidSystemMessage = *body.Value.RebuildMidSystemMessage
	}
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	normalizeClaudeKey(&entry)
	h.cfg.ClaudeKey[targetIndex] = entry
	h.cfg.SanitizeClaudeKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteClaudeKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.ClaudeKey, 0, len(h.cfg.ClaudeKey))
			for _, v := range h.cfg.ClaudeKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			h.cfg.ClaudeKey = out
			h.cfg.SanitizeClaudeKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.ClaudeKey {
			if strings.TrimSpace(h.cfg.ClaudeKey[i].APIKey) == val {
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
			h.cfg.ClaudeKey = append(h.cfg.ClaudeKey[:matchIndex], h.cfg.ClaudeKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeClaudeKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.ClaudeKey) {
			h.cfg.ClaudeKey = append(h.cfg.ClaudeKey[:idx], h.cfg.ClaudeKey[idx+1:]...)
			h.cfg.SanitizeClaudeKeys()
			h.persistLocked(c)
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
		if strings.TrimSpace(arr[i].BaseURL) == "" {
			continue
		}
		for keyIndex := range arr[i].APIKeyEntries {
			field := fmt.Sprintf("openai-compatibility[%d].api-key-entries[%d].weight", i, keyIndex)
			if rejectInvalidCredentialWeight(c, field, arr[i].APIKeyEntries[keyIndex].Weight) {
				return
			}
		}
		filtered = append(filtered, arr[i])
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OpenAICompatibility = filtered
	h.cfg.SanitizeOpenAICompatibility()
	h.persistLocked(c)
}
func (h *Handler) PatchOpenAICompat(c *gin.Context) {
	type openAICompatPatch struct {
		Name                  *string                             `json:"name"`
		Prefix                *string                             `json:"prefix"`
		Disabled              *bool                               `json:"disabled"`
		DisableCooling        json.RawMessage                     `json:"disable-cooling"`
		BaseURL               *string                             `json:"base-url"`
		APIKeyEntries         *[]config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		Models                *[]config.OpenAICompatibilityModel  `json:"models"`
		Headers               *map[string]string                  `json:"headers"`
		SupportPromptCacheKey *bool                               `json:"support-prompt-cache-key"`
		RequestRetry          *int                                `json:"request-retry"`
		RequestScopedErrors   *[]config.RequestScopedErrorRule    `json:"request-scoped-errors"`
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

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpenAICompatibility) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Name != nil {
		match := strings.TrimSpace(*body.Name)
		for i := range h.cfg.OpenAICompatibility {
			if h.cfg.OpenAICompatibility[i].Name == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.OpenAICompatibility[targetIndex]
	if body.Value.Name != nil {
		entry.Name = strings.TrimSpace(*body.Value.Name)
	}
	if body.Value.Prefix != nil {
		entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
	}
	if body.Value.Disabled != nil {
		entry.Disabled = *body.Value.Disabled
	}
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.BaseURL != nil {
		trimmed := strings.TrimSpace(*body.Value.BaseURL)
		if trimmed == "" {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:targetIndex], h.cfg.OpenAICompatibility[targetIndex+1:]...)
			h.cfg.SanitizeOpenAICompatibility()
			h.persistLocked(c)
			return
		}
		entry.BaseURL = trimmed
	}
	if body.Value.APIKeyEntries != nil {
		for keyIndex := range *body.Value.APIKeyEntries {
			weight := (*body.Value.APIKeyEntries)[keyIndex].Weight
			if rejectInvalidCredentialWeight(c, fmt.Sprintf("api-key-entries[%d].weight", keyIndex), weight) {
				return
			}
		}
		entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), (*body.Value.APIKeyEntries)...)
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.OpenAICompatibilityModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.SupportPromptCacheKey != nil {
		entry.SupportPromptCacheKey = *body.Value.SupportPromptCacheKey
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	normalizeOpenAICompatibilityEntry(&entry)
	h.cfg.OpenAICompatibility[targetIndex] = entry
	h.cfg.SanitizeOpenAICompatibility()
	h.persistLocked(c)
}

func (h *Handler) DeleteOpenAICompat(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if name := c.Query("name"); name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.OpenAICompatibility))
		for _, v := range h.cfg.OpenAICompatibility {
			if v.Name != name {
				out = append(out, v)
			}
		}
		h.cfg.OpenAICompatibility = out
		h.cfg.SanitizeOpenAICompatibility()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.OpenAICompatibility) {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:idx], h.cfg.OpenAICompatibility[idx+1:]...)
			h.cfg.SanitizeOpenAICompatibility()
			h.persistLocked(c)
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
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("vertex-api-key[%d].weight", i), arr[i].Weight) {
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.VertexCompatAPIKey = append([]config.VertexCompatKey(nil), arr...)
	h.cfg.SanitizeVertexCompatKeys()
	h.persistLocked(c)
}
func (h *Handler) PatchVertexCompatKey(c *gin.Context) {
	type vertexCompatPatch struct {
		APIKey         *string                     `json:"api-key"`
		Weight         json.RawMessage             `json:"weight"`
		Prefix         *string                     `json:"prefix"`
		BaseURL        *string                     `json:"base-url"`
		ProxyURL       *string                     `json:"proxy-url"`
		Headers        *map[string]string          `json:"headers"`
		Models         *[]config.VertexCompatModel `json:"models"`
		ExcludedModels *[]string                   `json:"excluded-models"`
		DisableCooling json.RawMessage             `json:"disable-cooling"`
		RequestRetry   *int                        `json:"request-retry"`
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

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.VertexCompatAPIKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		if match != "" {
			for i := range h.cfg.VertexCompatAPIKey {
				if h.cfg.VertexCompatAPIKey[i].APIKey == match {
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

	entry := h.cfg.VertexCompatAPIKey[targetIndex]
	if body.Value.APIKey != nil {
		trimmed := strings.TrimSpace(*body.Value.APIKey)
		if trimmed == "" {
			h.cfg.VertexCompatAPIKey = append(h.cfg.VertexCompatAPIKey[:targetIndex], h.cfg.VertexCompatAPIKey[targetIndex+1:]...)
			h.cfg.SanitizeVertexCompatKeys()
			h.persistLocked(c)
			return
		}
		entry.APIKey = trimmed
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
		entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
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
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	normalizeVertexCompatKey(&entry)
	h.cfg.VertexCompatAPIKey[targetIndex] = entry
	h.cfg.SanitizeVertexCompatKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteVertexCompatKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.VertexCompatKey, 0, len(h.cfg.VertexCompatAPIKey))
			for _, v := range h.cfg.VertexCompatAPIKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			h.cfg.VertexCompatAPIKey = out
			h.cfg.SanitizeVertexCompatKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.VertexCompatAPIKey {
			if strings.TrimSpace(h.cfg.VertexCompatAPIKey[i].APIKey) == val {
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
			h.cfg.VertexCompatAPIKey = append(h.cfg.VertexCompatAPIKey[:matchIndex], h.cfg.VertexCompatAPIKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeVertexCompatKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, errScan := fmt.Sscanf(idxStr, "%d", &idx)
		if errScan == nil && idx >= 0 && idx < len(h.cfg.VertexCompatAPIKey) {
			h.cfg.VertexCompatAPIKey = append(h.cfg.VertexCompatAPIKey[:idx], h.cfg.VertexCompatAPIKey[idx+1:]...)
			h.cfg.SanitizeVertexCompatKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// oauth-excluded-models: map[string][]string
func (h *Handler) GetOAuthExcludedModels(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-excluded-models": config.NormalizeOAuthExcludedModels(h.cfg.OAuthExcludedModels)})
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
	h.cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(entries)
	h.persist(c)
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
		if h.cfg.OAuthExcludedModels == nil {
			c.JSON(404, gin.H{"error": "provider not found"})
			return
		}
		if _, ok := h.cfg.OAuthExcludedModels[provider]; !ok {
			c.JSON(404, gin.H{"error": "provider not found"})
			return
		}
		delete(h.cfg.OAuthExcludedModels, provider)
		if len(h.cfg.OAuthExcludedModels) == 0 {
			h.cfg.OAuthExcludedModels = nil
		}
		h.persist(c)
		return
	}
	if h.cfg.OAuthExcludedModels == nil {
		h.cfg.OAuthExcludedModels = make(map[string][]string)
	}
	h.cfg.OAuthExcludedModels[provider] = normalized
	h.persist(c)
}

func (h *Handler) DeleteOAuthExcludedModels(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		c.JSON(400, gin.H{"error": "missing provider"})
		return
	}
	if h.cfg.OAuthExcludedModels == nil {
		c.JSON(404, gin.H{"error": "provider not found"})
		return
	}
	if _, ok := h.cfg.OAuthExcludedModels[provider]; !ok {
		c.JSON(404, gin.H{"error": "provider not found"})
		return
	}
	delete(h.cfg.OAuthExcludedModels, provider)
	if len(h.cfg.OAuthExcludedModels) == 0 {
		h.cfg.OAuthExcludedModels = nil
	}
	h.persist(c)
}

// oauth-model-alias: map[string][]OAuthModelAlias
func (h *Handler) GetOAuthModelAlias(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-model-alias": sanitizedOAuthModelAlias(h.cfg.OAuthModelAlias)})
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
	h.cfg.OAuthModelAlias = sanitizedOAuthModelAlias(entries)
	h.persist(c)
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
		if h.cfg.OAuthModelAlias == nil {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		if _, ok := h.cfg.OAuthModelAlias[channel]; !ok {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		delete(h.cfg.OAuthModelAlias, channel)
		if len(h.cfg.OAuthModelAlias) == 0 {
			h.cfg.OAuthModelAlias = nil
		}
		h.persist(c)
		return
	}
	if h.cfg.OAuthModelAlias == nil {
		h.cfg.OAuthModelAlias = make(map[string][]config.OAuthModelAlias)
	}
	h.cfg.OAuthModelAlias[channel] = normalized
	h.persist(c)
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
	if h.cfg.OAuthModelAlias == nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	if _, ok := h.cfg.OAuthModelAlias[channel]; !ok {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	delete(h.cfg.OAuthModelAlias, channel)
	if len(h.cfg.OAuthModelAlias) == 0 {
		h.cfg.OAuthModelAlias = nil
	}
	h.persist(c)
}

// oauth-request-scoped-errors: map[string][]RequestScopedErrorRule
func (h *Handler) GetOAuthRequestScopedErrors(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-request-scoped-errors": sanitizedOAuthRequestScopedErrors(h.cfg.OAuthRequestScopedErrors)})
}

func (h *Handler) PutOAuthRequestScopedErrors(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string][]config.RequestScopedErrorRule
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]config.RequestScopedErrorRule `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.cfg.OAuthRequestScopedErrors = sanitizedOAuthRequestScopedErrors(entries)
	h.persist(c)
}

func (h *Handler) PatchOAuthRequestScopedErrors(c *gin.Context) {
	var body struct {
		Provider *string                         `json:"provider"`
		Channel  *string                         `json:"channel"`
		Rules    []config.RequestScopedErrorRule `json:"rules"`
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

	normalizedMap := sanitizedOAuthRequestScopedErrors(map[string][]config.RequestScopedErrorRule{channel: body.Rules})
	normalized := normalizedMap[channel]
	if len(normalized) == 0 {
		if h.cfg.OAuthRequestScopedErrors == nil {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		if _, ok := h.cfg.OAuthRequestScopedErrors[channel]; !ok {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		delete(h.cfg.OAuthRequestScopedErrors, channel)
		if len(h.cfg.OAuthRequestScopedErrors) == 0 {
			h.cfg.OAuthRequestScopedErrors = nil
		}
		h.persist(c)
		return
	}
	if h.cfg.OAuthRequestScopedErrors == nil {
		h.cfg.OAuthRequestScopedErrors = make(map[string][]config.RequestScopedErrorRule)
	}
	h.cfg.OAuthRequestScopedErrors[channel] = normalized
	h.persist(c)
}

func (h *Handler) DeleteOAuthRequestScopedErrors(c *gin.Context) {
	channel := strings.ToLower(strings.TrimSpace(c.Query("channel")))
	if channel == "" {
		channel = strings.ToLower(strings.TrimSpace(c.Query("provider")))
	}
	if channel == "" {
		c.JSON(400, gin.H{"error": "missing channel"})
		return
	}
	if h.cfg.OAuthRequestScopedErrors == nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	if _, ok := h.cfg.OAuthRequestScopedErrors[channel]; !ok {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	delete(h.cfg.OAuthRequestScopedErrors, channel)
	if len(h.cfg.OAuthRequestScopedErrors) == 0 {
		h.cfg.OAuthRequestScopedErrors = nil
	}
	h.persist(c)
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
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("codex-api-key[%d].weight", i), entry.Weight) {
			return
		}
		filtered = append(filtered, entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.CodexKey = filtered
	h.cfg.SanitizeCodexKeys()
	h.persistLocked(c)
}
func (h *Handler) PatchCodexKey(c *gin.Context) {
	type codexKeyPatch struct {
		APIKey              *string                          `json:"api-key"`
		Weight              json.RawMessage                  `json:"weight"`
		Prefix              *string                          `json:"prefix"`
		BaseURL             *string                          `json:"base-url"`
		ProxyURL            *string                          `json:"proxy-url"`
		AlphaSearch         *bool                            `json:"alpha-search"`
		Models              *[]config.CodexModel             `json:"models"`
		Headers             *map[string]string               `json:"headers"`
		ExcludedModels      *[]string                        `json:"excluded-models"`
		DisableCooling      json.RawMessage                  `json:"disable-cooling"`
		RequestRetry        *int                             `json:"request-retry"`
		RequestScopedErrors *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
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

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.CodexKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.CodexKey {
			if h.cfg.CodexKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.CodexKey[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
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
			h.cfg.CodexKey = append(h.cfg.CodexKey[:targetIndex], h.cfg.CodexKey[targetIndex+1:]...)
			h.cfg.SanitizeCodexKeys()
			h.persistLocked(c)
			return
		}
		entry.BaseURL = trimmed
	}
	if body.Value.ProxyURL != nil {
		entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
	}
	if body.Value.AlphaSearch != nil {
		entry.AlphaSearch = *body.Value.AlphaSearch
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
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	normalizeCodexKey(&entry)
	h.cfg.CodexKey[targetIndex] = entry
	h.cfg.SanitizeCodexKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteCodexKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.CodexKey, 0, len(h.cfg.CodexKey))
			for _, v := range h.cfg.CodexKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			h.cfg.CodexKey = out
			h.cfg.SanitizeCodexKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.CodexKey {
			if strings.TrimSpace(h.cfg.CodexKey[i].APIKey) == val {
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
			h.cfg.CodexKey = append(h.cfg.CodexKey[:matchIndex], h.cfg.CodexKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeCodexKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.CodexKey) {
			h.cfg.CodexKey = append(h.cfg.CodexKey[:idx], h.cfg.CodexKey[idx+1:]...)
			h.cfg.SanitizeCodexKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// xai-api-key: []XAIKey
func (h *Handler) GetXAIKeys(c *gin.Context) {
	c.JSON(200, gin.H{"xai-api-key": h.xaiKeysWithAuthIndex()})
}

func (h *Handler) PutXAIKeys(c *gin.Context) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.XAIKey
	if errUnmarshal := json.Unmarshal(data, &arr); errUnmarshal != nil {
		var obj struct {
			Items []config.XAIKey `json:"items"`
		}
		if errObject := json.Unmarshal(data, &obj); errObject != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	filtered := make([]config.XAIKey, 0, len(arr))
	for i := range arr {
		entry := arr[i]
		normalizeCodexKey(&entry)
		if entry.BaseURL == "" {
			continue
		}
		if rejectInvalidCredentialWeight(c, fmt.Sprintf("xai-api-key[%d].weight", i), entry.Weight) {
			return
		}
		filtered = append(filtered, entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.XAIKey = filtered
	h.cfg.SanitizeXAIKeys()
	h.persistLocked(c)
}

func (h *Handler) PatchXAIKey(c *gin.Context) {
	type xaiKeyPatch struct {
		APIKey              *string                          `json:"api-key"`
		Priority            *int                             `json:"priority"`
		Weight              json.RawMessage                  `json:"weight"`
		Prefix              *string                          `json:"prefix"`
		BaseURL             *string                          `json:"base-url"`
		Websockets          *bool                            `json:"websockets"`
		ProxyURL            *string                          `json:"proxy-url"`
		Models              *[]config.XAIModel               `json:"models"`
		Headers             *map[string]string               `json:"headers"`
		ExcludedModels      *[]string                        `json:"excluded-models"`
		DisableCooling      json.RawMessage                  `json:"disable-cooling"`
		RequestRetry        *int                             `json:"request-retry"`
		RequestScopedErrors *[]config.RequestScopedErrorRule `json:"request-scoped-errors"`
	}
	var body struct {
		Index *int         `json:"index"`
		Match *string      `json:"match"`
		Value *xaiKeyPatch `json:"value"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.XAIKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.XAIKey {
			if h.cfg.XAIKey[i].APIKey == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.XAIKey[targetIndex]
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
			h.cfg.XAIKey = append(h.cfg.XAIKey[:targetIndex], h.cfg.XAIKey[targetIndex+1:]...)
			h.cfg.SanitizeXAIKeys()
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
		entry.Models = append([]config.XAIModel(nil), (*body.Value.Models)...)
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
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	normalizeCodexKey(&entry)
	h.cfg.XAIKey[targetIndex] = entry
	h.cfg.SanitizeXAIKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteXAIKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.XAIKey, 0, len(h.cfg.XAIKey))
			for _, entry := range h.cfg.XAIKey {
				if strings.TrimSpace(entry.APIKey) == val && strings.TrimSpace(entry.BaseURL) == base {
					continue
				}
				out = append(out, entry)
			}
			h.cfg.XAIKey = out
			h.cfg.SanitizeXAIKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.XAIKey {
			if strings.TrimSpace(h.cfg.XAIKey[i].APIKey) == val {
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
			h.cfg.XAIKey = append(h.cfg.XAIKey[:matchIndex], h.cfg.XAIKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeXAIKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, errScan := fmt.Sscanf(idxStr, "%d", &idx)
		if errScan == nil && idx >= 0 && idx < len(h.cfg.XAIKey) {
			h.cfg.XAIKey = append(h.cfg.XAIKey[:idx], h.cfg.XAIKey[idx+1:]...)
			h.cfg.SanitizeXAIKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

func applyDisableCoolingPatch(c *gin.Context, raw json.RawMessage, target **bool) bool {
	if len(raw) == 0 {
		return true
	}
	if strings.TrimSpace(string(raw)) == "null" {
		*target = nil
		return true
	}
	var value bool
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		c.JSON(400, gin.H{"error": "disable-cooling must be a boolean or null"})
		return false
	}
	*target = &value
	return true
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
		if len(copyEntry.RequestScopedErrors) > 0 {
			copyEntry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), copyEntry.RequestScopedErrors...)
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
	if normalized, ok := config.NormalizeClaudeFingerprintProfile(entry.FingerprintProfile); ok {
		entry.FingerprintProfile = normalized
	} else {
		entry.FingerprintProfile = strings.TrimSpace(entry.FingerprintProfile)
	}
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

func sanitizedOAuthRequestScopedErrors(entries map[string][]config.RequestScopedErrorRule) map[string][]config.RequestScopedErrorRule {
	if len(entries) == 0 {
		return nil
	}
	copied := make(map[string][]config.RequestScopedErrorRule, len(entries))
	for channel, rules := range entries {
		if len(rules) == 0 {
			continue
		}
		copied[channel] = append([]config.RequestScopedErrorRule(nil), rules...)
	}
	if len(copied) == 0 {
		return nil
	}
	cfg := config.Config{OAuthRequestScopedErrors: copied}
	cfg.SanitizeOAuthRequestScopedErrors()
	if len(cfg.OAuthRequestScopedErrors) == 0 {
		return nil
	}
	return cfg.OAuthRequestScopedErrors
}
