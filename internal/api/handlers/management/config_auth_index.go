package management

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
)

type configAuthIndexViews struct {
	gemini         []string
	claude         []string
	codex          []string
	vertex         []string
	openAIEntries  [][]string
	openAIFallback []string
}

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
	Name          string                                   `json:"name"`
	Priority      int                                      `json:"priority,omitempty"`
	Prefix        string                                   `json:"prefix,omitempty"`
	BaseURL       string                                   `json:"base-url"`
	APIKeyEntries []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models        []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers       map[string]string                        `json:"headers,omitempty"`
	AuthIndex     string                                   `json:"auth-index,omitempty"`
}

func (h *Handler) buildConfigAuthIndexViews() configAuthIndexViews {
	cfg := h.cfg
	if cfg == nil {
		return configAuthIndexViews{}
	}

	liveIndexByID := map[string]string{}
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil || strings.TrimSpace(auth.ID) == "" {
				continue
			}
			auth.EnsureIndex()
			if auth.Index == "" {
				continue
			}
			liveIndexByID[auth.ID] = auth.Index
		}
	}

	views := configAuthIndexViews{
		gemini:         make([]string, len(cfg.GeminiKey)),
		claude:         make([]string, len(cfg.ClaudeKey)),
		codex:          make([]string, len(cfg.CodexKey)),
		vertex:         make([]string, len(cfg.VertexCompatAPIKey)),
		openAIEntries:  make([][]string, len(cfg.OpenAICompatibility)),
		openAIFallback: make([]string, len(cfg.OpenAICompatibility)),
	}

	auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		return views
	}

	cursor := 0
	nextAuthIndex := func() string {
		if cursor >= len(auths) {
			return ""
		}
		auth := auths[cursor]
		cursor++
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			return ""
		}
		// Do not expose an auth-index until it is present in the live auth manager.
		// API tools resolve auth_index against h.authManager.List(), so returning
		// config-only indexes can temporarily break tool calls around config edits.
		return liveIndexByID[auth.ID]
	}

	for i := range cfg.GeminiKey {
		if strings.TrimSpace(cfg.GeminiKey[i].APIKey) == "" {
			continue
		}
		views.gemini[i] = nextAuthIndex()
	}
	for i := range cfg.ClaudeKey {
		if strings.TrimSpace(cfg.ClaudeKey[i].APIKey) == "" {
			continue
		}
		views.claude[i] = nextAuthIndex()
	}
	for i := range cfg.CodexKey {
		if strings.TrimSpace(cfg.CodexKey[i].APIKey) == "" {
			continue
		}
		views.codex[i] = nextAuthIndex()
	}
	for i := range cfg.OpenAICompatibility {
		entries := cfg.OpenAICompatibility[i].APIKeyEntries
		if len(entries) == 0 {
			views.openAIFallback[i] = nextAuthIndex()
			continue
		}

		views.openAIEntries[i] = make([]string, len(entries))
		for j := range entries {
			views.openAIEntries[i][j] = nextAuthIndex()
		}
	}
	for i := range cfg.VertexCompatAPIKey {
		if strings.TrimSpace(cfg.VertexCompatAPIKey[i].APIKey) == "" {
			continue
		}
		views.vertex[i] = nextAuthIndex()
	}

	return views
}

func (h *Handler) geminiKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil || h.cfg == nil {
		return nil
	}
	views := h.buildConfigAuthIndexViews()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey: h.cfg.GeminiKey[i],
			AuthIndex: views.gemini[i],
		}
	}
	return out
}

func (h *Handler) claudeKeysWithAuthIndex() []claudeKeyWithAuthIndex {
	if h == nil || h.cfg == nil {
		return nil
	}
	views := h.buildConfigAuthIndexViews()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		out[i] = claudeKeyWithAuthIndex{
			ClaudeKey: h.cfg.ClaudeKey[i],
			AuthIndex: views.claude[i],
		}
	}
	return out
}

func (h *Handler) codexKeysWithAuthIndex() []codexKeyWithAuthIndex {
	if h == nil || h.cfg == nil {
		return nil
	}
	views := h.buildConfigAuthIndexViews()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		out[i] = codexKeyWithAuthIndex{
			CodexKey:  h.cfg.CodexKey[i],
			AuthIndex: views.codex[i],
		}
	}
	return out
}

func (h *Handler) vertexCompatKeysWithAuthIndex() []vertexCompatKeyWithAuthIndex {
	if h == nil || h.cfg == nil {
		return nil
	}
	views := h.buildConfigAuthIndexViews()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: h.cfg.VertexCompatAPIKey[i],
			AuthIndex:       views.vertex[i],
		}
	}
	return out
}

func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil || h.cfg == nil {
		return nil
	}

	views := h.buildConfigAuthIndexViews()
	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	for i := range normalized {
		entry := normalized[i]
		response := openAICompatibilityWithAuthIndex{
			Name:      entry.Name,
			Priority:  entry.Priority,
			Prefix:    entry.Prefix,
			BaseURL:   entry.BaseURL,
			Models:    entry.Models,
			Headers:   entry.Headers,
			AuthIndex: views.openAIFallback[i],
		}
		if len(entry.APIKeyEntries) > 0 {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				authIndex := ""
				if i < len(views.openAIEntries) && j < len(views.openAIEntries[i]) {
					authIndex = views.openAIEntries[i][j]
				}
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: entry.APIKeyEntries[j],
					AuthIndex:                 authIndex,
				}
			}
		}
		out[i] = response
	}
	return out
}
