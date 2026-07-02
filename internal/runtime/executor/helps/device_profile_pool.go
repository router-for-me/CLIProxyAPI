package helps

// Per-account device-fingerprint diversification.
//
// Every stock CLIProxyAPI (and codex2api / sub2api) instance falls back to the
// SAME fixed device values when a downstream client does not send its own — a
// single hardcoded claude-cli/@anthropic-ai-sdk/node tuple and a single Codex
// User-Agent. That monoculture lets an upstream cluster all such proxied
// accounts together and ban them as one. Real Claude Code / Codex users are a
// DIVERSE population (different client versions, OS, arch) instead.
//
// This file replaces the fixed fallback with a value drawn DETERMINISTICALLY
// from a realistic distribution, keyed by the account. Determinism is the point:
// the same account always resolves to the same tuple (so its fingerprint is
// stable across requests and restarts — a per-request shuffle would itself be a
// tell), while different accounts spread across the pool. The TLS-layer JA3/JA4
// (the real-client fingerprint verified byte-for-byte) is intentionally NOT
// varied here — only the application-layer device headers that a real user
// population legitimately varies.

import (
	"hash/fnv"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// claudeProfileTuple is one internally-plausible Claude Code device identity.
// node versions are kept within the family that shares the pinned Node uTLS
// ClientHello so the TLS JA3 stays consistent with X-Stainless-Runtime-Version.
type claudeProfileTuple struct {
	ua   string
	pkg  string
	node string
	os   string
	arch string
}

// claudeProfilePool spreads accounts across recent, real Claude Code releases
// and common OS/arch combinations. Values are plausible real tuples; the goal is
// population diversity, not any single "correct" identity.
var claudeProfilePool = []claudeProfileTuple{
	{ua: "claude-cli/2.1.72 (external, cli)", pkg: "0.75.0", node: "v24.4.0", os: "MacOS", arch: "arm64"},
	{ua: "claude-cli/2.1.70 (external, cli)", pkg: "0.74.0", node: "v22.11.0", os: "MacOS", arch: "arm64"},
	{ua: "claude-cli/2.1.68 (external, cli)", pkg: "0.74.0", node: "v22.9.0", os: "Linux", arch: "x64"},
	{ua: "claude-cli/2.1.66 (external, cli)", pkg: "0.73.0", node: "v24.3.0", os: "Linux", arch: "x64"},
	{ua: "claude-cli/2.1.72 (external, cli)", pkg: "0.75.0", node: "v24.4.0", os: "Windows", arch: "x64"},
	{ua: "claude-cli/2.1.63 (external, cli)", pkg: "0.74.0", node: "v24.3.0", os: "MacOS", arch: "x64"},
	{ua: "claude-cli/2.1.70 (external, cli)", pkg: "0.74.0", node: "v22.11.0", os: "Linux", arch: "arm64"},
	{ua: "claude-cli/2.1.69 (external, cli)", pkg: "0.74.0", node: "v22.9.0", os: "MacOS", arch: "arm64"},
}

// codexUAPool spreads accounts across recent Codex CLI releases and terminals.
// The format mirrors the real codex-tui User-Agent so isOfficialCodexUserAgent
// still recognizes it; only version / OS minor / terminal vary.
var codexUAPool = []string{
	"codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)",
	"codex-tui/0.134.0 (Mac OS 26.4.0; arm64) Apple_Terminal/455 (codex-tui; 0.134.0)",
	"codex-tui/0.135.0 (Mac OS 26.5.0; arm64) WezTerm/20240203-110809 (codex-tui; 0.135.0)",
	"codex-tui/0.133.0 (Mac OS 26.3.0; arm64) iTerm.app/3.5.11 (codex-tui; 0.133.0)",
	"codex-tui/0.136.0 (Mac OS 26.5.0; arm64) vscode/1.95.3 (codex-tui; 0.136.0)",
	"codex-tui/0.134.0 (Mac OS 26.4.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.134.0)",
}

// fnvIndex maps a scope key deterministically into [0, n).
func fnvIndex(scope string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(scope))
	return int(h.Sum32() % uint32(n))
}

// AccountFingerprintKey returns the stable per-account key used to derive
// diversified fingerprints. It reuses the same scope as the device-profile cache
// so a given account is consistent everywhere. When there is no real per-account
// identity (the scope collapses to "global" for a nil/anonymous auth with no API
// key), it returns "" so callers fall back to the canonical value instead of
// keying every account-less request onto one arbitrary pool entry.
func AccountFingerprintKey(auth *cliproxyauth.Auth, apiKey string) string {
	scope := claudeDeviceProfileScopeKey(auth, apiKey)
	if scope == "global" {
		return ""
	}
	return scope
}

// fingerprintRandomizationEnabled reports whether per-account diversification is on.
func fingerprintRandomizationEnabled(cfg *config.Config) bool {
	return cfg == nil || !cfg.DisableFingerprintRandomization
}

// perAccountClaudeProfile derives a stable device profile for the account from
// the pool, letting explicit config defaults override any field.
func perAccountClaudeProfile(scope string, cfg *config.Config) ClaudeDeviceProfile {
	t := claudeProfilePool[fnvIndex(scope, len(claudeProfilePool))]
	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}
	pick := func(cfgVal, poolVal string) string {
		if strings.TrimSpace(cfgVal) != "" {
			return strings.TrimSpace(cfgVal)
		}
		return poolVal
	}
	return ClaudeDeviceProfile{
		UserAgent:      pick(hd.UserAgent, t.ua),
		PackageVersion: pick(hd.PackageVersion, t.pkg),
		RuntimeVersion: pick(hd.RuntimeVersion, t.node),
		OS:             pick(hd.OS, t.os),
		Arch:           pick(hd.Arch, t.arch),
	}
}

// AugmentClaudeDeviceHeaders returns a header set to feed the device-profile
// machinery: it clones the client's headers and, for each device header the
// client did NOT supply, fills a per-account deterministic value. Client-supplied
// headers are always preserved, so a real Claude Code client's own identity still
// passes through untouched. When randomization is disabled or no account scope is
// available, the client headers are returned unchanged.
func AugmentClaudeDeviceHeaders(client http.Header, auth *cliproxyauth.Auth, apiKey string, cfg *config.Config) http.Header {
	if !fingerprintRandomizationEnabled(cfg) {
		return client
	}
	scope := AccountFingerprintKey(auth, apiKey)
	if strings.TrimSpace(scope) == "" {
		return client
	}
	profile := perAccountClaudeProfile(scope, cfg)

	out := http.Header{}
	if client != nil {
		out = client.Clone()
	}
	setIfAbsent := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		if strings.TrimSpace(out.Get(name)) != "" {
			return
		}
		out.Set(name, value)
	}
	setIfAbsent("User-Agent", profile.UserAgent)
	setIfAbsent("X-Stainless-Package-Version", profile.PackageVersion)
	setIfAbsent("X-Stainless-Runtime-Version", profile.RuntimeVersion)
	setIfAbsent("X-Stainless-Os", profile.OS)
	setIfAbsent("X-Stainless-Arch", profile.Arch)
	return out
}

// PerAccountCodexUserAgent returns a stable per-account Codex User-Agent drawn
// from the pool, or fallback when randomization is disabled or no scope exists.
func PerAccountCodexUserAgent(scope, fallback string, cfg *config.Config) string {
	if !fingerprintRandomizationEnabled(cfg) {
		return fallback
	}
	if strings.TrimSpace(scope) == "" {
		return fallback
	}
	return codexUAPool[fnvIndex(scope, len(codexUAPool))]
}
