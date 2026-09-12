package homeplugins

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalpluginstore "github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
)

type networkScopeDoer func(*http.Request) (*http.Response, error)

func (f networkScopeDoer) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestPluginStoreClientsShareProxyCooldown(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	const tokenEnv = "HOME_PLUGIN_NETWORK_SCOPE_TEST_TOKEN"
	t.Setenv(tokenEnv, t.Name())
	auth := []sdkpluginstore.ResolvedAuthConfig{{
		Match: "https://api.github.com/", Type: sdkpluginstore.AuthTypeGitHubToken,
		Token: sdkpluginstore.Secret(t.Name()),
	}}
	expiresAt := time.Now().Add(time.Hour)
	plugin := sdkpluginstore.Plugin{Repository: "https://github.com/test/home-network-scope"}
	// Seed the same process-wide cooldown used by management and Home clients.
	seed := sdkpluginstore.NewClientWithResolvedAuthExpiry(networkScopeDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"3600"}}, Body: io.NopCloser(strings.NewReader("limited"))}, nil
	}), "", auth, expiresAt).WithNetworkScope(proxy.URL)
	_, errRelease := seed.FetchLatestRelease(context.Background(), plugin)
	var rateLimit *internalpluginstore.RateLimitError
	if !errors.As(errRelease, &rateLimit) {
		t.Fatalf("seed cooldown: %v", errRelease)
	}

	cfg := &config.Config{}
	cfg.ProxyURL = " " + proxy.URL + " "
	cfg.Plugins.StoreAuth = []sdkpluginstore.AuthConfig{{
		Match: "https://api.github.com/", Type: sdkpluginstore.AuthTypeGitHubToken, TokenEnv: tokenEnv,
	}}
	for name, client := range map[string]sdkpluginstore.Client{
		"configured auth": newPluginStoreClient(cfg),
		"resolved auth":   newResolvedPluginStoreClient(cfg, auth, expiresAt),
	} {
		t.Run(name, func(t *testing.T) {
			_, errRelease := client.FetchLatestRelease(context.Background(), plugin)
			var rateLimit *internalpluginstore.RateLimitError
			if !errors.As(errRelease, &rateLimit) {
				t.Fatalf("Home client did not inherit the proxy cooldown: %v", errRelease)
			}
		})
	}
	if proxyCalls.Load() != 0 {
		t.Fatalf("cooldown made %d proxy requests, want 0", proxyCalls.Load())
	}
}
