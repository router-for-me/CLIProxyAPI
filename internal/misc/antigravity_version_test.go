package misc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func overrideAntigravityVersionURLsForTest(t *testing.T, hubManifestURL string) func() {
	t.Helper()

	oldHubManifest := antigravityHubLatestManifestURL
	antigravityHubLatestManifestURL = hubManifestURL

	return func() {
		antigravityHubLatestManifestURL = oldHubManifest
	}
}

func overrideAntigravityVersionCacheForTest(t *testing.T, version string, expiry time.Time) func() {
	t.Helper()

	antigravityVersionMu.Lock()
	oldVersion := cachedAntigravityVersion
	oldExpiry := antigravityVersionExpiry
	cachedAntigravityVersion = version
	antigravityVersionExpiry = expiry
	antigravityVersionMu.Unlock()

	return func() {
		antigravityVersionMu.Lock()
		cachedAntigravityVersion = oldVersion
		antigravityVersionExpiry = oldExpiry
		antigravityVersionMu.Unlock()
	}
}

func TestAntigravityLatestVersionUsesCurrentHubFallback(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restore()

	version := AntigravityLatestVersion()
	if version != "2.2.1" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, "2.2.1")
	}
}

func TestAntigravityUserAgentUsesHubFamily(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.2.1 darwin/arm64"
	if got := AntigravityUserAgent(); got != want {
		t.Fatalf("AntigravityUserAgent() = %q, want %q", got, want)
	}
}

func TestAntigravityVersionFromUserAgentParsesHubFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/hub/2.2.1 darwin/arm64"); got != "2.2.1" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "2.2.1")
	}
}

func TestAntigravityVersionFromUserAgentParsesLegacyFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/1.23.2 windows/amd64"); got != "1.23.2" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "1.23.2")
	}
}

func TestAntigravityLoadCodeAssistUserAgentUsesShortUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.2.1 darwin/arm64"
	if got := AntigravityLoadCodeAssistUserAgent(""); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent() = %q, want %q", got, want)
	}
	if got := AntigravityLoadCodeAssistUserAgent(want); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent(configured) = %q, want %q", got, want)
	}
}

func TestAntigravityOnboardUserUserAgentUsesLongUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.2.1 darwin/arm64 google-api-nodejs-client/10.3.0"
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
