package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestGetXAIClientVersionDefault(t *testing.T) {
	restore := SetXAIClientVersionForTest(DefaultXAIFallbackClientVersion)
	defer restore()

	if got := GetXAIClientVersion(); got != DefaultXAIFallbackClientVersion {
		t.Fatalf("GetXAIClientVersion() = %q, want %q", got, DefaultXAIFallbackClientVersion)
	}
}

func TestFetchXAINPMLatestVersionSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept header = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"@xai-official/grok","version":"1.0.52"}`))
	}))
	defer server.Close()

	restore := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restore()

	version, err := FetchXAINPMLatestVersion(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("FetchXAINPMLatestVersion() error = %v", err)
	}
	if version != "1.0.52" {
		t.Fatalf("FetchXAINPMLatestVersion() = %q, want 1.0.52", version)
	}
}

func TestFetchXAINPMLatestVersionHttpError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restore()

	_, err := FetchXAINPMLatestVersion(context.Background(), server.Client())
	if err == nil {
		t.Fatal("FetchXAINPMLatestVersion() expected error for HTTP 500, got nil")
	}
}

func TestFetchXAINPMLatestVersionMissingVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"@xai-official/grok"}`))
	}))
	defer server.Close()

	restore := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restore()

	_, err := FetchXAINPMLatestVersion(context.Background(), server.Client())
	if err == nil {
		t.Fatal("FetchXAINPMLatestVersion() expected error for missing version, got nil")
	}
}

func TestRefreshXAIClientVersionKeepsFallbackOnError(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest("1.0.50")
	defer restoreVersion()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusBadGateway)
	}))
	defer server.Close()

	restoreURL := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restoreURL()

	refreshXAIClientVersion(context.Background())

	if got := GetXAIClientVersion(); got != "1.0.50" {
		t.Fatalf("GetXAIClientVersion() = %q, want fallback 1.0.50 preserved on error", got)
	}
}

func TestRefreshXAIClientVersionUpdatesOnSuccess(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest("1.0.50")
	defer restoreVersion()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"@xai-official/grok","version":"1.0.55"}`))
	}))
	defer server.Close()

	restoreURL := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restoreURL()

	refreshXAIClientVersion(context.Background())

	if got := GetXAIClientVersion(); got != "1.0.55" {
		t.Fatalf("GetXAIClientVersion() = %q, want updated version 1.0.55", got)
	}
}

func TestRefreshXAIClientVersionAcceptsStableLatest(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest("1.0.44")
	defer restoreVersion()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.46"}`))
	}))
	defer server.Close()

	restoreURL := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restoreURL()

	version, errFetch := FetchXAINPMLatestVersion(context.Background(), server.Client())
	if errFetch != nil {
		t.Fatalf("FetchXAINPMLatestVersion() error = %v", errFetch)
	}
	if version != "1.0.46" {
		t.Fatalf("FetchXAINPMLatestVersion() = %q, want 1.0.46", version)
	}
	refreshXAIClientVersion(context.Background())
	if got := GetXAIClientVersion(); got != "1.0.46" {
		t.Fatalf("GetXAIClientVersion() = %q, want stable latest 1.0.46", got)
	}
}

func TestRefreshXAIClientVersionRejectsUnsafeVersions(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest(DefaultXAIFallbackClientVersion)
	defer restoreVersion()

	unsafeVersions := []string{
		"0.2.93",
		"1.0.12",
		"1.0.55-beta.1",
		"1.0.55\r\nX-Injected: yes",
	}
	for _, unsafeVersion := range unsafeVersions {
		t.Run(unsafeVersion, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":` + strconv.Quote(unsafeVersion) + `}`))
			}))
			defer server.Close()

			restoreURL := OverrideXAINPMRegistryURLForTest(server.URL)
			defer restoreURL()

			if _, errFetch := FetchXAINPMLatestVersion(context.Background(), server.Client()); errFetch == nil {
				t.Fatal("FetchXAINPMLatestVersion() expected error for unsafe version, got nil")
			}
			refreshXAIClientVersion(context.Background())
			if got := GetXAIClientVersion(); got != DefaultXAIFallbackClientVersion {
				t.Fatalf("GetXAIClientVersion() = %q, want fallback %s preserved", got, DefaultXAIFallbackClientVersion)
			}
		})
	}
}

func TestRefreshXAIClientVersionUsesConfiguredProxy(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest(DefaultXAIFallbackClientVersion)
	defer restoreVersion()

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.61"}`))
	}))
	defer registry.Close()

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
			_ = resp.Body.Close()
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

	restoreURL := OverrideXAINPMRegistryURLForTest(registry.URL)
	defer restoreURL()
	restoreProxy := setXAIVersionProxyURLForTest(proxyServer.URL)
	defer restoreProxy()

	refreshXAIClientVersion(context.Background())

	if !proxied.Load() {
		t.Fatal("expected npm lookup to use the configured proxy")
	}
	if got := GetXAIClientVersion(); got != "1.0.61" {
		t.Fatalf("GetXAIClientVersion() = %q, want 1.0.61 via proxy", got)
	}
}

func TestStartXAIVersionUpdaterLifecycle(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest(DefaultXAIFallbackClientVersion)
	defer restoreVersion()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"@xai-official/grok","version":"1.0.60"}`))
	}))
	defer server.Close()

	restoreURL := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restoreURL()
	notified := make(chan struct{}, 1)
	restoreHook := SetXAIVersionRefreshedHookForTest(notified)
	defer restoreHook()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartXAIVersionUpdater(ctx, "")
	waitXAIVersionRefresh(t, notified)

	if got := GetXAIClientVersion(); got != "1.0.60" {
		t.Fatalf("GetXAIClientVersion() = %q, want 1.0.60 after StartXAIVersionUpdater", got)
	}
}

func TestStartXAIVersionUpdaterRestartsWithNewContext(t *testing.T) {
	restoreVersion := SetXAIClientVersionForTest(DefaultXAIFallbackClientVersion)
	defer restoreVersion()

	var published atomic.Value
	published.Store("1.0.70")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version, _ := published.Load().(string)
		_, _ = w.Write([]byte(`{"version":` + strconv.Quote(version) + `}`))
	}))
	defer server.Close()

	restoreURL := OverrideXAINPMRegistryURLForTest(server.URL)
	defer restoreURL()
	notified := make(chan struct{}, 2)
	restoreHook := SetXAIVersionRefreshedHookForTest(notified)
	defer restoreHook()

	ctx, cancel := context.WithCancel(context.Background())
	StartXAIVersionUpdater(ctx, "")
	waitXAIVersionRefresh(t, notified)
	if got := GetXAIClientVersion(); got != "1.0.70" {
		cancel()
		t.Fatalf("GetXAIClientVersion() = %q, want 1.0.70", got)
	}
	cancel()

	published.Store("1.0.71")
	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	StartXAIVersionUpdater(restartCtx, "")
	waitXAIVersionRefresh(t, notified)
	if got := GetXAIClientVersion(); got != "1.0.71" {
		t.Fatalf("GetXAIClientVersion() = %q, want 1.0.71 after restart", got)
	}
}

func waitXAIVersionRefresh(t *testing.T, notified <-chan struct{}) {
	t.Helper()
	select {
	case <-notified:
	case <-t.Context().Done():
		t.Fatal("timed out waiting for Grok CLI version refresh")
	}
}

func setXAIVersionProxyURLForTest(proxyURL string) func() {
	xaiClientVersionMu.Lock()
	old := xaiVersionProxyURL
	xaiVersionProxyURL = proxyURL
	xaiClientVersionMu.Unlock()
	return func() {
		xaiClientVersionMu.Lock()
		xaiVersionProxyURL = old
		xaiClientVersionMu.Unlock()
	}
}
