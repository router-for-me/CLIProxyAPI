package helps

import (
	"net/http"
	"net/http/httptrace"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// WithAntigravityHTTPClientTrace traces each actual request, including redirects.
// The client copy keeps the original pooled transport and client settings.
func WithAntigravityHTTPClientTrace(client *http.Client, auth *cliproxyauth.Auth, operation string) *http.Client {
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
	clone.Transport = &antigravityConnectionTraceTransport{
		base: base, auth: cliproxyauth.Auth{Index: authIndex}, operation: operation,
	}
	return &clone
}

type antigravityConnectionTraceTransport struct {
	base      http.RoundTripper
	auth      cliproxyauth.Auth
	operation string
}

func (t *antigravityConnectionTraceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(WithAntigravityConnectionTrace(req, &t.auth, t.operation))
}

// CloseIdleConnections preserves the underlying transport cleanup behavior.
func (t *antigravityConnectionTraceTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// WithAntigravityConnectionTrace reports upstream connection reuse only in debug mode.
// It preserves existing trace hooks and never logs URLs, headers, or credential material.
func WithAntigravityConnectionTrace(req *http.Request, auth *cliproxyauth.Auth, operation string) *http.Request {
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
				"provider":      "antigravity",
				"operation":     operation,
				"upstream_host": host,
				"auth_index":    authIndex,
				"request_id":    requestID,
				"reused":        info.Reused,
				"was_idle":      info.WasIdle,
				"idle_time":     info.IdleTime.String(),
			}).Debug("antigravity upstream connection acquired")
		},
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}
