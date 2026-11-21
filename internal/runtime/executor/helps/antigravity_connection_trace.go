package helps

import (
	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// WithAntigravityHTTPClientTrace traces each actual request, including redirects.
// The client copy keeps the original pooled transport and client settings.
func WithAntigravityHTTPClientTrace(client *http.Client, auth *cliproxyauth.Auth, operation string) *http.Client {
	return withUpstreamHTTPClientTrace(client, "antigravity", auth, operation)
}

// WithAntigravityConnectionTrace reports upstream connection reuse only in debug mode.
// It preserves existing trace hooks and never logs URLs, headers, or credential material.
func WithAntigravityConnectionTrace(req *http.Request, auth *cliproxyauth.Auth, operation string) *http.Request {
	return withUpstreamConnectionTrace(req, "antigravity", auth, operation)
}
