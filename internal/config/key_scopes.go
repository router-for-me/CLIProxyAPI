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

// keyScopeOpenAICompatiblePrefix starts the provider key an openai-compatibility
// entry registers its credentials under, as util.OpenAICompatibleProviderKey builds it.
const keyScopeOpenAICompatiblePrefix = "openai-compatible-"

// NormalizeKeyScopeProvider returns the provider key a provider name in
// client.key-scopes refers to: lower case, with aliases resolved.
func NormalizeKeyScopeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if canonical, ok := keyScopeProviderAliases[provider]; ok {
		return canonical
	}
	return provider
}

// KeyScopeProviderKeys returns the provider keys a provider name in
// client.key-scopes stands for: the provider it names, through an alias if it
// is one, and the key an openai-compatibility entry of that name registers its
// credentials under (openai-compatible-<name>).
func KeyScopeProviderKeys(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	keys := []string{NormalizeKeyScopeProvider(provider)}
	if provider != "openai-compatibility" && !strings.HasPrefix(provider, keyScopeOpenAICompatiblePrefix) {
		keys = append(keys, keyScopeOpenAICompatiblePrefix+provider)
	}
	return keys
}

// KeyScope returns the client.key-scopes entry for a client API key: the one
// with the longest key-prefix the key starts with. An entry that names no
// provider and no pools is a configuration mistake and is skipped rather than
// locking keys out; one with pools still applies, so its keys fail closed. ok
// is false when no entry applies, and the key is unrestricted.
func (c ClientConfig) KeyScope(apiKey string) (scope ClientKeyScope, ok bool) {
	apiKey = strings.TrimSpace(apiKey)
	if c.KeyScopes == nil || apiKey == "" {
		return ClientKeyScope{}, false
	}
	longest := -1
	for _, candidate := range *c.KeyScopes {
		prefix := strings.TrimSpace(candidate.KeyPrefix)
		if prefix == "" || len(prefix) <= longest || !strings.HasPrefix(apiKey, prefix) {
			continue
		}
		if candidate.Pools == nil && !namesKeyScopeProvider(candidate.Providers) {
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
