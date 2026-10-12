package config

import "strings"

// keyScopeProviderAliases maps the names people use for a provider to the
// provider key credentials are registered under.
var keyScopeProviderAliases = map[string]string{
	"anthropic": "claude",
	"openai":    "codex",
	"chatgpt":   "codex",
	"muse":      "meta",
	"grok":      "xai",
}

// NormalizeKeyScopeProvider returns the provider key a provider name in
// client.key-scopes refers to: lower case, with aliases resolved.
func NormalizeKeyScopeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if canonical, ok := keyScopeProviderAliases[provider]; ok {
		return canonical
	}
	return provider
}

// KeyScope returns the client.key-scopes entry for a client API key: the one
// with the longest key-prefix the key starts with. An entry that names no
// provider is a configuration mistake and is skipped rather than locking keys
// out. ok is false when no entry applies, and the key is unrestricted.
func (c ClientConfig) KeyScope(apiKey string) (scope ClientKeyScope, ok bool) {
	apiKey = strings.TrimSpace(apiKey)
	if c.KeyScopes == nil || apiKey == "" {
		return ClientKeyScope{}, false
	}
	longest := -1
	for _, candidate := range *c.KeyScopes {
		prefix := strings.TrimSpace(candidate.KeyPrefix)
		if prefix == "" || len(prefix) <= longest || !strings.HasPrefix(apiKey, prefix) || !namesKeyScopeProvider(candidate.Providers) {
			continue
		}
		scope, longest = candidate, len(prefix)
	}
	return scope, longest >= 0
}

func namesKeyScopeProvider(providers []string) bool {
	for _, provider := range providers {
		if NormalizeKeyScopeProvider(provider) != "" {
			return true
		}
	}
	return false
}
