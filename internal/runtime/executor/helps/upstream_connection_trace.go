package helps

import (
	"net/http"
	"net/http/httptrace"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provider-agnostic upstream connection trace shared by the Antigravity and
// Claude wrappers. It lives in its own file so debug log lines name a neutral
// source file regardless of the provider.

func withUpstreamHTTPClientTrace(client *http.Client, provider string, auth *cliproxyauth.Auth, operation string) *http.Client {
	if client == nil {
		return nil
	}
	clone := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	authIndex := ""
	if auth != nil {
		authIndex = auth.Index
	}
	clone.Transport = &upstreamConnectionTraceTransport{
		base: base, provider: provider, auth: cliproxyauth.Auth{Index: authIndex}, operation: operation,
	}
	return &clone
}

type upstreamConnectionTraceTransport struct {
	base      http.RoundTripper
	provider  string
	auth      cliproxyauth.Auth
	operation string
}

func (t *upstreamConnectionTraceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(withUpstreamConnectionTrace(req, t.provider, &t.auth, t.operation))
}

// CloseIdleConnections preserves the underlying transport cleanup behavior.
func (t *upstreamConnectionTraceTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func withUpstreamConnectionTrace(req *http.Request, provider string, auth *cliproxyauth.Auth, operation string) *http.Request {
	if req == nil || req.URL == nil || !log.IsLevelEnabled(log.DebugLevel) {
		return req
	}
	authIndex := ""
	if auth != nil {
		authIndex = auth.Index
	}
	requestID := logging.GetRequestID(req.Context())
	host := req.URL.Hostname()
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			log.WithFields(log.Fields{
				"provider":      provider,
				"operation":     operation,
				"upstream_host": host,
				"auth_index":    authIndex,
				"request_id":    requestID,
				"reused":        info.Reused,
				"was_idle":      info.WasIdle,
				"idle_time":     info.IdleTime.String(),
			}).Debug(provider + " upstream connection acquired")
		},
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}
