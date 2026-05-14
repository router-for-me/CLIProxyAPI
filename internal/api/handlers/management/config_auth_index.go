package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

type geminiKeyWithAuthIndex struct {
	config.GeminiKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type claudeKeyWithAuthIndex struct {
	config.ClaudeKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type codexKeyWithAuthIndex struct {
	config.CodexKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type vertexCompatKeyWithAuthIndex struct {
	config.VertexCompatKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name           string                                   `json:"name"`
	Priority       int                                      `json:"priority,omitempty"`
	Disabled       bool                                     `json:"disabled"`
	Prefix         string                                   `json:"prefix,omitempty"`
	BaseURL        string                                   `json:"base-url"`
	APIKeyEntries  []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models         []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers        map[string]string                        `json:"headers,omitempty"`
	ExcludedModels []string                                 `json:"excluded-models,omitempty"`
	AuthIndex      string                                   `json:"auth-index,omitempty"`
}

// liveAuthIndexByID builds a map of auth ID -> auth index from the live auth manager.
// This method acquires and releases h.mu internally; callers should not hold the lock.
func (h *Handler) liveAuthIndexByID() map[string]string {
	out := map[string]string{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return out
	}
	// authManager.List() returns clones; EnsureIndex modifies only these temporary copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		out[id] = idx
	}
	return out
}

// geminiKeysWithAuthIndex returns Gemini key configs with auth index.
// liveAuthIndexByID acquires/releases h.mu internally, then this method acquires h.mu to protect cfg.
func (h *Handler) geminiKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}
	return h.geminiKeysWithAuthIndexWithoutLock(liveIndexByID)
}

// geminiKeysWithAuthIndexWithoutLock returns Gemini key configs with auth index (no lock management).
// Caller must hold h.mu. h.cfg must not be nil.
func (h *Handler) geminiKeysWithAuthIndexWithoutLock(liveIndexByID map[string]string) []geminiKeyWithAuthIndex {
	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

// claudeKeysWithAuthIndex returns Claude key configs with auth index.
// liveAuthIndexByID acquires/releases h.mu internally, then this method acquires h.mu to protect cfg.
func (h *Handler) claudeKeysWithAuthIndex() []claudeKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}
	return h.claudeKeysWithAuthIndexWithoutLock(liveIndexByID)
}

// claudeKeysWithAuthIndexWithoutLock returns Claude key configs with auth index (no lock management).
// Caller must hold h.mu. h.cfg must not be nil.
func (h *Handler) claudeKeysWithAuthIndexWithoutLock(liveIndexByID map[string]string) []claudeKeyWithAuthIndex {
	idGen := synthesizer.NewStableIDGenerator()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("claude:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = claudeKeyWithAuthIndex{
			ClaudeKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

// codexKeysWithAuthIndex returns Codex key configs with auth index.
// liveAuthIndexByID acquires/releases h.mu internally, then this method acquires h.mu to protect cfg.
func (h *Handler) codexKeysWithAuthIndex() []codexKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}
	return h.codexKeysWithAuthIndexWithoutLock(liveIndexByID)
}

// codexKeysWithAuthIndexWithoutLock returns Codex key configs with auth index (no lock management).
// Caller must hold h.mu. h.cfg must not be nil.
func (h *Handler) codexKeysWithAuthIndexWithoutLock(liveIndexByID map[string]string) []codexKeyWithAuthIndex {
	idGen := synthesizer.NewStableIDGenerator()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("codex:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = codexKeyWithAuthIndex{
			CodexKey:  entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

// vertexCompatKeysWithAuthIndex returns Vertex key configs with auth index.
// liveAuthIndexByID acquires/releases h.mu internally, then this method acquires h.mu to protect cfg.
func (h *Handler) vertexCompatKeysWithAuthIndex() []vertexCompatKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}
	return h.vertexCompatKeysWithAuthIndexWithoutLock(liveIndexByID)
}

// vertexCompatKeysWithAuthIndexWithoutLock returns Vertex key configs with auth index (no lock management).
// Caller must hold h.mu. h.cfg must not be nil.
func (h *Handler) vertexCompatKeysWithAuthIndexWithoutLock(liveIndexByID map[string]string) []vertexCompatKeyWithAuthIndex {
	idGen := synthesizer.NewStableIDGenerator()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		authIndex := liveIndexByID[id]
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: entry,
			AuthIndex:       authIndex,
		}
	}
	return out
}

// openAICompatibilityWithAuthIndex returns OpenAI compatibility configs with auth index.
// liveAuthIndexByID acquires/releases h.mu internally, then this method acquires h.mu to protect cfg.
func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}
	return h.openAICompatibilityWithAuthIndexWithoutLock(liveIndexByID)
}

// openAICompatibilityWithAuthIndexWithoutLock returns OpenAI compatibility configs with auth index (no lock management).
// Caller must hold h.mu. h.cfg must not be nil.
func (h *Handler) openAICompatibilityWithAuthIndexWithoutLock(liveIndexByID map[string]string) []openAICompatibilityWithAuthIndex {
	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	idGen := synthesizer.NewStableIDGenerator()
	for i := range normalized {
		entry := normalized[i]
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)

		response := openAICompatibilityWithAuthIndex{
			Name:           entry.Name,
			Priority:       entry.Priority,
			Disabled:       entry.Disabled,
			Prefix:         entry.Prefix,
			BaseURL:        entry.BaseURL,
			Models:         entry.Models,
			Headers:        entry.Headers,
			ExcludedModels: entry.ExcludedModels,
			AuthIndex:      "",
		}
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			response.AuthIndex = liveIndexByID[id]
		} else {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: apiKeyEntry,
					AuthIndex:                 liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}

// allProvidersWithAuthIndex returns all provider configurations with auth index.
// Acquires h.mu once for all providers (vs 5 separate lock acquisitions
// when calling individual *WithAuthIndex() methods).
// liveAuthIndexByID acquires/releases h.mu internally, then this method acquires h.mu
// and delegates to *WithoutLock methods to build all results.
func (h *Handler) allProvidersWithAuthIndex() map[string]any {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	result := make(map[string]any, 5)
	result["gemini-api-key"] = h.geminiKeysWithAuthIndexWithoutLock(liveIndexByID)
	result["claude-api-key"] = h.claudeKeysWithAuthIndexWithoutLock(liveIndexByID)
	result["codex-api-key"] = h.codexKeysWithAuthIndexWithoutLock(liveIndexByID)
	result["openai-compatibility"] = h.openAICompatibilityWithAuthIndexWithoutLock(liveIndexByID)
	result["vertex-api-key"] = h.vertexCompatKeysWithAuthIndexWithoutLock(liveIndexByID)

	return result
}
