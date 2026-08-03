package helps

import (
	"net/url"
	"strings"
)

// IsAnthropicUpstreamURL reports whether a resolved request targets Anthropic's
// first-party API origin. Claude-specific body, header, HTTP, and TLS behavior
// must all use this gate so they cannot drift onto custom ports or userinfo URLs.
func IsAnthropicUpstreamURL(u *url.URL) bool {
	if u == nil || u.User != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.anthropic.com") {
		return false
	}
	port := u.Port()
	return port == "" || port == "443"
}
