package management

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type monitorSourceRef struct {
	EntityID        string `json:"entity_id"`
	EntityKind      string `json:"entity_kind"`
	Kind            string `json:"kind"`
	ProviderType    string `json:"provider_type"`
	AuthIndex       string `json:"auth_index,omitempty"`
	ConfigIndex     *int   `json:"config_index,omitempty"`
	ConfigPath      string `json:"config_path,omitempty"`
	CanonicalSource string `json:"canonical_source"`
	DisplayName     string `json:"display_name"`
	DisplaySecret   string `json:"display_secret"`
	Disabled        bool   `json:"disabled"`
	CanCopy         bool   `json:"can_copy"`
	CanEdit         bool   `json:"can_edit"`
	CanToggle       bool   `json:"can_toggle"`
	CopyValue       string `json:"copy_value,omitempty"`
	EditPath        string `json:"edit_path,omitempty"`
	AuthFileName    string `json:"auth_file_name,omitempty"`
}

type monitorSourceResolver struct {
	authByIndex      map[string]*coreauth.Auth
	authBySource     map[string]monitorSourceRef
	providerBySource map[string]monitorSourceRef
}

func newMonitorSourceResolver(cfg *config.Config, authManager *coreauth.Manager) *monitorSourceResolver {
	resolver := &monitorSourceResolver{
		authByIndex:      make(map[string]*coreauth.Auth),
		authBySource:     make(map[string]monitorSourceRef),
		providerBySource: make(map[string]monitorSourceRef),
	}

	if authManager != nil {
		for _, auth := range authManager.List() {
			if auth == nil {
				continue
			}
			idx := strings.TrimSpace(auth.EnsureIndex())
			if idx != "" {
				resolver.authByIndex[idx] = auth
			}
			ref := buildAuthMonitorSourceRef(auth)
			for _, candidate := range collectAuthSourceCandidates(auth) {
				if candidate == "" {
					continue
				}
				if _, exists := resolver.authBySource[candidate]; !exists {
					resolver.authBySource[candidate] = ref
				}
			}
		}
	}

	if cfg != nil {
		for index, entry := range cfg.OpenAICompatibility {
			providerDisabled := len(entry.APIKeyEntries) > 0
			for _, keyEntry := range entry.APIKeyEntries {
				if !keyEntry.Disabled {
					providerDisabled = false
					break
				}
			}
			providerRef := buildProviderMonitorSourceRef("openai", index, providerDisplayName("openai"), entry.Name, providerDisabled)
			providerRef.CanCopy = false
			providerRef.CopyValue = ""
			for _, candidate := range collectOpenAIProviderCandidates(entry.Name, entry.Prefix) {
				if candidate == "" {
					continue
				}
				if _, exists := resolver.providerBySource[candidate]; !exists {
					resolver.providerBySource[candidate] = providerRef
				}
			}
			for _, keyEntry := range entry.APIKeyEntries {
				apiKey := strings.TrimSpace(keyEntry.APIKey)
				if apiKey == "" {
					continue
				}
				ref := buildProviderMonitorSourceRef("openai", index, providerDisplayName("openai"), apiKey, keyEntry.Disabled)
				for _, candidate := range collectProviderSourceCandidates(apiKey, entry.Prefix) {
					if candidate == "" {
						continue
					}
					if _, exists := resolver.providerBySource[candidate]; !exists {
						resolver.providerBySource[candidate] = ref
					}
				}
			}
		}
		for index, entry := range cfg.GeminiKey {
			ref := buildProviderMonitorSourceRef("gemini", index, providerDisplayName("gemini"), entry.APIKey, entry.Disabled || hasDisableAllModelsRule(entry.ExcludedModels))
			for _, candidate := range collectProviderSourceCandidates(entry.APIKey, entry.Prefix) {
				if candidate == "" {
					continue
				}
				if _, exists := resolver.providerBySource[candidate]; !exists {
					resolver.providerBySource[candidate] = ref
				}
			}
		}
		for index, entry := range cfg.ClaudeKey {
			ref := buildProviderMonitorSourceRef("claude", index, providerDisplayName("claude"), entry.APIKey, entry.Disabled || hasDisableAllModelsRule(entry.ExcludedModels))
			for _, candidate := range collectProviderSourceCandidates(entry.APIKey, entry.Prefix) {
				if candidate == "" {
					continue
				}
				if _, exists := resolver.providerBySource[candidate]; !exists {
					resolver.providerBySource[candidate] = ref
				}
			}
		}
		for index, entry := range cfg.CodexKey {
			ref := buildProviderMonitorSourceRef("codex", index, providerDisplayName("codex"), entry.APIKey, entry.Disabled || hasDisableAllModelsRule(entry.ExcludedModels))
			for _, candidate := range collectProviderSourceCandidates(entry.APIKey, entry.Prefix) {
				if candidate == "" {
					continue
				}
				if _, exists := resolver.providerBySource[candidate]; !exists {
					resolver.providerBySource[candidate] = ref
				}
			}
		}
		for index, entry := range cfg.VertexCompatAPIKey {
			ref := buildProviderMonitorSourceRef("vertex", index, providerDisplayName("vertex"), entry.APIKey, entry.Disabled || hasDisableAllModelsRule(entry.ExcludedModels))
			for _, candidate := range collectProviderSourceCandidates(entry.APIKey, entry.Prefix) {
				if candidate == "" {
					continue
				}
				if _, exists := resolver.providerBySource[candidate]; !exists {
					resolver.providerBySource[candidate] = ref
				}
			}
		}
	}

	return resolver
}

func (r *monitorSourceResolver) Resolve(source, authIndex string) monitorSourceRef {
	if r == nil {
		return monitorUnknownSourceRef(source)
	}

	authIndexKey := strings.TrimSpace(authIndex)
	if authIndexKey != "" {
		if auth, ok := r.authByIndex[authIndexKey]; ok && auth != nil {
			return buildAuthMonitorSourceRef(auth)
		}
	}

	sourceKey := strings.TrimSpace(source)
	if sourceKey != "" {
		if ref, ok := r.authBySource[sourceKey]; ok {
			return ref
		}
		if ref, ok := r.providerBySource[sourceKey]; ok {
			return ref
		}
	}

	return monitorUnknownSourceRef(sourceKey)
}

func (r *monitorSourceResolver) FilterKeysForProviderType(providerType string) ([]string, []string) {
	if r == nil {
		return nil, nil
	}

	normalizedType := normalizeMonitorProviderType(providerType)
	if normalizedType == "" {
		return nil, nil
	}

	sourceSet := make(map[string]struct{})
	authIndexSet := make(map[string]struct{})

	for authIndex, auth := range r.authByIndex {
		if auth == nil {
			continue
		}
		if normalizeMonitorProviderType(auth.Provider) != normalizedType {
			continue
		}
		trimmed := strings.TrimSpace(authIndex)
		if trimmed != "" {
			authIndexSet[trimmed] = struct{}{}
		}
	}

	for source, ref := range r.authBySource {
		if normalizeMonitorProviderType(ref.ProviderType) != normalizedType {
			continue
		}
		trimmed := strings.TrimSpace(source)
		if trimmed != "" {
			sourceSet[trimmed] = struct{}{}
		}
	}

	for source, ref := range r.providerBySource {
		if normalizeMonitorProviderType(ref.ProviderType) != normalizedType {
			continue
		}
		trimmed := strings.TrimSpace(source)
		if trimmed != "" {
			sourceSet[trimmed] = struct{}{}
		}
	}

	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}

	authIndices := make([]string, 0, len(authIndexSet))
	for authIndex := range authIndexSet {
		authIndices = append(authIndices, authIndex)
	}

	return sources, authIndices
}

func buildAuthMonitorSourceRef(auth *coreauth.Auth) monitorSourceRef {
	if auth == nil {
		return monitorUnknownSourceRef("")
	}
	authIndex := strings.TrimSpace(auth.EnsureIndex())
	providerType := normalizeMonitorProviderType(auth.Provider)
	fileName := strings.TrimSpace(auth.FileName)
	if fileName == "" {
		fileName = auth.ID
	}
	displaySecret := monitorDisplaySecret(authPrimarySource(auth))
	if displaySecret == "" {
		displaySecret = monitorDisplaySecret(fileName)
	}
	copyValue := strings.TrimSpace(auth.Attributes["api_key"])
	canCopy := copyValue != ""
	return monitorSourceRef{
		EntityID:        "auth:" + authIndex,
		EntityKind:      "auth-file",
		Kind:            "auth-file",
		ProviderType:    providerType,
		AuthIndex:       authIndex,
		ConfigPath:      "/auth-files",
		CanonicalSource: strings.TrimSpace(authPrimarySource(auth)),
		DisplayName:     providerDisplayName(providerType),
		DisplaySecret:   displaySecret,
		Disabled:        auth.Disabled || auth.Status == coreauth.StatusDisabled,
		CanCopy:         canCopy,
		CanEdit:         true,
		CanToggle:       true,
		CopyValue:       copyValue,
		EditPath:        "/auth-files",
		AuthFileName:    fileName,
	}
}

func buildProviderMonitorSourceRef(providerType string, index int, displayName, canonicalSource string, disabled bool) monitorSourceRef {
	configIndex := index
	editPath := "/ai-providers/" + providerType + "/" + strconv.Itoa(index)
	copyValue := strings.TrimSpace(canonicalSource)
	return monitorSourceRef{
		EntityID:        "provider:" + providerType + ":" + strconv.Itoa(index),
		EntityKind:      "provider-config",
		Kind:            providerType,
		ProviderType:    providerType,
		ConfigIndex:     &configIndex,
		ConfigPath:      "/ai-providers/" + providerType,
		CanonicalSource: strings.TrimSpace(canonicalSource),
		DisplayName:     displayName,
		DisplaySecret:   monitorDisplaySecret(canonicalSource),
		Disabled:        disabled,
		CanCopy:         true,
		CanEdit:         true,
		CanToggle:       true,
		CopyValue:       copyValue,
		EditPath:        editPath,
	}
}

func monitorUnknownSourceRef(source string) monitorSourceRef {
	trimmed := strings.TrimSpace(source)
	return monitorSourceRef{
		EntityID:        "unknown:" + trimmed,
		EntityKind:      "unknown",
		Kind:            "unknown",
		ProviderType:    "unknown",
		CanonicalSource: trimmed,
		DisplayName:     "",
		DisplaySecret:   monitorDisplaySecret(trimmed),
		Disabled:        false,
		CanCopy:         false,
		CanEdit:         false,
		CanToggle:       false,
	}
}

func collectProviderSourceCandidates(apiKey, prefix string) []string {
	set := map[string]struct{}{}
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		set[trimmed] = struct{}{}
	}
	add(apiKey)
	add(util.HideAPIKey(strings.TrimSpace(apiKey)))
	add(prefix)
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	return values
}

func collectOpenAIProviderCandidates(name, prefix string) []string {
	set := map[string]struct{}{}
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		set[trimmed] = struct{}{}
	}
	add(name)
	add(prefix)
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	return values
}

func collectAuthSourceCandidates(auth *coreauth.Auth) []string {
	set := map[string]struct{}{}
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		set[trimmed] = struct{}{}
	}
	add(authPrimarySource(auth))
	if _, value := auth.AccountInfo(); value != "" {
		add(value)
	}
	add(authEmail(auth))
	add(auth.ID)
	add(strings.TrimSpace(auth.FileName))
	add(filepath.Base(strings.TrimSpace(auth.FileName)))
	if auth != nil && auth.Attributes != nil {
		add(auth.Attributes["api_key"])
		add(util.HideAPIKey(strings.TrimSpace(auth.Attributes["api_key"])))
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	return values
}

func authPrimarySource(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.TrimSpace(auth.Provider)
	if strings.EqualFold(provider, "gemini-cli") {
		if id := strings.TrimSpace(auth.ID); id != "" {
			return id
		}
	}
	if strings.EqualFold(provider, "vertex") && auth.Metadata != nil {
		if projectID, ok := auth.Metadata["project_id"].(string); ok {
			if trimmed := strings.TrimSpace(projectID); trimmed != "" {
				return trimmed
			}
		}
		if project, ok := auth.Metadata["project"].(string); ok {
			if trimmed := strings.TrimSpace(project); trimmed != "" {
				return trimmed
			}
		}
	}
	if _, value := auth.AccountInfo(); value != "" {
		return strings.TrimSpace(value)
	}
	if auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok {
			if trimmed := strings.TrimSpace(email); trimmed != "" {
				return trimmed
			}
		}
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
	}
	return ""
}

func monitorDisplaySecret(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "@") {
		parts := strings.SplitN(trimmed, "@", 2)
		local := parts[0]
		domain := parts[1]
		if len(local) <= 4 {
			return local + "***@" + domain
		}
		return local[:4] + "***@" + domain
	}
	return util.HideAPIKey(trimmed)
}

func providerDisplayName(providerType string) string {
	switch normalizeMonitorProviderType(providerType) {
	case "openai":
		return "OpenAI"
	case "gemini":
		return "Gemini"
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "vertex":
		return "Vertex"
	case "aistudio":
		return "AI Studio"
	case "qwen":
		return "Qwen"
	case "iflow":
		return "iFlow"
	case "antigravity":
		return "Antigravity"
	case "kimi":
		return "Kimi"
	default:
		return strings.TrimSpace(providerType)
	}
}

func normalizeMonitorProviderType(providerType string) string {
	lower := strings.ToLower(strings.TrimSpace(providerType))
	switch lower {
	case "gemini-cli":
		return "gemini"
	case "openai-compatibility":
		return "openai"
	default:
		return lower
	}
}

func hasDisableAllModelsRule(models []string) bool {
	for _, model := range models {
		if strings.TrimSpace(model) == "*" {
			return true
		}
	}
	return false
}
