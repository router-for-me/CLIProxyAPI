package helps

import (
	"context"
	"net/http"
	"testing"

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

func TestNewDevinHTTPClient_ReusesTransportFromContext(t *testing.T) {
	baseTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", baseTransport)

	c1 := NewDevinHTTPClient(ctx, nil, nil, 0)
	c2 := NewDevinHTTPClient(ctx, nil, nil, 0)

	if c1.Transport != c2.Transport {
		t.Errorf("expected c1.Transport == c2.Transport across requests, got different pointers %p vs %p", c1.Transport, c2.Transport)
	}

	tr, ok := c1.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c1.Transport)
	}
	if !tr.DisableCompression {
		t.Error("expected DisableCompression = true")
	}
}

func TestNewDevinHTTPClient_RequestOverrideBeatsContextTransport(t *testing.T) {
	ctxTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(ctxTransport))
	ctx = proxyutil.WithOverride(ctx, "http://request-proxy.example.com:8080")

	client := NewDevinHTTPClient(ctx, nil, nil, 0)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == ctxTransport {
		t.Fatalf("expected request override transport, got %#v", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("expected request override proxy to be configured")
	}
}

func TestNewDevinHTTPClient_NonStandardRoundTripperDisablesGzip(t *testing.T) {
	var seenEncoding string
	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenEncoding = req.Header.Get("Accept-Encoding")
		return &http.Response{StatusCode: 200}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customRT)

	c := NewDevinHTTPClient(ctx, nil, nil, 0)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
	_, _ = c.Transport.RoundTrip(req)

	if seenEncoding != "identity" {
		t.Errorf("expected Accept-Encoding: identity, got %q", seenEncoding)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestResolveProxyURLPriority(t *testing.T) {
	authWith := &cliproxyauth.Auth{ProxyURL: "http://auth.example:1080"}
	authEmpty := &cliproxyauth.Auth{}
	// ProxyURL is promoted from the embedded SDKConfig and cannot be set in a composite literal.
	cfgWith := &config.Config{}
	cfgWith.ProxyURL = "http://global.example:1080"

	tests := []struct {
		name       string
		ctx        context.Context
		auth       *cliproxyauth.Auth
		cfg        *config.Config
		want       string
		wantSource ProxySource
	}{
		{
			name:       "request override wins",
			ctx:        proxyutil.WithOverride(context.Background(), "http://request.example:7902"),
			auth:       authWith,
			cfg:        cfgWith,
			want:       "http://request.example:7902",
			wantSource: ProxySourceRequest,
		},
		{
			name:       "auth beats global",
			ctx:        context.Background(),
			auth:       authWith,
			cfg:        cfgWith,
			want:       "http://auth.example:1080",
			wantSource: ProxySourceAuth,
		},
		{
			name:       "global fallback",
			ctx:        context.Background(),
			auth:       authEmpty,
			cfg:        cfgWith,
			want:       "http://global.example:1080",
			wantSource: ProxySourceGlobal,
		},
		{
			name:       "nothing configured",
			ctx:        context.Background(),
			auth:       authEmpty,
			cfg:        &config.Config{},
			want:       "",
			wantSource: ProxySourceNone,
		},
		{
			name:       "empty override is ignored",
			ctx:        proxyutil.WithOverride(context.Background(), "   "),
			auth:       authWith,
			cfg:        cfgWith,
			want:       "http://auth.example:1080",
			wantSource: ProxySourceAuth,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, source := ResolveProxyURL(tc.ctx, tc.cfg, tc.auth)
			if got != tc.want || source != tc.wantSource {
				t.Fatalf("ResolveProxyURL() = (%q, %q), want (%q, %q)", got, source, tc.want, tc.wantSource)
			}
		})
	}
}
