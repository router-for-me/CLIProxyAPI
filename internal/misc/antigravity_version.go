// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
package misc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	// antigravityFallbackVersion is the client version reported when the hub
	// manifest has not been fetched yet or cannot be reached. Cloud Code rejects
	// newer models for clients below 2.9.0, so this floor must stay at or above
	// that version.
	antigravityFallbackVersion = "2.9.1"
	antigravityHubPlatform     = "darwin/arm64"
	antigravityVersionCacheTTL = 6 * time.Hour
	antigravityFetchTimeout    = 10 * time.Second
	AntigravityNodeAPIClientUA = "google-api-nodejs-client/10.3.0"
	AntigravityGoogAPIClientUA = "gl-node/22.21.1"
)

var (
	antigravityHubLatestManifestURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
)

type antigravityHubUpdaterManifest struct {
	Version string `yaml:"version"`
	// StagingPercentage is kept as a raw node because upstream manifests are not
	// guaranteed to use a plain integer, and a malformed value must not discard an
	// otherwise valid version. See parseAntigravityStagingPercentage.
	StagingPercentage yaml.Node `yaml:"stagingPercentage"`
}

// antigravityVersionCandidate is a parsed Hub updater manifest after validation.
type antigravityVersionCandidate struct {
	version           string
	stagingPercentage *int
}

var (
	cachedAntigravityVersion = antigravityFallbackVersion
	antigravityVersionMu     sync.RWMutex
	antigravityVersionExpiry time.Time
	antigravityUpdaterOnce   sync.Once
)

// StartAntigravityVersionUpdater starts a background goroutine that periodically refreshes the cached antigravity version.
// This is intentionally decoupled from request execution to avoid blocking executors on version lookups.
func StartAntigravityVersionUpdater(ctx context.Context) {
	antigravityUpdaterOnce.Do(func() {
		go runAntigravityVersionUpdater(ctx)
	})
}

func runAntigravityVersionUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(antigravityVersionCacheTTL / 2)
	defer ticker.Stop()

	log.Infof("periodic antigravity version refresh started (interval=%s)", antigravityVersionCacheTTL/2)

	refreshAntigravityVersion(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshAntigravityVersion(ctx)
		}
	}
}

func refreshAntigravityVersion(ctx context.Context) {
	candidate, errFetch := fetchAntigravityLatestVersion(ctx)

	antigravityVersionMu.Lock()
	defer antigravityVersionMu.Unlock()

	now := time.Now()

	if errFetch == nil {
		// Align with electron-updater staged rollout: clients always see the latest
		// manifest version, but only fully released builds are adopted for UA spoofing.
		released, stagingPercentage := antigravityManifestIsClientReleased(candidate.stagingPercentage)
		if !released {
			antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
			fields := log.Fields{
				"version":            candidate.version,
				"staging_percentage": stagingPercentage,
				"cached_version":     cachedAntigravityVersion,
			}
			if cachedAntigravityVersion == "" {
				cachedAntigravityVersion = antigravityFallbackVersion
				fields["cached_version"] = cachedAntigravityVersion
			}
			log.WithFields(fields).Info("antigravity Hub latest is still staged; keeping client-aligned version")
			return
		}

		cachedAntigravityVersion = candidate.version
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		log.WithField("version", candidate.version).Info("fetched latest antigravity version")
		return
	}

	if cachedAntigravityVersion == "" || now.After(antigravityVersionExpiry) {
		cachedAntigravityVersion = antigravityFallbackVersion
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		log.WithError(errFetch).Warn("failed to refresh antigravity version, using fallback version")
		return
	}

	log.WithError(errFetch).Debug("failed to refresh antigravity version, keeping cached value")
}

// antigravityManifestIsClientReleased reports whether a Hub updater manifest should
// be treated as generally available to real clients, along with the effective rollout
// percentage behind the decision (100 when the manifest omits stagingPercentage).
// Returning the percentage keeps callers from dereferencing the pointer themselves.
//
// electron-updater always fetches the latest.yml entry, but only installs it when
// stagingPercentage is absent/fully rolled out, or the local .updaterId is inside
// the staged cohort. CPA cannot honestly claim membership in a minority staged
// cohort for all traffic, so only fully released versions are used for UA imitation.
func antigravityManifestIsClientReleased(stagingPercentage *int) (bool, int) {
	if stagingPercentage == nil {
		return true, 100
	}
	return *stagingPercentage >= 100, *stagingPercentage
}

// parseAntigravityStagingPercentage interprets the manifest stagingPercentage field
// the way electron-updater does: the raw value goes through an integer parse, and an
// unusable value is warned about rather than treated as fatal. Returning nil means
// "no staged rollout", i.e. the version is fully released.
//
// Values are clamped instead of rejected because electron-updater compares a
// per-install ratio against stagingPercentage/100: anything above 100 matches every
// client, and anything below 0 matches none. Rejecting the manifest instead would
// throw away a version that already passed validation.
func parseAntigravityStagingPercentage(node yaml.Node) *int {
	// Kind 0 means the key was absent; !!null means it was present but empty.
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil
	}

	raw := strings.TrimSpace(node.Value)
	if raw == "" {
		log.Warn("antigravity Hub updater manifest has a non-scalar stagingPercentage; treating version as fully released")
		return nil
	}

	parsed, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil {
		log.WithField("staging_percentage", raw).Warn("antigravity Hub updater manifest has an unparsable stagingPercentage; treating version as fully released")
		return nil
	}

	percentage := int(parsed)
	switch {
	case percentage > 100:
		log.WithField("staging_percentage", raw).Warn("antigravity Hub updater manifest stagingPercentage above 100; clamping to 100")
		percentage = 100
	case percentage < 0:
		log.WithField("staging_percentage", raw).Warn("antigravity Hub updater manifest stagingPercentage below 0; clamping to 0")
		percentage = 0
	}

	return &percentage
}

// AntigravityLatestVersion returns the cached antigravity version refreshed by StartAntigravityVersionUpdater.
// It falls back to antigravityFallbackVersion if the cache is empty or stale.
func AntigravityLatestVersion() string {
	antigravityVersionMu.RLock()
	if cachedAntigravityVersion != "" && time.Now().Before(antigravityVersionExpiry) {
		v := cachedAntigravityVersion
		antigravityVersionMu.RUnlock()
		return v
	}
	antigravityVersionMu.RUnlock()

	return antigravityFallbackVersion
}

// AntigravityUserAgent returns the User-Agent string used by the Antigravity Hub family.
func AntigravityUserAgent() string {
	return fmt.Sprintf("antigravity/hub/%s %s", AntigravityLatestVersion(), antigravityHubPlatform)
}

func isAntigravityFamilyUserAgent(lower string) bool {
	return strings.HasPrefix(lower, "antigravity/hub/") || strings.HasPrefix(lower, "antigravity/")
}

func antigravityBaseUserAgent(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return AntigravityUserAgent()
	}
	lower := strings.ToLower(userAgent)
	if isAntigravityFamilyUserAgent(lower) {
		if idx := strings.Index(lower, " google-api-nodejs-client/"); idx >= 0 {
			trimmed := strings.TrimSpace(userAgent[:idx])
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return userAgent
}

// AntigravityRequestUserAgent returns the short Antigravity runtime UA used by
// generate/stream/model-list requests.
func AntigravityRequestUserAgent(userAgent string) string {
	return antigravityBaseUserAgent(userAgent)
}

// AntigravityLoadCodeAssistUserAgent returns the short Antigravity UA used by
// loadCodeAssist requests.
func AntigravityLoadCodeAssistUserAgent(userAgent string) string {
	return AntigravityRequestUserAgent(userAgent)
}

// AntigravityOnboardUserUserAgent returns the long Antigravity control-plane UA
// used by onboardUser requests.
func AntigravityOnboardUserUserAgent(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return AntigravityUserAgent() + " " + AntigravityNodeAPIClientUA
	}
	lower := strings.ToLower(userAgent)
	if !isAntigravityFamilyUserAgent(lower) {
		return userAgent
	}
	if strings.Contains(lower, "google-api-nodejs-client/") {
		return userAgent
	}
	return antigravityBaseUserAgent(userAgent) + " " + AntigravityNodeAPIClientUA
}

// AntigravityVersionFromUserAgent extracts the Antigravity version prefix from
// either the short or long Antigravity UA forms.
func AntigravityVersionFromUserAgent(userAgent string) string {
	base := antigravityBaseUserAgent(userAgent)
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, "antigravity/hub/") {
		rest := base[len("antigravity/hub/"):]
		if idx := strings.IndexAny(rest, " 	"); idx >= 0 {
			rest = rest[:idx]
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return AntigravityLatestVersion()
		}
		return rest
	}
	const legacyPrefix = "antigravity/"
	if !strings.HasPrefix(lower, legacyPrefix) {
		return AntigravityLatestVersion()
	}
	rest := base[len(legacyPrefix):]
	if idx := strings.IndexAny(rest, " 	"); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return AntigravityLatestVersion()
	}
	return rest
}

func fetchAntigravityLatestVersion(ctx context.Context) (antigravityVersionCandidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	client := &http.Client{Timeout: antigravityFetchTimeout}
	return fetchAntigravityHubLatestManifestVersion(ctx, client)
}

func fetchAntigravityHubLatestManifestVersion(ctx context.Context, client *http.Client) (antigravityVersionCandidate, error) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, antigravityHubLatestManifestURL, nil)
	if errReq != nil {
		return antigravityVersionCandidate{}, fmt.Errorf("build antigravity Hub updater manifest request: %w", errReq)
	}
	httpReq.Header.Set("User-Agent", "electron-builder")
	httpReq.Header.Set("Cache-Control", "no-cache")

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return antigravityVersionCandidate{}, fmt.Errorf("fetch antigravity Hub updater manifest: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("antigravity Hub updater manifest response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return antigravityVersionCandidate{}, fmt.Errorf("antigravity Hub updater manifest returned status %d", resp.StatusCode)
	}

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if errRead != nil {
		return antigravityVersionCandidate{}, fmt.Errorf("read antigravity Hub updater manifest: %w", errRead)
	}

	var manifest antigravityHubUpdaterManifest
	if errDecode := yaml.Unmarshal(raw, &manifest); errDecode != nil {
		return antigravityVersionCandidate{}, fmt.Errorf("decode antigravity Hub updater manifest: %w", errDecode)
	}

	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		return antigravityVersionCandidate{}, errors.New("antigravity Hub updater manifest returned empty version")
	}
	if !isValidAntigravitySemVersion(version) {
		return antigravityVersionCandidate{}, fmt.Errorf("antigravity Hub updater manifest returned invalid version %q", version)
	}
	return antigravityVersionCandidate{
		version:           version,
		stagingPercentage: parseAntigravityStagingPercentage(manifest.StagingPercentage),
	}, nil
}

func isValidAntigravitySemVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}

	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}

	return true
}
