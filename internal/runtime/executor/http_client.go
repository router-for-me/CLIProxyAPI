package executor

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// newProxyAwareHTTPClient constructs an *http.Client honoring per-auth proxy settings.
// If a valid proxy URL is provided on the auth entry, it is applied to the transport.
// The timeout argument, when > 0, sets the client timeout; otherwise the default is used.
func newProxyAwareHTTPClient(_ context.Context, _ *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	transport := &http.Transport{}
	if auth != nil && auth.ProxyURL != "" {
		if u, err := url.Parse(auth.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
