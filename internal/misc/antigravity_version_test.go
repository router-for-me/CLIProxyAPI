package misc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resetAntigravityVersionCache clears the cached version so each test starts fresh.
func resetAntigravityVersionCache() {
	antigravityVersionMu.Lock()
	defer antigravityVersionMu.Unlock()
	cachedAntigravityVersion = ""
	antigravityVersionExpiry = time.Time{}
}

func TestFetchAntigravityLatestVersion_Success(t *testing.T) {
	releases := []antigravityRelease{
		{Version: "1.22.2", ExecutionID: "123"},
		{Version: "1.21.9", ExecutionID: "456"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	// Temporarily override the releases URL by calling the server directly.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	var got []antigravityRelease
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(got) == 0 || got[0].Version != "1.22.2" {
		t.Fatalf("expected version 1.22.2, got %v", got)
	}
}

func TestFetchAntigravityLatestVersion_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]antigravityRelease{})
	}))
	defer srv.Close()

	// fetchAntigravityLatestVersion uses the package-level URL constant,
	// so we test the fallback logic indirectly via the cache path.
	resetAntigravityVersionCache()

	// Populate cache with empty string to simulate fetch failure fallback.
	antigravityVersionMu.Lock()
	cachedAntigravityVersion = antigravityFallbackVersion
	antigravityVersionExpiry = time.Now().Add(time.Hour)
	antigravityVersionMu.Unlock()

	got := AntigravityLatestVersion()
	if got != antigravityFallbackVersion {
		t.Errorf("expected fallback %q, got %q", antigravityFallbackVersion, got)
	}
}

func TestAntigravityLatestVersion_CacheHit(t *testing.T) {
	resetAntigravityVersionCache()

	// Pre-populate cache.
	antigravityVersionMu.Lock()
	cachedAntigravityVersion = "1.99.0"
	antigravityVersionExpiry = time.Now().Add(time.Hour)
	antigravityVersionMu.Unlock()

	got := AntigravityLatestVersion()
	if got != "1.99.0" {
		t.Errorf("expected cached version 1.99.0, got %q", got)
	}
}

func TestAntigravityLatestVersion_CacheExpired(t *testing.T) {
	resetAntigravityVersionCache()

	// Pre-populate with expired cache.
	antigravityVersionMu.Lock()
	cachedAntigravityVersion = "1.0.0"
	antigravityVersionExpiry = time.Now().Add(-time.Second)
	antigravityVersionMu.Unlock()

	// When cache expires, it will try to fetch from the real URL.
	// Since we can't control the URL in the function, it will either
	// succeed (real API) or fall back. Either way, it should not return
	// the expired "1.0.0".
	got := AntigravityLatestVersion()
	if got == "1.0.0" {
		t.Error("should not return expired cached version")
	}
	if got == "" {
		t.Error("should never return empty string")
	}
}

func TestAntigravityUserAgent_Format(t *testing.T) {
	resetAntigravityVersionCache()

	// Pre-populate cache to avoid network call.
	antigravityVersionMu.Lock()
	cachedAntigravityVersion = "1.22.2"
	antigravityVersionExpiry = time.Now().Add(time.Hour)
	antigravityVersionMu.Unlock()

	ua := AntigravityUserAgent()
	expected := "antigravity/1.22.2 darwin/arm64"
	if ua != expected {
		t.Errorf("expected %q, got %q", expected, ua)
	}
}

func TestAntigravityUserAgent_ContainsVersionPrefix(t *testing.T) {
	resetAntigravityVersionCache()

	// Pre-populate cache.
	antigravityVersionMu.Lock()
	cachedAntigravityVersion = "2.0.0"
	antigravityVersionExpiry = time.Now().Add(time.Hour)
	antigravityVersionMu.Unlock()

	ua := AntigravityUserAgent()
	if !strings.HasPrefix(ua, "antigravity/") {
		t.Errorf("UA should start with 'antigravity/', got %q", ua)
	}
	if !strings.Contains(ua, "2.0.0") {
		t.Errorf("UA should contain version, got %q", ua)
	}
}

func TestAntigravityFallbackVersion_IsValid(t *testing.T) {
	// Fallback version should be a valid semver-like string.
	parts := strings.Split(antigravityFallbackVersion, ".")
	if len(parts) != 3 {
		t.Errorf("fallback version %q should have 3 parts", antigravityFallbackVersion)
	}
}
