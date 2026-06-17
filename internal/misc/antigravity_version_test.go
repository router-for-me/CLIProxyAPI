package misc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func overrideAntigravityVersionURLsForTest(t *testing.T, updaterBaseURL string, cliLatestURL string, cliListURL string) func() {
	t.Helper()

	oldUpdater := antigravityCLIUpdaterBaseURL
	oldCLILatest := antigravityCLILatestURL
	oldCLIList := antigravityCLIGCSListURL
	antigravityCLIUpdaterBaseURL = updaterBaseURL
	antigravityCLILatestURL = cliLatestURL
	antigravityCLIGCSListURL = cliListURL

	return func() {
		antigravityCLIUpdaterBaseURL = oldUpdater
		antigravityCLILatestURL = oldCLILatest
		antigravityCLIGCSListURL = oldCLIList
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

func TestAntigravityLatestVersionUsesCurrentCLIFallback(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restore()

	version := AntigravityLatestVersion()
	if version != "1.0.8" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, "1.0.8")
	}
}

func TestAntigravityUserAgentUsesCLIFamily(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "1.0.8", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/cli/1.0.8 darwin/arm64"
	if got := AntigravityUserAgent(); got != want {
		t.Fatalf("AntigravityUserAgent() = %q, want %q", got, want)
	}
}

func TestAntigravityVersionFromUserAgentParsesCLIFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/cli/1.0.8 darwin/arm64"); got != "1.0.8" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "1.0.8")
	}
}

func TestAntigravityCLIUpdaterManifestName(t *testing.T) {
	if got := antigravityCLIUpdaterManifestName(); got != "darwin_arm64" {
		t.Fatalf("antigravityCLIUpdaterManifestName() = %q, want %q", got, "darwin_arm64")
	}
}

func TestFetchAntigravityLatestVersionPrefersDarwinManifest(t *testing.T) {
	var cliLatestRequests atomic.Int32
	var cliListRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifests/darwin_arm64.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"1.0.8","url":"https://storage.googleapis.com/antigravity-public/antigravity-cli/1.0.8-5963827121094656/darwin-arm/cli_mac_arm64.tar.gz"}`))
		case "/cli-latest":
			cliLatestRequests.Add(1)
			http.Error(w, "should not be called", http.StatusInternalServerError)
		case "/cli-list":
			cliListRequests.Add(1)
			http.Error(w, "should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/manifests", server.URL+"/cli-latest", server.URL+"/cli-list")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "1.0.8" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "1.0.8")
	}
	if got := cliLatestRequests.Load(); got != 0 {
		t.Fatalf("CLI latest requests = %d, want 0", got)
	}
	if got := cliListRequests.Load(); got != 0 {
		t.Fatalf("CLI GCS list requests = %d, want 0", got)
	}
}

func TestFetchAntigravityLatestVersionFallsBackToCLILatest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifests/darwin_arm64.json":
			http.Error(w, "temporary outage", http.StatusInternalServerError)
		case "/cli-latest":
			_, _ = w.Write([]byte("1.0.9"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/manifests", server.URL+"/cli-latest", server.URL+"/cli-list")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "1.0.9" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "1.0.9")
	}
}

func TestFetchAntigravityLatestVersionFallsBackToCLIGCSList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifests/darwin_arm64.json":
			http.Error(w, "temporary outage", http.StatusInternalServerError)
		case "/cli-latest":
			http.Error(w, "temporary outage", http.StatusInternalServerError)
		case "/cli-list":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version='1.0' encoding='UTF-8'?>
<ListBucketResult xmlns='http://doc.s3.amazonaws.com/2006-03-01'>
  <CommonPrefixes><Prefix>antigravity-cli/1.0.7/</Prefix></CommonPrefixes>
  <CommonPrefixes><Prefix>antigravity-cli/1.0.8/</Prefix></CommonPrefixes>
  <CommonPrefixes><Prefix>antigravity-cli/1.0.8-5963827121094656/</Prefix></CommonPrefixes>
</ListBucketResult>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/manifests", server.URL+"/cli-latest", server.URL+"/cli-list")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "1.0.8" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "1.0.8")
	}
}

func TestLatestAntigravityCLIVersionFromPrefixesSortsByNumericSemver(t *testing.T) {
	prefixes := []string{
		"antigravity-cli/1.0.7/",
		"antigravity-cli/1.0.8/",
		"antigravity-cli/1.0.8-5963827121094656/",
		"antigravity-cli/latest/",
	}

	version, errParse := latestAntigravityCLIVersionFromPrefixes(prefixes)
	if errParse != nil {
		t.Fatalf("latestAntigravityCLIVersionFromPrefixes() error = %v", errParse)
	}
	if version != "1.0.8" {
		t.Fatalf("latestAntigravityCLIVersionFromPrefixes() = %q, want %q", version, "1.0.8")
	}
}
