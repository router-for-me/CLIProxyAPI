package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewProxyAwareHTTPClientDefaultTransportTimeouts(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(context.Background(), nil, nil, 0)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != defaultTLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout != defaultIdleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultIdleConnTimeout)
	}
	if transport.DialContext == nil {
		t.Error("expected DialContext to be set")
	}
}

func TestNewProxyAwareHTTPClientExplicitTimeoutPreserved(t *testing.T) {
	t.Parallel()

	explicit := 120 * time.Second
	client := newProxyAwareHTTPClient(context.Background(), nil, nil, explicit)

	if client.Timeout != explicit {
		t.Errorf("client.Timeout = %v, want %v", client.Timeout, explicit)
	}
}

func TestNewProxyAwareHTTPClientZeroTimeoutNoClientTimeout(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(context.Background(), nil, nil, 0)

	if client.Timeout != 0 {
		t.Errorf("client.Timeout = %v, want 0 (safe for streaming)", client.Timeout)
	}
}

func TestBuildProxyTransportAppliesTimeouts(t *testing.T) {
	t.Parallel()

	transport := buildProxyTransport("http://proxy.example.com:8080")
	if transport == nil {
		t.Fatal("expected non-nil transport for valid proxy URL")
	}
	if transport.TLSHandshakeTimeout != defaultTLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout != defaultIdleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultIdleConnTimeout)
	}
}
