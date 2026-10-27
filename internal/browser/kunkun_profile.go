package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// KunKunProfileIDAttribute is the Auth attribute that records the sandbox
// profile used by a KunKun OAuth login.
const KunKunProfileIDAttribute = "kunkun_profile_id"

// kunkunProfileHashLength keeps profile identifiers short enough for directory
// names while retaining a low collision probability.
const kunkunProfileHashLength = 12

// DeriveKunKunProfileID derives a stable sandbox profile id from the provider
// and the login-time proxy node. The same account re-logs in through the same
// sticky node and therefore lands in the same sandbox.
//
// When the provider or the proxy is empty, the legacy random provider-prefixed
// id is returned so CLI logins without a session proxy keep their behavior.
func DeriveKunKunProfileID(provider, proxyURL string) string {
	provider = canonicalKunKunProvider(provider)
	proxyURL = strings.TrimSpace(proxyURL)
	if provider == "" || proxyURL == "" {
		return newOAuthProfileID(provider)
	}

	digest := sha256.Sum256([]byte(provider + "\x00" + proxyURL))
	prefix := sanitizeProfilePrefix(provider)
	if prefix == "" {
		prefix = "oauth"
	}
	return prefix + "-kunkun-" + hex.EncodeToString(digest[:])[:kunkunProfileHashLength]
}

// canonicalKunKunProvider maps OAuth-facing provider names to the provider name
// stored on the auth record so launch-time and persist-time derivations agree.
func canonicalKunKunProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	if normalized == "anthropic" {
		return "claude"
	}
	return normalized
}
