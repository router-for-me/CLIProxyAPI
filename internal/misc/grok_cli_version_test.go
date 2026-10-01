package misc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func overrideGrokCLIVersionURLsForTest(t *testing.T, stableURL, registryURL string) {
	t.Helper()

	oldStable := grokCLIStableVersionURL
	oldRegistry := grokCLIRegistryVersionURL
	grokCLIStableVersionURL = stableURL
	grokCLIRegistryVersionURL = registryURL

	t.Cleanup(func() {
		grokCLIStableVersionURL = oldStable
		grokCLIRegistryVersionURL = oldRegistry
	})
}

func overrideGrokCLIVersionCacheForTest(t *testing.T, version string, expiry time.Time) func() {
	t.Helper()

	grokCLIVersionMu.Lock()
	oldVersion := cachedGrokCLIVersion
	oldExpiry := grokCLIVersionExpiry
	cachedGrokCLIVersion = version
	grokCLIVersionExpiry = expiry
	grokCLIVersionMu.Unlock()

	return func() {
		grokCLIVersionMu.Lock()
		cachedGrokCLIVersion = oldVersion
		grokCLIVersionExpiry = oldExpiry
		grokCLIVersionMu.Unlock()
	}
}

func TestGrokCLIFallbackVersionMeetsServerFloor(t *testing.T) {
	var major, minor, patch int
	if _, errParse := fmt.Sscanf(GrokCLIFallbackVersion, "%d.%d.%d", &major, &minor, &patch); errParse != nil {
		t.Fatalf("GrokCLIFallbackVersion = %q is not a dotted numeric version: %v", GrokCLIFallbackVersion, errParse)
	}
	if major < 1 || (major == 1 && minor == 0 && patch < 13) {
		t.Fatalf("GrokCLIFallbackVersion = %q, want at least 1.0.13", GrokCLIFallbackVersion)
	}
}

func TestGrokCLILatestVersionUsesFallbackWhenCacheIsStale(t *testing.T) {
	restore := overrideGrokCLIVersionCacheForTest(t, "9.9.9", time.Now().Add(-time.Minute))
	defer restore()

	if got := GrokCLILatestVersion(); got != GrokCLIFallbackVersion {
		t.Fatalf("GrokCLILatestVersion() = %q, want fallback %q", got, GrokCLIFallbackVersion)
	}
}

func TestRefreshGrokCLIVersionUsesStableFeed(t *testing.T) {
	restoreCache := overrideGrokCLIVersionCacheForTest(t, "", time.Time{})
	defer restoreCache()

	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.URL.Path != "/stable" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("2.3.4\n"))
	}))
	defer server.Close()
	overrideGrokCLIVersionURLsForTest(t, server.URL+"/stable", server.URL+"/missing")

	RefreshGrokCLIVersion(context.Background())

	if !requested {
		t.Fatal("stable feed was not requested")
	}
	grokCLIVersionMu.RLock()
	version := cachedGrokCLIVersion
	expiry := grokCLIVersionExpiry
	grokCLIVersionMu.RUnlock()
	if version != "2.3.4" {
		t.Fatalf("cached Grok CLI version = %q, want 2.3.4", version)
	}
	if !time.Now().Before(expiry) {
		t.Fatal("cached Grok CLI version expiry was not extended")
	}
}

func TestRefreshGrokCLIVersionFallsBackToRegistry(t *testing.T) {
	restoreCache := overrideGrokCLIVersionCacheForTest(t, "", time.Time{})
	defer restoreCache()

	stable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer stable.Close()
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"5.6.7"}`))
	}))
	defer registry.Close()
	overrideGrokCLIVersionURLsForTest(t, stable.URL, registry.URL)

	RefreshGrokCLIVersion(context.Background())

	grokCLIVersionMu.RLock()
	version := cachedGrokCLIVersion
	grokCLIVersionMu.RUnlock()
	if version != "5.6.7" {
		t.Fatalf("cached Grok CLI version = %q, want registry version 5.6.7", version)
	}
}

func TestIsValidGrokCLIVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "1.0.46", want: true},
		{version: "1.0", want: false},
		{version: "1.0.46-beta", want: false},
		{version: "v1.0.46", want: false},
		{version: "1.0.046", want: true},
		{version: "", want: false},
		{version: "1.0.99999999999", want: false},
	}
	for _, tt := range tests {
		if got := IsValidGrokCLIVersion(tt.version); got != tt.want {
			t.Errorf("IsValidGrokCLIVersion(%q) = %t, want %t", tt.version, got, tt.want)
		}
	}
}
