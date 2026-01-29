package executor

import "strings"

// KimiUserAgent is sent to Kimi API when the provider is Kimi, so requests are not rejected.
// See https://www.kimi.com/code/docs/en/more/third-party-agents.html
const KimiUserAgent = "claude-code/2.0"

// IsKimiProvider reports whether the provider is Kimi (base URL or provider key).
// When true, requests should use KimiUserAgent.
func IsKimiProvider(baseURL, providerKey string) bool {
	base := strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(base, "api.kimi.com") {
		return true
	}
	key := strings.ToLower(strings.TrimSpace(providerKey))
	return key == "kimi" || key == "kimi-for-coding"
}
