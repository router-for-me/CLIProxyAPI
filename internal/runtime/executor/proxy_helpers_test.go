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
	oldHTTPProxy, hadHTTPProxy := os.LookupEnv("HTTP_PROXY")
	if err := os.Setenv("HTTP_PROXY", "http://env-proxy.example.com:8080"); err != nil {
		t.Fatalf("Setenv(HTTP_PROXY): %v", err)
	}
	defer func() {
		if hadHTTPProxy {
			_ = os.Setenv("HTTP_PROXY", oldHTTPProxy)
			return
		}
		_ = os.Unsetenv("HTTP_PROXY")
	}()

	oldHTTPSProxy, hadHTTPSProxy := os.LookupEnv("HTTPS_PROXY")
	if err := os.Setenv("HTTPS_PROXY", "http://env-proxy.example.com:8080"); err != nil {
		t.Fatalf("Setenv(HTTPS_PROXY): %v", err)
	}
	defer func() {
		if hadHTTPSProxy {
			_ = os.Setenv("HTTPS_PROXY", oldHTTPSProxy)
			return
		}
		_ = os.Unsetenv("HTTPS_PROXY")
	}()

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
	oldHTTPProxy, hadHTTPProxy := os.LookupEnv("HTTP_PROXY")
	if err := os.Setenv("HTTP_PROXY", "http://env-proxy.example.com:8080"); err != nil {
		t.Fatalf("Setenv(HTTP_PROXY): %v", err)
	}
	defer func() {
		if hadHTTPProxy {
			_ = os.Setenv("HTTP_PROXY", oldHTTPProxy)
			return
		}
		_ = os.Unsetenv("HTTP_PROXY")
	}()
	oldHTTPSProxy, hadHTTPSProxy := os.LookupEnv("HTTPS_PROXY")
	if err := os.Setenv("HTTPS_PROXY", "http://env-proxy.example.com:8080"); err != nil {
		t.Fatalf("Setenv(HTTPS_PROXY): %v", err)
	}
	defer func() {
		if hadHTTPSProxy {
			_ = os.Setenv("HTTPS_PROXY", oldHTTPSProxy)
			return
		}
		_ = os.Unsetenv("HTTPS_PROXY")
	}()

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
