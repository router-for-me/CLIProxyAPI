package helps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeClaudeDeviceProfileKVClient struct {
	values        map[string][]byte
	getErr        error
	setErr        error
	setNXErr      error
	expireErr     error
	setNXResult   bool
	getCount      int
	setCount      int
	setNXCount    int
	expireCount   int
	lastSetTTL    time.Duration
	lastSetNXTTL  time.Duration
	lastExpireTTL time.Duration
}

func newFakeClaudeDeviceProfileKVClient() *fakeClaudeDeviceProfileKVClient {
	return &fakeClaudeDeviceProfileKVClient{
		values:      make(map[string][]byte),
		setNXResult: true,
	}
}

func (c *fakeClaudeDeviceProfileKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.getCount++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *fakeClaudeDeviceProfileKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.setCount++
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeClaudeDeviceProfileKVClient) KVSetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	c.setNXCount++
	c.lastSetNXTTL = ttl
	if c.setNXErr != nil {
		return false, c.setNXErr
	}
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	if c.setNXResult {
		c.values[key] = append([]byte(nil), value...)
		return true, nil
	}
	return false, nil
}

func (c *fakeClaudeDeviceProfileKVClient) KVExpire(_ context.Context, _ string, ttl time.Duration) (bool, error) {
	c.expireCount++
	c.lastExpireTTL = ttl
	if c.expireErr != nil {
		return false, c.expireErr
	}
	return true, nil
}

func useFakeClaudeDeviceProfileKVClient(t *testing.T, client *fakeClaudeDeviceProfileKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentClaudeDeviceProfileKVClient
	currentClaudeDeviceProfileKVClient = func() (claudeDeviceProfileKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentClaudeDeviceProfileKVClient = previous
	})
}

func mustClaudeDeviceProfileJSON(t *testing.T, value claudeDeviceProfileKVValue) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal device profile: %v", errMarshal)
	}
	return raw
}

func claudeDeviceHeaders(userAgent string) http.Header {
	return http.Header{
		"User-Agent":                  {userAgent},
		"X-Stainless-Package-Version": {defaultClaudeFingerprintPackageVersion},
		"X-Stainless-Runtime-Version": {defaultClaudeFingerprintRuntimeVersion},
		"X-Stainless-Os":              {"Windows"},
		"X-Stainless-Arch":            {"x64"},
	}
}

func TestResolveClaudeDeviceProfileLocalUsesBaselineForInvalidSignals(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	auth := &cliproxyauth.Auth{ID: "auth-invalid-signals"}
	headers := claudeDeviceHeaders("claude-cli/999.0.0 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "999.0.0")
	headers.Set("X-Stainless-Runtime-Version", "v999.0.0")

	profile := resolveClaudeDeviceProfileLocal(auth, "api-key", headers, nil)
	baseline := defaultClaudeDeviceProfile(nil)
	if profile.UserAgent != baseline.UserAgent || profile.PackageVersion != baseline.PackageVersion || profile.RuntimeVersion != baseline.RuntimeVersion {
		t.Fatalf("invalid profile = %#v, want local baseline %#v", profile, baseline)
	}
}

func TestApplyClaudeLegacyDeviceHeadersReplacesInvalidNativeSoftwareSignals(t *testing.T) {
	request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	incoming := claudeDeviceHeaders("claude-cli/999.0.0 (external, cli)")
	incoming.Set("X-Stainless-Package-Version", "999.0.0")
	incoming.Set("X-Stainless-Runtime-Version", "v999.0.0")

	ApplyClaudeLegacyDeviceHeaders(request, incoming, nil, true)

	baseline := defaultClaudeDeviceProfile(nil)
	if got := request.Header.Get("User-Agent"); got != baseline.UserAgent {
		t.Fatalf("User-Agent = %q, want local baseline %q", got, baseline.UserAgent)
	}
	if got := request.Header.Get("X-Stainless-Package-Version"); got != baseline.PackageVersion {
		t.Fatalf("X-Stainless-Package-Version = %q, want %q", got, baseline.PackageVersion)
	}
	if got := request.Header.Get("X-Stainless-Runtime-Version"); got != baseline.RuntimeVersion {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want %q", got, baseline.RuntimeVersion)
	}
}

func TestApplyClaudeLegacyDeviceHeadersAcceptsConfiguredMeasuredBaseline(t *testing.T) {
	request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
		UserAgent:      "claude-cli/2.2.0 (external, cli)",
		PackageVersion: "0.95.0",
		RuntimeVersion: "v26.4.0",
		OS:             "MacOS",
		Arch:           "arm64",
	}}
	incoming := claudeDeviceHeaders("claude-cli/2.2.0 (external, cli)")
	incoming.Set("X-Stainless-Package-Version", "0.95.0")
	incoming.Set("X-Stainless-Runtime-Version", "v26.4.0")

	ApplyClaudeLegacyDeviceHeaders(request, incoming, cfg, true)

	if got := request.Header.Get("User-Agent"); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("User-Agent = %q, want configured measured baseline", got)
	}
	if got := request.Header.Get("X-Stainless-Package-Version"); got != "0.95.0" {
		t.Fatalf("X-Stainless-Package-Version = %q, want 0.95.0", got)
	}
	if got := request.Header.Get("X-Stainless-Runtime-Version"); got != "v26.4.0" {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want v26.4.0", got)
	}
}

func TestResolveClaudeDeviceProfileRequiredHomeReadWithoutCandidate(t *testing.T) {
	client := newFakeClaudeDeviceProfileKVClient()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	key := claudeDeviceProfileKVKey(auth, "api-key", ClaudeDeviceProfile{})
	client.values[key] = mustClaudeDeviceProfileJSON(t, claudeDeviceProfileKVValue{
		UserAgent:      "claude-cli/2.2.0 (external, cli)",
		PackageVersion: "0.80.0",
		RuntimeVersion: "v24.4.0",
		OS:             "Windows",
		Arch:           "x64",
	})
	useFakeClaudeDeviceProfileKVClient(t, client, true, nil)

	profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", nil, nil)
	if errProfile != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() error = %v", errProfile)
	}
	if profile.UserAgent != defaultClaudeFingerprintUserAgent {
		t.Fatalf("UserAgent = %q, want local baseline %q for unmeasured cached profile", profile.UserAgent, defaultClaudeFingerprintUserAgent)
	}
	if profile.OS != defaultClaudeFingerprintOS || profile.Arch != defaultClaudeFingerprintArch {
		t.Fatalf("platform = %s/%s, want baseline pinned %s/%s", profile.OS, profile.Arch, defaultClaudeFingerprintOS, defaultClaudeFingerprintArch)
	}
	if client.expireCount != 1 || client.lastExpireTTL != claudeDeviceProfileTTL {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/%v", client.expireCount, client.lastExpireTTL, claudeDeviceProfileTTL)
	}
}

func TestResolveClaudeDeviceProfileRequiredHomeCandidateLocksRereadsAndWrites(t *testing.T) {
	client := newFakeClaudeDeviceProfileKVClient()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	useFakeClaudeDeviceProfileKVClient(t, client, true, nil)

	profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), nil)
	if errProfile != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() error = %v", errProfile)
	}
	if profile.UserAgent != defaultClaudeFingerprintUserAgent {
		t.Fatalf("UserAgent = %q, want candidate %q", profile.UserAgent, defaultClaudeFingerprintUserAgent)
	}
	if client.setNXCount != 1 || client.lastSetNXTTL != claudeDeviceProfileLockTTL {
		t.Fatalf("KVSetNX count/ttl = %d/%v, want 1/%v", client.setNXCount, client.lastSetNXTTL, claudeDeviceProfileLockTTL)
	}
	if client.getCount != 1 {
		t.Fatalf("KVGet count = %d, want re-read after lock", client.getCount)
	}
	if client.setCount != 1 || client.lastSetTTL != claudeDeviceProfileTTL {
		t.Fatalf("KVSet count/ttl = %d/%v, want 1/%v", client.setCount, client.lastSetTTL, claudeDeviceProfileTTL)
	}
}

func TestResolveClaudeDeviceProfileRequiredHomeSeparatesVSCodeAgentSDKFromCLI(t *testing.T) {
	client := newFakeClaudeDeviceProfileKVClient()
	auth := &cliproxyauth.Auth{ID: "auth-home-subclient-isolation"}
	useFakeClaudeDeviceProfileKVClient(t, client, true, nil)

	cliProfile, errCLI := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), nil)
	if errCLI != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() CLI error = %v", errCLI)
	}
	vscodeUA := "claude-cli/2.1.258 (external, claude-vscode, agent-sdk/0.3.220)"
	vscodeProfile, errVSCode := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", claudeDeviceHeaders(vscodeUA), nil)
	if errVSCode != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() VSCode error = %v", errVSCode)
	}

	if cliProfile.UserAgent != defaultClaudeFingerprintUserAgent {
		t.Fatalf("CLI UserAgent = %q, want CLI profile", cliProfile.UserAgent)
	}
	if vscodeProfile.UserAgent != vscodeUA {
		t.Fatalf("VSCode UserAgent = %q, want %q", vscodeProfile.UserAgent, vscodeUA)
	}
	if client.setCount != 2 {
		t.Fatalf("KVSet count = %d, want separate CLI and VSCode profiles", client.setCount)
	}
	cliKey := claudeDeviceProfileKVKey(auth, "api-key", cliProfile)
	vscodeKey := claudeDeviceProfileKVKey(auth, "api-key", vscodeProfile)
	if cliKey == vscodeKey {
		t.Fatalf("CLI and VSCode KV keys are equal: %q", cliKey)
	}
	if _, ok := client.values[cliKey]; !ok {
		t.Fatalf("CLI profile missing from KV key %q", cliKey)
	}
	if _, ok := client.values[vscodeKey]; !ok {
		t.Fatalf("VSCode profile missing from KV key %q", vscodeKey)
	}
}

func TestResolveClaudeDeviceProfileRequiredHomeNormalizesUnmeasuredCachedProfile(t *testing.T) {
	client := newFakeClaudeDeviceProfileKVClient()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	key := claudeDeviceProfileKVKey(auth, "api-key", ClaudeDeviceProfile{})
	client.values[key] = mustClaudeDeviceProfileJSON(t, claudeDeviceProfileKVValue{
		UserAgent:      "claude-cli/2.4.0 (external, cli)",
		PackageVersion: "0.90.0",
		RuntimeVersion: "v24.5.0",
		OS:             "Windows",
		Arch:           "x64",
	})
	useFakeClaudeDeviceProfileKVClient(t, client, true, nil)

	profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", claudeDeviceHeaders("claude-cli/2.3.0 (external, cli)"), nil)
	if errProfile != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() error = %v", errProfile)
	}
	if profile.UserAgent != defaultClaudeFingerprintUserAgent {
		t.Fatalf("UserAgent = %q, want local baseline %q", profile.UserAgent, defaultClaudeFingerprintUserAgent)
	}
	if client.setCount != 0 {
		t.Fatalf("KVSet count = %d, want no downgrade write", client.setCount)
	}
	if client.expireCount != 1 {
		t.Fatalf("KVExpire count = %d, want cached refresh", client.expireCount)
	}
}

func TestResolveClaudeDeviceProfileRequiredHomeFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		client  *fakeClaudeDeviceProfileKVClient
	}{
		{name: "read", client: &fakeClaudeDeviceProfileKVClient{values: make(map[string][]byte), getErr: errors.New("get failed")}},
		{name: "lock", headers: claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), client: &fakeClaudeDeviceProfileKVClient{values: make(map[string][]byte), setNXResult: true, setNXErr: errors.New("lock failed")}},
		{name: "lock-miss", headers: claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), client: &fakeClaudeDeviceProfileKVClient{values: make(map[string][]byte), setNXResult: false}},
		{name: "reread", headers: claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), client: &fakeClaudeDeviceProfileKVClient{values: make(map[string][]byte), setNXResult: true, getErr: errors.New("re-read failed")}},
		{name: "write", headers: claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), client: &fakeClaudeDeviceProfileKVClient{values: make(map[string][]byte), setNXResult: true, setErr: errors.New("write failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeClaudeDeviceProfileKVClient(t, tc.client, true, nil)
			if _, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), &cliproxyauth.Auth{ID: "auth-1"}, "api-key", tc.headers, nil); errProfile == nil {
				t.Fatalf("ResolveClaudeDeviceProfileRequired() error = nil, want error")
			}
		})
	}
}

func TestResolveClaudeDeviceProfilePreservesConfirmedClientAtBaselineVersion(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	client := newFakeClaudeDeviceProfileKVClient()
	useFakeClaudeDeviceProfileKVClient(t, client, false, nil)
	auth := &cliproxyauth.Auth{ID: "auth-baseline-entrypoint"}
	headers := claudeDeviceHeaders("claude-cli/2.1.258 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "0.112.1")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")

	profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", headers, nil)
	if errProfile != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() error = %v", errProfile)
	}
	if profile.UserAgent != "claude-cli/2.1.258 (external, cli)" {
		t.Fatalf("UserAgent = %q, want confirmed cli entrypoint preserved", profile.UserAgent)
	}
	if profile.PackageVersion != "0.112.1" || profile.RuntimeVersion != "v26.3.0" {
		t.Fatalf("software profile = %s/%s, want 0.112.1/v26.3.0", profile.PackageVersion, profile.RuntimeVersion)
	}
}

func TestResolveClaudeDeviceProfileSeparatesVSCodeAgentSDKFromCLI(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	client := newFakeClaudeDeviceProfileKVClient()
	useFakeClaudeDeviceProfileKVClient(t, client, false, nil)
	auth := &cliproxyauth.Auth{ID: "auth-subclient-isolation"}

	cliHeaders := claudeDeviceHeaders("claude-cli/2.1.258 (external, cli)")
	cliHeaders.Set("X-Stainless-Package-Version", "0.112.1")
	cliHeaders.Set("X-Stainless-Runtime-Version", "v26.3.0")
	cliProfile, errCLI := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", cliHeaders, nil)
	if errCLI != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() CLI error = %v", errCLI)
	}

	vscodeUA := "claude-cli/2.1.258 (external, claude-vscode, agent-sdk/0.3.220)"
	vscodeHeaders := claudeDeviceHeaders(vscodeUA)
	vscodeHeaders.Set("X-Stainless-Package-Version", "0.112.1")
	vscodeHeaders.Set("X-Stainless-Runtime-Version", "v26.3.0")
	vscodeProfile, errVSCode := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", vscodeHeaders, nil)
	if errVSCode != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() VSCode error = %v", errVSCode)
	}

	if cliProfile.UserAgent != "claude-cli/2.1.258 (external, cli)" {
		t.Fatalf("CLI UserAgent = %q, want CLI profile", cliProfile.UserAgent)
	}
	if vscodeProfile.UserAgent != vscodeUA {
		t.Fatalf("VSCode UserAgent = %q, want %q", vscodeProfile.UserAgent, vscodeUA)
	}

	cliProfileAgain, errCLIAgain := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", cliHeaders, nil)
	if errCLIAgain != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() second CLI error = %v", errCLIAgain)
	}
	if cliProfileAgain.UserAgent != cliProfile.UserAgent {
		t.Fatalf("second CLI UserAgent = %q, want isolated cached %q", cliProfileAgain.UserAgent, cliProfile.UserAgent)
	}
}

func TestResolveClaudeDeviceProfileRequiredNonHomeKeepsLocalCache(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	client := newFakeClaudeDeviceProfileKVClient()
	useFakeClaudeDeviceProfileKVClient(t, client, false, nil)
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	cfg := &config.Config{}

	first, errFirst := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), cfg)
	if errFirst != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() first error = %v", errFirst)
	}
	second, errSecond := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", nil, cfg)
	if errSecond != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() second error = %v", errSecond)
	}
	if second.UserAgent != first.UserAgent {
		t.Fatalf("cached UserAgent = %q, want %q", second.UserAgent, first.UserAgent)
	}
	if client.getCount != 0 || client.setCount != 0 || client.setNXCount != 0 {
		t.Fatalf("KV calls = get %d set %d setnx %d, want all zero", client.getCount, client.setCount, client.setNXCount)
	}
}
