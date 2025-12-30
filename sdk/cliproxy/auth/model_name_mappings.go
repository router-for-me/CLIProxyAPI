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
			from := strings.TrimSpace(entry.From)
			to := strings.TrimSpace(entry.To)
			if from == "" || to == "" {
				continue
			}
			if strings.EqualFold(from, to) {
				continue
			}
			aliasKey := strings.ToLower(to)
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = from
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

// SetGlobalModelNameMappings updates the global model name mapping table used during execution.
// The mapping is applied per-auth channel to resolve the upstream model name while keeping the
// client-visible model name unchanged for translation/response formatting.
func (m *Manager) SetGlobalModelNameMappings(mappings map[string][]internalconfig.ModelNameMapping) {
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

func (m *Manager) applyGlobalModelNameMappingMetadata(auth *Auth, requestedModel string, metadata map[string]any) map[string]any {
	original := m.resolveGlobalUpstreamModelForAuth(auth, requestedModel)
	if original == "" {
		return metadata
	}
	if metadata != nil {
		if v, ok := metadata[util.ModelMappingOriginalModelMetadataKey]; ok {
			if s, okStr := v.(string); okStr && strings.EqualFold(s, original) {
				return metadata
			}
		}
	}
	out := make(map[string]any, 1)
	if len(metadata) > 0 {
		out = make(map[string]any, len(metadata)+1)
		for k, v := range metadata {
			out[k] = v
		}
	}
	out[util.ModelMappingOriginalModelMetadataKey] = original
	return out
}

func (m *Manager) resolveGlobalUpstreamModelForAuth(auth *Auth, requestedModel string) string {
	if m == nil || auth == nil {
		return ""
	}
	channel := globalModelMappingChannelForAuth(auth)
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

func globalModelMappingChannelForAuth(auth *Auth) string {
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
	return globalModelMappingChannel(provider, authKind)
}

func globalModelMappingChannel(provider, authKind string) string {
	switch provider {
	case "gemini":
		if authKind == "apikey" {
			return "apikey-gemini"
		}
		return "gemini"
	case "codex":
		if authKind == "apikey" {
			return ""
		}
		return "codex"
	case "claude":
		if authKind == "apikey" {
			return ""
		}
		return "claude"
	case "vertex":
		if authKind == "apikey" {
			return ""
		}
		return "vertex"
	case "antigravity", "qwen", "iflow":
		return provider
	default:
		return ""
	}
}
