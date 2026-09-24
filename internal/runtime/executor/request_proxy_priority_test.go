package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRequestProxyOverridesCredentialProxyForWebsocketAndAntigravity(t *testing.T) {
	t.Parallel()

	const requestProxy = "http://request-proxy.example:8081"
	ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), requestProxy)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example:8082"}}
	auth := &cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"}

	if got := antigravityProxyURL(ctx, cfg, auth); got != requestProxy {
		t.Fatalf("antigravity proxy = %q, want %q", got, requestProxy)
	}

	dialer := newProxyAwareWebsocketDialer(ctx, cfg, auth)
	if dialer.Proxy == nil {
		t.Fatal("websocket proxy function is nil")
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	proxyURL, errProxy := dialer.Proxy(req)
	if errProxy != nil {
		t.Fatalf("websocket proxy: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != requestProxy {
		t.Fatalf("websocket proxy = %v, want %s", proxyURL, requestProxy)
	}
}
