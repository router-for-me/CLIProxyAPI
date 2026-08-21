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

	candidate, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if candidate.version != "2.2.1" {
		t.Fatalf("fetchAntigravityLatestVersion() version = %q, want %q", candidate.version, "2.2.1")
	}
	if candidate.stagingPercentage != nil {
		t.Fatalf("fetchAntigravityLatestVersion() stagingPercentage = %v, want nil", *candidate.stagingPercentage)
	}
}

func TestFetchAntigravityLatestVersionParsesStagingPercentage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("version: 2.4.2\nstagingPercentage: 10\npath: Antigravity-arm64-mac.zip\n"))
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restore()

	candidate, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if candidate.version != "2.4.2" {
		t.Fatalf("version = %q, want %q", candidate.version, "2.4.2")
	}
	if candidate.stagingPercentage == nil || *candidate.stagingPercentage != 10 {
		t.Fatalf("stagingPercentage = %v, want 10", candidate.stagingPercentage)
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

func TestAntigravityManifestIsClientReleased(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	cases := []struct {
		name              string
		stagingPercentage *int
		wantReleased      bool
		wantPercentage    int
	}{
		{name: "absent", stagingPercentage: nil, wantReleased: true, wantPercentage: 100},
		{name: "full", stagingPercentage: intPtr(100), wantReleased: true, wantPercentage: 100},
		{name: "partial", stagingPercentage: intPtr(10), wantReleased: false, wantPercentage: 10},
		{name: "zero", stagingPercentage: intPtr(0), wantReleased: false, wantPercentage: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			released, percentage := antigravityManifestIsClientReleased(tc.stagingPercentage)
			if released != tc.wantReleased {
				t.Fatalf("released = %v, want %v", released, tc.wantReleased)
			}
			if percentage != tc.wantPercentage {
				t.Fatalf("percentage = %d, want %d", percentage, tc.wantPercentage)
			}
		})
	}
}

func TestFetchAntigravityLatestVersionToleratesOddStagingPercentage(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	cases := []struct {
		name string
		body string
		want *int
	}{
		{name: "quoted integer", body: "version: 2.4.2\nstagingPercentage: \"10\"\n", want: intPtr(10)},
		{name: "float truncates", body: "version: 2.4.2\nstagingPercentage: 99.9\n", want: intPtr(99)},
		{name: "above range clamps", body: "version: 2.4.2\nstagingPercentage: 150\n", want: intPtr(100)},
		{name: "below range clamps", body: "version: 2.4.2\nstagingPercentage: -5\n", want: intPtr(0)},
		{name: "garbage ignored", body: "version: 2.4.2\nstagingPercentage: soon\n", want: nil},
		{name: "empty ignored", body: "version: 2.4.2\nstagingPercentage:\n", want: nil},
		{name: "non-scalar ignored", body: "version: 2.4.2\nstagingPercentage:\n  mac: 10\n", want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/yaml")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
			defer restore()

			candidate, errFetch := fetchAntigravityLatestVersion(context.Background())
			if errFetch != nil {
				t.Fatalf("fetchAntigravityLatestVersion() error = %v, want the version to survive a malformed stagingPercentage", errFetch)
			}
			if candidate.version != "2.4.2" {
				t.Fatalf("version = %q, want %q", candidate.version, "2.4.2")
			}
			switch {
			case tc.want == nil && candidate.stagingPercentage != nil:
				t.Fatalf("stagingPercentage = %d, want nil", *candidate.stagingPercentage)
			case tc.want != nil && candidate.stagingPercentage == nil:
				t.Fatalf("stagingPercentage = nil, want %d", *tc.want)
			case tc.want != nil && *candidate.stagingPercentage != *tc.want:
				t.Fatalf("stagingPercentage = %d, want %d", *candidate.stagingPercentage, *tc.want)
			}
		})
	}
}

func TestRefreshAntigravityVersionKeepsStableWhenLatestIsStaged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("version: 2.4.2\nstagingPercentage: 10\npath: Antigravity-arm64-mac.zip\n"))
	}))
	defer server.Close()

	restoreURL := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restoreURL()
	// Seed a cached version that differs from antigravityFallbackVersion, otherwise
	// "kept the cached value" and "silently fell back" are indistinguishable.
	restoreCache := overrideAntigravityVersionCacheForTest(t, "2.3.0", time.Time{})
	defer restoreCache()

	before := time.Now()
	refreshAntigravityVersion(context.Background())

	if got := AntigravityLatestVersion(); got != "2.3.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q while latest is staged", got, "2.3.0")
	}

	// The staged path must also extend the cache window; otherwise the next
	// AntigravityLatestVersion() call reads the stale cache and returns the fallback.
	antigravityVersionMu.RLock()
	expiry := antigravityVersionExpiry
	antigravityVersionMu.RUnlock()
	if !expiry.After(before) {
		t.Fatalf("antigravityVersionExpiry = %v, want it extended past %v", expiry, before)
	}
}

func TestRefreshAntigravityVersionAdoptsFullyReleasedLatest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("version: 2.4.2\nstagingPercentage: 100\npath: Antigravity-arm64-mac.zip\n"))
	}))
	defer server.Close()

	restoreURL := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restoreURL()
	restoreCache := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Time{})
	defer restoreCache()

	refreshAntigravityVersion(context.Background())

	if got := AntigravityLatestVersion(); got != "2.4.2" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q after full release", got, "2.4.2")
	}
}
