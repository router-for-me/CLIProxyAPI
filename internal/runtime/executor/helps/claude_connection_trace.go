package helps

import (
	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// WithClaudeHTTPClientTrace logs, in debug mode only, whether each Claude upstream
// request reused a pooled connection and how long that connection sat idle. This
// is what diagnosing silent idle drops on the path to Anthropic (#7357) needs. It
// never logs URLs, headers, or credential material.
func WithClaudeHTTPClientTrace(client *http.Client, auth *cliproxyauth.Auth, operation string) *http.Client {
	return withUpstreamHTTPClientTrace(client, "claude", auth, operation)
}
