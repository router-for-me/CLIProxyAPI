package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	defaultClaudeFingerprintUserAgent      = "claude-cli/2.1.63 (external, cli)"
	defaultClaudeFingerprintPackageVersion = "0.74.0"
	defaultClaudeFingerprintRuntimeVersion = "v24.3.0"
	defaultClaudeFingerprintOS             = "MacOS"
	defaultClaudeFingerprintArch           = "arm64"
	claudeDeviceProfileTTL                 = 7 * 24 * time.Hour
	claudeDeviceProfileCleanupPeriod       = time.Hour
)

var (
	claudeCLIVersionPattern = regexp.MustCompile(`^claude-cli/(\d+)\.(\d+)\.(\d+)`)

	claudeDeviceProfileCache            = make(map[string]claudeDeviceProfileCacheEntry)
	claudeDeviceProfileCacheMu          sync.RWMutex
	claudeDeviceProfileCacheCleanupOnce sync.Once

	claudeDeviceProfileBeforeCandidateStore func(claudeDeviceProfile)
)

type claudeCLIVersion struct {
	major int
	minor int
	patch int
}

func (v claudeCLIVersion) Compare(other claudeCLIVersion) int {
	switch {
	case v.major != other.major:
		if v.major > other.major {
			return 1
		}
		return -1
	case v.minor != other.minor:
		if v.minor > other.minor {
			return 1
		}
		return -1
	case v.patch != other.patch:
		if v.patch > other.patch {
			return 1
		}
		return -1
	default:
		return 0
	}
}

type claudeDeviceProfile struct {
	UserAgent      string
	PackageVersion string
	RuntimeVersion string
	OS             string
	Arch           string
	Version        claudeCLIVersion
}

type claudeDeviceProfileCacheEntry struct {
	profile claudeDeviceProfile
	expire  time.Time
}

func claudeDeviceProfileStabilizationEnabled(cfg *config.Config) bool {
	if cfg == nil || cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile == nil {
		return false
	}
	return *cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile
}

func defaultClaudeDeviceProfile(cfg *config.Config) claudeDeviceProfile {
	hdrDefault := func(cfgVal, fallback string) string {
		if strings.TrimSpace(cfgVal) != "" {
			return strings.TrimSpace(cfgVal)
		}
		return fallback
	}

	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}

	profile := claudeDeviceProfile{
		UserAgent:      hdrDefault(hd.UserAgent, defaultClaudeFingerprintUserAgent),
		PackageVersion: hdrDefault(hd.PackageVersion, defaultClaudeFingerprintPackageVersion),
		RuntimeVersion: hdrDefault(hd.RuntimeVersion, defaultClaudeFingerprintRuntimeVersion),
		OS:             hdrDefault(hd.OS, defaultClaudeFingerprintOS),
		Arch:           hdrDefault(hd.Arch, defaultClaudeFingerprintArch),
	}
	if version, ok := parseClaudeCLIVersion(profile.UserAgent); ok {
		profile.Version = version
	}
	return profile
}

// mapStainlessOS maps runtime.GOOS to Stainless SDK OS names.
func mapStainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	default:
		return "Other::" + runtime.GOOS
	}
}

// mapStainlessArch maps runtime.GOARCH to Stainless SDK architecture names.
func mapStainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return "other::" + runtime.GOARCH
	}
}

func parseClaudeCLIVersion(userAgent string) (claudeCLIVersion, bool) {
	matches := claudeCLIVersionPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if len(matches) != 4 {
		return claudeCLIVersion{}, false
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return claudeCLIVersion{}, false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return claudeCLIVersion{}, false
	}
	patch, err := strconv.Atoi(matches[3])
	if err != nil {
		return claudeCLIVersion{}, false
	}
	return claudeCLIVersion{major: major, minor: minor, patch: patch}, true
}

func shouldUpgradeClaudeDeviceProfile(candidate, current claudeDeviceProfile) bool {
	if candidate.UserAgent == "" {
		return false
	}
	if current.UserAgent == "" {
		return true
	}
	if current.Version == (claudeCLIVersion{}) {
		return false
	}
	return candidate.Version.Compare(current.Version) > 0
}

func extractClaudeDeviceProfile(headers http.Header, cfg *config.Config) (claudeDeviceProfile, bool) {
	if headers == nil {
		return claudeDeviceProfile{}, false
	}

	userAgent := strings.TrimSpace(headers.Get("User-Agent"))
	version, ok := parseClaudeCLIVersion(userAgent)
	if !ok {
		return claudeDeviceProfile{}, false
	}

	baseline := defaultClaudeDeviceProfile(cfg)
	profile := claudeDeviceProfile{
		UserAgent:      userAgent,
		PackageVersion: firstNonEmptyHeader(headers, "X-Stainless-Package-Version", baseline.PackageVersion),
		RuntimeVersion: firstNonEmptyHeader(headers, "X-Stainless-Runtime-Version", baseline.RuntimeVersion),
		OS:             firstNonEmptyHeader(headers, "X-Stainless-Os", baseline.OS),
		Arch:           firstNonEmptyHeader(headers, "X-Stainless-Arch", baseline.Arch),
		Version:        version,
	}
	return profile, true
}

func firstNonEmptyHeader(headers http.Header, name, fallback string) string {
	if headers == nil {
		return fallback
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	return fallback
}

func claudeDeviceProfileScopeKey(auth *cliproxyauth.Auth, apiKey string) string {
	switch {
	case auth != nil && strings.TrimSpace(auth.ID) != "":
		return "auth:" + strings.TrimSpace(auth.ID)
	case strings.TrimSpace(apiKey) != "":
		return "api_key:" + strings.TrimSpace(apiKey)
	default:
		return "global"
	}
}

func claudeDeviceProfileCacheKey(auth *cliproxyauth.Auth, apiKey string) string {
	sum := sha256.Sum256([]byte(claudeDeviceProfileScopeKey(auth, apiKey)))
	return hex.EncodeToString(sum[:])
}

func startClaudeDeviceProfileCacheCleanup() {
	go func() {
		ticker := time.NewTicker(claudeDeviceProfileCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredClaudeDeviceProfiles()
		}
	}()
}

func purgeExpiredClaudeDeviceProfiles() {
	now := time.Now()
	claudeDeviceProfileCacheMu.Lock()
	for key, entry := range claudeDeviceProfileCache {
		if !entry.expire.After(now) {
			delete(claudeDeviceProfileCache, key)
		}
	}
	claudeDeviceProfileCacheMu.Unlock()
}

func resolveClaudeDeviceProfile(auth *cliproxyauth.Auth, apiKey string, headers http.Header, cfg *config.Config) claudeDeviceProfile {
	claudeDeviceProfileCacheCleanupOnce.Do(startClaudeDeviceProfileCacheCleanup)

	cacheKey := claudeDeviceProfileCacheKey(auth, apiKey)
	now := time.Now()
	baseline := defaultClaudeDeviceProfile(cfg)
	candidate, hasCandidate := extractClaudeDeviceProfile(headers, cfg)
	if hasCandidate && !shouldUpgradeClaudeDeviceProfile(candidate, baseline) {
		hasCandidate = false
	}

	claudeDeviceProfileCacheMu.RLock()
	entry, hasCached := claudeDeviceProfileCache[cacheKey]
	cachedValid := hasCached && entry.expire.After(now) && entry.profile.UserAgent != ""
	claudeDeviceProfileCacheMu.RUnlock()

	if hasCandidate {
		if claudeDeviceProfileBeforeCandidateStore != nil {
			claudeDeviceProfileBeforeCandidateStore(candidate)
		}

		claudeDeviceProfileCacheMu.Lock()
		entry, hasCached = claudeDeviceProfileCache[cacheKey]
		cachedValid = hasCached && entry.expire.After(now) && entry.profile.UserAgent != ""
		if cachedValid && !shouldUpgradeClaudeDeviceProfile(candidate, entry.profile) {
			entry.expire = now.Add(claudeDeviceProfileTTL)
			claudeDeviceProfileCache[cacheKey] = entry
			claudeDeviceProfileCacheMu.Unlock()
			return entry.profile
		}

		claudeDeviceProfileCache[cacheKey] = claudeDeviceProfileCacheEntry{
			profile: candidate,
			expire:  now.Add(claudeDeviceProfileTTL),
		}
		claudeDeviceProfileCacheMu.Unlock()
		return candidate
	}

	if cachedValid {
		claudeDeviceProfileCacheMu.Lock()
		entry = claudeDeviceProfileCache[cacheKey]
		if entry.expire.After(now) && entry.profile.UserAgent != "" {
			entry.expire = now.Add(claudeDeviceProfileTTL)
			claudeDeviceProfileCache[cacheKey] = entry
			claudeDeviceProfileCacheMu.Unlock()
			return entry.profile
		}
		claudeDeviceProfileCacheMu.Unlock()
	}

	return baseline
}

func applyClaudeDeviceProfileHeaders(r *http.Request, profile claudeDeviceProfile) {
	if r == nil {
		return
	}
	for _, headerName := range []string{
		"User-Agent",
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
		"X-Stainless-Os",
		"X-Stainless-Arch",
	} {
		r.Header.Del(headerName)
	}
	r.Header.Set("User-Agent", profile.UserAgent)
	r.Header.Set("X-Stainless-Package-Version", profile.PackageVersion)
	r.Header.Set("X-Stainless-Runtime-Version", profile.RuntimeVersion)
	r.Header.Set("X-Stainless-Os", profile.OS)
	r.Header.Set("X-Stainless-Arch", profile.Arch)
}

func applyClaudeLegacyDeviceHeaders(r *http.Request, ginHeaders http.Header, cfg *config.Config) {
	if r == nil {
		return
	}
	profile := defaultClaudeDeviceProfile(cfg)
	miscEnsure := func(name, fallback string) {
		if strings.TrimSpace(r.Header.Get(name)) != "" {
			return
		}
		if strings.TrimSpace(ginHeaders.Get(name)) != "" {
			r.Header.Set(name, strings.TrimSpace(ginHeaders.Get(name)))
			return
		}
		r.Header.Set(name, fallback)
	}

	miscEnsure("X-Stainless-Runtime-Version", profile.RuntimeVersion)
	miscEnsure("X-Stainless-Package-Version", profile.PackageVersion)
	miscEnsure("X-Stainless-Os", mapStainlessOS())
	miscEnsure("X-Stainless-Arch", mapStainlessArch())

	// Legacy mode preserves per-auth custom header overrides. By the time we get
	// here, ApplyCustomHeadersFromAttrs has already populated r.Header.
	if strings.TrimSpace(r.Header.Get("User-Agent")) != "" {
		return
	}

	clientUA := ""
	if ginHeaders != nil {
		clientUA = strings.TrimSpace(ginHeaders.Get("User-Agent"))
	}
	if isClaudeCodeClient(clientUA) {
		r.Header.Set("User-Agent", clientUA)
		return
	}
	r.Header.Set("User-Agent", profile.UserAgent)
}
