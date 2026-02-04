package diff

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// BuildConfigChangeDetails computes a redacted, human-readable list of config changes.
// Secrets are never printed; only structural or non-sensitive fields are surfaced.
func BuildConfigChangeDetails(oldCfg, newCfg *config.Config) []string {
	changes := make([]string, 0, 16)
	if oldCfg == nil || newCfg == nil {
		return changes
	}

	// Simple scalars
	if oldCfg.Port != newCfg.Port {
		changes = append(changes, fmt.Sprintf("port: %d -> %d", oldCfg.Port, newCfg.Port))
	}
	if oldCfg.AuthDir != newCfg.AuthDir {
		changes = append(changes, fmt.Sprintf("auth-dir: %s -> %s", oldCfg.AuthDir, newCfg.AuthDir))
	}
	if oldCfg.Debug != newCfg.Debug {
		changes = append(changes, fmt.Sprintf("debug: %t -> %t", oldCfg.Debug, newCfg.Debug))
	}
	if oldCfg.Pprof.Enable != newCfg.Pprof.Enable {
		changes = append(changes, fmt.Sprintf("pprof.enable: %t -> %t", oldCfg.Pprof.Enable, newCfg.Pprof.Enable))
	}
	if strings.TrimSpace(oldCfg.Pprof.Addr) != strings.TrimSpace(newCfg.Pprof.Addr) {
		changes = append(changes, fmt.Sprintf("pprof.addr: %s -> %s", strings.TrimSpace(oldCfg.Pprof.Addr), strings.TrimSpace(newCfg.Pprof.Addr)))
	}
	if oldCfg.LoggingToFile != newCfg.LoggingToFile {
		changes = append(changes, fmt.Sprintf("logging-to-file: %t -> %t", oldCfg.LoggingToFile, newCfg.LoggingToFile))
	}
	if oldCfg.UsageStatisticsEnabled != newCfg.UsageStatisticsEnabled {
		changes = append(changes, fmt.Sprintf("usage-statistics-enabled: %t -> %t", oldCfg.UsageStatisticsEnabled, newCfg.UsageStatisticsEnabled))
	}
	if oldCfg.DisableCooling != newCfg.DisableCooling {
		changes = append(changes, fmt.Sprintf("disable-cooling: %t -> %t", oldCfg.DisableCooling, newCfg.DisableCooling))
	}
	if oldCfg.RequestLog != newCfg.RequestLog {
		changes = append(changes, fmt.Sprintf("request-log: %t -> %t", oldCfg.RequestLog, newCfg.RequestLog))
	}
	if oldCfg.LogsMaxTotalSizeMB != newCfg.LogsMaxTotalSizeMB {
		changes = append(changes, fmt.Sprintf("logs-max-total-size-mb: %d -> %d", oldCfg.LogsMaxTotalSizeMB, newCfg.LogsMaxTotalSizeMB))
	}
	if oldCfg.ErrorLogsMaxFiles != newCfg.ErrorLogsMaxFiles {
		changes = append(changes, fmt.Sprintf("error-logs-max-files: %d -> %d", oldCfg.ErrorLogsMaxFiles, newCfg.ErrorLogsMaxFiles))
	}
	if oldCfg.RequestRetry != newCfg.RequestRetry {
		changes = append(changes, fmt.Sprintf("request-retry: %d -> %d", oldCfg.RequestRetry, newCfg.RequestRetry))
	}
	if oldCfg.MaxRetryInterval != newCfg.MaxRetryInterval {
		changes = append(changes, fmt.Sprintf("max-retry-interval: %d -> %d", oldCfg.MaxRetryInterval, newCfg.MaxRetryInterval))
	}
	if oldCfg.ProxyURL != newCfg.ProxyURL {
		changes = append(changes, fmt.Sprintf("proxy-url: %s -> %s", formatProxyURL(oldCfg.ProxyURL), formatProxyURL(newCfg.ProxyURL)))
	}
	if oldCfg.WebsocketAuth != newCfg.WebsocketAuth {
		changes = append(changes, fmt.Sprintf("ws-auth: %t -> %t", oldCfg.WebsocketAuth, newCfg.WebsocketAuth))
	}
	if oldCfg.ForceModelPrefix != newCfg.ForceModelPrefix {
		changes = append(changes, fmt.Sprintf("force-model-prefix: %t -> %t", oldCfg.ForceModelPrefix, newCfg.ForceModelPrefix))
	}
	if oldCfg.NonStreamKeepAliveInterval != newCfg.NonStreamKeepAliveInterval {
		changes = append(changes, fmt.Sprintf("nonstream-keepalive-interval: %d -> %d", oldCfg.NonStreamKeepAliveInterval, newCfg.NonStreamKeepAliveInterval))
	}

	// Quota-exceeded behavior
	if oldCfg.QuotaExceeded.SwitchProject != newCfg.QuotaExceeded.SwitchProject {
		changes = append(changes, fmt.Sprintf("quota-exceeded.switch-project: %t -> %t", oldCfg.QuotaExceeded.SwitchProject, newCfg.QuotaExceeded.SwitchProject))
	}
	if oldCfg.QuotaExceeded.SwitchPreviewModel != newCfg.QuotaExceeded.SwitchPreviewModel {
		changes = append(changes, fmt.Sprintf("quota-exceeded.switch-preview-model: %t -> %t", oldCfg.QuotaExceeded.SwitchPreviewModel, newCfg.QuotaExceeded.SwitchPreviewModel))
	}

	if oldCfg.Routing.Strategy != newCfg.Routing.Strategy {
		changes = append(changes, fmt.Sprintf("routing.strategy: %s -> %s", oldCfg.Routing.Strategy, newCfg.Routing.Strategy))
	}

	// API keys (redacted) and counts
	if len(oldCfg.APIKeys) != len(newCfg.APIKeys) {
		changes = append(changes, fmt.Sprintf("api-keys count: %d -> %d", len(oldCfg.APIKeys), len(newCfg.APIKeys)))
	} else if !reflect.DeepEqual(trimStrings(oldCfg.APIKeys), trimStrings(newCfg.APIKeys)) {
		changes = append(changes, "api-keys: values updated (count unchanged, redacted)")
	}
	if len(oldCfg.GeminiKey) != len(newCfg.GeminiKey) {
		changes = append(changes, fmt.Sprintf("gemini-api-key count: %d -> %d", len(oldCfg.GeminiKey), len(newCfg.GeminiKey)))
	} else {
		for i := range oldCfg.GeminiKey {
			o := oldCfg.GeminiKey[i]
			n := newCfg.GeminiKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("gemini[%d].base-url: %s -> %s", i, strings.TrimSpace(o.BaseURL), strings.TrimSpace(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("gemini[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("gemini[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("gemini[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("gemini[%d].headers: updated", i))
			}
			oldModels := SummarizeGeminiModels(o.Models)
			newModels := SummarizeGeminiModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("gemini[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("gemini[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
		}
	}

	// Claude keys (do not print key material)
	if len(oldCfg.ClaudeKey) != len(newCfg.ClaudeKey) {
		changes = append(changes, fmt.Sprintf("claude-api-key count: %d -> %d", len(oldCfg.ClaudeKey), len(newCfg.ClaudeKey)))
	} else {
		for i := range oldCfg.ClaudeKey {
			o := oldCfg.ClaudeKey[i]
			n := newCfg.ClaudeKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("claude[%d].base-url: %s -> %s", i, strings.TrimSpace(o.BaseURL), strings.TrimSpace(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("claude[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("claude[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("claude[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("claude[%d].headers: updated", i))
			}
			oldModels := SummarizeClaudeModels(o.Models)
			newModels := SummarizeClaudeModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("claude[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("claude[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
			if o.Cloak != nil && n.Cloak != nil {
				if strings.TrimSpace(o.Cloak.Mode) != strings.TrimSpace(n.Cloak.Mode) {
					changes = append(changes, fmt.Sprintf("claude[%d].cloak.mode: %s -> %s", i, o.Cloak.Mode, n.Cloak.Mode))
				}
				if o.Cloak.StrictMode != n.Cloak.StrictMode {
					changes = append(changes, fmt.Sprintf("claude[%d].cloak.strict-mode: %t -> %t", i, o.Cloak.StrictMode, n.Cloak.StrictMode))
				}
				if len(o.Cloak.SensitiveWords) != len(n.Cloak.SensitiveWords) {
					changes = append(changes, fmt.Sprintf("claude[%d].cloak.sensitive-words: %d -> %d", i, len(o.Cloak.SensitiveWords), len(n.Cloak.SensitiveWords)))
				}
			}
		}
	}

	// Codex keys (do not print key material)
	if len(oldCfg.CodexKey) != len(newCfg.CodexKey) {
		changes = append(changes, fmt.Sprintf("codex-api-key count: %d -> %d", len(oldCfg.CodexKey), len(newCfg.CodexKey)))
	} else {
		for i := range oldCfg.CodexKey {
			o := oldCfg.CodexKey[i]
			n := newCfg.CodexKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("codex[%d].base-url: %s -> %s", i, strings.TrimSpace(o.BaseURL), strings.TrimSpace(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("codex[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("codex[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("codex[%d].api-key: updated", i))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("codex[%d].headers: updated", i))
			}
			oldModels := SummarizeCodexModels(o.Models)
			newModels := SummarizeCodexModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("codex[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			oldExcluded := SummarizeExcludedModels(o.ExcludedModels)
			newExcluded := SummarizeExcludedModels(n.ExcludedModels)
			if oldExcluded.hash != newExcluded.hash {
				changes = append(changes, fmt.Sprintf("codex[%d].excluded-models: updated (%d -> %d entries)", i, oldExcluded.count, newExcluded.count))
			}
		}
	}

	// AmpCode settings (redacted where needed)
	oldAmpURL := strings.TrimSpace(oldCfg.AmpCode.UpstreamURL)
	newAmpURL := strings.TrimSpace(newCfg.AmpCode.UpstreamURL)
	if oldAmpURL != newAmpURL {
		changes = append(changes, fmt.Sprintf("ampcode.upstream-url: %s -> %s", oldAmpURL, newAmpURL))
	}
	oldAmpKey := strings.TrimSpace(oldCfg.AmpCode.UpstreamAPIKey)
	newAmpKey := strings.TrimSpace(newCfg.AmpCode.UpstreamAPIKey)
	switch {
	case oldAmpKey == "" && newAmpKey != "":
		changes = append(changes, "ampcode.upstream-api-key: added")
	case oldAmpKey != "" && newAmpKey == "":
		changes = append(changes, "ampcode.upstream-api-key: removed")
	case oldAmpKey != newAmpKey:
		changes = append(changes, "ampcode.upstream-api-key: updated")
	}
	if oldCfg.AmpCode.RestrictManagementToLocalhost != newCfg.AmpCode.RestrictManagementToLocalhost {
		changes = append(changes, fmt.Sprintf("ampcode.restrict-management-to-localhost: %t -> %t", oldCfg.AmpCode.RestrictManagementToLocalhost, newCfg.AmpCode.RestrictManagementToLocalhost))
	}
	oldMappings := SummarizeAmpModelMappings(oldCfg.AmpCode.ModelMappings)
	newMappings := SummarizeAmpModelMappings(newCfg.AmpCode.ModelMappings)
	if oldMappings.hash != newMappings.hash {
		changes = append(changes, fmt.Sprintf("ampcode.model-mappings: updated (%d -> %d entries)", oldMappings.count, newMappings.count))
	}
	if oldCfg.AmpCode.ForceModelMappings != newCfg.AmpCode.ForceModelMappings {
		changes = append(changes, fmt.Sprintf("ampcode.force-model-mappings: %t -> %t", oldCfg.AmpCode.ForceModelMappings, newCfg.AmpCode.ForceModelMappings))
	}
	oldUpstreamAPIKeysCount := len(oldCfg.AmpCode.UpstreamAPIKeys)
	newUpstreamAPIKeysCount := len(newCfg.AmpCode.UpstreamAPIKeys)
	if !equalUpstreamAPIKeys(oldCfg.AmpCode.UpstreamAPIKeys, newCfg.AmpCode.UpstreamAPIKeys) {
		changes = append(changes, fmt.Sprintf("ampcode.upstream-api-keys: updated (%d -> %d entries)", oldUpstreamAPIKeysCount, newUpstreamAPIKeysCount))
	}

	if entries, _ := DiffOAuthExcludedModelChanges(oldCfg.OAuthExcludedModels, newCfg.OAuthExcludedModels); len(entries) > 0 {
		changes = append(changes, entries...)
	}
	if entries, _ := DiffOAuthModelAliasChanges(oldCfg.OAuthModelAlias, newCfg.OAuthModelAlias); len(entries) > 0 {
		changes = append(changes, entries...)
	}

	// Remote management (never print the key)
	if oldCfg.RemoteManagement.AllowRemote != newCfg.RemoteManagement.AllowRemote {
		changes = append(changes, fmt.Sprintf("remote-management.allow-remote: %t -> %t", oldCfg.RemoteManagement.AllowRemote, newCfg.RemoteManagement.AllowRemote))
	}
	if oldCfg.RemoteManagement.DisableControlPanel != newCfg.RemoteManagement.DisableControlPanel {
		changes = append(changes, fmt.Sprintf("remote-management.disable-control-panel: %t -> %t", oldCfg.RemoteManagement.DisableControlPanel, newCfg.RemoteManagement.DisableControlPanel))
	}
	oldPanelRepo := strings.TrimSpace(oldCfg.RemoteManagement.PanelGitHubRepository)
	newPanelRepo := strings.TrimSpace(newCfg.RemoteManagement.PanelGitHubRepository)
	if oldPanelRepo != newPanelRepo {
		changes = append(changes, fmt.Sprintf("remote-management.panel-github-repository: %s -> %s", oldPanelRepo, newPanelRepo))
	}
	if oldCfg.RemoteManagement.SecretKey != newCfg.RemoteManagement.SecretKey {
		switch {
		case oldCfg.RemoteManagement.SecretKey == "" && newCfg.RemoteManagement.SecretKey != "":
			changes = append(changes, "remote-management.secret-key: created")
		case oldCfg.RemoteManagement.SecretKey != "" && newCfg.RemoteManagement.SecretKey == "":
			changes = append(changes, "remote-management.secret-key: deleted")
		default:
			changes = append(changes, "remote-management.secret-key: updated")
		}
	}

	// OpenAI compatibility providers (summarized)
	if compat := DiffOpenAICompatibility(oldCfg.OpenAICompatibility, newCfg.OpenAICompatibility); len(compat) > 0 {
		changes = append(changes, "openai-compatibility:")
		for _, c := range compat {
			changes = append(changes, "  "+c)
		}
	}

	// Vertex-compatible API keys
	if len(oldCfg.VertexCompatAPIKey) != len(newCfg.VertexCompatAPIKey) {
		changes = append(changes, fmt.Sprintf("vertex-api-key count: %d -> %d", len(oldCfg.VertexCompatAPIKey), len(newCfg.VertexCompatAPIKey)))
	} else {
		for i := range oldCfg.VertexCompatAPIKey {
			o := oldCfg.VertexCompatAPIKey[i]
			n := newCfg.VertexCompatAPIKey[i]
			if strings.TrimSpace(o.BaseURL) != strings.TrimSpace(n.BaseURL) {
				changes = append(changes, fmt.Sprintf("vertex[%d].base-url: %s -> %s", i, strings.TrimSpace(o.BaseURL), strings.TrimSpace(n.BaseURL)))
			}
			if strings.TrimSpace(o.ProxyURL) != strings.TrimSpace(n.ProxyURL) {
				changes = append(changes, fmt.Sprintf("vertex[%d].proxy-url: %s -> %s", i, formatProxyURL(o.ProxyURL), formatProxyURL(n.ProxyURL)))
			}
			if strings.TrimSpace(o.Prefix) != strings.TrimSpace(n.Prefix) {
				changes = append(changes, fmt.Sprintf("vertex[%d].prefix: %s -> %s", i, strings.TrimSpace(o.Prefix), strings.TrimSpace(n.Prefix)))
			}
			if strings.TrimSpace(o.APIKey) != strings.TrimSpace(n.APIKey) {
				changes = append(changes, fmt.Sprintf("vertex[%d].api-key: updated", i))
			}
			oldModels := SummarizeVertexModels(o.Models)
			newModels := SummarizeVertexModels(n.Models)
			if oldModels.hash != newModels.hash {
				changes = append(changes, fmt.Sprintf("vertex[%d].models: updated (%d -> %d entries)", i, oldModels.count, newModels.count))
			}
			if !equalStringMap(o.Headers, n.Headers) {
				changes = append(changes, fmt.Sprintf("vertex[%d].headers: updated", i))
			}
		}
	}

	return changes
}

func trimStrings(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = strings.TrimSpace(in[i])
	}
	return out
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func formatProxyURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "<none>"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "<redacted>"
	}
	host := strings.TrimSpace(parsed.Host)
	scheme := strings.TrimSpace(parsed.Scheme)
	if host == "" {
		// Allow host:port style without scheme.
		parsed2, err2 := url.Parse("http://" + trimmed)
		if err2 == nil {
			host = strings.TrimSpace(parsed2.Host)
		}
		scheme = ""
	}
	if host == "" {
		return "<redacted>"
	}
	if scheme == "" {
		return host
	}
	return scheme + "://" + host
}

func equalStringSet(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	aSet := make(map[string]struct{}, len(a))
	for _, k := range a {
		aSet[strings.TrimSpace(k)] = struct{}{}
	}
	bSet := make(map[string]struct{}, len(b))
	for _, k := range b {
		bSet[strings.TrimSpace(k)] = struct{}{}
	}
	if len(aSet) != len(bSet) {
		return false
	}
	for k := range aSet {
		if _, ok := bSet[k]; !ok {
			return false
		}
	}
	return true
}

// equalUpstreamAPIKeys compares two slices of AmpUpstreamAPIKeyEntry for equality.
// Comparison is done by count and content (upstream key and client keys).
func equalUpstreamAPIKeys(a, b []config.AmpUpstreamAPIKeyEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].UpstreamAPIKey) != strings.TrimSpace(b[i].UpstreamAPIKey) {
			return false
		}
		if !equalStringSet(a[i].APIKeys, b[i].APIKeys) {
			return false
		}
	}
	return true
}
