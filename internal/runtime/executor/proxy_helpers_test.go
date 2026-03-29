package executor

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func setEnvironmentProxy(t *testing.T, proxyURL string) {
	t.Helper()

	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
		oldValue, hadValue := os.LookupEnv(key)
		if err := os.Setenv(key, proxyURL); err != nil {
			t.Fatalf("Setenv(%s): %v", key, err)
		}
		cleanupKey := key
		cleanupOldValue := oldValue
		cleanupHadValue := hadValue
		t.Cleanup(func() {
			if cleanupHadValue {
				_ = os.Setenv(cleanupKey, cleanupOldValue)
				return
			}
			_ = os.Unsetenv(cleanupKey)
		})
	}
}

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

func TestNewProxyAwareHTTPClientFallsBackToEnvironmentProxy(t *testing.T) {
	setEnvironmentProxy(t, "http://env-proxy.example.com:8080")

	client := newProxyAwareHTTPClient(context.Background(), &config.Config{}, &cliproxyauth.Auth{}, 0)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("expected environment proxy transport to configure Proxy function")
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy() error = %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://env-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://env-proxy.example.com:8080", proxyURL)
	}
}

func TestNewProxyAwareHTTPClientExplicitProxyWinsOverEnvironmentProxy(t *testing.T) {
	setEnvironmentProxy(t, "http://env-proxy.example.com:8080")

	client := newProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://config-proxy.example.com:8080"}},
		nil,
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy() error = %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://config-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://config-proxy.example.com:8080", proxyURL)
	}
}

func TestNewProxyAwareHTTPClientReusesEnvironmentProxyTransport(t *testing.T) {
	setEnvironmentProxy(t, "http://env-proxy.example.com:8080")

	clientA := newProxyAwareHTTPClient(context.Background(), &config.Config{}, &cliproxyauth.Auth{}, 0)
	clientB := newProxyAwareHTTPClient(context.Background(), &config.Config{}, &cliproxyauth.Auth{}, 0)

	transportA, okA := clientA.Transport.(*http.Transport)
	if !okA {
		t.Fatalf("clientA transport type = %T, want *http.Transport", clientA.Transport)
	}
	transportB, okB := clientB.Transport.(*http.Transport)
	if !okB {
		t.Fatalf("clientB transport type = %T, want *http.Transport", clientB.Transport)
	}
	if transportA != transportB {
		t.Fatal("expected environment proxy transport to be shared across clients")
	}
}
