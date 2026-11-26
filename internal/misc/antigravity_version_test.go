package misc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func overrideAntigravityVersionURLsForTest(t *testing.T, hubManifestURL string) func() {
	t.Helper()
	return OverrideAntigravityHubManifestURLForTest(hubManifestURL)
}

func overrideAntigravityVersionCacheForTest(t *testing.T, version string, expiry time.Time) func() {
	t.Helper()
	return SetAntigravityVersionCacheForTest(version, expiry)
}

func TestAntigravityLatestVersionUsesCurrentHubFallback(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restore()

	version := AntigravityLatestVersion()
	if version != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, antigravityFallbackVersion)
	}
}

// Cloud Code resolves newer models only for clients reporting at least 2.23.0;
// older versions get 404 Requested entity was not found.
func TestAntigravityFallbackVersionMeetsBackendFloor(t *testing.T) {
	const floorMajor, floorMinor = 2, 23

	var major, minor, patch int
	if _, err := fmt.Sscanf(antigravityFallbackVersion, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("antigravityFallbackVersion = %q is not a dotted version: %v", antigravityFallbackVersion, err)
	}
	if major < floorMajor || (major == floorMajor && minor < floorMinor) {
		t.Fatalf("antigravityFallbackVersion = %q, want at least %d.%d.0", antigravityFallbackVersion, floorMajor, floorMinor)
	}
}

func TestAntigravityUserAgentUsesHubFamily(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"2.23.0", "antigravity/hub/2.23.0 (aidev_client; os_type=darwin; arch=arm64; cl=998057262)"},
		{"2.24.0", "antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682)"},
		{"2.29.0", "antigravity/hub/2.29.0 (aidev_client; os_type=darwin; arch=arm64; cl=1013060670)"},
		{"2.32.1", "antigravity/hub/2.32.1 (aidev_client; os_type=darwin; arch=arm64; cl=1021296516)"},
		{"2.32.2", "antigravity/hub/2.32.2 (aidev_client; os_type=darwin; arch=arm64; cl=1022991777)"},
		{"2.33.0", "antigravity/hub/2.33.0 (aidev_client; os_type=darwin; arch=arm64; cl=1023160026)"},
		{"2.34.0", "antigravity/hub/2.34.0 (aidev_client; os_type=darwin; arch=arm64; cl=1026637980)"},
		{"2.35.0", "antigravity/hub/2.35.0 (aidev_client; os_type=darwin; arch=arm64; cl=1028447422)"},
		{"2.35.1", "antigravity/hub/2.35.1 (aidev_client; os_type=darwin; arch=arm64; cl=1029194470)"},
		{"2.35.2", "antigravity/hub/2.35.2 (aidev_client; os_type=darwin; arch=arm64; cl=1029311855)"},
		{"2.30.0", "antigravity/hub/2.35.2 (aidev_client; os_type=darwin; arch=arm64; cl=1029311855)"},
		{"2.36.0", "antigravity/hub/2.35.2 (aidev_client; os_type=darwin; arch=arm64; cl=1029311855)"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			restore := overrideAntigravityVersionCacheForTest(t, tc.version, time.Now().Add(time.Hour))
			defer restore()
			if got := AntigravityUserAgent(); got != tc.want {
				t.Fatalf("AntigravityUserAgent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAntigravityUserAgentPinsUnmappedVersionUsingNumericSemverOrder(t *testing.T) {
	antigravityHubChangelists["2.100.0"] = "1099999999"
	defer delete(antigravityHubChangelists, "2.100.0")

	restore := overrideAntigravityVersionCacheForTest(t, "2.101.0", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.100.0 (aidev_client; os_type=darwin; arch=arm64; cl=1099999999)"
	if got := AntigravityUserAgent(); got != want {
		t.Fatalf("AntigravityUserAgent() = %q, want %q", got, want)
	}
}

func TestAntigravityFallbackVersionHasMatchingChangelist(t *testing.T) {
	if antigravityFallbackVersion != "2.35.2" {
		t.Fatalf("antigravityFallbackVersion = %q, want %q", antigravityFallbackVersion, "2.35.2")
	}
	if antigravityHubChangelists[antigravityFallbackVersion] == "" {
		t.Fatal("fallback version must be updated together with its verified changelist")
	}
}

func TestAntigravityVersionFromUserAgentParsesHubFamily(t *testing.T) {
	for _, userAgent := range []string{
		"antigravity/hub/2.24.0 darwin/arm64",
		"antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682)",
		"antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682) google-api-nodejs-client/10.3.0",
	} {
		if got := AntigravityVersionFromUserAgent(userAgent); got != "2.24.0" {
			t.Fatalf("AntigravityVersionFromUserAgent(%q) = %q, want %q", userAgent, got, "2.24.0")
		}
	}
}

func TestAntigravityVersionFromUserAgentParsesLegacyFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/1.23.2 windows/amd64"); got != "1.23.2" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "1.23.2")
	}
}

func TestAntigravityVersionFromUserAgentFallbackMatchesPinnedHubVersion(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.36.0", time.Now().Add(time.Hour))
	defer restore()

	for _, ua := range []string{"", "custom-client/1.0", "antigravity/hub/ "} {
		if got := AntigravityVersionFromUserAgent(ua); got != "2.35.2" {
			t.Fatalf("AntigravityVersionFromUserAgent(%q) = %q, want %q", ua, got, "2.35.2")
		}
	}
}

func TestAntigravityLoadCodeAssistUserAgentUsesShortUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.35.2", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.35.2 (aidev_client; os_type=darwin; arch=arm64; cl=1029311855)"
	if got := AntigravityLoadCodeAssistUserAgent(""); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent() = %q, want %q", got, want)
	}
	if got := AntigravityLoadCodeAssistUserAgent(want); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent(configured) = %q, want %q", got, want)
	}
}

func TestAntigravityOnboardUserUserAgentUsesLongUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.35.2", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.35.2 (aidev_client; os_type=darwin; arch=arm64; cl=1029311855) google-api-nodejs-client/10.3.0"
	if got := AntigravityOnboardUserUserAgent(""); got != want {
		t.Fatalf("AntigravityOnboardUserUserAgent() = %q, want %q", got, want)
	}
}

func TestFetchAntigravityLatestVersionUsesHubManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hub/latest-arm64-mac.yml":
			if got := r.Header.Get("User-Agent"); got != "electron-builder" {
				t.Errorf("hub manifest User-Agent = %q, want %q", got, "electron-builder")
			}
			if got := r.Header.Get("Cache-Control"); got != "no-cache" {
				t.Errorf("hub manifest Cache-Control = %q, want %q", got, "no-cache")
			}
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("version: 2.2.1\npath: Antigravity-arm64-mac.zip\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "2.2.1" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "2.2.1")
	}
}

func TestFetchAntigravityLatestVersionReturnsHubManifestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary outage", http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restore()

	_, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch == nil {
		t.Fatal("fetchAntigravityLatestVersion() error = nil, want error")
	}
}

func waitAntigravityVersionRefresh(t *testing.T, notified <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-notified:
	case <-timer.C:
		t.Fatal("timed out waiting for antigravity version refresh")
	}
}

func TestStartAntigravityVersionUpdaterUsesConfiguredProxy(t *testing.T) {
	restoreCache := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restoreCache()
	restoreProxy := setAntigravityVersionProxyURLForTest("")
	defer restoreProxy()

	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("version: 2.35.0\npath: Antigravity-arm64-mac.zip\n"))
	}))
	defer manifest.Close()

	var proxied atomic.Bool
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(true)
		if r.URL.Scheme == "" || r.URL.Host == "" {
			http.Error(w, "expected absolute proxy URI", http.StatusBadRequest)
			return
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

	restoreURL := overrideAntigravityVersionURLsForTest(t, manifest.URL)
	defer restoreURL()
	notified := make(chan struct{}, 1)
	restoreHook := SetAntigravityVersionRefreshedHookForTest(notified)
	defer restoreHook()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartAntigravityVersionUpdaterWithProxy(ctx, proxyServer.URL)
	waitAntigravityVersionRefresh(t, notified)

	if !proxied.Load() {
		t.Fatal("expected antigravity manifest lookup to route through configured proxy")
	}
	if got := AntigravityLatestVersion(); got != "2.35.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", got, "2.35.0")
	}
}

func TestStartAntigravityVersionUpdaterLegacyCallPreservesActiveProxiedUpdater(t *testing.T) {
	restoreCache := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restoreCache()
	restoreProxy := setAntigravityVersionProxyURLForTest("")
	defer restoreProxy()

	var published atomic.Value
	published.Store("2.35.0")
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		version, _ := published.Load().(string)
		_, _ = w.Write([]byte("version: " + version + "\n"))
	}))
	defer manifest.Close()

	var proxied atomic.Bool
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(true)
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

	restoreURL := overrideAntigravityVersionURLsForTest(t, manifest.URL)
	defer restoreURL()
	notified := make(chan struct{}, 2)
	restoreHook := SetAntigravityVersionRefreshedHookForTest(notified)
	defer restoreHook()
	trigger := make(chan struct{}, 1)
	restoreTrigger := SetAntigravityVersionTriggerForTest(trigger)
	defer restoreTrigger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartAntigravityVersionUpdaterWithProxy(ctx, proxyServer.URL)
	waitAntigravityVersionRefresh(t, notified)
	if !proxied.Load() {
		t.Fatal("expected initial fetch to route through configured proxy")
	}

	// A subsequent legacy 1-arg StartAntigravityVersionUpdater call while active must be a no-op
	// and preserve both the running updater goroutine and its configured proxy.
	legacyCtx, legacyCancel := context.WithCancel(context.Background())
	StartAntigravityVersionUpdater(legacyCtx)
	legacyCancel()

	proxied.Store(false)
	published.Store("2.36.0")
	trigger <- struct{}{}
	waitAntigravityVersionRefresh(t, notified)

	if !proxied.Load() {
		t.Fatal("legacy StartAntigravityVersionUpdater cleared the active updater proxy")
	}
	if got := AntigravityLatestVersion(); got != "2.36.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", got, "2.36.0")
	}
}

func TestStartAntigravityVersionUpdaterRestartsWithNewContext(t *testing.T) {
	restoreCache := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restoreCache()
	restoreProxy := setAntigravityVersionProxyURLForTest("")
	defer restoreProxy()

	var published atomic.Value
	published.Store("2.35.0")
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		version, _ := published.Load().(string)
		_, _ = w.Write([]byte("version: " + version + "\n"))
	}))
	defer manifest.Close()

	restoreURL := overrideAntigravityVersionURLsForTest(t, manifest.URL)
	defer restoreURL()
	notified := make(chan struct{}, 2)
	restoreHook := SetAntigravityVersionRefreshedHookForTest(notified)
	defer restoreHook()

	ctx, cancel := context.WithCancel(context.Background())
	StartAntigravityVersionUpdaterWithProxy(ctx, "")
	waitAntigravityVersionRefresh(t, notified)
	if got := AntigravityLatestVersion(); got != "2.35.0" {
		cancel()
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", got, "2.35.0")
	}
	cancel()

	published.Store("2.36.0")
	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	StartAntigravityVersionUpdaterWithProxy(restartCtx, "")
	waitAntigravityVersionRefresh(t, notified)
	if got := AntigravityLatestVersion(); got != "2.36.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q after restart", got, "2.36.0")
	}
}

func TestFetchAntigravityLatestVersionDirectModeDisablesEnvProxy(t *testing.T) {
	for _, mode := range []string{"direct", "none"} {
		t.Run(mode, func(t *testing.T) {
			restoreProxy := setAntigravityVersionProxyURLForTest(mode)
			defer restoreProxy()

			client, closeTransport, errClient := antigravityVersionHTTPClient()
			if errClient != nil {
				t.Fatalf("antigravityVersionHTTPClient() error = %v", errClient)
			}
			if closeTransport != nil {
				defer closeTransport()
			}
			transport, ok := client.Transport.(*http.Transport)
			if !ok || transport == nil {
				t.Fatalf("client.Transport = %T, want non-nil *http.Transport", client.Transport)
			}
			if transport.Proxy != nil {
				t.Fatalf("expected transport.Proxy == nil for %q mode", mode)
			}
		})
	}
}

func TestFetchAntigravityLatestVersionInvalidProxyDoesNotFallBackDirect(t *testing.T) {
	var manifestHits atomic.Int32
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestHits.Add(1)
		_, _ = w.Write([]byte("version: 2.35.0\n"))
	}))
	defer manifest.Close()

	restoreURL := overrideAntigravityVersionURLsForTest(t, manifest.URL)
	defer restoreURL()
	restoreProxy := setAntigravityVersionProxyURLForTest("ftp://secret-user:secret-pass@127.0.0.1:21")
	defer restoreProxy()

	_, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch == nil {
		t.Fatal("fetchAntigravityLatestVersion() error = nil, want error for invalid proxy")
	}
	if strings.Contains(errFetch.Error(), "secret-user") || strings.Contains(errFetch.Error(), "secret-pass") {
		t.Fatalf("error leaked proxy credentials: %v", errFetch)
	}
	if hits := manifestHits.Load(); hits != 0 {
		t.Fatalf("manifest server received %d direct requests on invalid proxy, want 0", hits)
	}
}

func TestRefreshAntigravityVersionCancelledContextPreservesCache(t *testing.T) {
	restoreCache := overrideAntigravityVersionCacheForTest(t, "2.35.0", time.Now().Add(time.Hour))
	defer restoreCache()
	restoreProxy := setAntigravityVersionProxyURLForTest("")
	defer restoreProxy()

	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected call", http.StatusInternalServerError)
	}))
	defer manifest.Close()

	restoreURL := overrideAntigravityVersionURLsForTest(t, manifest.URL)
	defer restoreURL()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	refreshAntigravityVersion(ctx)

	if got := AntigravityLatestVersion(); got != "2.35.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want preserved cached %q", got, "2.35.0")
	}
}

func setAntigravityVersionProxyURLForTest(proxyURL string) func() {
	antigravityVersionMu.Lock()
	old := antigravityVersionProxyURL
	oldProvider := antigravityVersionProxyProvider
	antigravityVersionProxyURL = proxyURL
	antigravityVersionProxyProvider = nil
	antigravityVersionMu.Unlock()
	return func() {
		antigravityVersionMu.Lock()
		antigravityVersionProxyURL = old
		antigravityVersionProxyProvider = oldProvider
		antigravityVersionMu.Unlock()
	}
}
