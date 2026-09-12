package helps

import (
	"net/url"
	"strings"
)

// IsChatGPTUpstreamURL reports whether a resolved request targets ChatGPT's
// first-party web origin. Chrome TLS/HTTP2 fingerprinting and ChatGPT web
// header defaults must use this gate so management APICall and Codex cannot
// drift onto lookalike hosts, custom ports, or userinfo URLs as the codebase
// evolves.
func IsChatGPTUpstreamURL(u *url.URL) bool {
	if u == nil || u.User != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "chatgpt.com") {
		return false
	}
	port := u.Port()
	return port == "" || port == "443"
}
