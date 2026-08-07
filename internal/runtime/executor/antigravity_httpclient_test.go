package executor

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestNewAntigravityHTTPClientConfigTimeoutReusesSingleton verifies that when
// request-timeout-seconds is configured, Antigravity still reuses its shared
// HTTP/1.1 singleton transport instead of cloning a fresh one per request (which
// would lose connection pooling). The proxy helper installs a non-proxy default
// transport to apply the connect timeout; Antigravity must detect that and
// prefer its singleton.
func TestNewAntigravityHTTPClientConfigTimeoutReusesSingleton(t *testing.T) {
	// Force singleton initialization.
	antigravityTransportOnce.Do(initAntigravityTransport)

	cfg := &internalconfig.Config{RequestTimeoutSeconds: 30}
	client := newAntigravityHTTPClient(context.Background(), cfg, nil, 0)

	if client.Transport == nil {
		t.Fatal("Transport is nil, expected the shared antigravity singleton")
	}
	if client.Transport != antigravityTransport {
		t.Fatalf("Transport = %p, want the shared antigravity singleton %p (config timeout must not trigger a per-request clone)", client.Transport, antigravityTransport)
	}
}

// TestNewAntigravityHTTPClientNoConfigReusesSingleton is the baseline: without
// any timeout configured, the singleton is used (legacy behavior preserved).
func TestNewAntigravityHTTPClientNoConfigReusesSingleton(t *testing.T) {
	antigravityTransportOnce.Do(initAntigravityTransport)

	client := newAntigravityHTTPClient(context.Background(), &internalconfig.Config{}, nil, 0)
	if client.Transport != antigravityTransport {
		t.Fatalf("Transport = %p, want singleton %p", client.Transport, antigravityTransport)
	}
}

// TestNewAntigravityHTTPClientProxyClones ensures proxy configured transports
// still get cloned with HTTP/1.1 enforcement (proxy path unchanged).
func TestNewAntigravityHTTPClientProxyClones(t *testing.T) {
	antigravityTransportOnce.Do(initAntigravityTransport)

	client := newAntigravityHTTPClient(
		context.Background(),
		&internalconfig.Config{},
		&auth.Auth{ProxyURL: "http://proxy.example.com:8080"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport for proxy path", client.Transport)
	}
	if transport == antigravityTransport {
		t.Fatal("proxy path should clone the transport, not reuse the singleton")
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("proxy clone should force HTTP/1.1 (ForceAttemptHTTP2=false)")
	}
}

// rtStub is a minimal round tripper for verifying context-injected transports
// are honored by newAntigravityHTTPClient.
type rtStub struct{ called bool }

func (r *rtStub) RoundTrip(*http.Request) (*http.Response, error) {
	r.called = true
	return &http.Response{StatusCode: http.StatusTeapot}, nil
}

// TestNewAntigravityHTTPClientContextRoundTripperHonored verifies that when an
// execution context carries a round tripper, newAntigravityHTTPClient does NOT
// short-circuit to the singleton and instead honors the injected transport (via
// the proxy helper). This preserves custom RoundTripperProvider / test mock
// behavior that the singleton short-circuit would otherwise bypass.
func TestNewAntigravityHTTPClientContextRoundTripperHonored(t *testing.T) {
	antigravityTransportOnce.Do(initAntigravityTransport)

	stub := &rtStub{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", stub)
	// No proxy URL on auth/cfg, so without the context-RT check the singleton
	// short-circuit would kick in.
	client := newAntigravityHTTPClient(ctx, &internalconfig.Config{}, nil, 0)

	if client.Transport == antigravityTransport {
		t.Fatal("Transport is the singleton; expected the context-injected round tripper to be honored")
	}
	// The proxy helper sets Transport to the context RT when one is present.
	if client.Transport != stub {
		t.Fatalf("Transport = %p, want the context-injected stub %p", client.Transport, stub)
	}
}
