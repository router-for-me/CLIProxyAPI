package misc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func overrideAntigravityVersionURLsForTest(t *testing.T, hubURL string, legacyURL string) func() {
	t.Helper()

	oldHubURL := antigravityHubGCSListURL
	oldLegacyURL := antigravityReleasesURL
	antigravityHubGCSListURL = hubURL
	antigravityReleasesURL = legacyURL

	return func() {
		antigravityHubGCSListURL = oldHubURL
		antigravityReleasesURL = oldLegacyURL
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
	if version != "2.1.0" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, "2.1.0")
	}
}

func TestFetchAntigravityLatestVersionPrefersHubGCSList(t *testing.T) {
	var legacyRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gcs":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version='1.0' encoding='UTF-8'?>
<ListBucketResult xmlns='http://doc.s3.amazonaws.com/2006-03-01'>
  <CommonPrefixes><Prefix>antigravity-hub/2.0.9-4666288509943808/</Prefix></CommonPrefixes>
  <CommonPrefixes><Prefix>antigravity-hub/2.0.11-6560309696135168/</Prefix></CommonPrefixes>
  <CommonPrefixes><Prefix>antigravity-hub/2.1.0-6066040229199872/</Prefix></CommonPrefixes>
</ListBucketResult>`))
		case "/legacy":
			legacyRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"version":"9.9.9","execution_id":"1"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/gcs", server.URL+"/legacy")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "2.1.0" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "2.1.0")
	}
	if got := legacyRequests.Load(); got != 0 {
		t.Fatalf("legacy releases API requests = %d, want 0", got)
	}
}

func TestFetchAntigravityLatestVersionFallsBackToLegacyReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gcs":
			http.Error(w, "temporary outage", http.StatusInternalServerError)
		case "/legacy":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"version":"2.0.0","execution_id":"6324554176528384"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/gcs", server.URL+"/legacy")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "2.0.0" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "2.0.0")
	}
}

func TestLatestAntigravityHubVersionFromPrefixesSortsByNumericSemver(t *testing.T) {
	prefixes := []string{
		"antigravity-hub/2.0.9-4666288509943808/",
		"antigravity-hub/2.0.10-5119448496078848/",
		"antigravity-hub/2.0.11-6560309696135168/",
		"antigravity-hub/not-a-version/",
	}

	version, errParse := latestAntigravityHubVersionFromPrefixes(prefixes)
	if errParse != nil {
		t.Fatalf("latestAntigravityHubVersionFromPrefixes() error = %v", errParse)
	}
	if version != "2.0.11" {
		t.Fatalf("latestAntigravityHubVersionFromPrefixes() = %q, want %q", version, "2.0.11")
	}
}

func TestLatestAntigravityHubVersionFromPrefixesIgnoresSignedVersionParts(t *testing.T) {
	prefixes := []string{
		"antigravity-hub/9.+9.9-4666288509943808/",
		"antigravity-hub/2.1.0-6066040229199872/",
	}

	version, errParse := latestAntigravityHubVersionFromPrefixes(prefixes)
	if errParse != nil {
		t.Fatalf("latestAntigravityHubVersionFromPrefixes() error = %v", errParse)
	}
	if version != "2.1.0" {
		t.Fatalf("latestAntigravityHubVersionFromPrefixes() = %q, want %q", version, "2.1.0")
	}
}
