// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
package misc

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	antigravityFallbackVersion = "1.0.8"
	antigravityCLIPlatform     = "darwin/arm64"
	antigravityVersionCacheTTL = 6 * time.Hour
	antigravityFetchTimeout    = 10 * time.Second
	AntigravityNodeAPIClientUA = "google-api-nodejs-client/10.3.0"
	AntigravityGoogAPIClientUA = "gl-node/22.21.1"
)

var (
	antigravityCLIUpdaterBaseURL = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests"
	antigravityCLILatestURL      = "https://storage.googleapis.com/antigravity-public/antigravity-cli/latest"
	antigravityCLIGCSListURL     = "https://storage.googleapis.com/antigravity-public/?prefix=antigravity-cli/&delimiter=/"
)

type antigravityCLIUpdaterManifest struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA512  string `json:"sha512"`
}

type antigravityGCSList struct {
	CommonPrefixes []antigravityGCSPrefix `xml:"CommonPrefixes"`
}

type antigravityGCSPrefix struct {
	Prefix string `xml:"Prefix"`
}

type antigravitySemVersion struct {
	raw   string
	parts [3]int
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
	version, errFetch := fetchAntigravityLatestVersion(ctx)

	antigravityVersionMu.Lock()
	defer antigravityVersionMu.Unlock()

	now := time.Now()

	if errFetch == nil {
		cachedAntigravityVersion = version
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		log.WithField("version", version).Info("fetched latest antigravity version")
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

// AntigravityUserAgent returns the User-Agent string used by the agy CLI family.
func AntigravityUserAgent() string {
	return fmt.Sprintf("antigravity/cli/%s %s", AntigravityLatestVersion(), antigravityCLIPlatform)
}

func isAntigravityFamilyUserAgent(lower string) bool {
	return strings.HasPrefix(lower, "antigravity/cli/") || strings.HasPrefix(lower, "antigravity/")
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

// AntigravityLoadCodeAssistUserAgent returns the long Antigravity control-plane
// UA used by loadCodeAssist requests.
func AntigravityLoadCodeAssistUserAgent(userAgent string) string {
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
	for _, familyPrefix := range []string{"antigravity/cli/", "antigravity/hub/"} {
		if strings.HasPrefix(lower, familyPrefix) {
			rest := base[len(familyPrefix):]
			if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
				rest = rest[:idx]
			}
			rest = strings.TrimSpace(rest)
			if rest == "" {
				return AntigravityLatestVersion()
			}
			return rest
		}
	}
	const legacyPrefix = "antigravity/"
	if !strings.HasPrefix(lower, legacyPrefix) {
		return AntigravityLatestVersion()
	}
	rest := base[len(legacyPrefix):]
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return AntigravityLatestVersion()
	}
	return rest
}

func antigravityCLIUpdaterManifestName() string {
	return strings.ReplaceAll(antigravityCLIPlatform, "/", "_")
}

func fetchAntigravityLatestVersion(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	client := &http.Client{Timeout: antigravityFetchTimeout}

	version, errManifest := fetchAntigravityCLIUpdaterManifestVersion(ctx, client)
	if errManifest == nil {
		return version, nil
	}

	log.WithError(errManifest).Debug("failed to fetch antigravity CLI updater manifest, trying CLI latest pointer")

	version, errLatest := fetchAntigravityCLILatestVersion(ctx, client)
	if errLatest == nil {
		return version, nil
	}

	log.WithError(errLatest).Debug("failed to fetch antigravity CLI latest version, trying CLI GCS prefix list")

	version, errList := fetchAntigravityCLIGCSLatestVersion(ctx, client)
	if errList == nil {
		return version, nil
	}

	return "", fmt.Errorf("fetch antigravity CLI updater manifest: %v; fetch antigravity CLI latest: %v; fetch antigravity CLI GCS version: %w", errManifest, errLatest, errList)
}

func fetchAntigravityCLIUpdaterManifestVersion(ctx context.Context, client *http.Client) (string, error) {
	manifestURL := fmt.Sprintf("%s/%s.json", strings.TrimSuffix(antigravityCLIUpdaterBaseURL, "/"), antigravityCLIUpdaterManifestName())
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if errReq != nil {
		return "", fmt.Errorf("build antigravity CLI updater manifest request: %w", errReq)
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return "", fmt.Errorf("fetch antigravity CLI updater manifest: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("antigravity CLI updater manifest response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("antigravity CLI updater manifest returned status %d", resp.StatusCode)
	}

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if errRead != nil {
		return "", fmt.Errorf("read antigravity CLI updater manifest: %w", errRead)
	}

	var manifest antigravityCLIUpdaterManifest
	if errDecode := json.Unmarshal(raw, &manifest); errDecode != nil {
		return "", fmt.Errorf("decode antigravity CLI updater manifest: %w", errDecode)
	}

	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		return "", errors.New("antigravity CLI updater manifest returned empty version")
	}
	if _, ok := parseAntigravitySemVersion(version); !ok {
		return "", fmt.Errorf("antigravity CLI updater manifest returned invalid version %q", version)
	}
	return version, nil
}

func fetchAntigravityCLILatestVersion(ctx context.Context, client *http.Client) (string, error) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, antigravityCLILatestURL, nil)
	if errReq != nil {
		return "", fmt.Errorf("build antigravity CLI latest request: %w", errReq)
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return "", fmt.Errorf("fetch antigravity CLI latest: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("antigravity CLI latest response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("antigravity CLI latest returned status %d", resp.StatusCode)
	}

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 256))
	if errRead != nil {
		return "", fmt.Errorf("read antigravity CLI latest: %w", errRead)
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		return "", errors.New("antigravity CLI latest returned empty version")
	}
	semVersion, ok := parseAntigravitySemVersion(version)
	if !ok {
		return "", fmt.Errorf("antigravity CLI latest returned invalid version %q", version)
	}
	return semVersion.raw, nil
}

func fetchAntigravityCLIGCSLatestVersion(ctx context.Context, client *http.Client) (string, error) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, antigravityCLIGCSListURL, nil)
	if errReq != nil {
		return "", fmt.Errorf("build antigravity CLI GCS request: %w", errReq)
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return "", fmt.Errorf("fetch antigravity CLI GCS list: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("antigravity CLI GCS response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("antigravity CLI GCS list returned status %d", resp.StatusCode)
	}

	var list antigravityGCSList
	if errDecode := xml.NewDecoder(resp.Body).Decode(&list); errDecode != nil {
		return "", fmt.Errorf("decode antigravity CLI GCS list: %w", errDecode)
	}

	prefixes := make([]string, 0, len(list.CommonPrefixes))
	for _, commonPrefix := range list.CommonPrefixes {
		prefixes = append(prefixes, commonPrefix.Prefix)
	}

	return latestAntigravityCLIVersionFromPrefixes(prefixes)
}

func latestAntigravityCLIVersionFromPrefixes(prefixes []string) (string, error) {
	var best antigravitySemVersion
	found := false

	for _, prefix := range prefixes {
		version, ok := antigravityCLIVersionFromPrefix(prefix)
		if !ok {
			continue
		}
		semVersion, ok := parseAntigravitySemVersion(version)
		if !ok {
			continue
		}
		if !found || compareAntigravitySemVersion(semVersion, best) > 0 {
			best = semVersion
			found = true
		}
	}

	if !found {
		return "", errors.New("antigravity-cli GCS list contained no version prefixes")
	}

	return best.raw, nil
}

func antigravityCLIVersionFromPrefix(prefix string) (string, bool) {
	const cliPrefix = "antigravity-cli/"
	prefix = strings.TrimSpace(prefix)
	prefix = strings.TrimSuffix(prefix, "/")
	if !strings.HasPrefix(prefix, cliPrefix) {
		return "", false
	}

	name := strings.TrimPrefix(prefix, cliPrefix)
	if name == "latest" || name == "test" || name == "tools" || strings.HasPrefix(name, "v") {
		return "", false
	}

	separator := strings.LastIndex(name, "-")
	if separator > 0 && separator < len(name)-1 {
		version := strings.TrimSpace(name[:separator])
		executionID := name[separator+1:]
		if version != "" && executionID != "" {
			allDigits := true
			for _, ch := range executionID {
				if ch < '0' || ch > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				if _, ok := parseAntigravitySemVersion(version); ok {
					return version, true
				}
			}
		}
	}

	version := strings.TrimSpace(name)
	if version == "" {
		return "", false
	}
	if _, ok := parseAntigravitySemVersion(version); !ok {
		return "", false
	}
	return version, true
}

func parseAntigravitySemVersion(version string) (antigravitySemVersion, bool) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return antigravitySemVersion{}, false
	}

	semVersion := antigravitySemVersion{raw: version}
	for i, part := range parts {
		if part == "" {
			return antigravitySemVersion{}, false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return antigravitySemVersion{}, false
			}
		}
		value, errParse := strconv.Atoi(part)
		if errParse != nil {
			return antigravitySemVersion{}, false
		}
		semVersion.parts[i] = value
	}

	return semVersion, true
}

func compareAntigravitySemVersion(left antigravitySemVersion, right antigravitySemVersion) int {
	for i := range left.parts {
		if left.parts[i] > right.parts[i] {
			return 1
		}
		if left.parts[i] < right.parts[i] {
			return -1
		}
	}
	return 0
}
