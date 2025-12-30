package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

type modelNameMappingTable struct {
	// reverse maps channel -> alias (lower) -> original upstream model name.
	reverse map[string]map[string]string
}

func compileModelNameMappingTable(mappings map[string][]internalconfig.ModelNameMapping) *modelNameMappingTable {
	if len(mappings) == 0 {
		return &modelNameMappingTable{}
	}
	out := &modelNameMappingTable{
		reverse: make(map[string]map[string]string, len(mappings)),
	}
	for rawChannel, entries := range mappings {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		rev := make(map[string]string, len(entries))
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = name
		}
		if len(rev) > 0 {
			out.reverse[channel] = rev
		}
	}
	if len(out.reverse) == 0 {
		out.reverse = nil
	}
	return out
}

// SetOAuthModelMappings updates the OAuth model name mapping table used during execution.
// The mapping is applied per-auth channel to resolve the upstream model name while keeping the
// client-visible model name unchanged for translation/response formatting.
func (m *Manager) SetOAuthModelMappings(mappings map[string][]internalconfig.ModelNameMapping) {
	if m == nil {
		return
	}
	table := compileModelNameMappingTable(mappings)
	// atomic.Value requires non-nil store values.
	if table == nil {
		table = &modelNameMappingTable{}
	}
	m.modelNameMappings.Store(table)
}

// applyOAuthModelMapping resolves the upstream model from OAuth model mappings
// and returns the resolved model along with updated metadata. If a mapping exists,
// the returned model is the upstream model and metadata contains the original
// requested model for response translation.
func (m *Manager) applyOAuthModelMapping(auth *Auth, requestedModel string, metadata map[string]any) (string, map[string]any) {
	upstreamModel := m.resolveOAuthUpstreamModel(auth, requestedModel)
	if upstreamModel == "" {
		return requestedModel, metadata
	}
	out := make(map[string]any, 1)
	if len(metadata) > 0 {
		out = make(map[string]any, len(metadata)+1)
		for k, v := range metadata {
			out[k] = v
		}
	}
	out[util.ModelMappingOriginalModelMetadataKey] = upstreamModel
	return upstreamModel, out
}

func (m *Manager) resolveOAuthUpstreamModel(auth *Auth, requestedModel string) string {
	if m == nil || auth == nil {
		return ""
	}
	channel := modelMappingChannel(auth)
	if channel == "" {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(requestedModel))
	if key == "" {
		return ""
	}
	raw := m.modelNameMappings.Load()
	table, _ := raw.(*modelNameMappingTable)
	if table == nil || table.reverse == nil {
		return ""
	}
	rev := table.reverse[channel]
	if rev == nil {
		return ""
	}
	original := strings.TrimSpace(rev[key])
	if original == "" || strings.EqualFold(original, requestedModel) {
		return ""
	}
	return original
}

// modelMappingChannel extracts the OAuth model mapping channel from an Auth object.
// It determines the provider and auth kind from the Auth's attributes and delegates
// to OAuthModelMappingChannel for the actual channel resolution.
func modelMappingChannel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := ""
	if auth.Attributes != nil {
		authKind = strings.ToLower(strings.TrimSpace(auth.Attributes["auth_kind"]))
	}
	if authKind == "" {
		if kind, _ := auth.AccountInfo(); strings.EqualFold(kind, "api_key") {
			authKind = "apikey"
		}
	}
	return OAuthModelMappingChannel(provider, authKind)
}

// OAuthModelMappingChannel returns the OAuth model mapping channel name for a given provider
// and auth kind. Returns empty string if the provider/authKind combination doesn't support
// OAuth model mappings (e.g., API key authentication).
//
// Supported channels: gemini-cli, vertex, aistudio, antigravity, claude, codex, qwen, iflow.
func OAuthModelMappingChannel(provider, authKind string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authKind = strings.ToLower(strings.TrimSpace(authKind))
	switch provider {
	case "gemini":
		// gemini provider uses gemini-api-key config, not oauth-model-mappings.
		// OAuth-based gemini auth is converted to "gemini-cli" by the synthesizer.
		return ""
	case "vertex":
		if authKind == "apikey" {
			return ""
		}
		return "vertex"
	case "claude":
		if authKind == "apikey" {
			return ""
		}
		return "claude"
	case "codex":
		if authKind == "apikey" {
			return ""
		}
		return "codex"
	case "gemini-cli", "aistudio", "antigravity", "qwen", "iflow":
		return provider
	default:
		return ""
	}
}
