package auth

import (
	"errors"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	protocolAffinityPrefer = "prefer"
	protocolAffinityStrict = "strict"
	protocolAffinityOff    = "off"
)

const (
	protocolFamilyOpenAIChat      = "openai-chat"
	protocolFamilyOpenAIResponses = "openai-responses"
	protocolFamilyClaude          = "claude"
	protocolFamilyGemini          = "gemini"
	protocolFamilyAntigravity     = "antigravity"
)

func (m *Manager) protocolAffinityMode() string {
	cfg := &internalconfig.Config{}
	if m != nil {
		if loaded, ok := m.runtimeConfig.Load().(*internalconfig.Config); ok && loaded != nil {
			cfg = loaded
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Routing.ProtocolAffinity)) {
	case "", protocolAffinityPrefer:
		return protocolAffinityPrefer
	case protocolAffinityStrict:
		return protocolAffinityStrict
	case protocolAffinityOff, "none", "disabled", "false":
		return protocolAffinityOff
	default:
		return protocolAffinityPrefer
	}
}

func (m *Manager) protocolAffinityProviderBatches(providers []string, opts cliproxyexecutor.Options) ([][]string, bool) {
	mode := m.protocolAffinityMode()
	if mode == protocolAffinityOff {
		return nil, false
	}
	preferences := sourceProtocolPreference(opts.SourceFormat)
	if len(preferences) == 0 {
		return nil, false
	}
	normalized := normalizeProviderKeys(providers)
	providerFamilies := make(map[string]string, len(normalized))
	for _, provider := range normalized {
		providerFamilies[provider] = providerProtocolFamily(provider)
	}
	used := make(map[string]struct{}, len(normalized))
	batchForFamily := func(family string) []string {
		batch := make([]string, 0, len(normalized))
		for _, provider := range normalized {
			if _, ok := used[provider]; ok {
				continue
			}
			if providerFamilies[provider] != family {
				continue
			}
			used[provider] = struct{}{}
			batch = append(batch, provider)
		}
		return batch
	}
	if mode == protocolAffinityStrict {
		return [][]string{batchForFamily(preferences[0])}, true
	}

	batches := make([][]string, 0, len(preferences)+1)
	for _, family := range preferences {
		if batch := batchForFamily(family); len(batch) > 0 {
			batches = append(batches, batch)
		}
	}
	fallback := make([]string, 0, len(normalized))
	for _, provider := range normalized {
		if _, ok := used[provider]; ok {
			continue
		}
		fallback = append(fallback, provider)
	}
	if len(fallback) > 0 {
		batches = append(batches, fallback)
	}
	if len(batches) == 0 {
		return nil, false
	}
	return batches, true
}

func sourceProtocolPreference(format sdktranslator.Format) []string {
	switch strings.ToLower(strings.TrimSpace(format.String())) {
	case "openai", "openai-chat", "chat-completions", "completions", "completion", "openai-image", "openai-video":
		return []string{protocolFamilyOpenAIChat, protocolFamilyOpenAIResponses}
	case "openai-response", "openai-responses", "responses", "codex":
		return []string{protocolFamilyOpenAIResponses, protocolFamilyOpenAIChat}
	case "claude", "anthropic":
		return []string{protocolFamilyClaude}
	case "gemini", "google", "vertex", "aistudio":
		return []string{protocolFamilyGemini}
	case "antigravity":
		return []string{protocolFamilyAntigravity}
	default:
		return nil
	}
}

func providerProtocolFamily(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch {
	case provider == "openai", provider == "openai-compatibility", strings.HasPrefix(provider, "openai-compatible-"):
		return protocolFamilyOpenAIChat
	case provider == "kimi":
		return protocolFamilyOpenAIChat
	case provider == "codex", provider == "xai":
		return protocolFamilyOpenAIResponses
	case provider == "claude":
		return protocolFamilyClaude
	case provider == "gemini", provider == "vertex", provider == "aistudio":
		return protocolFamilyGemini
	case provider == "antigravity":
		return protocolFamilyAntigravity
	default:
		return ""
	}
}

func shouldFallbackProtocolAffinityPick(err error) bool {
	if err == nil {
		return false
	}
	var modelCooldown *modelCooldownError
	if errors.As(err, &modelCooldown) {
		return true
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(authErr.Code)) {
	case "auth_not_found", "auth_unavailable", "provider_not_found":
		return true
	default:
		return false
	}
}
