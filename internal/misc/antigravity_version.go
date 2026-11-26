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

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	// antigravityFallbackVersion is the client version reported when the hub
	// manifest has not been fetched yet or cannot be reached. Cloud Code rejects
	// newer models for clients below 2.23.0, so this floor must stay at or above
	// that version.
	antigravityFallbackVersion = "2.35.2"
	antigravityHubPlatform     = "os_type=darwin; arch=arm64"
	antigravityVersionCacheTTL = 6 * time.Hour
	antigravityFetchTimeout    = 10 * time.Second
	AntigravityNodeAPIClientUA = "google-api-nodejs-client/10.3.0"
	AntigravityGoogAPIClientUA = "gl-node/22.21.1"
)

var (
	antigravityHubLatestManifestURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
)

// Changelists belong to specific Hub releases, not to the latest manifest in
// general. Update the fallback release and its entry together. Newer entries
// are verified from official language_server --stamp build metadata. Unmapped
// releases fall back to the highest mapped (version, cl) pair so User-Agent
// never omits cl= or pairs a newer version with an older build's CL.
var antigravityHubChangelists = map[string]string{
	"2.23.0": "998057262",
	"2.24.0": "1000335682",
	"2.29.0": "1013060670",
	"2.32.1": "1021296516",
	"2.32.2": "1022991777",
	"2.33.0": "1023160026",
	"2.34.0": "1026637980",
	"2.35.0": "1028447422",
	"2.35.1": "1029194470",
	"2.35.2": "1029311855",
}

type antigravityHubUpdaterManifest struct {
	Version string `yaml:"version"`
}

var (
	cachedAntigravityVersion        = antigravityFallbackVersion
	antigravityVersionProxyURL      string
	antigravityVersionProxyProvider func() string
	antigravityVersionMu            sync.RWMutex
	antigravityVersionExpiry        time.Time
	antigravityUpdaterCtx           context.Context
	antigravityUpdaterCancel        context.CancelFunc
	antigravityVersionRefreshed     chan struct{}
	antigravityVersionTrigger       <-chan struct{}
)

// StartAntigravityVersionUpdater starts a background goroutine that periodically refreshes the cached antigravity version
// using the inherited process environment proxy settings. If an updater is already active, this call is a no-op so it
// preserves the active updater and its proxy configuration.
// This is intentionally decoupled from request execution to avoid blocking executors on version lookups.
func StartAntigravityVersionUpdater(ctx context.Context) {
	startAntigravityVersionUpdater(ctx, "", nil, false)
}

// StartAntigravityVersionUpdaterWithProxy starts a background goroutine that periodically refreshes the cached
// antigravity version using proxyURL (empty inherits process environment proxies, "direct"/"none" bypasses them).
// A later call cancels any previous updater goroutine and binds the new context and proxy URL.
func StartAntigravityVersionUpdaterWithProxy(ctx context.Context, proxyURL string) {
	startAntigravityVersionUpdater(ctx, proxyURL, nil, true)
}

// StartAntigravityVersionUpdaterWithProxyProvider starts a background goroutine that periodically refreshes the cached
// antigravity version, resolving the current proxy setting dynamically on each fetch via proxyProvider.
// A later call cancels any previous updater goroutine and binds the new context and proxy provider.
func StartAntigravityVersionUpdaterWithProxyProvider(ctx context.Context, proxyProvider func() string) {
	startAntigravityVersionUpdater(ctx, "", proxyProvider, true)
}

func startAntigravityVersionUpdater(ctx context.Context, proxyURL string, proxyProvider func() string, replaceActive bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)

	antigravityVersionMu.Lock()
	if !replaceActive && antigravityUpdaterCtx != nil && antigravityUpdaterCtx.Err() == nil {
		antigravityVersionMu.Unlock()
		cancel()
		return
	}
	if antigravityUpdaterCancel != nil {
		antigravityUpdaterCancel()
	}
	antigravityUpdaterCtx = runCtx
	antigravityUpdaterCancel = cancel
	antigravityVersionProxyURL = strings.TrimSpace(proxyURL)
	antigravityVersionProxyProvider = proxyProvider
	trigger := antigravityVersionTrigger
	antigravityVersionMu.Unlock()

	go runAntigravityVersionUpdater(runCtx, cancel, trigger)
}

func runAntigravityVersionUpdater(ctx context.Context, cancel context.CancelFunc, trigger <-chan struct{}) {
	defer cancel()

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
		case <-trigger:
			refreshAntigravityVersion(ctx)
		}
	}
}

func refreshAntigravityVersion(ctx context.Context) {
	if ctx != nil && ctx.Err() != nil {
		return
	}

	version, errFetch := fetchAntigravityLatestVersion(ctx)
	if ctx != nil && ctx.Err() != nil {
		return
	}

	antigravityVersionMu.Lock()
	if ctx != nil && ctx.Err() != nil {
		antigravityVersionMu.Unlock()
		return
	}

	now := time.Now()

	switch {
	case errFetch == nil:
		cachedAntigravityVersion = version
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		antigravityVersionMu.Unlock()
		log.WithField("version", version).Info("fetched latest antigravity version")
	case cachedAntigravityVersion == "" || now.After(antigravityVersionExpiry):
		cachedAntigravityVersion = antigravityFallbackVersion
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		antigravityVersionMu.Unlock()
		log.WithError(errFetch).Warn("failed to refresh antigravity version, using fallback version")
	default:
		antigravityVersionMu.Unlock()
		log.WithError(errFetch).Debug("failed to refresh antigravity version, keeping cached value")
	}

	notifyAntigravityVersionRefreshed()
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

func resolveAntigravityHubVersionAndChangelist(version string) (string, string) {
	if changelist := antigravityHubChangelists[version]; changelist != "" {
		return version, changelist
	}

	var (
		bestVersion string
		bestCL      string
		bestSemver  [3]int
		hasBest     bool
	)
	for candidateVersion, candidateCL := range antigravityHubChangelists {
		if candidateCL == "" {
			continue
		}
		parsed, ok := parseAntigravitySemVersion(candidateVersion)
		if !ok {
			continue
		}
		if !hasBest || compareAntigravitySemVersion(parsed, bestSemver) > 0 {
			bestVersion = candidateVersion
			bestCL = candidateCL
			bestSemver = parsed
			hasBest = true
		}
	}
	if hasBest {
		return bestVersion, bestCL
	}
	return antigravityFallbackVersion, antigravityHubChangelists[antigravityFallbackVersion]
}

func antigravityEffectiveVersion() string {
	version, _ := resolveAntigravityHubVersionAndChangelist(AntigravityLatestVersion())
	return version
}

// AntigravityUserAgent returns the User-Agent string used by the Antigravity Hub family.
func AntigravityUserAgent() string {
	version, changelist := resolveAntigravityHubVersionAndChangelist(AntigravityLatestVersion())
	metadata := "aidev_client; " + antigravityHubPlatform
	if changelist != "" {
		metadata += "; cl=" + changelist
	}
	return fmt.Sprintf("antigravity/hub/%s (%s)", version, metadata)
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

// AntigravityRequestUserAgent returns the Antigravity runtime UA used by
// generate/stream/model-list requests.
func AntigravityRequestUserAgent(userAgent string) string {
	return antigravityBaseUserAgent(userAgent)
}

// AntigravityLoadCodeAssistUserAgent returns the Antigravity runtime UA used by
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
			return antigravityEffectiveVersion()
		}
		return rest
	}
	const legacyPrefix = "antigravity/"
	if !strings.HasPrefix(lower, legacyPrefix) {
		return antigravityEffectiveVersion()
	}
	rest := base[len(legacyPrefix):]
	if idx := strings.IndexAny(rest, " 	"); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return antigravityEffectiveVersion()
	}
	return rest
}

func resolveAntigravityVersionProxyURL() string {
	antigravityVersionMu.RLock()
	provider := antigravityVersionProxyProvider
	proxyURL := antigravityVersionProxyURL
	antigravityVersionMu.RUnlock()

	if provider != nil {
		return strings.TrimSpace(provider())
	}
	return strings.TrimSpace(proxyURL)
}

func antigravityVersionHTTPClient() (*http.Client, func(), error) {
	proxyURL := resolveAntigravityVersionProxyURL()

	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		return nil, nil, fmt.Errorf("build antigravity version proxy transport (%s): %w", proxyutil.Redact(proxyURL), errBuild)
	}

	client := &http.Client{Timeout: antigravityFetchTimeout}
	if transport != nil {
		client.Transport = transport
		return client, transport.CloseIdleConnections, nil
	}
	return client, nil, nil
}

func fetchAntigravityLatestVersion(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return "", errCtx
	}

	client, closeTransport, errClient := antigravityVersionHTTPClient()
	if errClient != nil {
		return "", errClient
	}
	if closeTransport != nil {
		defer closeTransport()
	}
	return fetchAntigravityHubLatestManifestVersion(ctx, client)
}

func fetchAntigravityHubLatestManifestVersion(ctx context.Context, client *http.Client) (string, error) {
	antigravityVersionMu.RLock()
	manifestURL := antigravityHubLatestManifestURL
	antigravityVersionMu.RUnlock()

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if errReq != nil {
		return "", fmt.Errorf("build antigravity Hub updater manifest request: %w", errReq)
	}
	httpReq.Header.Set("User-Agent", "electron-builder")
	httpReq.Header.Set("Cache-Control", "no-cache")

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return "", fmt.Errorf("fetch antigravity Hub updater manifest: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("antigravity Hub updater manifest response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("antigravity Hub updater manifest returned status %d", resp.StatusCode)
	}

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if errRead != nil {
		return "", fmt.Errorf("read antigravity Hub updater manifest: %w", errRead)
	}

	var manifest antigravityHubUpdaterManifest
	if errDecode := yaml.Unmarshal(raw, &manifest); errDecode != nil {
		return "", fmt.Errorf("decode antigravity Hub updater manifest: %w", errDecode)
	}

	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		return "", errors.New("antigravity Hub updater manifest returned empty version")
	}
	if !isValidAntigravitySemVersion(version) {
		return "", fmt.Errorf("antigravity Hub updater manifest returned invalid version %q", version)
	}
	return version, nil
}

func parseAntigravitySemVersion(version string) ([3]int, bool) {
	var parsed [3]int
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return parsed, false
	}

	for i, part := range parts {
		if part == "" {
			return parsed, false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return parsed, false
			}
		}
		value, errParse := strconv.Atoi(part)
		if errParse != nil {
			return parsed, false
		}
		parsed[i] = value
	}

	return parsed, true
}

func compareAntigravitySemVersion(a, b [3]int) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func isValidAntigravitySemVersion(version string) bool {
	_, ok := parseAntigravitySemVersion(version)
	return ok
}

func notifyAntigravityVersionRefreshed() {
	antigravityVersionMu.RLock()
	ch := antigravityVersionRefreshed
	antigravityVersionMu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// OverrideAntigravityHubManifestURLForTest overrides the Antigravity Hub manifest URL for testing purposes.
func OverrideAntigravityHubManifestURLForTest(url string) func() {
	antigravityVersionMu.Lock()
	old := antigravityHubLatestManifestURL
	antigravityHubLatestManifestURL = url
	antigravityVersionMu.Unlock()
	return func() {
		antigravityVersionMu.Lock()
		antigravityHubLatestManifestURL = old
		antigravityVersionMu.Unlock()
	}
}

// SetAntigravityVersionCacheForTest overrides the cached Antigravity version and expiry for testing purposes.
func SetAntigravityVersionCacheForTest(version string, expiry time.Time) func() {
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

// SetAntigravityVersionRefreshedHookForTest receives one signal after each non-cancelled refresh attempt.
func SetAntigravityVersionRefreshedHookForTest(ch chan struct{}) func() {
	antigravityVersionMu.Lock()
	old := antigravityVersionRefreshed
	antigravityVersionRefreshed = ch
	antigravityVersionMu.Unlock()
	return func() {
		antigravityVersionMu.Lock()
		antigravityVersionRefreshed = old
		antigravityVersionMu.Unlock()
	}
}

// SetAntigravityVersionTriggerForTest installs a trigger channel that drives periodic refreshes in tests.
func SetAntigravityVersionTriggerForTest(ch <-chan struct{}) func() {
	antigravityVersionMu.Lock()
	old := antigravityVersionTrigger
	antigravityVersionTrigger = ch
	antigravityVersionMu.Unlock()
	return func() {
		antigravityVersionMu.Lock()
		antigravityVersionTrigger = old
		antigravityVersionMu.Unlock()
	}
}
