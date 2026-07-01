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
	family := sourceProtocolFamily(opts.SourceFormat)
	if family == "" {
		return nil, false
	}
	normalized := normalizeProviderKeys(providers)
	preferred := make([]string, 0, len(normalized))
	fallback := make([]string, 0, len(normalized))
	for _, provider := range normalized {
		if providerProtocolFamily(provider) == family {
			preferred = append(preferred, provider)
		} else {
			fallback = append(fallback, provider)
		}
	}
	if mode == protocolAffinityStrict {
		return [][]string{preferred}, true
	}
	if len(preferred) == 0 {
		return nil, false
	}
	if len(fallback) == 0 {
		return [][]string{preferred}, true
	}
	return [][]string{preferred, fallback}, true
}

func sourceProtocolFamily(format sdktranslator.Format) string {
	switch strings.ToLower(strings.TrimSpace(format.String())) {
	case "openai", "openai-chat", "chat-completions", "openai-response", "openai-responses", "responses", "openai-image", "openai-video":
		return "openai"
	case "codex":
		return "openai"
	case "claude", "anthropic":
		return "claude"
	case "gemini", "google", "vertex", "aistudio":
		return "gemini"
	case "antigravity":
		return "antigravity"
	default:
		return ""
	}
}

func providerProtocolFamily(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch {
	case provider == "openai", provider == "openai-compatibility", strings.HasPrefix(provider, "openai-compatible-"):
		return "openai"
	case provider == "codex", provider == "xai", provider == "kimi":
		return "openai"
	case provider == "claude":
		return "claude"
	case provider == "gemini", provider == "vertex", provider == "aistudio":
		return "gemini"
	case provider == "antigravity":
		return "antigravity"
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
