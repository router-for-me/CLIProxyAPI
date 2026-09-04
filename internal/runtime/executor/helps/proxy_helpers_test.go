package helps

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

func TestNewProxyAwareHTTPClientInsecureTLS(t *testing.T) {
	t.Parallel()

	insecureClient := NewProxyAwareHTTPClient(context.Background(), nil, &cliproxyauth.Auth{
		Attributes: map[string]string{"insecure": "true"},
	}, 0)
	transport, ok := insecureClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", insecureClient.Transport)
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify = false, want true")
	}
}

func TestNewProxyAwareHTTPClientClonesTransportForInsecureTLS(t *testing.T) {
	t.Parallel()

	original := http.DefaultTransport.(*http.Transport).Clone()
	original.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(original))

	client := NewProxyAwareHTTPClient(ctx, nil, &cliproxyauth.Auth{
		Attributes: map[string]string{"insecure": "true"},
	}, 0)
	configured, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if configured == original {
		t.Fatal("insecure TLS mutated the original transport")
	}
	if original.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("insecure TLS mutated the original TLS config")
	}
	if configured.TLSClientConfig == original.TLSClientConfig {
		t.Fatal("insecure TLS reused the original TLS config")
	}
	if !configured.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify = false, want true")
	}
	if configured.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want %d", configured.TLSClientConfig.MinVersion, tls.VersionTLS12)
	}
}
