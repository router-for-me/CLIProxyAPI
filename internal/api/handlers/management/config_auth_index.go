package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type geminiKeyWithAuthIndex struct {
	config.GeminiKey
	AuthIndex  string `json:"auth-index,omitempty"`
	AuthKey    string `json:"auth-key,omitempty"`
	AuthSource string `json:"auth-source,omitempty"`
}

type claudeKeyWithAuthIndex struct {
	config.ClaudeKey
	AuthIndex  string `json:"auth-index,omitempty"`
	AuthKey    string `json:"auth-key,omitempty"`
	AuthSource string `json:"auth-source,omitempty"`
}

type codexKeyWithAuthIndex struct {
	config.CodexKey
	AuthIndex  string `json:"auth-index,omitempty"`
	AuthKey    string `json:"auth-key,omitempty"`
	AuthSource string `json:"auth-source,omitempty"`
}

type vertexCompatKeyWithAuthIndex struct {
	config.VertexCompatKey
	AuthIndex  string `json:"auth-index,omitempty"`
	AuthKey    string `json:"auth-key,omitempty"`
	AuthSource string `json:"auth-source,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex  string `json:"auth-index,omitempty"`
	AuthKey    string `json:"auth-key,omitempty"`
	AuthSource string `json:"auth-source,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name           string                                   `json:"name"`
	Priority       int                                      `json:"priority,omitempty"`
	Disabled       bool                                     `json:"disabled"`
	Prefix         string                                   `json:"prefix,omitempty"`
	BaseURL        string                                   `json:"base-url"`
	ProxyURL       string                                   `json:"proxy-url,omitempty"`
	Auth           *config.CommandAuthConfig                `json:"auth,omitempty"`
	APIKeyEntries  []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models         []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers        map[string]string                        `json:"headers,omitempty"`
	DisableCooling bool                                     `json:"disable-cooling,omitempty"`
	AuthIndex      string                                   `json:"auth-index,omitempty"`
	AuthKey        string                                   `json:"auth-key,omitempty"`
	AuthSource     string                                   `json:"auth-source,omitempty"`
}

type configWithAuthMetadata struct {
	config.Config
	GeminiKey           []geminiKeyWithAuthIndex           `json:"gemini-api-key"`
	CodexKey            []codexKeyWithAuthIndex            `json:"codex-api-key"`
	ClaudeKey           []claudeKeyWithAuthIndex           `json:"claude-api-key"`
	VertexCompatAPIKey  []vertexCompatKeyWithAuthIndex     `json:"vertex-api-key"`
	OpenAICompatibility []openAICompatibilityWithAuthIndex `json:"openai-compatibility"`
}

func (h *Handler) managementConfigSnapshot() configWithAuthMetadata {
	if h == nil {
		return configWithAuthMetadata{}
	}
	h.mu.Lock()
	base := config.Config{}
	if h.cfg != nil {
		base = *h.cfg
	}
	h.mu.Unlock()

	return configWithAuthMetadata{
		Config:              base,
		GeminiKey:           h.geminiKeysWithAuthIndex(),
		CodexKey:            h.codexKeysWithAuthIndex(),
		ClaudeKey:           h.claudeKeysWithAuthIndex(),
		VertexCompatAPIKey:  h.vertexCompatKeysWithAuthIndex(),
		OpenAICompatibility: h.openAICompatibilityWithAuthIndex(),
	}
}

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
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
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

func commandAuthConfigManagementKey(auth *config.CommandAuthConfig) string {
	return commandAuthManagementKey(config.CommandAuthIdentity(auth))
}

func isCommandAuthAPIKey(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "auth-command:")
}

func clearCommandAuthAPIKey(apiKey string, auth *config.CommandAuthConfig) string {
	apiKey = strings.TrimSpace(apiKey)
	if auth != nil && isCommandAuthAPIKey(apiKey) {
		return ""
	}
	return apiKey
}

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

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		authIndex := ""
		authKey := ""
		authSource := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL)
			id, _ := idGen.Next("gemini:apikey", idParts...)
			authIndex = liveIndexByID[id]
			authKey = commandAuthConfigManagementKey(entry.Auth)
			authSource = coreauth.AttrAuthSourceCommand
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey:  entry,
			AuthIndex:  authIndex,
			AuthKey:    authKey,
			AuthSource: authSource,
		}
	}
	return out
}

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

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		authIndex := ""
		authKey := ""
		authSource := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("claude:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL)
			id, _ := idGen.Next("claude:apikey", idParts...)
			authIndex = liveIndexByID[id]
			authKey = commandAuthConfigManagementKey(entry.Auth)
			authSource = coreauth.AttrAuthSourceCommand
		}
		out[i] = claudeKeyWithAuthIndex{
			ClaudeKey:  entry,
			AuthIndex:  authIndex,
			AuthKey:    authKey,
			AuthSource: authSource,
		}
	}
	return out
}

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

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		authIndex := ""
		authKey := ""
		authSource := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("codex:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL)
			id, _ := idGen.Next("codex:apikey", idParts...)
			authIndex = liveIndexByID[id]
			authKey = commandAuthConfigManagementKey(entry.Auth)
			authSource = coreauth.AttrAuthSourceCommand
		}
		out[i] = codexKeyWithAuthIndex{
			CodexKey:   entry,
			AuthIndex:  authIndex,
			AuthKey:    authKey,
			AuthSource: authSource,
		}
	}
	return out
}

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

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		var id string
		authKey := ""
		authSource := ""
		if strings.TrimSpace(entry.APIKey) != "" {
			id, _ = idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL, strings.TrimSpace(entry.ProxyURL))
			id, _ = idGen.Next("vertex:apikey", idParts...)
			authKey = commandAuthConfigManagementKey(entry.Auth)
			authSource = coreauth.AttrAuthSourceCommand
		}
		authIndex := liveIndexByID[id]
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: entry,
			AuthIndex:       authIndex,
			AuthKey:         authKey,
			AuthSource:      authSource,
		}
	}
	return out
}

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
			ProxyURL:       entry.ProxyURL,
			Auth:           entry.Auth,
			Models:         entry.Models,
			Headers:        entry.Headers,
			DisableCooling: entry.DisableCooling,
			AuthIndex:      "",
		}
		if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL, strings.TrimSpace(entry.ProxyURL))
			id, _ := idGen.Next(idKind, idParts...)
			response.AuthIndex = liveIndexByID[id]
			response.AuthKey = commandAuthConfigManagementKey(entry.Auth)
			response.AuthSource = coreauth.AttrAuthSourceCommand
		} else if len(entry.APIKeyEntries) == 0 {
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
