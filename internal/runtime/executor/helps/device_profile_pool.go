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
	// pkg is pinned to 0.94.0: the real @anthropic-ai/sdk bundled by the current
	// claude-cli (live-captured from 2.1.153 on this machine → X-Stainless-Package-
	// Version: 0.94.0). An impossible cli-version→sdk-version pairing is a sharper
	// tell than uniformity, so this field is NOT diversified. node stays in the
	// Node-24 family so X-Stainless-Runtime-Version matches the pinned Node-24 uTLS
	// ClientHello. Diversity comes from cli version + OS + arch.
	{ua: "claude-cli/2.1.153 (external, cli)", pkg: "0.94.0", node: "v24.3.0", os: "MacOS", arch: "arm64"},
	{ua: "claude-cli/2.1.152 (external, cli)", pkg: "0.94.0", node: "v24.4.0", os: "MacOS", arch: "arm64"},
	{ua: "claude-cli/2.1.151 (external, cli)", pkg: "0.94.0", node: "v24.3.0", os: "Linux", arch: "x64"},
	{ua: "claude-cli/2.1.150 (external, cli)", pkg: "0.94.0", node: "v24.4.0", os: "Linux", arch: "x64"},
	{ua: "claude-cli/2.1.153 (external, cli)", pkg: "0.94.0", node: "v24.3.0", os: "Windows", arch: "x64"},
	{ua: "claude-cli/2.1.149 (external, cli)", pkg: "0.94.0", node: "v24.3.0", os: "MacOS", arch: "x64"},
	{ua: "claude-cli/2.1.152 (external, cli)", pkg: "0.94.0", node: "v24.4.0", os: "Linux", arch: "arm64"},
	{ua: "claude-cli/2.1.151 (external, cli)", pkg: "0.94.0", node: "v24.3.0", os: "MacOS", arch: "arm64"},
}

// codexUAPool spreads accounts across recent Codex CLI releases and terminals.
// The format mirrors the real codex-tui User-Agent so isOfficialCodexUserAgent
// still recognizes it; only version / OS minor / terminal vary.
var codexUAPool = []string{
	// Real codex_cli_rs UA shape: prefix codex_cli_rs/, ends at the terminal token
	// (no trailing "(codex-tui; ver)"). Versions in the current 0.14x line.
	// Version pinned to the live-captured codex_cli_rs/0.142.5; vary ONLY OS-minor
	// (real shipped macOS 26.0-26.2.0) and terminal. Version is intentionally NOT
	// diversified: advertising an older UA (0.140/0.141) while emitting a 0.142.x-only
	// beta flag (remote_compaction_v2) is an impossible pairing — a sharper tell than
	// uniformity (mirrors the claudeProfilePool no-impossible-pairing rule). Do NOT
	// invent non-existent OS versions like 26.5.0.
	"codex_cli_rs/0.142.5 (Mac OS 26.2.0; arm64) iTerm.app/3.6.10 (codex_cli_rs; 0.142.5)",
	"codex_cli_rs/0.142.5 (Mac OS 26.1.0; arm64) Apple_Terminal/455 (codex_cli_rs; 0.142.5)",
	"codex_cli_rs/0.142.5 (Mac OS 26.2.0; arm64) WezTerm/20240203-110809 (codex_cli_rs; 0.142.5)",
	"codex_cli_rs/0.142.5 (Mac OS 26.0.0; arm64) iTerm.app/3.5.11 (codex_cli_rs; 0.142.5)",
	"codex_cli_rs/0.142.5 (Mac OS 26.2.0; arm64) vscode/1.95.3 (codex_cli_rs; 0.142.5)",
	"codex_cli_rs/0.142.5 (Mac OS 26.1.0; arm64) iTerm.app/3.6.10 (codex_cli_rs; 0.142.5)",
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
		// No auth.ID and no apiKey (e.g. a Codex OAuth auth whose ID is not yet
		// populated). Fall back to a stable account identifier from metadata so
		// diversification still works, before giving up and using the canonical
		// value. account_id is used (not email) to keep account-less/anonymous
		// callers on the canonical fallback.
		if auth != nil && auth.Metadata != nil {
			if v, ok := auth.Metadata["account_id"].(string); ok {
				if tv := strings.TrimSpace(v); tv != "" {
					return "meta:" + tv
				}
			}
		}
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
