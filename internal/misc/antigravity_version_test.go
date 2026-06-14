package misc

import (
	"testing"
	"time"
)

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

func TestAntigravityLatestVersionUsesFallback(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restore()

	version := AntigravityLatestVersion()
	if version != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, antigravityFallbackVersion)
	}
}

func TestAntigravityLatestVersionReturnsCachedVersion(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.0.11", time.Now().Add(time.Hour))
	defer restore()

	version := AntigravityLatestVersion()
	if version != "2.0.11" {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, "2.0.11")
	}
}

func TestAntigravityLatestVersionExpiresCache(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.0.11", time.Now().Add(-time.Hour))
	defer restore()

	version := AntigravityLatestVersion()
	if version != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q (expired cache)", version, antigravityFallbackVersion)
	}
}

func TestAntigravityUserAgent(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.1.0", time.Now().Add(time.Hour))
	defer restore()

	ua := AntigravityUserAgent()
	if ua == "" {
		t.Fatal("AntigravityUserAgent() returned empty string")
	}
}

func TestAntigravityVersionFromUserAgent(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		want      string
	}{
		{
			name:      "short form",
			userAgent: "antigravity/2.1.0",
			want:      "2.1.0",
		},
		{
			name:      "long form with goog",
			userAgent: "antigravity/2.1.0 goog",
			want:      "2.1.0",
		},
		{
			name:      "fallback on empty",
			userAgent: "",
			want:      "1.0.0", // uses AntigravityLatestVersion which returns cached value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := overrideAntigravityVersionCacheForTest(t, "1.0.0", time.Now().Add(time.Hour))
			defer restore()

			got := AntigravityVersionFromUserAgent(tt.userAgent)
			if got != tt.want {
				t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}