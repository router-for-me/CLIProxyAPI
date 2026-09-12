package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func mustParseClaudeCLIVersion(t *testing.T, userAgent string) claudeCLIVersion {
	t.Helper()
	version, ok := parseClaudeCLIVersion(userAgent)
	if !ok {
		t.Fatalf("parseClaudeCLIVersion(%q) failed", userAgent)
	}
	return version
}

// The configured claude-header-defaults.user-agent is a FLOOR, not a lock: a
// genuine Claude Code that upgraded past the pin must stay plausible, while a
// stale copied User-Agent below the pin stays implausible.
func TestPlausibleClaudeCLIVersionTreatsBaselineAsFloor(t *testing.T) {
	baseline := mustParseClaudeCLIVersion(t, "claude-cli/2.1.252 (external, cli)")
	for _, test := range []struct {
		name      string
		userAgent string
		want      bool
	}{
		{name: "newer patch", userAgent: "claude-cli/2.1.259 (external, cli)", want: true},
		{name: "exact baseline", userAgent: "claude-cli/2.1.252 (external, cli)", want: true},
		{name: "older patch", userAgent: "claude-cli/2.1.240 (external, cli)", want: false},
		{name: "newer minor", userAgent: "claude-cli/2.2.0 (external, cli)", want: true},
		{name: "next major", userAgent: "claude-cli/3.0.0 (external, cli)", want: true},
		{name: "older minor", userAgent: "claude-cli/2.0.999 (external, cli)", want: false},
		{name: "absurd future major", userAgent: "claude-cli/999.0.0 (external, cli)", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := mustParseClaudeCLIVersion(t, test.userAgent)
			if got := plausibleClaudeCLIVersion(candidate, baseline); got != test.want {
				t.Fatalf("plausibleClaudeCLIVersion(%q, 2.1.252) = %t, want %t", test.userAgent, got, test.want)
			}
		})
	}
}

// The Stainless package and Node runtime versions move independently of the CLI
// version, so they are floors too. Equality there would re-create the same
// silent downgrade the next time the bundled SDK or Node build is bumped.
func TestMeetsClaudeDeviceProfileBaselineTreatsSoftwareTupleAsFloor(t *testing.T) {
	baseline := defaultClaudeDeviceProfile(&config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
		UserAgent:      "claude-cli/2.1.252 (external, cli)",
		PackageVersion: "0.112.1",
		RuntimeVersion: "v26.3.0",
	}})
	newProfile := func(userAgent, packageVersion, runtimeVersion string) ClaudeDeviceProfile {
		profile := ClaudeDeviceProfile{
			UserAgent:      userAgent,
			PackageVersion: packageVersion,
			RuntimeVersion: runtimeVersion,
		}
		if version, ok := parseClaudeCLIVersion(userAgent); ok {
			profile.version = version
			profile.hasVersion = true
		}
		return profile
	}
	for _, test := range []struct {
		name    string
		profile ClaudeDeviceProfile
		want    bool
	}{
		{name: "exact baseline", profile: newProfile("claude-cli/2.1.252 (external, cli)", "0.112.1", "v26.3.0"), want: true},
		{name: "newer cli", profile: newProfile("claude-cli/2.1.259 (external, cli)", "0.112.1", "v26.3.0"), want: true},
		{name: "newer package", profile: newProfile("claude-cli/2.1.259 (external, cli)", "0.113.0", "v26.3.0"), want: true},
		{name: "newer runtime", profile: newProfile("claude-cli/2.1.259 (external, cli)", "0.112.1", "v27.0.0"), want: true},
		{name: "older package", profile: newProfile("claude-cli/2.1.259 (external, cli)", "0.111.9", "v26.3.0"), want: false},
		{name: "older runtime", profile: newProfile("claude-cli/2.1.259 (external, cli)", "0.112.1", "v25.9.9"), want: false},
		{name: "older cli", profile: newProfile("claude-cli/2.1.240 (external, cli)", "0.112.1", "v26.3.0"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := meetsClaudeDeviceProfileBaseline(test.profile, baseline); got != test.want {
				t.Fatalf("meetsClaudeDeviceProfileBaseline(%#v) = %t, want %t", test.profile, got, test.want)
			}
		})
	}
}

func versionGateBaselineConfig() *config.Config {
	return &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
		UserAgent:      "claude-cli/2.1.252 (external, cli)",
		PackageVersion: "0.112.1",
		RuntimeVersion: "v26.3.0",
		OS:             "MacOS",
		Arch:           "arm64",
	}}
}

// A real Claude Code 2.1.259 against a 2.1.252 pin must be confirmed, so it is
// passed through rather than cloaked.
func TestDetectClaudeCodeRequestConfirmsUpgradedNativeClient(t *testing.T) {
	cfg := versionGateBaselineConfig()
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)

	headers := confirmedClaudeCodeHeaders()
	headers.Set("User-Agent", "claude-cli/2.1.259 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "0.112.1")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")
	if detection := DetectClaudeCodeRequest(headers, payload, false, cfg); !detection.Confirmed {
		t.Fatalf("detection = %#v, want confirmed for 2.1.259 against a 2.1.252 baseline", detection)
	}

	stale := confirmedClaudeCodeHeaders()
	stale.Set("User-Agent", "claude-cli/2.1.240 (external, cli)")
	if detection := DetectClaudeCodeRequest(stale, payload, false, cfg); detection.Confirmed {
		t.Fatalf("detection = %#v, want unconfirmed for a stale 2.1.240 user agent", detection)
	}
}

// Once the gate accepts a newer client, the learned per-credential profile must
// adopt it so cloaked requests on the same credential stop presenting the
// configured constant.
func TestResolveClaudeDeviceProfileAdoptsUpgradedClientVersion(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	t.Cleanup(ResetClaudeDeviceProfileCache)

	cfg := versionGateBaselineConfig()
	headers := http.Header{
		"User-Agent":                  {"claude-cli/2.1.259 (external, cli)"},
		"X-Stainless-Package-Version": {"0.112.1"},
		"X-Stainless-Runtime-Version": {"v26.3.0"},
		"X-Stainless-Os":              {"MacOS"},
		"X-Stainless-Arch":            {"arm64"},
	}
	learned := resolveClaudeDeviceProfileLocal(nil, "version-gate-key", headers, cfg)
	if learned.UserAgent != "claude-cli/2.1.259 (external, cli)" {
		t.Fatalf("learned profile user agent = %q, want the upgraded 2.1.259", learned.UserAgent)
	}

	cloaked := resolveClaudeDeviceProfileLocal(nil, "version-gate-key", nil, cfg)
	if cloaked.UserAgent != "claude-cli/2.1.259 (external, cli)" {
		t.Fatalf("cloaked profile user agent = %q, want the learned 2.1.259", cloaked.UserAgent)
	}

	// A stale client on the same credential must not drag the learned profile back.
	staleHeaders := http.Header{
		"User-Agent":                  {"claude-cli/2.1.240 (external, cli)"},
		"X-Stainless-Package-Version": {"0.112.1"},
		"X-Stainless-Runtime-Version": {"v26.3.0"},
	}
	if profile := resolveClaudeDeviceProfileLocal(nil, "version-gate-key", staleHeaders, cfg); profile.UserAgent != "claude-cli/2.1.259 (external, cli)" {
		t.Fatalf("profile after stale request = %q, want the learned 2.1.259", profile.UserAgent)
	}
}

func TestObserveClaudeClientVersionRecordsAndReports(t *testing.T) {
	ResetClaudeClientVersionRegistry()
	t.Cleanup(ResetClaudeClientVersionRegistry)

	cfg := versionGateBaselineConfig()
	headers := http.Header{"User-Agent": {"claude-cli/2.1.259 (external, cli)"}}

	if warn := ObserveClaudeClientVersion(nil, "version-gate-key", headers, cfg); !warn {
		t.Fatal("first mismatched observation = false, want a warning")
	}
	if warn := ObserveClaudeClientVersion(nil, "version-gate-key", headers, cfg); warn {
		t.Fatal("repeat observation = true, want the warning suppressed")
	}

	report := ClaudeClientVersionReport(cfg)
	if report.ConfiguredUserAgent != "claude-cli/2.1.252 (external, cli)" {
		t.Fatalf("configured user agent = %q", report.ConfiguredUserAgent)
	}
	if report.ConfiguredVersion != "2.1.252" {
		t.Fatalf("configured version = %q, want 2.1.252", report.ConfiguredVersion)
	}
	if len(report.Credentials) != 1 {
		t.Fatalf("credentials = %#v, want exactly one", report.Credentials)
	}
	credential := report.Credentials[0]
	if credential.HighestObservedVersion != "2.1.259" {
		t.Fatalf("highest observed = %q, want 2.1.259", credential.HighestObservedVersion)
	}
	if !credential.Mismatched || credential.Requests != 2 {
		t.Fatalf("credential = %#v, want mismatched with 2 requests", credential)
	}
	if len(credential.ObservedVersions) != 1 || credential.ObservedVersions[0].Version != "2.1.259" {
		t.Fatalf("observed versions = %#v", credential.ObservedVersions)
	}

	// A second, higher version on the same credential warns again and wins.
	newer := http.Header{"User-Agent": {"claude-cli/2.2.1 (external, cli)"}}
	if warn := ObserveClaudeClientVersion(nil, "version-gate-key", newer, cfg); !warn {
		t.Fatal("new version observation = false, want a warning")
	}
	if got := ClaudeClientVersionReport(cfg).Credentials[0].HighestObservedVersion; got != "2.2.1" {
		t.Fatalf("highest observed after upgrade = %q, want 2.2.1", got)
	}

	// A client at the configured baseline is recorded but never warns.
	ResetClaudeClientVersionRegistry()
	atBaseline := http.Header{"User-Agent": {"claude-cli/2.1.252 (external, cli)"}}
	if warn := ObserveClaudeClientVersion(nil, "other-key", atBaseline, cfg); warn {
		t.Fatal("baseline observation = true, want no warning")
	}
	if report := ClaudeClientVersionReport(cfg); report.Credentials[0].Mismatched {
		t.Fatalf("credential = %#v, want no mismatch at the configured baseline", report.Credentials[0])
	}
}
