// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
package misc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	antigravityReleasesURL     = "https://antigravity-auto-updater-974169037036.us-central1.run.app/releases"
	antigravityFallbackVersion = "1.22.2"
	antigravityVersionCacheTTL = 6 * time.Hour
	antigravityFetchTimeout    = 10 * time.Second
)

type antigravityRelease struct {
	Version     string `json:"version"`
	ExecutionID string `json:"execution_id"`
}

var (
	cachedAntigravityVersion string
	antigravityVersionMu     sync.RWMutex
	antigravityVersionExpiry time.Time
)

// AntigravityLatestVersion returns the latest antigravity version from the releases API.
// It caches the result for antigravityVersionCacheTTL and falls back to antigravityFallbackVersion
// if the fetch fails.
func AntigravityLatestVersion() string {
	antigravityVersionMu.RLock()
	if cachedAntigravityVersion != "" && time.Now().Before(antigravityVersionExpiry) {
		v := cachedAntigravityVersion
		antigravityVersionMu.RUnlock()
		return v
	}
	antigravityVersionMu.RUnlock()

	antigravityVersionMu.Lock()
	defer antigravityVersionMu.Unlock()

	// Double-check after acquiring write lock.
	if cachedAntigravityVersion != "" && time.Now().Before(antigravityVersionExpiry) {
		return cachedAntigravityVersion
	}

	version := fetchAntigravityLatestVersion()
	cachedAntigravityVersion = version
	antigravityVersionExpiry = time.Now().Add(antigravityVersionCacheTTL)
	return version
}

// AntigravityUserAgent returns the User-Agent string for antigravity requests
// using the latest version fetched from the releases API.
func AntigravityUserAgent() string {
	return fmt.Sprintf("antigravity/%s darwin/arm64", AntigravityLatestVersion())
}

func fetchAntigravityLatestVersion() string {
	client := &http.Client{Timeout: antigravityFetchTimeout}
	resp, err := client.Get(antigravityReleasesURL)
	if err != nil {
		log.WithError(err).Warn("failed to fetch antigravity releases, using fallback version")
		return antigravityFallbackVersion
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.WithField("status", resp.StatusCode).Warn("antigravity releases API returned non-200, using fallback version")
		return antigravityFallbackVersion
	}

	var releases []antigravityRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		log.WithError(err).Warn("failed to decode antigravity releases response, using fallback version")
		return antigravityFallbackVersion
	}

	if len(releases) == 0 {
		log.Warn("antigravity releases API returned empty list, using fallback version")
		return antigravityFallbackVersion
	}

	version := releases[0].Version
	if version == "" {
		log.Warn("antigravity releases API returned empty version, using fallback version")
		return antigravityFallbackVersion
	}

	log.WithField("version", version).Info("fetched latest antigravity version")
	return version
}
