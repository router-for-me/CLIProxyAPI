package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
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

func TestNewProxyAwareHTTPClientDisableHTTP2NoProxyForcesHTTP11(t *testing.T) {
	t.Setenv(proxyutil.DisableUpstreamHTTP2Env, "true")

	client := NewProxyAwareHTTPClient(context.Background(), nil, nil, 3*time.Second)

	if client.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %v, want 3s", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	assertTransportForcesHTTP11(t, transport)
}

func TestNewProxyAwareHTTPClientDisableHTTP2ContextTransportForcesHTTP11(t *testing.T) {
	t.Setenv(proxyutil.DisableUpstreamHTTP2Env, "true")

	base := &http.Transport{ForceAttemptHTTP2: true, Proxy: http.ProxyFromEnvironment}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", base)
	client := NewProxyAwareHTTPClient(ctx, nil, nil, 0)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport == base {
		t.Fatal("context transport was not cloned")
	}
	if transport.Proxy == nil {
		t.Fatal("Proxy function was not preserved")
	}
	assertTransportForcesHTTP11(t, transport)
}

func TestNewProxyAwareHTTPClientDisableHTTP2FalseKeepsDefaultTransportBehavior(t *testing.T) {
	t.Setenv(proxyutil.DisableUpstreamHTTP2Env, "false")

	client := NewProxyAwareHTTPClient(context.Background(), nil, nil, 0)

	if client.Transport != nil {
		t.Fatalf("transport = %T, want nil default transport", client.Transport)
	}
}

func assertTransportForcesHTTP11(t *testing.T, transport *http.Transport) {
	t.Helper()

	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false")
	}
	if transport.TLSNextProto == nil {
		t.Fatal("TLSNextProto = nil, want empty map")
	}
	if len(transport.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto length = %d, want 0", len(transport.TLSNextProto))
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig = nil")
	}
	if got := transport.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("NextProtos = %v, want [http/1.1]", got)
	}
}
