package cliproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceStartModelCatalogUpdatersStartsAntigravityVersionUpdaterWithProxy(t *testing.T) {
	registry.SetLocalModelCatalogs(true)
	defer registry.SetLocalModelCatalogs(false)

	restoreCache := misc.SetAntigravityVersionCacheForTest("", time.Time{})
	defer restoreCache()

	var published atomic.Value
	published.Store("2.35.0")
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		version, _ := published.Load().(string)
		_, _ = w.Write([]byte("version: " + version + "\npath: Antigravity-arm64-mac.zip\n"))
	}))
	defer manifestServer.Close()

	xaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.46"}`))
	}))
	defer xaiServer.Close()

	var manifestProxied atomic.Bool
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() == manifestServer.URL || r.URL.Host == manifestServer.Listener.Addr().String() {
			manifestProxied.Store(true)
		}
		outbound, errReq := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if errReq != nil {
			http.Error(w, errReq.Error(), http.StatusBadGateway)
			return
		}
		outbound.Header = r.Header.Clone()
		direct := &http.Client{Transport: &http.Transport{Proxy: nil}}
		resp, errDo := direct.Do(outbound)
		if errDo != nil {
			http.Error(w, errDo.Error(), http.StatusBadGateway)
			return
		}
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("proxy upstream response body close error: %v", errClose)
			}
		}()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxyServer.Close()

	restoreManifestURL := misc.OverrideAntigravityHubManifestURLForTest(manifestServer.URL)
	defer restoreManifestURL()
	restoreXAIURL := helps.OverrideXAINPMRegistryURLForTest(xaiServer.URL)
	defer restoreXAIURL()

	notified := make(chan struct{}, 4)
	restoreHook := misc.SetAntigravityVersionRefreshedHookForTest(notified)
	defer restoreHook()
	trigger := make(chan struct{}, 1)
	restoreTrigger := misc.SetAntigravityVersionTriggerForTest(trigger)
	defer restoreTrigger()

	waitRefresh := func(stage string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-notified:
		case <-timer.C:
			t.Fatalf("timed out waiting for antigravity version refresh (%s)", stage)
		}
	}

	s := &Service{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				ProxyURL: "direct",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.startModelCatalogUpdaters(ctx)
	waitRefresh("initial direct startup")

	if manifestProxied.Load() {
		t.Fatal("expected initial direct startup to bypass proxy server")
	}
	if got := misc.AntigravityLatestVersion(); got != "2.35.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", got, "2.35.0")
	}

	// Hot-reload ProxyURL from "direct" to proxyServer without restarting the service.
	if commit := s.commitConfigUpdate(&config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL: proxyServer.URL,
		},
	}); commit.cfg == nil {
		t.Fatal("config update rejected")
	}
	published.Store("2.36.0")
	trigger <- struct{}{}
	waitRefresh("after hot-reload to proxy")

	if !manifestProxied.Load() {
		t.Fatal("expected hot-reloaded ProxyURL to route periodic antigravity refresh through proxy")
	}
	if got := misc.AntigravityLatestVersion(); got != "2.36.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q after hot-reload", got, "2.36.0")
	}

	// Calling the legacy 1-arg sdk/auth.StartAntigravityVersionUpdater while the service updater
	// is active must be a no-op and preserve the active updater and configured proxy.
	legacyActiveCtx, legacyActiveCancel := context.WithCancel(context.Background())
	sdkAuth.StartAntigravityVersionUpdater(legacyActiveCtx)
	legacyActiveCancel()

	manifestProxied.Store(false)
	published.Store("2.37.0")
	trigger <- struct{}{}
	waitRefresh("after legacy call while active")

	if !manifestProxied.Load() {
		t.Fatal("legacy StartAntigravityVersionUpdater cleared the active service proxy")
	}
	if got := misc.AntigravityLatestVersion(); got != "2.37.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q after legacy no-op", got, "2.37.0")
	}

	// Hot-reload ProxyURL back to "direct" without restarting the service.
	if commit := s.commitConfigUpdate(&config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL: "direct",
		},
	}); commit.cfg == nil {
		t.Fatal("config update back to direct rejected")
	}
	manifestProxied.Store(false)
	published.Store("2.38.0")
	trigger <- struct{}{}
	waitRefresh("after hot-reload back to direct")

	if manifestProxied.Load() {
		t.Fatal("expected hot-reloaded direct ProxyURL to bypass proxy")
	}
	if got := misc.AntigravityLatestVersion(); got != "2.38.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q after reload to direct", got, "2.38.0")
	}

	// After service shutdown cancels the active updater, the legacy 1-arg wrapper can start cleanly.
	cancel()
	published.Store("2.39.0")
	legacyRestartCtx, legacyRestartCancel := context.WithCancel(context.Background())
	defer legacyRestartCancel()

	sdkAuth.StartAntigravityVersionUpdater(legacyRestartCtx)
	waitRefresh("legacy startup after service shutdown")

	if got := misc.AntigravityLatestVersion(); got != "2.39.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q via legacy wrapper after shutdown", got, "2.39.0")
	}
}
