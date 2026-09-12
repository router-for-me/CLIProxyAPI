package helps

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// The Claude Code CLI version an operator pins in claude-header-defaults.user-agent
// is a floor, not a lock (see plausibleClaudeCLIVersion). That makes an outdated pin
// harmless for pass-through, but it also makes it invisible: nothing else in the
// request path tells the operator that every client on a credential has moved past
// the configured constant, which is still what cloaked requests present upstream.
//
// This registry records the Claude Code CLI versions actually observed per
// credential so the mismatch can be warned about once and inspected later through
// GET /v0/management/claude-client-versions.

const claudeClientVersionsPerCredentialLimit = 32

type claudeClientVersionObservation struct {
	version   claudeCLIVersion
	userAgent string
	requests  int64
	firstSeen time.Time
	lastSeen  time.Time
	warned    bool
}

type claudeClientVersionCredential struct {
	label        string
	requests     int64
	observations map[string]*claudeClientVersionObservation
}

var (
	claudeClientVersionsMu sync.Mutex
	claudeClientVersions   = make(map[string]*claudeClientVersionCredential)
)

// ClaudeClientVersionObservationReport is one observed CLI version on a credential.
type ClaudeClientVersionObservationReport struct {
	Version   string `json:"version"`
	UserAgent string `json:"user_agent"`
	Requests  int64  `json:"requests"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	// AtOrAboveConfigured is false for a client older than the configured
	// baseline; those requests are cloaked rather than passed through.
	AtOrAboveConfigured bool `json:"at_or_above_configured"`
}

// ClaudeClientVersionCredentialReport summarizes one credential's observed clients.
type ClaudeClientVersionCredentialReport struct {
	Credential             string                                 `json:"credential"`
	Requests               int64                                  `json:"requests"`
	HighestObservedVersion string                                 `json:"highest_observed_version"`
	HighestObservedAgent   string                                 `json:"highest_observed_user_agent"`
	Mismatched             bool                                   `json:"mismatched"`
	ObservedVersions       []ClaudeClientVersionObservationReport `json:"observed_versions"`
}

// ClaudeClientVersionsReport is the payload of GET /v0/management/claude-client-versions.
type ClaudeClientVersionsReport struct {
	ConfiguredUserAgent string                                `json:"configured_user_agent"`
	ConfiguredVersion   string                                `json:"configured_version"`
	ConfigKey           string                                `json:"config_key"`
	Credentials         []ClaudeClientVersionCredentialReport `json:"credentials"`
}

// ClaudeClientVersionConfigKey names the setting an operator edits to move the pin.
const ClaudeClientVersionConfigKey = "claude-header-defaults.user-agent"

// ResetClaudeClientVersionRegistry clears every recorded observation. Tests only.
func ResetClaudeClientVersionRegistry() {
	claudeClientVersionsMu.Lock()
	claudeClientVersions = make(map[string]*claudeClientVersionCredential)
	claudeClientVersionsMu.Unlock()
}

func claudeClientVersionLabel(auth *cliproxyauth.Auth, apiKey string) string {
	if auth != nil && strings.TrimSpace(auth.ID) != "" {
		return "auth:" + strings.TrimSpace(auth.ID)
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		return "api-key:" + maskClaudeClientVersionSecret(key)
	}
	return "global"
}

// maskClaudeClientVersionSecret keeps the management response from echoing a
// usable credential back to the caller.
func maskClaudeClientVersionSecret(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// ObserveClaudeClientVersion records the Claude Code CLI version carried by an
// incoming request and reports whether the caller should emit the operator
// warning. It returns true at most once per (credential, version) pair, and only
// when the observed version differs from the configured baseline.
func ObserveClaudeClientVersion(auth *cliproxyauth.Auth, apiKey string, headers http.Header, cfg *config.Config) bool {
	userAgent := strings.TrimSpace(headerValue(headers, "User-Agent"))
	if !claudeCodeNativeUserAgentPattern.MatchString(userAgent) {
		return false
	}
	version, okVersion := parseClaudeCLIVersion(userAgent)
	if !okVersion {
		return false
	}
	baseline := defaultClaudeDeviceProfile(cfg)
	if !baseline.hasVersion {
		return false
	}

	scope := claudeDeviceProfileScopeKey(auth, apiKey)
	now := time.Now().UTC()

	claudeClientVersionsMu.Lock()
	defer claudeClientVersionsMu.Unlock()

	credential, found := claudeClientVersions[scope]
	if !found {
		credential = &claudeClientVersionCredential{
			label:        claudeClientVersionLabel(auth, apiKey),
			observations: make(map[string]*claudeClientVersionObservation),
		}
		claudeClientVersions[scope] = credential
	}
	credential.requests++

	key := version.String()
	observation, seen := credential.observations[key]
	if !seen {
		// Bound the per-credential map so a client spraying fabricated versions
		// cannot grow the registry without limit.
		if len(credential.observations) >= claudeClientVersionsPerCredentialLimit {
			return false
		}
		observation = &claudeClientVersionObservation{
			version:   version,
			userAgent: userAgent,
			firstSeen: now,
		}
		credential.observations[key] = observation
	}
	observation.requests++
	observation.lastSeen = now

	if version.Compare(baseline.version) == 0 || observation.warned {
		return false
	}
	observation.warned = true
	return true
}

// ClaudeClientVersionReport renders the registry alongside the configured baseline.
func ClaudeClientVersionReport(cfg *config.Config) ClaudeClientVersionsReport {
	baseline := defaultClaudeDeviceProfile(cfg)
	report := ClaudeClientVersionsReport{
		ConfiguredUserAgent: baseline.UserAgent,
		ConfigKey:           ClaudeClientVersionConfigKey,
		Credentials:         []ClaudeClientVersionCredentialReport{},
	}
	if baseline.hasVersion {
		report.ConfiguredVersion = baseline.version.String()
	}

	claudeClientVersionsMu.Lock()
	defer claudeClientVersionsMu.Unlock()

	for _, credential := range claudeClientVersions {
		entry := ClaudeClientVersionCredentialReport{
			Credential:       credential.label,
			Requests:         credential.requests,
			ObservedVersions: []ClaudeClientVersionObservationReport{},
		}
		var highest *claudeClientVersionObservation
		for _, observation := range credential.observations {
			entry.ObservedVersions = append(entry.ObservedVersions, ClaudeClientVersionObservationReport{
				Version:             observation.version.String(),
				UserAgent:           observation.userAgent,
				Requests:            observation.requests,
				FirstSeen:           observation.firstSeen.Format(time.RFC3339),
				LastSeen:            observation.lastSeen.Format(time.RFC3339),
				AtOrAboveConfigured: baseline.hasVersion && observation.version.Compare(baseline.version) >= 0,
			})
			if highest == nil || observation.version.Compare(highest.version) > 0 {
				highest = observation
			}
		}
		sort.Slice(entry.ObservedVersions, func(i, j int) bool {
			return entry.ObservedVersions[i].Version < entry.ObservedVersions[j].Version
		})
		if highest != nil {
			entry.HighestObservedVersion = highest.version.String()
			entry.HighestObservedAgent = highest.userAgent
			entry.Mismatched = baseline.hasVersion && highest.version.Compare(baseline.version) != 0
		}
		report.Credentials = append(report.Credentials, entry)
	}
	sort.Slice(report.Credentials, func(i, j int) bool {
		return report.Credentials[i].Credential < report.Credentials[j].Credential
	})
	return report
}

// ConfiguredClaudeUserAgent returns the operator-configured Claude Code
// User-Agent baseline, for log messages that tell the operator what to change.
func ConfiguredClaudeUserAgent(cfg *config.Config) string {
	return defaultClaudeDeviceProfile(cfg).UserAgent
}
