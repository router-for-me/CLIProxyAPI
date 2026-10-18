package helps

import (
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// NormalizeCodexServiceTier falls back to Fast for OAuth accounts without ProMax.
// API-key routes retain their upstream's service-tier contract.
func NormalizeCodexServiceTier(body []byte, auth *cliproxyauth.Auth) []byte {
	if auth.AuthKind() != cliproxyauth.AuthKindOAuth || auth.SupportsCodexUltrafast() ||
		gjson.GetBytes(body, "service_tier").String() != "ultrafast" {
		return body
	}
	return SetStringIfDifferent(body, "service_tier", "priority")
}
