package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const configAPIKeyDisablePattern = "*"

func setConfigAPIKeyExcludedAll(models []string, disable bool) []string {
	if disable {
		for _, item := range models {
			if strings.TrimSpace(item) == configAPIKeyDisablePattern {
				return config.NormalizeExcludedModels(models)
			}
		}
		return config.NormalizeExcludedModels(append(append([]string(nil), models...), configAPIKeyDisablePattern))
	}
	filtered := make([]string, 0, len(models))
	for _, item := range models {
		if strings.TrimSpace(item) == configAPIKeyDisablePattern {
			continue
		}
		filtered = append(filtered, item)
	}
	return config.NormalizeExcludedModels(filtered)
}

func toggleConfigAPIKeyExcludedAll(cfg *config.Config, auth *coreauth.Auth, disable bool) (bool, error) {
	if cfg == nil || auth == nil || !coreauth.IsConfigCredentialAuth(auth) {
		return false, nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return false, fmt.Errorf("auth id is empty")
	}

	idGen := synthesizer.NewStableIDGenerator()

	for i := range cfg.GeminiKey {
		entry := &cfg.GeminiKey[i]
		var id string
		if strings.TrimSpace(entry.APIKey) != "" {
			id, _ = idGen.Next("gemini:apikey", entry.APIKey, entry.BaseURL)
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL)
			id, _ = idGen.Next("gemini:apikey", idParts...)
		}
		if id == authID {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		var id string
		if strings.TrimSpace(entry.APIKey) != "" {
			id, _ = idGen.Next("claude:apikey", entry.APIKey, entry.BaseURL)
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL)
			id, _ = idGen.Next("claude:apikey", idParts...)
		}
		if id == authID {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.CodexKey {
		entry := &cfg.CodexKey[i]
		var id string
		if strings.TrimSpace(entry.APIKey) != "" {
			id, _ = idGen.Next("codex:apikey", entry.APIKey, entry.BaseURL)
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL)
			id, _ = idGen.Next("codex:apikey", idParts...)
		}
		if id == authID {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Auth == nil || strings.TrimSpace(compat.Auth.Command) == "" {
			continue
		}
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
		idParts := append(synthesizer.CommandAuthIDParts(compat.Auth), strings.TrimSpace(compat.BaseURL), strings.TrimSpace(compat.ProxyURL))
		id, _ := idGen.Next(idKind, idParts...)
		if id == authID {
			// OpenAI-compatibility providers expose no per-credential excluded-models field.
			// A command-auth provider maps to a single synthesized credential, so toggling the
			// provider's Disabled flag is the persistent equivalent of disabling that credential.
			compat.Disabled = disable
			return true, nil
		}
	}
	for i := range cfg.VertexCompatAPIKey {
		entry := &cfg.VertexCompatAPIKey[i]
		var id string
		if strings.TrimSpace(entry.APIKey) != "" {
			id, _ = idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		} else if entry.Auth != nil && strings.TrimSpace(entry.Auth.Command) != "" {
			idParts := append(synthesizer.CommandAuthIDParts(entry.Auth), entry.BaseURL, strings.TrimSpace(entry.ProxyURL))
			id, _ = idGen.Next("vertex:apikey", idParts...)
		}
		if id == authID {
			entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
			return true, nil
		}
	}

	return false, nil
}
