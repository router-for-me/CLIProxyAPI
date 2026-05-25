package cliproxy

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (s *Service) persistConfigBackedAutoDisabledAuth(auth *coreauth.Auth) {
	if s == nil || auth == nil || auth.Attributes == nil || strings.TrimSpace(s.configPath) == "" {
		return
	}
	source := strings.TrimSpace(auth.Attributes["source"])
	if !strings.HasPrefix(source, "config:") {
		return
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.cfg == nil {
		return
	}
	if !setConfigBackedAuthDisabledState(s.cfg, auth, true) {
		return
	}
	if err := config.SaveConfigPreserveComments(s.configPath, s.cfg); err != nil {
		log.WithError(err).WithField("auth_id", auth.ID).Error("failed to persist insufficient-balance auth disable")
		return
	}
	if s.watcher != nil {
		s.watcher.SetConfig(s.cfg)
	}
	if s.coreManager != nil {
		s.coreManager.SetConfig(s.cfg)
	}
	log.WithField("auth_id", auth.ID).Warn("persisted auth disable after upstream reported insufficient balance")
}

func setConfigBackedAuthDisabledState(cfg *config.Config, auth *coreauth.Auth, disabled bool) bool {
	if cfg == nil || auth == nil || auth.Attributes == nil {
		return false
	}
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	authBaseURL := strings.TrimSpace(auth.Attributes["base_url"])
	targetID := strings.TrimSpace(auth.ID)
	matchesTargetID := func(candidateID string) bool {
		return targetID != "" && strings.TrimSpace(candidateID) == targetID
	}
	matchesKeyAndBaseURL := func(entryKey, entryBaseURL string) bool {
		if strings.TrimSpace(entryKey) != apiKey {
			return false
		}
		entryBaseURL = strings.TrimSpace(entryBaseURL)
		if authBaseURL != "" {
			return entryBaseURL == authBaseURL
		}
		return entryBaseURL == ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))

	switch provider {
	case "gemini":
		idGen := synthesizer.NewStableIDGenerator()
		for i := range cfg.GeminiKey {
			id, _ := idGen.Next("gemini:apikey", cfg.GeminiKey[i].APIKey, cfg.GeminiKey[i].BaseURL)
			if matchesTargetID(id) || (targetID == "" && matchesKeyAndBaseURL(cfg.GeminiKey[i].APIKey, cfg.GeminiKey[i].BaseURL)) {
				if cfg.GeminiKey[i].Disabled == disabled {
					return false
				}
				cfg.GeminiKey[i].Disabled = disabled
				return true
			}
		}
	case "claude":
		idGen := synthesizer.NewStableIDGenerator()
		for i := range cfg.ClaudeKey {
			id, _ := idGen.Next("claude:apikey", cfg.ClaudeKey[i].APIKey, cfg.ClaudeKey[i].BaseURL)
			if matchesTargetID(id) || (targetID == "" && matchesKeyAndBaseURL(cfg.ClaudeKey[i].APIKey, cfg.ClaudeKey[i].BaseURL)) {
				if cfg.ClaudeKey[i].Disabled == disabled {
					return false
				}
				cfg.ClaudeKey[i].Disabled = disabled
				return true
			}
		}
	case "codex":
		idGen := synthesizer.NewStableIDGenerator()
		for i := range cfg.CodexKey {
			id, _ := idGen.Next("codex:apikey", cfg.CodexKey[i].APIKey, cfg.CodexKey[i].BaseURL)
			if matchesTargetID(id) || (targetID == "" && matchesKeyAndBaseURL(cfg.CodexKey[i].APIKey, cfg.CodexKey[i].BaseURL)) {
				if cfg.CodexKey[i].Disabled == disabled {
					return false
				}
				cfg.CodexKey[i].Disabled = disabled
				return true
			}
		}
	case "vertex":
		idGen := synthesizer.NewStableIDGenerator()
		for i := range cfg.VertexCompatAPIKey {
			entry := &cfg.VertexCompatAPIKey[i]
			id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
			if matchesTargetID(id) || (targetID == "" && matchesKeyAndBaseURL(entry.APIKey, entry.BaseURL)) {
				if entry.Disabled == disabled {
					return false
				}
				entry.Disabled = disabled
				return true
			}
		}
	default:
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		proxyURL := strings.TrimSpace(auth.ProxyURL)
		idGen := synthesizer.NewStableIDGenerator()
		for i := range cfg.OpenAICompatibility {
			providerName := strings.ToLower(strings.TrimSpace(cfg.OpenAICompatibility[i].Name))
			if providerName == "" {
				providerName = "openai-compatibility"
			}
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			for j := range cfg.OpenAICompatibility[i].APIKeyEntries {
				entry := &cfg.OpenAICompatibility[i].APIKeyEntries[j]
				id, _ := idGen.Next(idKind, entry.APIKey, cfg.OpenAICompatibility[i].BaseURL, entry.ProxyURL)
				fallbackMatch := targetID == "" &&
					strings.EqualFold(cfg.OpenAICompatibility[i].Name, compatName) &&
					strings.TrimSpace(entry.APIKey) == apiKey &&
					strings.TrimSpace(entry.ProxyURL) == proxyURL
				if matchesTargetID(id) || fallbackMatch {
					if entry.Disabled == disabled {
						return false
					}
					entry.Disabled = disabled
					return true
				}
			}
		}
	}

	return false
}
