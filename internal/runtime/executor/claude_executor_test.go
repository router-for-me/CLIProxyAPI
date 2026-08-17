package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func resetClaudeDeviceProfileCache() {
	helps.ResetClaudeDeviceProfileCache()
}

func claudeOAuthTestMetadata() map[string]any {
	return map[string]any{
		"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
}

func malformedClaudeTreeSignatureForClaudeExecutorTest() string {
	return base64.StdEncoding.EncodeToString([]byte{0x12, 0xFF, 0xFE, 0xFD})
}

func newClaudeHeaderTestRequest(t *testing.T, incoming http.Header) *http.Request {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginReq := httptest.NewRequest(http.MethodPost, "http://localhost/v1/messages", nil)
	ginReq.Header = incoming.Clone()
	ginCtx.Request = ginReq

	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	return req.WithContext(context.WithValue(req.Context(), "gin", ginCtx))
}

func assertClaudeFingerprint(t *testing.T, headers http.Header, userAgent, pkgVersion, runtimeVersion, osName, arch string) {
	t.Helper()

	if got := headers.Get("User-Agent"); got != userAgent {
		t.Fatalf("User-Agent = %q, want %q", got, userAgent)
	}
	if got := headers.Get("X-Stainless-Package-Version"); got != pkgVersion {
		t.Fatalf("X-Stainless-Package-Version = %q, want %q", got, pkgVersion)
	}
	if got := headers.Get("X-Stainless-Runtime-Version"); got != runtimeVersion {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want %q", got, runtimeVersion)
	}
	if got := headers.Get("X-Stainless-Os"); got != osName {
		t.Fatalf("X-Stainless-Os = %q, want %q", got, osName)
	}
	if got := headers.Get("X-Stainless-Arch"); got != arch {
		t.Fatalf("X-Stainless-Arch = %q, want %q", got, arch)
	}
}

func TestApplyClaudeHeaders_FastModeBetaIsConditional(t *testing.T) {
	baseline := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5"}`), nil, false)
	betasWithoutFastMode := baseline
	betasWithFastMode := baseline + "," + claudeFastModeBeta

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "omitted speed excludes fast mode beta",
			body: `{"model":"claude-opus-5"}`,
			want: betasWithoutFastMode,
		},
		{
			name: "fast speed appends fast mode beta",
			body: `{"model":"claude-opus-5","speed":"fast"}`,
			want: betasWithFastMode,
		},
		{
			name: "explicit body beta appends fast mode beta",
			body: `{"model":"claude-opus-5","betas":["fast-mode-2026-02-01"]}`,
			want: betasWithFastMode,
		},
	}

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fast-mode-beta"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extraBetas, body := extractAndRemoveBetas([]byte(tt.body))
			req := newClaudeHeaderTestRequest(t, nil)
			if errApply := applyClaudeHeaders(req, auth, "key-fast-mode-beta", false, extraBetas, body, nil, nil, false); errApply != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", errApply)
			}
			if got := req.Header.Get("Anthropic-Beta"); got != tt.want {
				t.Fatalf("Anthropic-Beta = %q, want %q", got, tt.want)
			}
		})
	}
}

func assertClaudeCredentialIdentity(t *testing.T, body []byte, headers http.Header, deviceIDs []string, accountUUID string) {
	t.Helper()
	userID := gjson.GetBytes(body, "metadata.user_id").String()
	deviceID := gjson.Get(userID, "device_id").String()
	inPool := false
	for _, candidate := range deviceIDs {
		if deviceID == candidate {
			inPool = true
			break
		}
	}
	if !inPool {
		t.Fatalf("device_id = %q, want selected credential device pool entry", deviceID)
	}
	if got := gjson.Get(userID, "account_uuid").String(); got != accountUUID {
		t.Fatalf("account_uuid = %q, want selected credential account %q", got, accountUUID)
	}
	sessionID := gjson.Get(userID, "session_id").String()
	if sessionID == "" || sessionID != headers.Get("X-Claude-Code-Session-Id") {
		t.Fatalf("metadata session_id = %q, header session ID = %q", sessionID, headers.Get("X-Claude-Code-Session-Id"))
	}
	resigned, errResign := finalizeAnthropicMessagesBodyCCH(body, "")
	if errResign != nil {
		t.Fatalf("re-finalize Claude CCH: %v", errResign)
	}
	if !bytes.Equal(resigned, body) {
		t.Fatal("Claude CCH was calculated before final credential metadata rewrite")
	}
}

// assertClaudeCountTokensIdentity pins the count_tokens shape captured from real
// Claude Code 2.1.220: the endpoint carries no metadata whatsoever. Anthropic
// rejects the field there with "metadata: Extra inputs are not permitted", so the
// credential identity travels only on the header and on the Messages endpoint.
func assertClaudeCountTokensIdentity(t *testing.T, body []byte, headers http.Header) {
	t.Helper()
	if got := gjson.GetBytes(body, "metadata"); got.Exists() {
		t.Fatalf("count_tokens metadata = %s, want it absent", got.Raw)
	}
	if got := headers.Get("X-Claude-Code-Session-Id"); got == "" {
		t.Fatal("count_tokens is missing X-Claude-Code-Session-Id")
	}
	resigned, errResign := finalizeAnthropicMessagesBodyCCH(body, "")
	if errResign != nil {
		t.Fatalf("re-finalize Claude CCH: %v", errResign)
	}
	if !bytes.Equal(resigned, body) {
		t.Fatal("count_tokens CCH was calculated before the final body rewrite")
	}
}

func TestApplyClaudeHeaders_UsesConfiguredBaselineFingerprint(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.70 (external, cli)",
			PackageVersion:         "0.80.0",
			RuntimeVersion:         "v24.5.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			Timeout:                "900",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-baseline",
		Attributes: map[string]string{
			"api_key":                            "key-baseline",
			"header:User-Agent":                  "evil-client/9.9",
			"header:X-Stainless-Os":              "Linux",
			"header:X-Stainless-Arch":            "x64",
			"header:X-Stainless-Package-Version": "9.9.9",
		},
	}
	incoming := http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	}

	req := newClaudeHeaderTestRequest(t, incoming)
	applyClaudeHeaders(req, auth, "key-baseline", false, nil, nil, cfg, nil, false)

	assertClaudeFingerprint(t, req.Header, "evil-client/9.9", "9.9.9", "v24.5.0", "Linux", "x64")
	if got := req.Header.Get("X-Stainless-Timeout"); got != "900" {
		t.Fatalf("X-Stainless-Timeout = %q, want %q", got, "900")
	}
}

func TestApplyClaudeHeaders_RejectsUnmeasuredClaudeCLIFingerprints(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.60 (external, cli)",
			PackageVersion:         "0.70.0",
			RuntimeVersion:         "v22.0.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-upgrade",
		Attributes: map[string]string{
			"api_key": "key-upgrade",
		},
	}

	firstReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.62 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.74.0"},
		"X-Stainless-Runtime-Version": []string{"v24.3.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(firstReq, auth, "key-upgrade", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, firstReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "MacOS", "arm64")

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"lobe-chat/1.0"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-upgrade", false, nil, nil, cfg, nil, false)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "MacOS", "arm64")

	higherReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.63 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.75.0"},
		"X-Stainless-Runtime-Version": []string{"v24.4.0"},
		"X-Stainless-Os":              []string{"MacOS"},
		"X-Stainless-Arch":            []string{"arm64"},
	})
	applyClaudeHeaders(higherReq, auth, "key-upgrade", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, higherReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "MacOS", "arm64")

	lowerReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.61 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.73.0"},
		"X-Stainless-Runtime-Version": []string{"v24.2.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(lowerReq, auth, "key-upgrade", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, lowerReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "MacOS", "arm64")
}

func TestApplyClaudeHeaders_DoesNotDowngradeConfiguredBaselineOnFirstClaudeClient(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.70 (external, cli)",
			PackageVersion:         "0.80.0",
			RuntimeVersion:         "v24.5.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-baseline-floor",
		Attributes: map[string]string{
			"api_key": "key-baseline-floor",
		},
	}

	olderClaudeReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.62 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.74.0"},
		"X-Stainless-Runtime-Version": []string{"v24.3.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(olderClaudeReq, auth, "key-baseline-floor", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, olderClaudeReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")

	newerClaudeReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.71 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.81.0"},
		"X-Stainless-Runtime-Version": []string{"v24.6.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(newerClaudeReq, auth, "key-baseline-floor", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, newerClaudeReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")
}

func TestApplyClaudeHeaders_UpgradesCachedSoftwareFingerprintWhenBaselineAdvances(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	oldCfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.70 (external, cli)",
			PackageVersion:         "0.80.0",
			RuntimeVersion:         "v24.5.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	newCfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.77 (external, cli)",
			PackageVersion:         "0.87.0",
			RuntimeVersion:         "v24.8.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-baseline-reload",
		Attributes: map[string]string{
			"api_key": "key-baseline-reload",
		},
	}

	officialReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.71 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.81.0"},
		"X-Stainless-Runtime-Version": []string{"v24.6.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(officialReq, auth, "key-baseline-reload", false, nil, nil, oldCfg, nil, true)
	assertClaudeFingerprint(t, officialReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-baseline-reload", false, nil, nil, newCfg, nil, false)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.77 (external, cli)", "0.87.0", "v24.8.0", "MacOS", "arm64")
}

func TestApplyClaudeHeaders_LearnsOfficialFingerprintAfterCustomBaselineFallback(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "my-gateway/1.0",
			PackageVersion:         "custom-pkg",
			RuntimeVersion:         "custom-runtime",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-custom-baseline-learning",
		Attributes: map[string]string{
			"api_key": "key-custom-baseline-learning",
		},
	}

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-custom-baseline-learning", false, nil, nil, cfg, nil, false)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "my-gateway/1.0", "custom-pkg", "custom-runtime", "MacOS", "arm64")

	officialReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.77 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.87.0"},
		"X-Stainless-Runtime-Version": []string{"v24.8.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(officialReq, auth, "key-custom-baseline-learning", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, officialReq.Header, "my-gateway/1.0", "custom-pkg", "custom-runtime", "MacOS", "arm64")

	postLearningThirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(postLearningThirdPartyReq, auth, "key-custom-baseline-learning", false, nil, nil, cfg, nil, false)
	assertClaudeFingerprint(t, postLearningThirdPartyReq.Header, "my-gateway/1.0", "custom-pkg", "custom-runtime", "MacOS", "arm64")
}

func TestResolveClaudeDeviceProfile_RechecksCacheBeforeStoringCandidate(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.60 (external, cli)",
			PackageVersion:         "0.70.0",
			RuntimeVersion:         "v22.0.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-racy-upgrade",
		Attributes: map[string]string{
			"api_key": "key-racy-upgrade",
		},
	}

	lowPaused := make(chan struct{})
	releaseLow := make(chan struct{})
	var pauseOnce sync.Once
	var releaseOnce sync.Once

	helps.ClaudeDeviceProfileBeforeCandidateStore = func(candidate helps.ClaudeDeviceProfile) {
		if candidate.UserAgent != "claude-cli/2.1.60 (external, cli)" {
			return
		}
		pause := false
		pauseOnce.Do(func() {
			pause = true
			close(lowPaused)
		})
		if pause {
			<-releaseLow
		}
	}
	t.Cleanup(func() {
		helps.ClaudeDeviceProfileBeforeCandidateStore = nil
		releaseOnce.Do(func() { close(releaseLow) })
	})

	lowResultCh := make(chan helps.ClaudeDeviceProfile, 1)
	go func() {
		lowResultCh <- helps.ResolveClaudeDeviceProfile(auth, "key-racy-upgrade", http.Header{
			"User-Agent":                  []string{"claude-cli/2.1.60 (external, cli)"},
			"X-Stainless-Package-Version": []string{"0.70.0"},
			"X-Stainless-Runtime-Version": []string{"v22.0.0"},
			"X-Stainless-Os":              []string{"Linux"},
			"X-Stainless-Arch":            []string{"x64"},
		}, cfg)
	}()

	select {
	case <-lowPaused:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lower candidate to pause before storing")
	}

	highResult := helps.ResolveClaudeDeviceProfile(auth, "key-racy-upgrade", http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.60 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.70.0"},
		"X-Stainless-Runtime-Version": []string{"v22.0.0"},
		"X-Stainless-Os":              []string{"MacOS"},
		"X-Stainless-Arch":            []string{"arm64"},
	}, cfg)
	releaseOnce.Do(func() { close(releaseLow) })

	select {
	case lowResult := <-lowResultCh:
		if lowResult.UserAgent != "claude-cli/2.1.60 (external, cli)" {
			t.Fatalf("lowResult.UserAgent = %q, want %q", lowResult.UserAgent, "claude-cli/2.1.60 (external, cli)")
		}
		if lowResult.PackageVersion != "0.70.0" {
			t.Fatalf("lowResult.PackageVersion = %q, want %q", lowResult.PackageVersion, "0.70.0")
		}
		if lowResult.OS != "MacOS" || lowResult.Arch != "arm64" {
			t.Fatalf("lowResult platform = %s/%s, want %s/%s", lowResult.OS, lowResult.Arch, "MacOS", "arm64")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lower candidate result")
	}

	if highResult.UserAgent != "claude-cli/2.1.60 (external, cli)" {
		t.Fatalf("highResult.UserAgent = %q, want %q", highResult.UserAgent, "claude-cli/2.1.60 (external, cli)")
	}
	if highResult.OS != "MacOS" || highResult.Arch != "arm64" {
		t.Fatalf("highResult platform = %s/%s, want %s/%s", highResult.OS, highResult.Arch, "MacOS", "arm64")
	}

	cached := helps.ResolveClaudeDeviceProfile(auth, "key-racy-upgrade", http.Header{
		"User-Agent": []string{"curl/8.7.1"},
	}, cfg)
	if cached.UserAgent != "claude-cli/2.1.60 (external, cli)" {
		t.Fatalf("cached.UserAgent = %q, want %q", cached.UserAgent, "claude-cli/2.1.60 (external, cli)")
	}
	if cached.PackageVersion != "0.70.0" {
		t.Fatalf("cached.PackageVersion = %q, want %q", cached.PackageVersion, "0.70.0")
	}
	if cached.OS != "MacOS" || cached.Arch != "arm64" {
		t.Fatalf("cached platform = %s/%s, want %s/%s", cached.OS, cached.Arch, "MacOS", "arm64")
	}
}

func TestApplyClaudeHeaders_ThirdPartyBaselineThenOfficialUpgradeKeepsPinnedPlatform(t *testing.T) {
	resetClaudeDeviceProfileCache()
	stabilize := true

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.70 (external, cli)",
			PackageVersion:         "0.80.0",
			RuntimeVersion:         "v24.5.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-third-party-then-official",
		Attributes: map[string]string{
			"api_key": "key-third-party-then-official",
		},
	}

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-third-party-then-official", false, nil, nil, cfg, nil, false)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")

	officialReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.77 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.87.0"},
		"X-Stainless-Runtime-Version": []string{"v24.8.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(officialReq, auth, "key-third-party-then-official", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, officialReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")
}

func TestApplyClaudeHeaders_DisableDeviceProfileStabilization(t *testing.T) {
	resetClaudeDeviceProfileCache()

	stabilize := false
	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.60 (external, cli)",
			PackageVersion:         "0.70.0",
			RuntimeVersion:         "v22.0.0",
			OS:                     "MacOS",
			Arch:                   "arm64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-disable-stability",
		Attributes: map[string]string{
			"api_key": "key-disable-stability",
		},
	}

	firstReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.62 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.74.0"},
		"X-Stainless-Runtime-Version": []string{"v24.3.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(firstReq, auth, "key-disable-stability", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, firstReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "MacOS", "arm64")

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"lobe-chat/1.0"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-disable-stability", false, nil, nil, cfg, nil, false)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", helps.MapStainlessOS(), helps.MapStainlessArch())

	lowerReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.61 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.73.0"},
		"X-Stainless-Runtime-Version": []string{"v24.2.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(lowerReq, auth, "key-disable-stability", false, nil, nil, cfg, nil, true)
	assertClaudeFingerprint(t, lowerReq.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "MacOS", "arm64")
}

func TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients(t *testing.T) {
	resetClaudeDeviceProfileCache()

	stabilize := false
	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.60 (external, cli)",
			PackageVersion:         "0.70.0",
			RuntimeVersion:         "v22.0.0",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-legacy-ua-override",
		Attributes: map[string]string{
			"api_key":           "key-legacy-ua-override",
			"header:User-Agent": "config-ua/1.0",
		},
	}

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.62 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.74.0"},
		"X-Stainless-Runtime-Version": []string{"v24.3.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(req, auth, "key-legacy-ua-override", false, nil, nil, cfg, nil, true)

	assertClaudeFingerprint(t, req.Header, "config-ua/1.0", "0.70.0", "v22.0.0", helps.MapStainlessOS(), helps.MapStainlessArch())
}

func TestApplyClaudeHeaders_LegacyThirdPartyUsesStableConfiguredOSArch(t *testing.T) {
	resetClaudeDeviceProfileCache()

	stabilize := false
	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:              "claude-cli/2.1.60 (external, cli)",
			PackageVersion:         "0.70.0",
			RuntimeVersion:         "v22.0.0",
			OS:                     "Windows",
			Arch:                   "x64",
			StabilizeDeviceProfile: &stabilize,
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-legacy-runtime-os-arch",
		Attributes: map[string]string{
			"api_key": "key-legacy-runtime-os-arch",
		},
	}

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"curl/8.7.1"},
	})
	applyClaudeHeaders(req, auth, "key-legacy-runtime-os-arch", false, nil, nil, cfg, nil, false)

	assertClaudeFingerprint(t, req.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "Windows", "x64")
}

func TestApplyClaudeHeaders_UnsetStabilizationUsesStableConfiguredOSArch(t *testing.T) {
	resetClaudeDeviceProfileCache()

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:      "claude-cli/2.1.60 (external, cli)",
			PackageVersion: "0.70.0",
			RuntimeVersion: "v22.0.0",
			OS:             "Linux",
			Arch:           "x64",
		},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-unset-runtime-os-arch",
		Attributes: map[string]string{
			"api_key": "key-unset-runtime-os-arch",
		},
	}

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"curl/8.7.1"},
	})
	applyClaudeHeaders(req, auth, "key-unset-runtime-os-arch", false, nil, nil, cfg, nil, false)

	assertClaudeFingerprint(t, req.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", "Linux", "x64")
}

func TestApplyClaudeHeaders_UsesOAuthAuthorizationAndBrowserFingerprint(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-oat-header-test"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if errHeaders := applyClaudeHeaders(req, auth, "sk-ant-oat-header-test", false, nil, nil, &config.Config{}, nil, false, "11111111-2222-4333-8444-555555555555"); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-ant-oat-header-test" {
		t.Fatalf("Authorization = %q, want OAuth bearer", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want empty for OAuth", got)
	}
	if got := req.Header.Get("Anthropic-Dangerous-Direct-Browser-Access"); got != "true" {
		t.Fatalf("Anthropic-Dangerous-Direct-Browser-Access = %q, want true", got)
	}
	if got := req.Header.Get("Anthropic-Beta"); !strings.Contains(got, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want OAuth beta", got)
	}
}

func TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-sdk-fingerprint",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	assertClaudeFingerprint(t, seenHeaders, "claude-cli/2.1.220 (external, cli)", "0.94.0", "v26.3.0", helps.MapStainlessOS(), helps.MapStainlessArch())
	if got := seenHeaders.Get("X-App"); got != "cli" {
		t.Fatalf("X-App = %q, want cli", got)
	}
	if want := claudeCodeCLIBetas(payload, nil, false); seenHeaders.Get("Anthropic-Beta") != want {
		t.Fatalf("Anthropic-Beta = %q, want %q", seenHeaders.Get("Anthropic-Beta"), want)
	}

	system := gjson.GetBytes(seenBody, "system").Array()
	if len(system) != 2 {
		t.Fatalf("system block count = %d, want 2: %s", len(system), seenBody)
	}
	if got := system[0].Get("text").String(); got != "x-anthropic-billing-header: cc_version=2.1.220.04c; cc_entrypoint=cli;" {
		t.Fatalf("billing header = %q, want 2.1.220 CLI fingerprint", got)
	}
	if got := system[1].Get("text").String(); got != claudeCodeCLIIdentity {
		t.Fatalf("system[1].text = %q, want official CLI identity", got)
	}
	if got := system[1].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system[1].cache_control.type = %q, want ephemeral", got)
	}
	// This credential is an API key, and native only selects the 1h cache pool for
	// OAuth. The body ttl therefore has to stay absent, matching the fact that
	// claudeCodeCLIBetas does not emit extended-cache-ttl-2025-04-11 here either.
	if system[1].Get("cache_control.ttl").Exists() {
		t.Fatalf("API-key request must not carry a 1h body ttl: %s", system[1].Raw)
	}
	if betas := seenHeaders.Get("Anthropic-Beta"); strings.Contains(betas, claudeExtendedCacheTTLBeta) {
		t.Fatalf("API-key request must not declare extended-cache-ttl: %s", betas)
	}
	content := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("messages[0].content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "x", "")

	userID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	if !helps.IsValidUserID(userID) {
		t.Fatalf("metadata.user_id = %q, want Claude Code 2.1.220 JSON shape", userID)
	}
	if got, want := gjson.Get(userID, "session_id").String(), seenHeaders.Get("X-Claude-Code-Session-Id"); got != want {
		t.Fatalf("metadata session_id = %q, header session ID = %q", got, want)
	}
}

func TestClaudeExecutor_ConfirmedClaudeCodeRequestPreservesInteractiveIdentity(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const sessionID = "11111111-2222-4333-8444-555555555555"
	const userID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":"interactive-system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	incoming := http.Header{
		"User-Agent":                  {"claude-cli/2.1.220 (external, cli)"},
		"X-App":                       {"cli"},
		"Anthropic-Beta":              {"claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24"},
		"X-Claude-Code-Session-Id":    {sessionID},
		"X-Stainless-Package-Version": {"0.94.0"},
		"X-Stainless-Runtime-Version": {"v26.3.0"},
		"X-Stainless-Os":              {"MacOS"},
		"X-Stainless-Arch":            {"arm64"},
	}
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-confirmed-client",
		"base_url": server.URL,
	}}

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	assertClaudeFingerprint(t, seenHeaders, "claude-cli/2.1.220 (external, cli)", "0.94.0", "v26.3.0", "MacOS", "arm64")
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); got != "interactive-system" {
		t.Fatalf("system.0.text = %q, want confirmed client system preserved", got)
	}
	if got := gjson.GetBytes(seenBody, "system.#").Int(); got != 1 {
		t.Fatalf("system block count = %d, want 1", got)
	}
	if got := gjson.GetBytes(seenBody, "metadata.user_id").String(); got != userID {
		t.Fatalf("metadata.user_id = %q, want preserved %q", got, userID)
	}
	if got := seenHeaders.Get("Anthropic-Beta"); got != incoming.Get("Anthropic-Beta") {
		t.Fatalf("Anthropic-Beta = %q, want preserved %q", got, incoming.Get("Anthropic-Beta"))
	}
}

func TestClaudeExecutor_ConfirmedClaudeCodeWithoutCacheControlPreservesContent(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-stream"},
		{name: "stream", stream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenBody, _ = io.ReadAll(r.Body)
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			const sessionID = "11111111-2222-4333-8444-555555555555"
			const userID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`
			payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
			incoming := http.Header{
				"User-Agent":               {"claude-cli/2.1.220 (external, cli)"},
				"X-App":                    {"cli"},
				"Anthropic-Beta":           {"claude-code-20250219"},
				"X-Claude-Code-Session-Id": {sessionID},
			}
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-confirmed-markerless",
				"base_url": server.URL,
			}}
			req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}
			opts := cliproxyexecutor.Options{
				Stream:          tt.stream,
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: payload,
				Headers:         incoming,
			}

			if tt.stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}

			content := gjson.GetBytes(seenBody, "messages.0.content")
			if content.Type != gjson.String || content.String() != "x" {
				t.Fatalf("messages.0.content = %s, want native string content preserved; body=%s", content.Raw, seenBody)
			}
			if gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
				t.Fatalf("confirmed markerless native request received synthetic cache_control: %s", seenBody)
			}
		})
	}
}

func TestClaudeExecutor_ConfirmedVSCodeAgentSDKRequestPreservesIdentity(t *testing.T) {
	helps.ResetClaudeDeviceProfileCache()
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const sessionID = "22222222-3333-4444-8555-666666666666"
	const userID = `{"device_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","account_uuid":"","session_id":"22222222-3333-4444-8555-666666666666"}`
	const vscodeUA = "claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)"
	const billingHeader = "x-anthropic-billing-header: cc_version=2.1.220.04c; cc_entrypoint=claude-vscode;"
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":` + fmt.Sprintf("%q", billingHeader) + `},{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"vscode-agent-system"}],"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	incoming := http.Header{
		"User-Agent":     {vscodeUA},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Claude-Code-Session-Id":                  {sessionID},
		"X-Stainless-Package-Version":               {"0.94.0"},
		"X-Stainless-Runtime-Version":               {"v26.3.0"},
		"X-Stainless-Os":                            {"MacOS"},
		"X-Stainless-Arch":                          {"arm64"},
	}
	stabilize := true
	executor := NewClaudeExecutor(&config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{StabilizeDeviceProfile: &stabilize}})
	auth := &cliproxyauth.Auth{ID: "auth-vscode-agent-sdk", Attributes: map[string]string{
		"api_key":  "key-vscode-agent-sdk",
		"base_url": server.URL,
	}}

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	assertClaudeFingerprint(t, seenHeaders, vscodeUA, "0.94.0", "v26.3.0", "MacOS", "arm64")
	if got := seenHeaders.Get("Anthropic-Dangerous-Direct-Browser-Access"); got != "true" {
		t.Fatalf("Anthropic-Dangerous-Direct-Browser-Access = %q, want preserved true", got)
	}
	if got := seenHeaders.Get("X-Claude-Code-Session-Id"); got != sessionID {
		t.Fatalf("X-Claude-Code-Session-Id = %q, want preserved %q", got, sessionID)
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); got != billingHeader {
		t.Fatalf("system.0.text = %q, want VSCode attribution preserved", got)
	}
	if got := gjson.GetBytes(seenBody, "system.1.text").String(); got != "You are a Claude agent, built on Anthropic's Claude Agent SDK." {
		t.Fatalf("system.1.text = %q, want VSCode Agent SDK identity preserved", got)
	}
	if got := gjson.GetBytes(seenBody, "system.1.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("system.1.cache_control.ttl = %q, want preserved 1h", got)
	}
	if got := gjson.GetBytes(seenBody, "system.2.text").String(); got != "vscode-agent-system" {
		t.Fatalf("system.2.text = %q, want VSCode Agent SDK system preserved", got)
	}
	if got := gjson.GetBytes(seenBody, "system.#").Int(); got != 3 {
		t.Fatalf("system block count = %d, want 3", got)
	}
	if got := gjson.GetBytes(seenBody, "metadata.user_id").String(); got != userID {
		t.Fatalf("metadata.user_id = %q, want preserved %q", got, userID)
	}
}

func TestClaudeExecutor_CopiedVSCodeAgentSDKHeadersWithoutMetadataAreCloaked(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	payload := []byte(`{"model":"claude-opus-5","system":"spoofed-system","messages":[{"role":"user","content":"x"}]}`)
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-spoofed-client",
		"base_url": server.URL,
	}}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"User-Agent":     {"claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)"},
			"X-App":          {"cli"},
			"Anthropic-Beta": {"claude-code-20250219"},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := seenHeaders.Get("User-Agent"); got != "claude-cli/2.1.220 (external, cli)" {
		t.Fatalf("User-Agent = %q, want CLI cloak", got)
	}
	if got := gjson.GetBytes(seenBody, "system.#").Int(); got != 2 {
		t.Fatalf("system block count = %d, want billing and CLI identity only", got)
	}
	content := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("messages[0].content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "x", "")
	assertClaudeMidConversationSystemMessage(t, seenBody, 1, "spoofed-system", "")
}

func TestClaudeExecutor_AgentSDKEntrypointWithStrongSignalsUsesCLICloak(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	payload := []byte(`{"model":"claude-opus-4-6","system":"agent-sdk-system","messages":[{"role":"user","content":"x"}],"metadata":{"user_id":"agent-sdk-user"}}`)
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-agent-sdk-client",
		"base_url": server.URL,
	}}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"User-Agent":     {"claude-cli/2.1.220 (external, sdk-ts, agent-sdk/0.3.220)"},
			"X-App":          {"cli"},
			"Anthropic-Beta": {"claude-code-20250219"},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := seenHeaders.Get("User-Agent"); got != "claude-cli/2.1.220 (external, cli)" {
		t.Fatalf("User-Agent = %q, want CLI cloak", got)
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); !strings.Contains(got, "cc_entrypoint=cli;") {
		t.Fatalf("billing attribution = %q, want cli", got)
	}
	if got := gjson.GetBytes(seenBody, "system.1.text").String(); got != claudeCodeCLIIdentity {
		t.Fatalf("system.1.text = %q, want official CLI identity", got)
	}
}

func TestClaudeExecutor_ConfirmedVSCodeOAuthPreservesToolNames(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const userID = `{"device_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","account_uuid":"","session_id":"33333333-4444-4555-8666-777777777777"}`
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.04c; cc_entrypoint=claude-vscode; cch=00000;"}],"tools":[{"name":"bash","description":"known native name must pass through","input_schema":{"type":"object"}},{"name":"search_web","description":"unknown native name must pass through","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-native-vscode",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"account_uuid":      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"claude_device_ids": deviceIDs,
			"cloak_mode":        "always",
		},
	}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"User-Agent":                  {"claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)"},
			"X-App":                       {"cli"},
			"Anthropic-Beta":              {"claude-code-20250219"},
			"X-Stainless-Package-Version": {"0.94.0"},
			"X-Stainless-Runtime-Version": {"v26.3.0"},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); got != "bash" {
		t.Fatalf("tools.0.name = %q, want confirmed native known name preserved", got)
	}
	if got := gjson.GetBytes(seenBody, "tools.1.name").String(); got != "search_web" {
		t.Fatalf("tools.1.name = %q, want confirmed native unknown name preserved", got)
	}
	assertClaudeCredentialIdentity(t, seenBody, seenHeaders, deviceIDs, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	upstreamUserID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	if upstreamDeviceID := gjson.Get(upstreamUserID, "device_id").String(); upstreamDeviceID == strings.Repeat("c", 64) {
		t.Fatalf("device_id = %q, want native device replaced by credential pool", upstreamDeviceID)
	}
	if got := gjson.Get(upstreamUserID, "session_id").String(); got != "33333333-4444-4555-8666-777777777777" {
		t.Fatalf("session_id = %q, want downstream agent session", got)
	}
	if got := seenHeaders.Get("X-Claude-Code-Session-Id"); got != "33333333-4444-4555-8666-777777777777" {
		t.Fatalf("X-Claude-Code-Session-Id = %q, want downstream agent session", got)
	}
}

func TestClaudeDeviceProfileStabilizationEnabled_DefaultFalse(t *testing.T) {
	if helps.ClaudeDeviceProfileStabilizationEnabled(nil) {
		t.Fatal("expected nil config to default to disabled stabilization")
	}
	if helps.ClaudeDeviceProfileStabilizationEnabled(&config.Config{}) {
		t.Fatal("expected unset stabilize-device-profile to default to disabled stabilization")
	}
}

func TestApplyClaudeToolPrefix(t *testing.T) {
	input := []byte(`{"tools":[{"name":"alpha"},{"name":"proxy_bravo"}],"tool_choice":{"type":"tool","name":"charlie"},"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"delta","id":"t1","input":{}}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_alpha" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_alpha")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_bravo" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_bravo")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "proxy_charlie" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "proxy_charlie")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_delta" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_delta")
	}
}

func TestApplyClaudeToolPrefix_WithToolReference(t *testing.T) {
	input := []byte(`{"tools":[{"name":"alpha"}],"messages":[{"role":"user","content":[{"type":"tool_reference","tool_name":"beta"},{"type":"tool_reference","tool_name":"proxy_gamma"}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "messages.0.content.0.tool_name").String(); got != "proxy_beta" {
		t.Fatalf("messages.0.content.0.tool_name = %q, want %q", got, "proxy_beta")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.tool_name").String(); got != "proxy_gamma" {
		t.Fatalf("messages.0.content.1.tool_name = %q, want %q", got, "proxy_gamma")
	}
}

func TestSanitizeClaudeWebSearchDomains(t *testing.T) {
	// Mirrors the litellm payload from issue #2681: a non-empty allowed_domains
	// alongside an empty blocked_domains, which Anthropic rejects as ambiguous.
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search","allowed_domains":["anthropic.com"],"blocked_domains":[],"max_uses":8}]}`)
	out := sanitizeClaudeWebSearchDomains(input)

	if gjson.GetBytes(out, "tools.0.blocked_domains").Exists() {
		t.Fatalf("empty blocked_domains should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.allowed_domains").Array(); len(got) != 1 || got[0].String() != "anthropic.com" {
		t.Fatalf("non-empty allowed_domains should be preserved: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.max_uses").Int(); got != 8 {
		t.Fatalf("max_uses should be preserved: got %d", got)
	}
}

func TestSanitizeClaudeWebSearchDomains_LeavesNonBuiltinAndNonEmpty(t *testing.T) {
	// Empty arrays on non-web_search tools must be left untouched.
	input := []byte(`{"tools":[{"type":"custom","name":"x","blocked_domains":[]},{"type":"web_search_20250305","name":"web_search","blocked_domains":["evil.com"]}]}`)
	out := sanitizeClaudeWebSearchDomains(input)

	if !gjson.GetBytes(out, "tools.0.blocked_domains").Exists() {
		t.Fatalf("non-web_search tool fields should be untouched: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.1.blocked_domains").Array(); len(got) != 1 || got[0].String() != "evil.com" {
		t.Fatalf("non-empty blocked_domains should be preserved: %s", string(out))
	}
}

func TestApplyClaudeToolPrefix_SkipsBuiltinTools(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"my_custom_tool","input_schema":{"type":"object"}}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("built-in tool name should not be prefixed: tools.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_my_custom_tool" {
		t.Fatalf("custom tool should be prefixed: tools.1.name = %q, want %q", got, "proxy_my_custom_tool")
	}
}

func TestApplyClaudeToolPrefix_BuiltinToolSkipped(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
			{"name": "Read"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "web_search", "id": "ws1", "input": {}},
				{"type": "tool_use", "name": "Read", "id": "r1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("tools.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "web_search" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Read" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Read")
	}
}

func TestApplyClaudeToolPrefix_KnownBuiltinInHistoryOnly(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name": "Read"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "web_search", "id": "ws1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "web_search" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
	}
}

func TestApplyClaudeToolPrefix_CustomToolsPrefixed(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "Read"}, {"name": "Write"}],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "Read", "id": "r1", "input": {}},
				{"type": "tool_use", "name": "Write", "id": "w1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Write" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Write")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_Read" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Write" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Write")
	}
}

func TestApplyClaudeToolPrefix_ToolChoiceBuiltin(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search"},
			{"name": "Read"}
		],
		"tool_choice": {"type": "tool", "name": "web_search"}
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "web_search" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "web_search")
	}
}

func TestApplyClaudeToolPrefix_KnownFallbackBuiltinsRemainUnprefixed(t *testing.T) {
	for _, builtin := range []string{"web_search", "code_execution", "text_editor", "computer"} {
		t.Run(builtin, func(t *testing.T) {
			input := []byte(fmt.Sprintf(`{
				"tools":[{"name":"Read"}],
				"tool_choice":{"type":"tool","name":%q},
				"messages":[{"role":"assistant","content":[{"type":"tool_use","name":%q,"id":"toolu_1","input":{}},{"type":"tool_reference","tool_name":%q},{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":%q}]}]}]
			}`, builtin, builtin, builtin, builtin))
			out := applyClaudeToolPrefix(input, "proxy_")

			if got := gjson.GetBytes(out, "tool_choice.name").String(); got != builtin {
				t.Fatalf("tool_choice.name = %q, want %q", got, builtin)
			}
			if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != builtin {
				t.Fatalf("messages.0.content.0.name = %q, want %q", got, builtin)
			}
			if got := gjson.GetBytes(out, "messages.0.content.1.tool_name").String(); got != builtin {
				t.Fatalf("messages.0.content.1.tool_name = %q, want %q", got, builtin)
			}
			if got := gjson.GetBytes(out, "messages.0.content.2.content.0.tool_name").String(); got != builtin {
				t.Fatalf("messages.0.content.2.content.0.tool_name = %q, want %q", got, builtin)
			}
			if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
				t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
			}
		})
	}
}

func TestStripClaudeToolPrefixFromResponse(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_use","name":"proxy_alpha","id":"t1","input":{}},{"type":"tool_use","name":"bravo","id":"t2","input":{}}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")

	if got := gjson.GetBytes(out, "content.0.name").String(); got != "alpha" {
		t.Fatalf("content.0.name = %q, want %q", got, "alpha")
	}
	if got := gjson.GetBytes(out, "content.1.name").String(); got != "bravo" {
		t.Fatalf("content.1.name = %q, want %q", got, "bravo")
	}
}

func TestStripClaudeToolPrefixFromResponse_WithToolReference(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_reference","tool_name":"proxy_alpha"},{"type":"tool_reference","tool_name":"bravo"}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")

	if got := gjson.GetBytes(out, "content.0.tool_name").String(); got != "alpha" {
		t.Fatalf("content.0.tool_name = %q, want %q", got, "alpha")
	}
	if got := gjson.GetBytes(out, "content.1.tool_name").String(); got != "bravo" {
		t.Fatalf("content.1.tool_name = %q, want %q", got, "bravo")
	}
}

func TestStripClaudeToolPrefixFromStreamLine(t *testing.T) {
	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"proxy_alpha","id":"t1"},"index":0}`)
	out := stripClaudeToolPrefixFromStreamLine(line, "proxy_")

	payload := bytes.TrimSpace(out)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.name").String(); got != "alpha" {
		t.Fatalf("content_block.name = %q, want %q", got, "alpha")
	}
}

func TestStripClaudeToolPrefixFromStreamLine_WithToolReference(t *testing.T) {
	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_reference","tool_name":"proxy_beta"},"index":0}`)
	out := stripClaudeToolPrefixFromStreamLine(line, "proxy_")

	payload := bytes.TrimSpace(out)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.tool_name").String(); got != "beta" {
		t.Fatalf("content_block.tool_name = %q, want %q", got, "beta")
	}
}

func TestApplyClaudeToolPrefix_PreservesNestedMCPToolReference(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"mcp__nia__manage_resource"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want MCP name preserved", got)
	}
}

func TestClaudeExecutor_ExecuteStripsOpenAIEncryptedThinkingBeforeUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if strings.Contains(string(seenBody), "gAAAAABopenai-encrypted-content") || strings.Contains(string(seenBody), "codex reasoning") {
		t.Fatalf("invalid thinking block was forwarded: %s", string(seenBody))
	}
	content := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content length = %d, want 1: %s", len(content), string(seenBody))
	}
	if got := content[0].Get("text").String(); got != "Answer" {
		t.Fatalf("remaining content text = %q, want Answer", got)
	}
}

func TestClaudeExecutor_ExecuteStripsForeignToolUseSignaturesBeforeUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{
					"type":"tool_use",
					"id":"toolu_1",
					"name":"lookup",
					"input":{"q":"x"},
					"signature":"skip_thought_signature_validator",
					"thought_signature":"skip_thought_signature_validator",
					"extra_content":{"google":{"thought_signature":"skip_thought_signature_validator"}}
				}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	toolUse := gjson.GetBytes(seenBody, "messages.0.content.0")
	if !toolUse.Get("type").Exists() || toolUse.Get("type").String() != "tool_use" {
		t.Fatalf("tool_use block was not preserved: %s", string(seenBody))
	}
	for _, path := range []string{"signature", "thought_signature", "extra_content"} {
		if toolUse.Get(path).Exists() {
			t.Fatalf("foreign tool_use signature field %s was forwarded: %s", path, string(seenBody))
		}
	}
}

func TestShouldSanitizeClaudeMessagesForUpstream_OnlyClaudeFamily(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{model: "claude-sonnet-4-5", want: true},
		{model: "claude-3-5-sonnet-20241022", want: true},
		{model: "kimi-k2.5", want: false},
		{model: "mimo-v2", want: false},
		{model: "gemini-3.5-flash", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := shouldSanitizeClaudeMessagesForUpstream(tc.model)
			if got != tc.want {
				t.Errorf("shouldSanitizeClaudeMessagesForUpstream(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstream_BypassesUnknownModelSignatureMatrix(t *testing.T) {
	rawSignature := "skip_thought_signature_validator"
	body := []byte(`{
		"model": "kimi-k2.5",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "keep", "signature": "` + rawSignature + `"},
					{"type": "text", "text": "hello"},
					{"type": "tool_use", "id": "call_123", "name": "get_weather", "input": {}, "signature": "` + rawSignature + `"}
				]
			}
		]
	}`)

	output := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(context.Background(), body, "kimi-k2.5")
	parts := gjson.GetBytes(output, "messages.0.content").Array()
	if len(parts) != 3 {
		t.Fatalf("content length = %d, want 3 when sanitizer is bypassed: %s", len(parts), output)
	}
	if got := parts[0].Get("signature").String(); got != rawSignature {
		t.Fatalf("thinking signature = %q, want preserved %q", got, rawSignature)
	}
	if got := parts[2].Get("signature").String(); got != rawSignature {
		t.Fatalf("tool_use signature = %q, want preserved %q", got, rawSignature)
	}
}

func TestClaudeExecutor_ExecuteBypassesSignatureSanitizerForUnknownModel(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"mimo-v2","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"keep reasoning","signature":""},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mimo-v2",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if !strings.Contains(string(seenBody), "keep reasoning") {
		t.Fatalf("unknown-model thinking block should bypass Claude sanitizer: %s", string(seenBody))
	}
}

func TestClaudeExecutor_ExecuteStripsMalformedEPrefixThinkingBeforeUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	malformedSignature := malformedClaudeTreeSignatureForClaudeExecutorTest()
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"bad reasoning","signature":"` + malformedSignature + `"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if strings.Contains(string(seenBody), malformedSignature) || strings.Contains(string(seenBody), "bad reasoning") {
		t.Fatalf("malformed E-prefix thinking block was forwarded: %s", string(seenBody))
	}
	content := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content length = %d, want 1: %s", len(content), string(seenBody))
	}
	if got := content[0].Get("text").String(); got != "Answer" {
		t.Fatalf("remaining content text = %q, want Answer", got)
	}
}

func TestClaudeExecutor_ExecuteStripsInvalidBase64ThinkingBeforeUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"bad reasoning","signature":"E!!!invalid!!!"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if strings.Contains(string(seenBody), "E!!!invalid!!!") || strings.Contains(string(seenBody), "bad reasoning") {
		t.Fatalf("invalid-base64 thinking block was forwarded: %s", string(seenBody))
	}
	content := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content length = %d, want 1: %s", len(content), string(seenBody))
	}
}

func TestClaudeExecutor_ExecuteStripsEmptySignatureEmptyTextThinking(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","text":"","signature":""},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	content := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content length = %d, want 1: %s", len(content), string(seenBody))
	}
	if got := content[0].Get("type").String(); got != "text" {
		t.Fatalf("remaining content type = %q, want text: %s", got, string(seenBody))
	}
	if got := content[0].Get("text").String(); got != "Answer" {
		t.Fatalf("remaining content text = %q, want Answer: %s", got, string(seenBody))
	}
}

func TestClaudeExecutor_ExecuteStreamStripsOpenAIEncryptedThinkingBeforeUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if strings.Contains(string(seenBody), "gAAAAABopenai-encrypted-content") || strings.Contains(string(seenBody), "codex reasoning") {
		t.Fatalf("invalid thinking block was forwarded: %s", string(seenBody))
	}
}

func claudeOAuthCancellationTestMetadata() map[string]any {
	return map[string]any{
		"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
}

func TestClaudeExecutor_ExecuteStreamOAuthStartupCancellationIsRequestScoped(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "oauth-stream-startup-cancellation",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-stream-startup-cancellation",
			"base_url": server.URL,
		},
		Metadata: claudeOAuthCancellationTestMetadata(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		errCh <- errStream
	}()
	<-started
	cancel()

	select {
	case errStream := <-errCh:
		if !errors.Is(errStream, context.Canceled) {
			t.Fatalf("ExecuteStream() error = %v, want context.Canceled", errStream)
		}
		var requestErr cliproxyexecutor.RequestScopedError
		if !errors.As(errStream, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
			t.Fatalf("ExecuteStream() error = %T %v, want request-scoped cancellation", errStream, errStream)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for startup cancellation")
	}
}

func TestClaudeExecutor_ExecuteStreamOAuthCancellationIsRequestScoped(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "oauth-stream-cancellation",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-stream-cancellation",
			"base_url": server.URL,
		},
		Metadata: claudeOAuthCancellationTestMetadata(),
	}
	payload := []byte(`{"model":"claude-opus-5","system":"system prompt","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	ctx, cancel := context.WithCancel(context.Background())
	result, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errStream != nil {
		cancel()
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	<-started
	cancel()

	var cancellationErr error
	deadline := time.After(2 * time.Second)
	for cancellationErr == nil {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed without a cancellation result")
			}
			cancellationErr = chunk.Err
		case <-deadline:
			t.Fatal("timed out waiting for cancellation result")
		}
	}
	if !errors.Is(cancellationErr, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", cancellationErr)
	}
	var requestErr cliproxyexecutor.RequestScopedError
	if !errors.As(cancellationErr, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
		t.Fatalf("stream error = %T %v, want request-scoped cancellation", cancellationErr, cancellationErr)
	}
	var statusErr interface{ StatusCode() int }
	if errors.As(cancellationErr, &statusErr) {
		t.Fatalf("stream cancellation unexpectedly exposes HTTP status %d", statusErr.StatusCode())
	}
	for range result.Chunks {
	}
}

func TestClaudeExecutor_ExecuteStreamDirectPassthroughEmitsCompleteSSEEvents(t *testing.T) {
	firstData := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`
	secondData := `{"type":"message_stop"}`
	upstreamStream := "event: content_block_delta\n" +
		"data: " + firstData + "\n" +
		"\n" +
		"event: message_stop\n" +
		"data: " + secondData + "\n" +
		"\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(upstreamStream))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var payloads []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		payloads = append(payloads, string(chunk.Payload))
	}

	want := []string{
		"event: content_block_delta\n" + "data: " + firstData + "\n\n",
		"event: message_stop\n" + "data: " + secondData + "\n\n",
	}
	if len(payloads) != len(want) {
		t.Fatalf("payload count = %d, want %d: %#v", len(payloads), len(want), payloads)
	}
	for i := range want {
		if payloads[i] != want[i] {
			t.Fatalf("payload[%d] = %q, want %q", i, payloads[i], want[i])
		}
	}
}

// TestClaudeExecutor_ExecuteStreamDecodesCompressedSSE guards the dependency that
// lets CPA advertise the real client's Accept-Encoding on streaming requests:
// once compression is offered the upstream may compress the SSE body, so the
// streaming success path must decode it and still emit event boundaries intact.
func TestClaudeExecutor_ExecuteStreamDecodesCompressedSSE(t *testing.T) {
	firstData := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`
	secondData := `{"type":"message_stop"}`
	upstreamStream := "event: content_block_delta\n" +
		"data: " + firstData + "\n" +
		"\n" +
		"event: message_stop\n" +
		"data: " + secondData + "\n" +
		"\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		gzipWriter := gzip.NewWriter(w)
		if _, errWrite := gzipWriter.Write([]byte(upstreamStream)); errWrite != nil {
			t.Errorf("gzip write: %v", errWrite)
		}
		if errClose := gzipWriter.Close(); errClose != nil {
			t.Errorf("gzip close: %v", errClose)
		}
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var payloads []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		payloads = append(payloads, string(chunk.Payload))
	}

	want := []string{
		"event: content_block_delta\n" + "data: " + firstData + "\n\n",
		"event: message_stop\n" + "data: " + secondData + "\n\n",
	}
	if len(payloads) != len(want) {
		t.Fatalf("payload count = %d, want %d: %#v", len(payloads), len(want), payloads)
	}
	for i := range want {
		if payloads[i] != want[i] {
			t.Fatalf("payload[%d] = %q, want %q", i, payloads[i], want[i])
		}
	}
}

func TestClaudeExecutor_CountTokensExcludesInvalidOpenAIThinking(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	countTokens := func(payload []byte) int64 {
		t.Helper()
		resp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		if err != nil {
			t.Fatalf("CountTokens() error = %v", err)
		}
		return gjson.GetBytes(resp.Payload, "input_tokens").Int()
	}

	withInvalidThinking := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)
	withoutInvalidThinking := []byte(`{
		"messages": [
			{"role":"assistant","content":[{"type":"text","text":"Answer"}]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	if got, want := countTokens(withInvalidThinking), countTokens(withoutInvalidThinking); got != want {
		t.Fatalf("count with invalid thinking = %d, want sanitized count %d", got, want)
	}
}

func TestClaudeCountTokensBetasForCredentialMatchesNativeOAuth220(t *testing.T) {
	want := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,token-counting-2024-11-01"
	if got := claudeCountTokensBetasForCredential(true); got != want {
		t.Fatalf("OAuth count_tokens betas = %q, want %q", got, want)
	}
	wantAPIKey := "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,token-counting-2024-11-01"
	if got := claudeCountTokensBetasForCredential(false); got != wantAPIKey {
		t.Fatalf("API-key count_tokens betas = %q, want %q", got, wantAPIKey)
	}
	if got := withClaudeCountTokensOAuthBeta(wantAPIKey); got != want {
		t.Fatalf("confirmed-client count_tokens betas = %q, want %q", got, want)
	}
}

func TestShouldUseClaudeUpstreamTokenCount(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		baseURL string
		want    bool
	}{
		{name: "official OAuth", apiKey: "sk-ant-oat-official", baseURL: "https://api.anthropic.com", want: true},
		{name: "official API key", apiKey: "key-official", baseURL: "https://api.anthropic.com:443", want: true},
		{name: "custom OAuth", apiKey: "sk-ant-oat-custom", baseURL: "https://gateway.example"},
		{name: "custom API key", apiKey: "key-custom", baseURL: "https://gateway.example"},
		{name: "lookalike host", apiKey: "sk-ant-oat-lookalike", baseURL: "https://api.anthropic.com.example"},
		{name: "insecure official host", apiKey: "sk-ant-oat-http", baseURL: "http://api.anthropic.com"},
		{name: "missing credential", baseURL: "https://api.anthropic.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseClaudeUpstreamTokenCount(test.apiKey, test.baseURL); got != test.want {
				t.Fatalf("shouldUseClaudeUpstreamTokenCount() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestClaudeExecutor_LegacySystemReminderAcrossMessagesAndStream(t *testing.T) {
	var mu sync.Mutex
	captured := make(map[string][]byte)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "count_tokens") {
			t.Errorf("custom OAuth count_tokens unexpectedly reached upstream: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		kind := "messages"
		if gjson.GetBytes(body, "stream").Bool() {
			kind = "stream"
		}
		mu.Lock()
		captured[kind] = bytes.Clone(body)
		mu.Unlock()
		switch kind {
		case "stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_legacy","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
		}
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "oauth-legacy-reminder-paths",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-legacy-reminder-paths",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
	makePayload := func(userText string, stream bool) []byte {
		streamField := ""
		if stream {
			streamField = `,"stream":true`
		}
		return []byte(`{"model":"claude-opus-4-6","system":"legacy-system-prompt","messages":[{"role":"user","content":` + fmt.Sprintf("%q", userText) + `}]` + streamField + `}`)
	}

	if _, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-4-6", Payload: makePayload("messages-user", false),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	countResp, errCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-4-6", Payload: makePayload("count-user", false),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got := gjson.GetBytes(countResp.Payload, "input_tokens").Int(); got <= 0 {
		t.Fatalf("local count_tokens input_tokens = %d, want positive estimate", got)
	}
	streamResult, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-4-6", Payload: makePayload("stream-user", true),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	mu.Lock()
	bodies := map[string][]byte{
		"messages": bytes.Clone(captured["messages"]),
		"stream":   bytes.Clone(captured["stream"]),
	}
	mu.Unlock()
	for kind, wantUser := range map[string]string{"messages": "messages-user", "stream": "stream-user"} {
		body := bodies[kind]
		if len(body) == 0 {
			t.Fatalf("missing %s upstream capture", kind)
		}
		assertClaudeLegacySystemReminderLayout(t, body, "legacy-system-prompt", wantUser, "1h")
		if _, ok := claudeBillingCCHDigitsOffset(body); !ok {
			t.Fatalf("%s body is missing final CCH", kind)
		}
	}
}

func TestClaudeExecutor_CountTokensUpstreamCloakNeverPreservesCustomTool(t *testing.T) {
	var upstreamBody []byte
	var upstreamHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		upstreamHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer server.Close()

	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}
	auth := &cliproxyauth.Auth{
		ID: "oauth-never-count-tokens",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-never-count-tokens",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: deviceIDs,
			"cloak_mode":                          "never",
		},
	}
	payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"search"}],"tools":[{"name":"search_web","input_schema":{"type":"object"}}]}`)
	executor := NewClaudeExecutor(&config.Config{})
	_, errCount := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "count-never-agent-conversation",
		},
	})
	if errCount != nil {
		t.Fatalf("countTokensUpstream() error = %v", errCount)
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.name").String(); got != "search_web" {
		t.Fatalf("count_tokens tool name = %q, want cloak=never passthrough", got)
	}
	assertClaudeCountTokensIdentity(t, upstreamBody, upstreamHeaders)
}

func TestClaudeExecutor_CountTokensUpstreamConfirmedVSCodePreservesCustomTool(t *testing.T) {
	var upstreamName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "oauth-mcp-native-count-tokens",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-mcp-native-count-tokens",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"cloak_mode": "always",
		},
	}
	payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"search"}],"tools":[{"name":"search_web","input_schema":{"type":"object"}}]}`)
	_, errCount := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"User-Agent":     {"claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)"},
			"X-App":          {"cli"},
			"Anthropic-Beta": {"claude-code-20250219"},
		},
	})
	if errCount != nil {
		t.Fatalf("countTokensUpstream() error = %v", errCount)
	}
	if upstreamName != "search_web" {
		t.Fatalf("confirmed VSCode count_tokens tool name = %q, want unchanged", upstreamName)
	}
}

func TestClaudeExecutor_CountTokensCloakMatchesMeasuredDirectAnthropicShape(t *testing.T) {
	var upstreamBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":34}`)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-oat-cloaked-count-shape"}}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"tools":[{"name":"search_web","input_schema":{"type":"object"}}],"metadata":{"user_id":"remove"},"context_management":{"edits":[]},"diagnostics":{"previous_message_id":"remove"}}`)
	_, errCount := NewClaudeExecutor(&config.Config{}).countTokensUpstream(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount != nil {
		t.Fatalf("countTokensUpstream() error = %v", errCount)
	}
	if got := gjson.GetBytes(upstreamBody, "system"); got.Exists() {
		t.Fatalf("cloaked direct count system = %s, want absent", got.Raw)
	}
	for _, field := range []string{"metadata", "context_management", "diagnostics", "betas"} {
		if got := gjson.GetBytes(upstreamBody, field); got.Exists() {
			t.Fatalf("cloaked direct count %s = %s, want absent", field, got.Raw)
		}
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.name").String(); !helps.IsClaudeMCPToolName(got) {
		t.Fatalf("cloaked direct count tool = %q, want OAuth MCP alias", got)
	}
}

// TestClaudeExecutor_CountTokensCloakRelocatesCallerSystemAndObfuscates asserts
// that a cloaked direct-Anthropic count_tokens request keeps Claude Code's
// measured shape (no system field) while still accounting for the caller's
// system prompt and honouring sensitive-word obfuscation.
func TestClaudeExecutor_CountTokensCloakRelocatesCallerSystemAndObfuscates(t *testing.T) {
	const callerSystem = "third party ACMECORP orchestrator rules"
	const sensitiveWord = "ACMECORP"

	testCases := []struct {
		name          string
		model         string
		wantSystemMsg bool
	}{
		{name: "mid conversation system role", model: "claude-opus-5", wantSystemMsg: true},
		{name: "legacy system reminder", model: "claude-sonnet-4-5", wantSystemMsg: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamBody []byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var errRead error
				upstreamBody, errRead = io.ReadAll(req.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"input_tokens":34}`)),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":               "sk-ant-oat-count-relocate",
				"cloak_sensitive_words": sensitiveWord,
			}}
			payload := []byte(`{"model":"` + testCase.model + `","system":[{"type":"text","text":"` + callerSystem + `"}],` +
				`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"tools":[]}`)

			_, errCount := NewClaudeExecutor(&config.Config{}).countTokensUpstream(ctx, auth,
				cliproxyexecutor.Request{Model: testCase.model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if errCount != nil {
				t.Fatalf("countTokensUpstream() error = %v", errCount)
			}

			// Claude Code's count_tokens never carries a system field.
			if got := gjson.GetBytes(upstreamBody, "system"); got.Exists() {
				t.Fatalf("cloaked count system = %s, want absent", got.Raw)
			}
			// The caller's system prompt must still be counted, relocated into messages.
			// Compare decoded text so JSON escaping does not affect the assertions.
			var decodedTexts []string
			sawSystemRole := false
			gjson.GetBytes(upstreamBody, "messages").ForEach(func(_, message gjson.Result) bool {
				if message.Get("role").String() == "system" {
					sawSystemRole = true
				}
				message.Get("content").ForEach(func(_, block gjson.Result) bool {
					decodedTexts = append(decodedTexts, block.Get("text").String())
					return true
				})
				return true
			})
			joinedTexts := strings.Join(decodedTexts, "\n")
			if !strings.Contains(joinedTexts, "orchestrator rules") {
				t.Fatalf("caller system prompt was dropped from the counted body: %s", upstreamBody)
			}
			if testCase.wantSystemMsg {
				if !sawSystemRole {
					t.Fatalf("expected a mid-conversation system message, got %s", upstreamBody)
				}
			} else if !strings.Contains(joinedTexts, "<system-reminder>") {
				t.Fatalf("expected a legacy system reminder, got %s", upstreamBody)
			}
			// Sensitive words must not reach Anthropic verbatim on this endpoint either.
			if strings.Contains(joinedTexts, sensitiveWord) {
				t.Fatalf("sensitive word %q leaked to count_tokens: %s", sensitiveWord, upstreamBody)
			}
		})
	}
}

// TestClaudeExecutor_CountTokensCloakStrictModeDropsCallerSystem mirrors the
// Messages path: strict mode keeps only Claude Code identity, so a caller's
// system prompt must not be reintroduced into the counted body.
func TestClaudeExecutor_CountTokensCloakStrictModeDropsCallerSystem(t *testing.T) {
	var upstreamBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":34}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":           "sk-ant-oat-count-strict",
		"cloak_strict_mode": "true",
	}}
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"caller only secret directive"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"tools":[]}`)

	_, errCount := NewClaudeExecutor(&config.Config{}).countTokensUpstream(ctx, auth,
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount != nil {
		t.Fatalf("countTokensUpstream() error = %v", errCount)
	}
	if got := gjson.GetBytes(upstreamBody, "system"); got.Exists() {
		t.Fatalf("strict cloaked count system = %s, want absent", got.Raw)
	}
	if strings.Contains(string(upstreamBody), "secret directive") {
		t.Fatalf("strict mode must not forward the caller system prompt: %s", upstreamBody)
	}
}

func TestClaudeExecutor_CountTokensConfirmedNativePreservesMeasuredOAuthBody(t *testing.T) {
	var upstreamBody []byte
	var upstreamHeaders http.Header
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		upstreamHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":34}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-oat-native-count-shape"}}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"tools":[]}`)
	incomingBetas := "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,token-counting-2024-11-01"
	wantBetas := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,token-counting-2024-11-01"
	_, errCount := executor.countTokensUpstream(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"User-Agent":     {"claude-cli/2.1.220 (external, cli)"},
			"X-App":          {"cli"},
			"Anthropic-Beta": {incomingBetas},
		},
	})
	if errCount != nil {
		t.Fatalf("countTokensUpstream() error = %v", errCount)
	}
	if !bytes.Equal(upstreamBody, payload) {
		t.Fatalf("confirmed native count body changed\n got: %s\nwant: %s", upstreamBody, payload)
	}
	for _, field := range []string{"system", "metadata", "context_management", "betas"} {
		if got := gjson.GetBytes(upstreamBody, field); got.Exists() {
			t.Fatalf("confirmed native count body %s = %s, want absent", field, got.Raw)
		}
	}
	if got := strings.Join(upstreamHeaders["anthropic-beta"], ","); got != wantBetas {
		t.Fatalf("confirmed native count beta = %q, want %q", got, wantBetas)
	}
	if got := upstreamHeaders.Get("X-Stainless-Timeout"); got != "" {
		t.Fatalf("confirmed native count timeout = %q, want absent", got)
	}
}

func TestClaudeExecutor_CountTokensCountsLocallyWithoutUpstreamRequest(t *testing.T) {
	payload := []byte(`{
		"system":"client system instructions",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)
	const expectedCount int64 = 7

	testCases := []struct {
		name   string
		apiKey string
	}{
		{name: "custom API key", apiKey: "key-123"},
		{name: "custom OAuth", apiKey: "sk-ant-oat-custom"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected upstream count_tokens request: %s", r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  testCase.apiKey,
				"base_url": server.URL,
			}}
			resp, errCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-5",
				Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
			if errCount != nil {
				t.Fatalf("CountTokens() error = %v", errCount)
			}
			if got := gjson.GetBytes(resp.Payload, "input_tokens").Int(); got != expectedCount {
				t.Fatalf("input_tokens = %d, want %d; payload = %s", got, expectedCount, resp.Payload)
			}
		})
	}

	executor := NewClaudeExecutor(&config.Config{})
	resp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatGemini,
	})
	if err != nil {
		t.Fatalf("CountTokens() Gemini response error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "totalTokens").Int(); got != expectedCount {
		t.Fatalf("Gemini totalTokens = %d, want %d; payload = %s", got, expectedCount, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "promptTokensDetails.0.tokenCount").Int(); got != expectedCount {
		t.Fatalf("Gemini prompt token detail = %d, want %d; payload = %s", got, expectedCount, resp.Payload)
	}
}

func TestClaudeExecutor_CountTokensRejectsInvalidRequests(t *testing.T) {
	testCases := []struct {
		name    string
		payload string
	}{
		{name: "invalid JSON", payload: `not-json`},
		{name: "non-object", payload: `[]`},
		{name: "missing messages", payload: `{}`},
		{name: "empty messages", payload: `{"messages":[]}`},
		{name: "non-array messages", payload: `{"messages":"invalid"}`},
		{name: "invalid role", payload: `{"messages":[{"role":"system","content":"hello"}]}`},
		{name: "invalid content", payload: `{"messages":[{"role":"user","content":42}]}`},
		{name: "non-object content block", payload: `{"messages":[{"role":"user","content":[42]}]}`},
		{name: "untyped content block", payload: `{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`},
	}

	executor := NewClaudeExecutor(&config.Config{})
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-5",
				Payload: []byte(testCase.payload),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			assertStatusErr(t, err, http.StatusBadRequest)
			requestErr, ok := err.(cliproxyexecutor.RequestScopedError)
			if !ok || !requestErr.IsRequestScoped() {
				t.Fatalf("error %T is not request-scoped", err)
			}
		})
	}
}

func TestClaudeExecutor_CountTokensRebuildsMidSystemMessagesBeforeValidation(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"rebuild_mid_system_message": "true",
	}}
	payload := []byte(`{
		"system":"Top rule",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"system","content":"Mid rule"},
			{"role":"assistant","content":"answer"}
		]
	}`)

	resp, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "input_tokens").Int(); got <= 0 {
		t.Fatalf("input_tokens = %d, want positive count; payload = %s", got, resp.Payload)
	}
}

func TestClaudeExecutor_ReusesUserIDAcrossModelsWhenCacheEnabled(t *testing.T) {
	var userIDs []string
	var requestModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userID := gjson.GetBytes(body, "metadata.user_id").String()
		model := gjson.GetBytes(body, "model").String()
		userIDs = append(userIDs, userID)
		requestModels = append(requestModels, model)
		t.Logf("HTTP Server received request: model=%s, user_id=%s, url=%s", model, userID, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	t.Logf("End-to-end test: Fake HTTP server started at %s", server.URL)

	cacheEnabled := true
	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey:  "key-123",
				BaseURL: server.URL,
				Cloak: &config.CloakConfig{
					CacheUserID: &cacheEnabled,
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	models := []string{"claude-3-5-sonnet", "claude-3-5-haiku"}
	for _, model := range models {
		t.Logf("Sending request for model: %s", model)
		modelPayload, _ := sjson.SetBytes(payload, "model", model)
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: modelPayload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		}); err != nil {
			t.Fatalf("Execute(%s) error: %v", model, err)
		}
	}

	if len(userIDs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(userIDs))
	}
	if userIDs[0] == "" || userIDs[1] == "" {
		t.Fatal("expected user_id to be populated")
	}
	t.Logf("user_id[0] (model=%s): %s", requestModels[0], userIDs[0])
	t.Logf("user_id[1] (model=%s): %s", requestModels[1], userIDs[1])
	if userIDs[0] != userIDs[1] {
		t.Fatalf("expected user_id to be reused across models, got %q and %q", userIDs[0], userIDs[1])
	}
	if !helps.IsValidUserID(userIDs[0]) {
		t.Fatalf("user_id %q is not valid", userIDs[0])
	}
	t.Logf("✓ End-to-end test passed: Same user_id (%s) was used for both models", userIDs[0])
}

func TestClaudeExecutor_GeneratesNewUserIDByDefault(t *testing.T) {
	var userIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userIDs = append(userIDs, gjson.GetBytes(body, "metadata.user_id").String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	for i := 0; i < 2; i++ {
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		}); err != nil {
			t.Fatalf("Execute call %d error: %v", i, err)
		}
	}

	if len(userIDs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(userIDs))
	}
	if userIDs[0] == "" || userIDs[1] == "" {
		t.Fatal("expected user_id to be populated")
	}
	if userIDs[0] == userIDs[1] {
		t.Fatalf("expected user_id to change when caching is not enabled, got identical values %q", userIDs[0])
	}
	if !helps.IsValidUserID(userIDs[0]) || !helps.IsValidUserID(userIDs[1]) {
		t.Fatalf("user_ids should be valid, got %q and %q", userIDs[0], userIDs[1])
	}
}

func TestClaudeExecutor_ExecuteOpenAINonStreamRejectsEmptyClaudeStream(t *testing.T) {
	_, err := executeOpenAIChatCompletionThroughClaude(t, "")
	if err == nil {
		t.Fatal("Execute error = nil, want empty stream error")
	}
	assertStatusErr(t, err, http.StatusBadGateway)
	if !strings.Contains(err.Error(), "empty stream response") {
		t.Fatalf("Execute error = %q, want empty stream response", err.Error())
	}
}

func TestClaudeExecutor_ExecuteOpenAINonStreamRejectsClaudeErrorEvent(t *testing.T) {
	body := `data: {"type":"error","error":{"type":"overloaded_error","message":"upstream overloaded"}}` + "\n"
	_, err := executeOpenAIChatCompletionThroughClaude(t, body)
	if err == nil {
		t.Fatal("Execute error = nil, want upstream error event")
	}
	assertStatusErr(t, err, http.StatusBadGateway)
	if !strings.Contains(err.Error(), "upstream overloaded") {
		t.Fatalf("Execute error = %q, want upstream overloaded", err.Error())
	}
}

func TestClaudeExecutor_ExecuteOpenAINonStreamRejectsIncompleteClaudeStream(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022"}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	_, err := executeOpenAIChatCompletionThroughClaude(t, body)
	if err == nil {
		t.Fatal("Execute error = nil, want incomplete stream error")
	}
	assertStatusErr(t, err, http.StatusBadGateway)
	if !strings.Contains(err.Error(), "ended before message completion") {
		t.Fatalf("Execute error = %q, want incomplete stream error", err.Error())
	}
}

func TestClaudeExecutor_ExecuteOpenAINonStreamConvertsValidClaudeStream(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022"}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"output_tokens":1}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	resp, err := executeOpenAIChatCompletionThroughClaude(t, body)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "id").String(); got != "msg_123" {
		t.Fatalf("response id = %q, want msg_123; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "claude-3-5-sonnet-20241022" {
		t.Fatalf("response model = %q, want claude-3-5-sonnet-20241022", got)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok", got)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("usage.total_tokens = %d, want 3", got)
	}
}

func TestClaudeExecutor_ExecuteTransportMatchesResponseFormat(t *testing.T) {
	const model = "claude-3-5-sonnet-20241022"
	streamResponse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022"}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"output_tokens":1}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	jsonResponse := `{"id":"msg_123","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}`

	tests := []struct {
		name           string
		sourceFormat   sdktranslator.Format
		responseFormat sdktranslator.Format
		wantStream     bool
	}{
		{name: "OpenAI to OpenAI uses SSE", sourceFormat: sdktranslator.FormatOpenAI, responseFormat: sdktranslator.FormatOpenAI, wantStream: true},
		{name: "OpenAI to Claude uses JSON", sourceFormat: sdktranslator.FormatOpenAI, responseFormat: sdktranslator.FormatClaude, wantStream: false},
		{name: "Claude to OpenAI uses SSE", sourceFormat: sdktranslator.FormatClaude, responseFormat: sdktranslator.FormatOpenAI, wantStream: true},
		{name: "Claude to Claude uses JSON", sourceFormat: sdktranslator.FormatClaude, responseFormat: sdktranslator.FormatClaude, wantStream: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seenBody []byte
			var seenHeaders http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenBody, _ = io.ReadAll(r.Body)
				seenHeaders = r.Header.Clone()
				if tt.wantStream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(streamResponse))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(jsonResponse))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{
						Models: []config.PayloadModelRule{{Name: model, Protocol: "claude"}},
						Params: map[string]any{"stream": !tt.wantStream},
					}},
				},
			})
			attributes := map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}
			if tt.wantStream {
				attributes["header:Accept"] = "application/json"
				attributes["header:Accept-Encoding"] = "gzip, deflate, br, zstd"
			}
			auth := &cliproxyauth.Auth{Attributes: attributes}
			payload := []byte(`{"model":"claude-3-5-sonnet-20241022","stream":false,"messages":[{"role":"user","content":"hi"}]}`)

			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat:   tt.sourceFormat,
				ResponseFormat: tt.responseFormat,
				Headers: http.Header{
					"Anthropic-Beta": []string{"client-beta"},
				},
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			stream := gjson.GetBytes(seenBody, "stream")
			if !stream.Exists() || stream.Bool() != tt.wantStream {
				t.Fatalf("upstream stream = %s, want %t; body=%s", stream.Raw, tt.wantStream, string(seenBody))
			}
			wantAccept := "application/json"
			wantEncoding := "gzip, deflate, br, zstd"
			if tt.wantStream {
				wantAccept = "text/event-stream"
				wantEncoding = "identity"
			}
			if got := seenHeaders.Get("Accept"); got != wantAccept {
				t.Fatalf("Accept = %q, want %q", got, wantAccept)
			}
			if got := seenHeaders.Get("Accept-Encoding"); got != wantEncoding {
				t.Fatalf("Accept-Encoding = %q, want %q", got, wantEncoding)
			}
			if got := seenHeaders.Get("Anthropic-Beta"); !strings.Contains(got, "client-beta") {
				t.Fatalf("Anthropic-Beta = %q, want client beta preserved", got)
			}
		})
	}
}

func executeOpenAIChatCompletionThroughClaude(t *testing.T, upstreamBody string) (cliproxyexecutor.Response, error) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`)

	return executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
}

func assertStatusErr(t *testing.T, err error, want int) {
	t.Helper()

	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode", err)
	}
	if got := status.StatusCode(); got != want {
		t.Fatalf("StatusCode() = %d, want %d", got, want)
	}
}

func TestStripClaudeToolPrefixFromResponse_NestedToolReference(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"proxy_mcp__nia__manage_resource"}]}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")
	got := gjson.GetBytes(out, "content.0.content.0.tool_name").String()
	if got != "mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want %q", got, "mcp__nia__manage_resource")
	}
}

func TestApplyClaudeToolPrefix_NestedToolReferenceWithStringContent(t *testing.T) {
	// tool_result.content can be a string - should not be processed
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"plain string result"}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content").String()
	if got != "plain string result" {
		t.Fatalf("string content should remain unchanged = %q", got)
	}
}

func TestApplyClaudeToolPrefix_SkipsBuiltinToolReference(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"web_search"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "web_search" {
		t.Fatalf("built-in tool_reference should not be prefixed, got %q", got)
	}
}

func TestNormalizeCacheControlTTL_DowngradesLaterOneHourBlocks(t *testing.T) {
	payload := []byte(`{
		"tools": [{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],
		"messages": [{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]
	}`)

	out := normalizeCacheControlTTL(payload)

	if got := gjson.GetBytes(out, "tools.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("tools.0.cache_control.ttl = %q, want %q", got, "1h")
	}
	if gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("messages.0.content.0.cache_control.ttl should be removed after a default-5m block")
	}
}

func TestNormalizeCacheControlTTL_PreservesOriginalBytesWhenNoChange(t *testing.T) {
	// Payload where no TTL normalization is needed (all blocks use 1h with no
	// preceding 5m block). The text intentionally contains HTML chars (<, >, &)
	// that json.Marshal would escape to \u003c etc., altering byte identity.
	payload := []byte(`{"tools":[{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","text":"<system-reminder>foo & bar</system-reminder>","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	out := normalizeCacheControlTTL(payload)

	if !bytes.Equal(out, payload) {
		t.Fatalf("normalizeCacheControlTTL altered bytes when no change was needed.\noriginal: %s\ngot:      %s", payload, out)
	}
}

func TestNormalizeCacheControlTTL_PreservesKeyOrderWhenModified(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"name":"t1","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}]}`)

	out := normalizeCacheControlTTL(payload)

	if gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("messages.0.content.0.cache_control.ttl should be removed after a default-5m block")
	}

	outStr := string(out)
	idxModel := strings.Index(outStr, `"model"`)
	idxMessages := strings.Index(outStr, `"messages"`)
	idxTools := strings.Index(outStr, `"tools"`)
	idxSystem := strings.Index(outStr, `"system"`)
	if idxModel == -1 || idxMessages == -1 || idxTools == -1 || idxSystem == -1 {
		t.Fatalf("failed to locate top-level keys in output: %s", outStr)
	}
	if !(idxModel < idxMessages && idxMessages < idxTools && idxTools < idxSystem) {
		t.Fatalf("top-level key order changed:\noriginal: %s\ngot:      %s", payload, out)
	}
}

func TestEnforceCacheControlLimit_StripsNonLastToolBeforeMessages(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}}
		],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral"}}]}
		]
	}`)

	out := enforceCacheControlLimit(payload, 4)

	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want 4", got)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be removed first (non-last tool)")
	}
	if !gjson.GetBytes(out, "tools.1.cache_control").Exists() {
		t.Fatalf("tools.1.cache_control (last tool) should be preserved")
	}
	if !gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists() || !gjson.GetBytes(out, "messages.1.content.0.cache_control").Exists() {
		t.Fatalf("message cache_control blocks should be preserved when non-last tool removal is enough")
	}
}

func TestEnforceCacheControlLimit_PreservesKeyOrderWhenModified(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"u2","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"t1","cache_control":{"type":"ephemeral"}},{"name":"t2","cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}]}`)

	out := enforceCacheControlLimit(payload, 4)

	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want 4", got)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be removed first (non-last tool)")
	}

	outStr := string(out)
	idxModel := strings.Index(outStr, `"model"`)
	idxMessages := strings.Index(outStr, `"messages"`)
	idxTools := strings.Index(outStr, `"tools"`)
	idxSystem := strings.Index(outStr, `"system"`)
	if idxModel == -1 || idxMessages == -1 || idxTools == -1 || idxSystem == -1 {
		t.Fatalf("failed to locate top-level keys in output: %s", outStr)
	}
	if !(idxModel < idxMessages && idxMessages < idxTools && idxTools < idxSystem) {
		t.Fatalf("top-level key order changed:\noriginal: %s\ngot:      %s", payload, out)
	}
}

func TestEnforceCacheControlLimit_ToolOnlyPayloadStillRespectsLimit(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}},
			{"name":"t3","cache_control":{"type":"ephemeral"}},
			{"name":"t4","cache_control":{"type":"ephemeral"}},
			{"name":"t5","cache_control":{"type":"ephemeral"}}
		]
	}`)

	out := enforceCacheControlLimit(payload, 4)

	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want 4", got)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be removed to satisfy max=4")
	}
	if !gjson.GetBytes(out, "tools.4.cache_control").Exists() {
		t.Fatalf("last tool cache_control should be preserved when possible")
	}
}

func TestClaudeExecutor_ExecuteSanitizesSignaturesBeforeUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-4-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 16,
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"drop this","signature":""},
				{"type":"text","text":"I will run git status."},
				{"type":"tool_use","id":"Bash-1","name":"Bash","input":{"command":"git status"},"signature":"bad","thoughtSignature":"bad2","model":"claude-opus-4-1"}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"Bash-1","content":"ok"}]}
		]
	}`)

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	parts := gjson.GetBytes(seenBody, "messages.0.content").Array()
	if len(parts) != 2 {
		t.Fatalf("messages.0.content length = %d, want 2; body=%s", len(parts), seenBody)
	}
	if parts[0].Get("type").String() != "text" {
		t.Fatalf("first remaining part = %s, want text", parts[0].Raw)
	}
	toolUse := parts[1]
	if toolUse.Get("type").String() != "tool_use" {
		t.Fatalf("second remaining part = %s, want tool_use", toolUse.Raw)
	}
	for _, path := range []string{"signature", "thoughtSignature", "model"} {
		if toolUse.Get(path).Exists() {
			t.Fatalf("tool_use.%s should be removed before upstream: %s", path, seenBody)
		}
	}
}

func TestClaudeExecutor_Execute_InvalidGzipErrorBodyReturnsDecodeMessage(t *testing.T) {
	testClaudeExecutorInvalidCompressedErrorBody(t, func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error {
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		return err
	})
}

func TestClaudeExecutor_ExecuteStream_InvalidGzipErrorBodyReturnsDecodeMessage(t *testing.T) {
	testClaudeExecutorInvalidCompressedErrorBody(t, func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error {
		_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		return err
	})
}

func testClaudeExecutorInvalidCompressedErrorBody(
	t *testing.T,
	invoke func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error,
) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("not-a-valid-gzip-stream"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	err := invoke(executor, auth, payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decode error response body") {
		t.Fatalf("expected decode failure message, got: %v", err)
	}
	if statusProvider, ok := err.(interface{ StatusCode() int }); !ok || statusProvider.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected status code 400, got: %v", err)
	}
}

func TestEnsureModelMaxTokens_UsesRegisteredMaxCompletionTokens(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-claude-max-completion-tokens-client"
	modelID := "test-claude-max-completion-tokens-model"
	reg.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID:                  modelID,
		Type:                "claude",
		OwnedBy:             "anthropic",
		Object:              "model",
		Created:             time.Now().Unix(),
		MaxCompletionTokens: 4096,
		UserDefined:         true,
	}})
	defer reg.UnregisterClient(clientID)

	input := []byte(`{"model":"test-claude-max-completion-tokens-model","messages":[{"role":"user","content":"hi"}]}`)
	out := ensureModelMaxTokens(input, modelID)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 4096 {
		t.Fatalf("max_tokens = %d, want %d", got, 4096)
	}
}

func TestEnsureModelMaxTokens_DefaultsMissingValue(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-claude-default-max-tokens-client"
	modelID := "test-claude-default-max-tokens-model"
	reg.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID:          modelID,
		Type:        "claude",
		OwnedBy:     "anthropic",
		Object:      "model",
		Created:     time.Now().Unix(),
		UserDefined: true,
	}})
	defer reg.UnregisterClient(clientID)

	input := []byte(`{"model":"test-claude-default-max-tokens-model","messages":[{"role":"user","content":"hi"}]}`)
	out := ensureModelMaxTokens(input, modelID)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != defaultModelMaxTokens {
		t.Fatalf("max_tokens = %d, want %d", got, defaultModelMaxTokens)
	}
}

func TestEnsureModelMaxTokens_PreservesExplicitValue(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-claude-preserve-max-tokens-client"
	modelID := "test-claude-preserve-max-tokens-model"
	reg.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID:                  modelID,
		Type:                "claude",
		OwnedBy:             "anthropic",
		Object:              "model",
		Created:             time.Now().Unix(),
		MaxCompletionTokens: 4096,
		UserDefined:         true,
	}})
	defer reg.UnregisterClient(clientID)

	input := []byte(`{"model":"test-claude-preserve-max-tokens-model","max_tokens":2048,"messages":[{"role":"user","content":"hi"}]}`)
	out := ensureModelMaxTokens(input, modelID)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 2048 {
		t.Fatalf("max_tokens = %d, want %d", got, 2048)
	}
}

func TestEnsureModelMaxTokens_SkipsUnregisteredModel(t *testing.T) {
	input := []byte(`{"model":"test-claude-unregistered-model","messages":[{"role":"user","content":"hi"}]}`)
	out := ensureModelMaxTokens(input, "test-claude-unregistered-model")

	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Fatalf("max_tokens should remain unset, got %s", gjson.GetBytes(out, "max_tokens").Raw)
	}
}

// TestClaudeExecutor_ExecuteStream_SetsIdentityAcceptEncoding verifies that streaming
// requests use Accept-Encoding: identity so the upstream cannot respond with a
// compressed SSE body that would silently break the line scanner.
func TestClaudeExecutor_ExecuteStream_SetsIdentityAcceptEncoding(t *testing.T) {
	var gotEncoding, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	if gotEncoding != "identity" {
		t.Errorf("Accept-Encoding = %q, want %q", gotEncoding, "identity")
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want %q", gotAccept, "text/event-stream")
	}
}

// TestClaudeExecutor_Execute_SetsCompressedAcceptEncoding verifies that non-streaming
// requests keep the full accept-encoding to allow response compression (which
// decodeResponseBody handles correctly).
func TestClaudeExecutor_Execute_SetsCompressedAcceptEncoding(t *testing.T) {
	var gotEncoding, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotEncoding != "gzip, deflate, br, zstd" {
		t.Errorf("Accept-Encoding = %q, want %q", gotEncoding, "gzip, deflate, br, zstd")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want %q", gotAccept, "application/json")
	}
}

// TestClaudeExecutor_ExecuteStream_GzipSuccessBodyDecoded verifies that a streaming
// HTTP 200 response with Content-Encoding: gzip is correctly decompressed before
// the line scanner runs, so SSE chunks are not silently dropped.
func TestClaudeExecutor_ExecuteStream_GzipSuccessBodyDecoded(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("data: {\"type\":\"message_stop\"}\n"))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}

	if combined.Len() == 0 {
		t.Fatal("expected at least one chunk from gzip-encoded SSE body, got none (body was not decompressed)")
	}
	if !strings.Contains(combined.String(), "message_stop") {
		t.Errorf("expected SSE content in chunks, got: %q", combined.String())
	}
}

func TestDecodeResponseBodyStackedRepeatedHeaders(t *testing.T) {
	payload := []byte("stacked Claude response")
	var gzipOutput bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzipOutput)
	if _, errWrite := gzipWriter.Write(payload); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := gzipWriter.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	var brotliOutput bytes.Buffer
	brotliWriter := brotli.NewWriter(&brotliOutput)
	if _, errWrite := brotliWriter.Write(gzipOutput.Bytes()); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := brotliWriter.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	header := make(http.Header)
	header.Add("Content-Encoding", "gzip")
	header.Add("Content-Encoding", "br")
	decoded, errDecode := decodeResponseBody(io.NopCloser(bytes.NewReader(brotliOutput.Bytes())), claudeResponseContentEncoding(header))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	defer decoded.Close()
	got, errRead := io.ReadAll(decoded)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decoded body = %q, want %q", got, payload)
	}
}

// TestDecodeResponseBody_MagicByteGzipNoHeader verifies that decodeResponseBody
// detects gzip-compressed content via magic bytes even when Content-Encoding is absent.
func TestDecodeResponseBody_MagicByteGzipNoHeader(t *testing.T) {
	const plaintext = "data: {\"type\":\"message_stop\"}\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(plaintext))
	_ = gz.Close()

	rc := io.NopCloser(&buf)
	decoded, err := decodeResponseBody(rc, "")
	if err != nil {
		t.Fatalf("decodeResponseBody error: %v", err)
	}
	defer decoded.Close()

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("decoded = %q, want %q", got, plaintext)
	}
}

// TestDecodeResponseBody_MagicByteZstdNoHeader verifies that decodeResponseBody
// detects zstd-compressed content via magic bytes even when Content-Encoding is absent.
func TestDecodeResponseBody_MagicByteZstdNoHeader(t *testing.T) {
	const plaintext = "data: {\"type\":\"message_stop\"}\n"

	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	_, _ = enc.Write([]byte(plaintext))
	_ = enc.Close()

	rc := io.NopCloser(&buf)
	decoded, err := decodeResponseBody(rc, "")
	if err != nil {
		t.Fatalf("decodeResponseBody error: %v", err)
	}
	defer decoded.Close()

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("decoded = %q, want %q", got, plaintext)
	}
}

// TestDecodeResponseBody_PlainTextNoHeader verifies that decodeResponseBody returns
// plain text untouched when Content-Encoding is absent and no magic bytes match.
func TestDecodeResponseBody_PlainTextNoHeader(t *testing.T) {
	const plaintext = "data: {\"type\":\"message_stop\"}\n"
	rc := io.NopCloser(strings.NewReader(plaintext))
	decoded, err := decodeResponseBody(rc, "")
	if err != nil {
		t.Fatalf("decodeResponseBody error: %v", err)
	}
	defer decoded.Close()

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("decoded = %q, want %q", got, plaintext)
	}
}

// TestClaudeExecutor_ExecuteStream_GzipNoContentEncodingHeader verifies the full
// pipeline: when the upstream returns a gzip-compressed SSE body WITHOUT setting
// Content-Encoding (a misbehaving upstream), the magic-byte sniff in
// decodeResponseBody still decompresses it, so chunks reach the caller.
func TestClaudeExecutor_ExecuteStream_GzipNoContentEncodingHeader(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("data: {\"type\":\"message_stop\"}\n"))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Intentionally omit Content-Encoding to simulate misbehaving upstream.
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}

	if combined.Len() == 0 {
		t.Fatal("expected chunks from gzip body without Content-Encoding header, got none (magic-byte sniff failed)")
	}
	if !strings.Contains(combined.String(), "message_stop") {
		t.Errorf("unexpected chunk content: %q", combined.String())
	}
}

// TestClaudeExecutor_Execute_GzipErrorBodyNoContentEncodingHeader verifies that the
// error path (4xx) correctly decompresses a gzip body even when the upstream omits
// the Content-Encoding header.  This closes the gap left by PR #1771, which only
// fixed header-declared compression on the error path.
func TestClaudeExecutor_Execute_GzipErrorBodyNoContentEncodingHeader(t *testing.T) {
	const errJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"test error"}}`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(errJSON))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Intentionally omit Content-Encoding to simulate misbehaving upstream.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err == nil {
		t.Fatal("expected an error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "test error") {
		t.Errorf("error message should contain decompressed JSON, got: %q", err.Error())
	}
}

// TestClaudeExecutor_ExecuteStream_GzipErrorBodyNoContentEncodingHeader verifies
// the same for the streaming executor: 4xx gzip body without Content-Encoding is
// decoded and the error message is readable.
func TestClaudeExecutor_ExecuteStream_GzipErrorBodyNoContentEncodingHeader(t *testing.T) {
	const errJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"stream test error"}}`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(errJSON))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Intentionally omit Content-Encoding to simulate misbehaving upstream.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err == nil {
		t.Fatal("expected an error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "stream test error") {
		t.Errorf("error message should contain decompressed JSON, got: %q", err.Error())
	}
}

// TestClaudeExecutor_ExecuteStream_AcceptEncodingOverrideCannotBypassIdentity verifies that the
// streaming executor enforces Accept-Encoding: identity regardless of auth.Attributes override.
func TestClaudeExecutor_ExecuteStream_AcceptEncodingOverrideCannotBypassIdentity(t *testing.T) {
	var gotEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                "key-123",
		"base_url":               server.URL,
		"header:Accept-Encoding": "gzip, deflate, br, zstd",
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	if gotEncoding != "identity" {
		t.Errorf("Accept-Encoding = %q; stream path must enforce identity regardless of auth.Attributes override", gotEncoding)
	}
}

// assertClaudeMidConversationSystemMessage checks a forwarded caller system prompt.
// wantTTL is "" for the native default marker and "1h" once
// upgradeClaudeCacheControlTTL has run, which only happens for OAuth credentials.
func assertClaudeMidConversationSystemMessage(t *testing.T, body []byte, messageIndex int, wantText, wantTTL string) {
	t.Helper()
	messagePath := fmt.Sprintf("messages.%d", messageIndex)
	if got := gjson.GetBytes(body, messagePath+".role").String(); got != "system" {
		t.Fatalf("%s.role = %q, want system", messagePath, got)
	}
	content := gjson.GetBytes(body, messagePath+".content").Array()
	if len(content) != 1 {
		t.Fatalf("%s.content has %d blocks, want 1", messagePath, len(content))
	}
	if got := content[0].Get("text").String(); got != wantText {
		t.Fatalf("%s.content.0.text lost caller prompt: got len %d, want len %d", messagePath, len(got), len(wantText))
	}
	if got := content[0].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("%s.content.0.cache_control.type = %q, want ephemeral", messagePath, got)
	}
	if got := content[0].Get("cache_control.ttl").String(); got != wantTTL {
		t.Fatalf("%s.content.0.cache_control.ttl = %q, want %q: %s", messagePath, got, wantTTL, content[0].Raw)
	}
}

func assertClaudeLegacySystemReminderLayout(t *testing.T, body []byte, wantSystem, wantUser, wantTTL string) {
	t.Helper()
	if got := gjson.GetBytes(body, "system.#").Int(); got != 2 {
		t.Fatalf("top-level system block count = %d, want billing and identity only", got)
	}
	if got := gjson.GetBytes(body, "messages.#").Int(); got != 1 {
		t.Fatalf("message count = %d, want one user turn and no role=system", got)
	}
	content := gjson.GetBytes(body, "messages.0.content").Array()
	if len(content) != 3 {
		t.Fatalf("user content has %d blocks, want currentDate, caller reminder, and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	if got := content[1].Get("text").String(); got != claudeCallerSystemReminder(wantSystem) {
		t.Fatalf("caller reminder lost system prompt: got len %d, want len %d", len(got), len(wantSystem))
	}
	if content[1].Get("cache_control").Exists() {
		t.Fatalf("caller reminder unexpectedly has cache_control: %s", content[1].Raw)
	}
	assertEphemeralUserTextBlock(t, content[2], wantUser, wantTTL)
}

func assertClaudeCodeCurrentDateBlock(t *testing.T, block gjson.Result) {
	t.Helper()
	assertClaudeCodeCurrentDateBlockAt(t, block, time.Now())
}

func assertClaudeCodeCurrentDateBlockAt(t *testing.T, block gjson.Result, now time.Time) {
	t.Helper()
	if got := block.Get("type").String(); got != "text" {
		t.Fatalf("currentDate block type = %q, want text", got)
	}
	if got, want := block.Get("text").String(), claudeCodeCurrentDateReminder(now); got != want {
		t.Fatalf("currentDate reminder = %q, want %q", got, want)
	}
	if block.Get("cache_control").Exists() {
		t.Fatalf("currentDate block must not contain cache_control: %s", block.Raw)
	}
}

// assertEphemeralUserTextBlock checks the cloaked first-user block. wantTTL is ""
// for the native default marker and "1h" once upgradeClaudeCacheControlTTL has run,
// which only happens for OAuth credentials.
func assertEphemeralUserTextBlock(t *testing.T, block gjson.Result, wantText, wantTTL string) {
	t.Helper()
	if got := block.Get("type").String(); got != "text" {
		t.Fatalf("user block type = %q, want text", got)
	}
	if got := block.Get("text").String(); got != wantText {
		t.Fatalf("user block text = %q, want %q", got, wantText)
	}
	if got := block.Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("user block cache_control.type = %q, want ephemeral", got)
	}
	if got := block.Get("cache_control.ttl").String(); got != wantTTL {
		t.Fatalf("user block cache_control.ttl = %q, want %q: %s", got, wantTTL, block.Raw)
	}
}

func TestClaudeBillingFingerprintUsesLatestUserText(t *testing.T) {
	const prompt = "CPA_OFFICIAL_BASEURL_CLI_SYSTEM_EMPTY_b82d4e"
	payload := []byte(`{"system":"must not seed the build hash","messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"<system-reminder>date</system-reminder>"},{"type":"text","text":"` + prompt + `"}]}]}`)
	if got := claudeBillingFingerprintMessageText(payload); got != prompt {
		t.Fatalf("claudeBillingFingerprintMessageText() = %q, want %q", got, prompt)
	}
	if got := computeFingerprint(prompt, "2.1.220"); got != "e06" {
		t.Fatalf("computeFingerprint() = %q, want official 2.1.220 capture suffix e06", got)
	}
}

func TestClaudeCodeLocalDateMatchesNativeLocalCalendarAlgorithm(t *testing.T) {
	instant := time.Date(2026, time.July, 31, 15, 30, 0, 0, time.UTC)
	kiritimati := time.FixedZone("Kiritimati", 14*60*60)
	minusTwelve := time.FixedZone("Etc/GMT+12", -12*60*60)

	if got := claudeCodeLocalDate(instant.In(kiritimati)); got != "2026-08-01" {
		t.Fatalf("Kiritimati local date = %q, want 2026-08-01", got)
	}
	if got := claudeCodeLocalDate(instant.In(minusTwelve)); got != "2026-07-31" {
		t.Fatalf("GMT-12 local date = %q, want 2026-07-31", got)
	}
	wantReminder := "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-08-01.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n\n"
	if got := claudeCodeCurrentDateReminder(instant.In(kiritimati)); got != wantReminder {
		t.Fatalf("currentDate reminder = %q, want exact native text %q", got, wantReminder)
	}
}

func TestClaudeCodeTimezoneUsesCredentialThenConfiguredProfile(t *testing.T) {
	instant := time.Date(2026, time.August, 2, 1, 30, 0, 0, time.UTC)
	cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{Timezone: "Asia/Tokyo"}}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"timezone": "Pacific/Honolulu"}}
	if got := claudeCodeLocalDate(instant.In(claudeCodeTimezone(cfg, auth))); got != "2026-08-01" {
		t.Fatalf("credential currentDate = %q, want 2026-08-01", got)
	}
	if got := claudeCodeLocalDate(instant.In(claudeCodeTimezone(cfg, nil))); got != "2026-08-02" {
		t.Fatalf("configured currentDate = %q, want 2026-08-02", got)
	}
	invalidAuth := &cliproxyauth.Auth{Metadata: map[string]any{"timezone": "not/a-timezone"}}
	if got := claudeCodeTimezone(cfg, invalidAuth).String(); got != "Asia/Tokyo" {
		t.Fatalf("invalid credential timezone = %q, want config fallback", got)
	}
	invalid := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{Timezone: "not/a-timezone"}}
	if got := claudeCodeTimezone(invalid, nil); got != time.Local {
		t.Fatalf("invalid timezone location = %v, want time.Local", got)
	}
}

func TestInjectClaudeCodeCurrentDateIsIdempotentAndAlignsFirstUserCache(t *testing.T) {
	fixed := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)

	first := injectClaudeCodeCurrentDate(payload, fixed)
	if !bytes.Contains(first, []byte(`<system-reminder>`)) || bytes.Contains(first, []byte(`\u003csystem-reminder`)) {
		t.Fatalf("currentDate angle brackets must match JSON.stringify bytes: %s", first)
	}
	second := injectClaudeCodeCurrentDate(first, fixed)
	if !bytes.Equal(first, second) {
		t.Fatalf("currentDate injection is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
	content := gjson.GetBytes(first, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("first user content has %d blocks, want 2: %s", len(content), first)
	}
	if got := content[0].Get("text").String(); got != claudeCodeCurrentDateReminder(fixed) {
		t.Fatalf("currentDate text = %q, want exact native reminder", got)
	}
	if content[0].Get("cache_control").Exists() {
		t.Fatalf("currentDate block must not contain cache_control: %s", content[0].Raw)
	}
	assertEphemeralUserTextBlock(t, content[1], "hello", "")
}

func TestInjectClaudeCodeCurrentDateMovesExistingCopyToFirstBlock(t *testing.T) {
	fixed := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	dateBlock := buildTextBlock(claudeCodeCurrentDateReminder(fixed), nil)
	payload := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"hello"},` + dateBlock + `]}]}`)

	out := injectClaudeCodeCurrentDate(payload, fixed)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content has %d blocks, want one currentDate and user text: %s", len(content), out)
	}
	assertClaudeCodeCurrentDateBlockAt(t, content[0], fixed)
	assertEphemeralUserTextBlock(t, content[1], "hello", "")
}

func TestInjectClaudeCodeCurrentDatePrecedesExistingReminder(t *testing.T) {
	fixed := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	reminder := "<system-reminder>\ncaller instructions\n</system-reminder>"
	payload := []byte(`{"messages":[{"role":"user","content":[` +
		buildTextBlock(reminder, nil) + `,` +
		`{"type":"text","text":"continue","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)

	out := injectClaudeCodeCurrentDate(payload, fixed)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 3 {
		t.Fatalf("content has %d blocks, want currentDate, reminder, and user text: %s", len(content), out)
	}
	assertClaudeCodeCurrentDateBlockAt(t, content[0], fixed)
	if got := content[1].Get("text").String(); got != reminder {
		t.Fatalf("content[1].text = %q, want standalone reminder", got)
	}
	assertEphemeralUserTextBlock(t, content[2], "continue", "")
}

func TestInjectClaudeCodeCurrentDateFollowsLeadingToolResults(t *testing.T) {
	fixed := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	payload := []byte(`{"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},` +
		`{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},` +
		`{"type":"text","text":"continue"}]}]}`)

	first := injectClaudeCodeCurrentDate(payload, fixed)
	second := injectClaudeCodeCurrentDate(first, fixed)
	if !bytes.Equal(first, second) {
		t.Fatalf("currentDate injection is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}

	content := gjson.GetBytes(first, "messages.1.content").Array()
	if len(content) != 3 {
		t.Fatalf("content has %d blocks, want tool_result, currentDate, and user text: %s", len(content), first)
	}
	if got := content[0].Get("type").String(); got != "tool_result" {
		t.Fatalf("content[0].type = %q, want tool_result to stay first: %s", got, first)
	}
	if got := content[0].Get("tool_use_id").String(); got != "toolu_1" {
		t.Fatalf("content[0].tool_use_id = %q, want toolu_1", got)
	}
	assertClaudeCodeCurrentDateBlockAt(t, content[1], fixed)
	assertEphemeralUserTextBlock(t, content[2], "continue", "")
}

func TestInjectClaudeCodeCurrentDateFollowsAllLeadingToolResults(t *testing.T) {
	fixed := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	payload := []byte(`{"messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_1","name":"Read","input":{}},` +
		`{"type":"tool_use","id":"toolu_2","name":"Read","input":{}}]},` +
		`{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},` +
		`{"type":"tool_result","tool_use_id":"toolu_2","content":"ok"}]}]}`)

	out := injectClaudeCodeCurrentDate(payload, fixed)
	content := gjson.GetBytes(out, "messages.1.content").Array()
	if len(content) != 3 {
		t.Fatalf("content has %d blocks, want two tool_results and currentDate: %s", len(content), out)
	}
	for idx, wantID := range []string{"toolu_1", "toolu_2"} {
		if got := content[idx].Get("type").String(); got != "tool_result" {
			t.Fatalf("content[%d].type = %q, want tool_result: %s", idx, got, out)
		}
		if got := content[idx].Get("tool_use_id").String(); got != wantID {
			t.Fatalf("content[%d].tool_use_id = %q, want %q", idx, got, wantID)
		}
	}
	assertClaudeCodeCurrentDateBlockAt(t, content[2], fixed)
}

// Test case 1: String system prompt becomes an authoritative mid-conversation
// system message after the first user turn.
func TestCheckSystemInstructionsWithMode_StringSystemPreserved(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5","system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	system := gjson.GetBytes(out, "system")
	if !system.IsArray() {
		t.Fatalf("system should be an array, got %s", system.Type)
	}
	blocks := system.Array()
	if len(blocks) != 2 {
		t.Fatalf("expected billing and identity blocks only, got %d", len(blocks))
	}
	if got := blocks[0].Get("text").String(); !strings.Contains(got, "cc_entrypoint=cli;") {
		t.Fatalf("blocks[0] should use CLI billing attribution, got %q", got)
	}
	if blocks[1].Get("text").String() != claudeCodeCLIIdentity {
		t.Fatalf("blocks[1] should be official CLI identity, got %q", blocks[1].Get("text").String())
	}
	if got := blocks[1].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("blocks[1] cache_control.type = %q, want ephemeral", got)
	}
	if blocks[1].Get("cache_control.ttl").Exists() {
		t.Fatalf("blocks[1] cache_control must not carry a default ttl: %s", blocks[1].Raw)
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("messages[0].content has %d blocks, want currentDate and user text: %s", len(content), out)
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
	assertClaudeMidConversationSystemMessage(t, out, 1, "You are a helpful assistant.", "")
}

func TestClaudeUsesLegacySystemReminder(t *testing.T) {
	tests := map[string]bool{
		"claude-opus-4-6":          true,
		"claude-opus-4-7":          true,
		"claude-sonnet-5":          false,
		"prefix/claude-sonnet-4-6": true,
		"claude-3-5-haiku-latest":  true,
		"claude-opus-5":            false,
		"prefix/claude-opus-4-8":   false,
		"claude-fable-5":           false,
		"claude-future-6":          false,
		"":                         false,
	}
	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			payload := []byte(`{"model":` + fmt.Sprintf("%q", model) + `}`)
			if got := claudeUsesLegacySystemReminder(payload); got != want {
				t.Fatalf("claudeUsesLegacySystemReminder(%q) = %v, want %v", model, got, want)
			}
		})
	}
}

func TestCheckSystemInstructionsWithMode_FutureModelDefaultsToMidSystem(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-6","system":"future instructions","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)
	if got := gjson.GetBytes(out, "system.#").Int(); got != 2 {
		t.Fatalf("top-level system block count = %d, want 2", got)
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("user content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
	assertClaudeMidConversationSystemMessage(t, out, 1, "future instructions", "")
}

func TestCheckSystemInstructionsWithMode_LegacyModelUsesSystemReminder(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-4-6","system":"legacy instructions","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)
	if got := gjson.GetBytes(out, "system.#").Int(); got != 2 {
		t.Fatalf("top-level system block count = %d, want billing and identity only", got)
	}
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 1 {
		t.Fatalf("message count = %d, want no role=system insertion", got)
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 3 {
		t.Fatalf("user content has %d blocks, want currentDate, caller reminder, and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	if got := content[1].Get("text").String(); got != claudeCallerSystemReminder("legacy instructions") {
		t.Fatalf("caller system reminder = %q", got)
	}
	if content[1].Get("cache_control").Exists() {
		t.Fatalf("caller system reminder unexpectedly has cache_control: %s", content[1].Raw)
	}
	assertEphemeralUserTextBlock(t, content[2], "hi", "")
}

func TestCheckSystemInstructionsWithMode_LegacyModelKeepsSystemBlocksSeparate(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-4-6","system":[` +
		`{"type":"text","text":"first guidance","cache_control":{"type":"ephemeral","ttl":"1h"}},` +
		`{"type":"text","text":"second guidance"}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 4 {
		t.Fatalf("user content has %d blocks, want currentDate, two caller reminders, and user text: %s", len(content), out)
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	for idx, want := range []string{"first guidance", "second guidance"} {
		block := content[idx+1]
		if got := block.Get("text").String(); got != claudeCallerSystemReminder(want) {
			t.Fatalf("content[%d].text = %q, want separate caller reminder %q", idx+1, got, want)
		}
		if block.Get("cache_control").Exists() {
			t.Fatalf("content[%d] caller reminder unexpectedly has cache_control: %s", idx+1, block.Raw)
		}
	}
	assertEphemeralUserTextBlock(t, content[3], "hi", "")
}

// Test case 2: Strict mode keeps only the injected Claude Code system blocks.
func TestCheckSystemInstructionsWithMode_StringSystemStrict(t *testing.T) {
	payload := []byte(`{"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, true)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("strict mode should produce 2 injected blocks, got %d", len(blocks))
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("strict mode content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
}

// Test case 3: Empty string system prompt adds only currentDate before user text.
func TestCheckSystemInstructionsWithMode_EmptyStringSystemIgnored(t *testing.T) {
	payload := []byte(`{"system":"","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("empty string system should still produce 2 injected blocks, got %d", len(blocks))
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("empty system content has %d blocks, want 2", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
}

// Test case 4: Array system prompt becomes one mid-conversation system message.
func TestCheckSystemInstructionsWithMode_ArraySystemStillWorks(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"Be concise."}],"messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 top-level system blocks, got %d", len(blocks))
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("messages[0].content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
	assertClaudeMidConversationSystemMessage(t, out, 1, "Be concise.", "")
}

func TestCheckSystemInstructionsWithMode_ArraySystemKeepsBlocksAsSeparateMessages(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5","system":[` +
		`{"type":"text","text":"first guidance","cache_control":{"type":"ephemeral","ttl":"1h"}},` +
		`{"type":"text","text":"second guidance"}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want user and two separate system messages: %s", got, out)
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("user content has %d blocks, want currentDate and user text: %s", len(content), out)
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
	assertClaudeMidConversationSystemMessage(t, out, 1, "first guidance", "")
	assertClaudeMidConversationSystemMessage(t, out, 2, "second guidance", "")
}

func TestRelocateClaudeSystemPromptForCountTokensKeepsBlocksSeparate(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		legacy bool
	}{
		{name: "mid-system model", model: "claude-opus-5"},
		{name: "legacy model", model: "claude-opus-4-6", legacy: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(`{"model":"` + test.model + `","system":[` +
				`{"type":"text","text":"first guidance"},` +
				`{"type":"text","text":"second guidance"}],` +
				`"messages":[{"role":"user","content":"hi"}]}`)

			out := relocateClaudeSystemPromptForCountTokens(payload, false)
			if gjson.GetBytes(out, "system").Exists() {
				t.Fatalf("count_tokens system must be absent: %s", out)
			}
			if test.legacy {
				content := gjson.GetBytes(out, "messages.0.content").Array()
				if len(content) != 3 {
					t.Fatalf("legacy content has %d blocks, want two reminders and user text: %s", len(content), out)
				}
				if got := content[0].Get("text").String(); got != claudeCallerSystemReminder("first guidance") {
					t.Fatalf("first caller reminder = %q", got)
				}
				if got := content[1].Get("text").String(); got != claudeCallerSystemReminder("second guidance") {
					t.Fatalf("second caller reminder = %q", got)
				}
				if got := content[2].Get("text").String(); got != "hi" {
					t.Fatalf("user text = %q, want hi", got)
				}
				return
			}
			if got := gjson.GetBytes(out, "messages.#").Int(); got != 3 {
				t.Fatalf("message count = %d, want user and two system messages: %s", got, out)
			}
			assertClaudeMidConversationSystemMessage(t, out, 1, "first guidance", "")
			assertClaudeMidConversationSystemMessage(t, out, 2, "second guidance", "")
		})
	}
}

// Test case 5: Special characters survive the mid-conversation system move.
func TestCheckSystemInstructionsWithMode_StringWithSpecialChars(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5","system":"Use <xml> tags & \"quotes\" in output.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	wantSystem := `Use <xml> tags & "quotes" in output.`
	if got := gjson.GetBytes(out, "system.#").Int(); got != 2 {
		t.Fatalf("top-level system block count = %d, want 2", got)
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("messages[0].content has %d blocks, want 2", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hi", "")
	assertClaudeMidConversationSystemMessage(t, out, 1, wantSystem, "")
}

func TestCheckSystemInstructionsWithSigningMode_LongPromptIsExactAndIdempotent(t *testing.T) {
	wantSystem := "\nPI_SYSTEM_BEGIN\nEmbedded reference: # currentDate\nToday's date is caller-owned text.\n" + strings.Repeat("Preserve tools, policies, and caller semantics exactly.\n", 560) + "PI_SYSTEM_END  \n"
	payloadMap := map[string]any{
		"model":  "claude-opus-5",
		"system": wantSystem,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "hello",
		}},
	}
	payload, errMarshal := json.Marshal(payloadMap)
	if errMarshal != nil {
		t.Fatalf("marshal payload: %v", errMarshal)
	}

	first := checkSystemInstructionsWithSigningMode(payload, false, true, "2.1.220", "cli", "")
	second := checkSystemInstructionsWithSigningMode(first, false, true, "2.1.220", "cli", "")
	if !bytes.Equal(first, second) {
		t.Fatalf("complete cloak layout is not byte-idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
	if got := gjson.GetBytes(first, "system.#").Int(); got != 2 {
		t.Fatalf("top-level system block count = %d, want 2", got)
	}
	if got := gjson.GetBytes(first, "messages.#").Int(); got != 2 {
		t.Fatalf("message count = %d, want user then system", got)
	}
	content := gjson.GetBytes(first, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("user content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "hello", "")
	assertClaudeMidConversationSystemMessage(t, first, 1, wantSystem, "")
	if strings.Contains(content[0].Get("text").String(), "PI_SYSTEM_BEGIN") || strings.Contains(content[1].Get("text").String(), "PI_SYSTEM_BEGIN") {
		t.Fatal("caller system prompt leaked into the user content blocks")
	}
	if !bytes.Contains(first, []byte(`<system-reminder>`)) || bytes.Contains(first, []byte(`\u003csystem-reminder`)) {
		t.Fatalf("currentDate reminder angle brackets must remain literal JSON bytes")
	}

	signed, errSign := finalizeAnthropicMessagesBodyCCH(first, "")
	if errSign != nil {
		t.Fatalf("finalize Claude CCH: %v", errSign)
	}
	resigned, errResign := finalizeAnthropicMessagesBodyCCH(signed, "")
	if errResign != nil {
		t.Fatalf("re-finalize Claude CCH: %v", errResign)
	}
	if !bytes.Equal(signed, resigned) {
		t.Fatal("CCH finalization is not byte-idempotent after long prompt preservation")
	}
}

func TestClaudeExecutor_CustomBaseURLOmitsCCHByDefault(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}

	billingHeader := gjson.GetBytes(seenBody, "system.0.text").String()
	if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
		t.Fatalf("system.0.text = %q, want billing header", billingHeader)
	}
	if strings.Contains(billingHeader, "cch=") {
		t.Fatalf("custom BaseURL must not include CCH, got %q", billingHeader)
	}
}

func TestClaudeExecutor_CustomBaseURLAPIKeyDoesNotEnableCCHSigning(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey:                 "key-123",
			BaseURL:                server.URL,
			ExperimentalCCHSigning: true,
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	const messageText = "please keep literal cch=00000 in this message"
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"please keep literal cch=00000 in this message"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := gjson.GetBytes(seenBody, "messages.0.content.1.text").String(); got != messageText {
		t.Fatalf("message text = %q, want %q", got, messageText)
	}
	assertClaudeCodeCurrentDateBlock(t, gjson.GetBytes(seenBody, "messages.0.content.0"))

	if billing := gjson.GetBytes(seenBody, "system.0.text").String(); strings.Contains(billing, "cch=") {
		t.Fatalf("custom BaseURL billing header must not contain CCH: %q", billing)
	}
}

func TestClaudeExecutor_CustomBaseURLOAuthGeneratesMissingCCH(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":    "sk-ant-oat-custom-cch",
			"base_url":   server.URL,
			"cloak_mode": "never",
		},
		Metadata: claudeOAuthTestMetadata(),
	}
	payload := []byte(`{"model":"claude-opus-4-6","system":"keep original system","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, ok := claudeBillingCCHDigitsOffset(seenBody); !ok {
		t.Fatalf("Claude OAuth custom BaseURL body is missing generated CCH: %s", seenBody)
	}
	if got := gjson.GetBytes(seenBody, "system.1.text").String(); got != "keep original system" {
		t.Fatalf("system.1.text = %q, want preserved system text", got)
	}
}

func TestClaudeExecutor_RebuildMidSystemMessageDisabledByDefault(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey:  "key-123",
			BaseURL: server.URL,
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"system":[{"type":"text","text":"Top rule","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"system","content":"Mid rule"},{"role":"user","content":[{"type":"text","text":"continue"}]}],"metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"account_uuid\":\"\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"}}`)
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":     "claude-cli/2.1.220 (external, cli)",
		"X-App":          "cli",
		"Anthropic-Beta": "claude-code-20250219",
	})

	_, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); got != "Top rule" {
		t.Fatalf("system.0.text = %q, want top-level system preserved", got)
	}
	if got := gjson.GetBytes(seenBody, `messages.#(role=="system").content`).String(); got != "Mid rule" {
		t.Fatalf("mid system message = %q, want original message preserved", got)
	}
}

func TestClaudeExecutor_RebuildMidSystemMessageOptInMovesSystemMessages(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey:                  "key-123",
			BaseURL:                 server.URL,
			RebuildMidSystemMessage: true,
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"system":"Top rule","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"system","content":"Mid string rule"},{"role":"assistant","content":[{"type":"text","text":"ok"}]},{"role":"system","content":[{"type":"text","text":"Mid array rule","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"text","text":"continue"}]}],"metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"account_uuid\":\"\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"}}`)
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":     "claude-cli/2.1.220 (external, cli)",
		"X-App":          "cli",
		"Anthropic-Beta": "claude-code-20250219",
	})

	_, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}

	system := gjson.GetBytes(seenBody, "system").Array()
	if len(system) != 3 {
		t.Fatalf("system has %d items, want 3: %s", len(system), gjson.GetBytes(seenBody, "system").Raw)
	}
	wantTexts := []string{"Top rule", "Mid string rule", "Mid array rule"}
	for i, want := range wantTexts {
		if got := system[i].Get("text").String(); got != want {
			t.Fatalf("system[%d].text = %q, want %q", i, got, want)
		}
	}
	if got := gjson.GetBytes(seenBody, "system.2.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system.2.cache_control.type = %q, want ephemeral", got)
	}
	if gjson.GetBytes(seenBody, `messages.#(role=="system")`).Exists() {
		t.Fatalf("messages should not contain system role after rebuild: %s", gjson.GetBytes(seenBody, "messages").Raw)
	}
	if got := gjson.GetBytes(seenBody, "messages.#").Int(); got != 3 {
		t.Fatalf("messages count = %d, want 3", got)
	}
}

func TestResolveClaudeWirePolicy(t *testing.T) {
	tests := []struct {
		name      string
		confirmed bool
		mode      string
		wantCloak bool
	}{
		{name: "unknown auto", mode: "auto", wantCloak: true},
		{name: "unknown always", mode: "always", wantCloak: true},
		{name: "unknown never", mode: "never", wantCloak: false},
		{name: "confirmed auto", confirmed: true, mode: "auto", wantCloak: false},
		{name: "confirmed always", confirmed: true, mode: "always", wantCloak: false},
		{name: "confirmed never", confirmed: true, mode: "never", wantCloak: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"cloak_mode": test.mode}}
			policy, _ := resolveClaudeWirePolicy(&config.Config{}, auth, "sk-ant-oat-test", test.confirmed)
			if !policy.OAuth {
				t.Fatal("resolveClaudeWirePolicy() OAuth = false, want true")
			}
			if policy.ConfirmedClaudeCode != test.confirmed {
				t.Fatalf("ConfirmedClaudeCode = %v, want %v", policy.ConfirmedClaudeCode, test.confirmed)
			}
			if policy.Cloak != test.wantCloak {
				t.Fatalf("Cloak = %v, want %v", policy.Cloak, test.wantCloak)
			}
		})
	}
}

func TestApplyCloaking_PreservesConfiguredStrictModeAndSensitiveWordsWhenModeOmitted(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak: &config.CloakConfig{
				StrictMode:     true,
				SensitiveWords: []string{"proxy"},
			},
		}},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}
	payload := []byte(`{"system":"proxy rules","messages":[{"role":"user","content":[{"type":"text","text":"proxy access"}]}]}`)

	out, cloaked, errCloaking := applyCloaking(
		context.Background(),
		cfg,
		auth,
		payload,
		"key-123",
		false,
		false,
	)
	if errCloaking != nil {
		t.Fatalf("applyCloaking() error = %v", errCloaking)
	}

	if !cloaked {
		t.Fatal("applyCloaking() cloaked = false, want true")
	}
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected strict mode to keep the 2 injected Claude CLI system blocks, got %d", len(blocks))
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("strict mode should add only currentDate before user text, got %d content blocks", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	if got := content[1].Get("text").String(); !strings.Contains(got, "\u200B") {
		t.Fatalf("expected configured sensitive word obfuscation to apply, got %q", got)
	}
}

func TestNormalizeClaudeSamplingForUpstream_RemovesTemperature(t *testing.T) {
	payload := []byte(`{"temperature":0,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	out := normalizeClaudeSamplingForUpstream(payload, false)

	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
}

func TestNormalizeClaudeSamplingForUpstream_RemovesTemperatureWithThinkingEnabled(t *testing.T) {
	payload := []byte(`{"temperature":0.2,"thinking":{"type":"enabled","budget_tokens":2048}}`)
	out := normalizeClaudeSamplingForUpstream(payload, false)

	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
}

func TestNormalizeClaudeSamplingForUpstream_RemovesTopPAndTopKForThinking(t *testing.T) {
	payload := []byte(`{"temperature":0.2,"top_p":0.9,"top_k":40,"thinking":{"type":"adaptive"}}`)
	out := normalizeClaudeSamplingForUpstream(payload, false)

	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
	if gjson.GetBytes(out, "top_p").Exists() {
		t.Fatalf("top_p should be removed when thinking is active")
	}
	if gjson.GetBytes(out, "top_k").Exists() {
		t.Fatalf("top_k should be removed when thinking is active")
	}
}

func TestNormalizeClaudeSamplingForUpstream_NoThinkingRemovesTemperatureAndTopP(t *testing.T) {
	payload := []byte(`{"temperature":0,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hi"}]}`)
	out := normalizeClaudeSamplingForUpstream(payload, false)

	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
	if gjson.GetBytes(out, "top_p").Exists() {
		t.Fatalf("top_p should be removed")
	}
	if got := gjson.GetBytes(out, "top_k").Int(); got != 40 {
		t.Fatalf("top_k = %v, want 40", got)
	}
}

func TestNormalizeClaudeSamplingForUpstream_AfterForcedToolChoiceRemovesTemperature(t *testing.T) {
	payload := []byte(`{"temperature":0,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"tool_choice":{"type":"any"}}`)
	out := disableThinkingIfToolChoiceForced(payload)
	out = normalizeClaudeSamplingForUpstream(out, false)

	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be removed when tool_choice forces tool use")
	}
	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
}

// The measured structured Haiku helper sends "temperature":1, and
// claudeCodeHelperShapeStructured keys on exactly that value. Stripping it would
// make CPA emit a shape no native client produces, so a confirmed native caller
// must keep it.
func TestNormalizeClaudeSamplingForUpstreamNativeKeepsMeasuredHelperTemperature(t *testing.T) {
	// Top-level key order and values mirror the measured structured helper.
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":[{"type":"text","text":"helper probe"}]}],"system":[{"type":"text","text":"Return a short title."}],"tools":[],"metadata":{"user_id":"u"},"max_tokens":32000,"thinking":{"type":"disabled"},"temperature":1,"output_config":{"format":{"type":"json_schema"}},"stream":true}`)
	if got := gjson.GetBytes(payload, "temperature"); !got.Exists() || got.Num != 1 {
		t.Fatalf("measured helper fixture should carry temperature=1, got %q", got.Raw)
	}

	out := normalizeClaudeSamplingForUpstream(payload, true)

	if got := gjson.GetBytes(out, "temperature"); !got.Exists() || got.Num != 1 {
		t.Fatalf("confirmed native must preserve the measured temperature, got %q", got.Raw)
	}
}

// Anthropic's real constraints, verified against the live API: with thinking
// active temperature must be 1, top_p must be >= 0.95 and top_k must be unset;
// otherwise temperature and top_p cannot both be specified. Preserving the
// native wire must never forward a combination that would 400.
func TestNormalizeClaudeSamplingForUpstreamNativeDropsOnlyRejectedCombinations(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		keep    map[string]float64
		dropped []string
	}{
		{
			name:    "thinking off keeps every accepted knob",
			payload: `{"temperature":0.5,"top_k":40}`,
			keep:    map[string]float64{"temperature": 0.5, "top_k": 40},
		},
		{
			name:    "thinking off drops top_p when temperature is also set",
			payload: `{"temperature":0.5,"top_p":0.9}`,
			keep:    map[string]float64{"temperature": 0.5},
			dropped: []string{"top_p"},
		},
		{
			name:    "thinking off keeps a lone top_p",
			payload: `{"top_p":0.9}`,
			keep:    map[string]float64{"top_p": 0.9},
		},
		{
			name:    "thinking disabled is not thinking",
			payload: `{"temperature":1,"thinking":{"type":"disabled"}}`,
			keep:    map[string]float64{"temperature": 1},
		},
		{
			name:    "thinking enabled keeps temperature 1",
			payload: `{"temperature":1,"thinking":{"type":"enabled","budget_tokens":1024}}`,
			keep:    map[string]float64{"temperature": 1},
		},
		{
			name:    "thinking enabled drops temperature that is not 1",
			payload: `{"temperature":0.5,"thinking":{"type":"enabled","budget_tokens":1024}}`,
			dropped: []string{"temperature"},
		},
		{
			name:    "thinking enabled keeps top_p at or above 0.95",
			payload: `{"top_p":0.99,"thinking":{"type":"enabled","budget_tokens":1024}}`,
			keep:    map[string]float64{"top_p": 0.99},
		},
		{
			name:    "thinking enabled drops top_p below 0.95",
			payload: `{"top_p":0.9,"thinking":{"type":"enabled","budget_tokens":1024}}`,
			dropped: []string{"top_p"},
		},
		{
			name:    "thinking enabled always drops top_k",
			payload: `{"top_k":40,"thinking":{"type":"enabled","budget_tokens":1024}}`,
			dropped: []string{"top_k"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeClaudeSamplingForUpstream([]byte(tc.payload), true)

			for field, want := range tc.keep {
				got := gjson.GetBytes(out, field)
				if !got.Exists() || got.Num != want {
					t.Fatalf("%s = %q, want %v preserved", field, got.Raw, want)
				}
			}
			for _, field := range tc.dropped {
				if got := gjson.GetBytes(out, field); got.Exists() {
					t.Fatalf("%s = %q, want dropped because Anthropic rejects it", field, got.Raw)
				}
			}
		})
	}
}

func TestRemapOAuthToolNames_AllClientNamesUseMCPAliases(t *testing.T) {
	for _, original := range []string{"Bash", "bash", "Glob", "glob"} {
		t.Run(original, func(t *testing.T) {
			body := []byte(`{"tools":[{"name":` + fmt.Sprintf("%q", original) + `,"description":"Run a client tool","input_schema":{"type":"object"}}]}`)
			out, reverseMap := remapOAuthToolNames(body)
			alias := gjson.GetBytes(out, "tools.0.name").String()
			if !helps.IsClaudeMCPToolName(alias) {
				t.Fatalf("tools.0.name = %q, want MCP alias", alias)
			}
			if reverseMap[alias] != original {
				t.Fatalf("reverseMap = %v, want %q -> %q", reverseMap, alias, original)
			}
			resp := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":` + fmt.Sprintf("%q", alias) + `,"input":{}}]}`)
			reversed, errReverse := reverseRemapOAuthToolNames(resp, reverseMap)
			if errReverse != nil {
				t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
			}
			if got := gjson.GetBytes(reversed, "content.0.name").String(); got != original {
				t.Fatalf("content.0.name = %q, want %q", got, original)
			}
		})
	}
}

func TestRemapOAuthToolNames_AllClientToolsAsMCP(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":2},
			{"name":"bash","description":"client shell tool","input_schema":{"type":"object"}},
			{"name":"Read","description":"client read tool","input_schema":{"type":"object"}},
			{"name":"mcp__context7__query-docs","description":"existing MCP tool","input_schema":{"type":"object"}},
			{"name":"search_web","description":"unknown one","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}},
			{"name":"Search_Web","description":"case-distinct unknown","input_schema":{"type":"object"}},
			{"name":"search_web","description":"repeated declaration","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"search_web"},
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_unknown","name":"search_web","input":{"q":"go"}},
				{"type":"tool_reference","tool_name":"Search_Web"}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_unknown","content":[{"type":"tool_reference","tool_name":"search_web"}]}
			]}
		]
	}`)

	out, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "credential-secret"})

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("typed builtin = %q, want unchanged", got)
	}
	bashAlias := gjson.GetBytes(out, "tools.1.name").String()
	readAlias := gjson.GetBytes(out, "tools.2.name").String()
	if !helps.IsClaudeMCPToolName(bashAlias) || !helps.IsClaudeMCPToolName(readAlias) {
		t.Fatalf("former vetted names did not receive MCP aliases: bash=%q Read=%q", bashAlias, readAlias)
	}
	if got := gjson.GetBytes(out, "tools.1.description").String(); got != "client shell tool" {
		t.Fatalf("bash description = %q, want preserved", got)
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("bash schema changed: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.3.name").String(); got != "mcp__context7__query-docs" {
		t.Fatalf("existing MCP tool = %q, want unchanged", got)
	}

	searchAlias := gjson.GetBytes(out, "tools.4.name").String()
	caseAlias := gjson.GetBytes(out, "tools.5.name").String()
	if !helps.IsClaudeMCPToolName(searchAlias) || !helps.IsClaudeMCPToolName(caseAlias) {
		t.Fatalf("generated aliases are invalid: %q, %q", searchAlias, caseAlias)
	}
	if searchAlias == caseAlias {
		t.Fatalf("case-distinct names share alias %q", searchAlias)
	}
	if got := gjson.GetBytes(out, "tools.6.name").String(); got != searchAlias {
		t.Fatalf("repeated declaration alias = %q, want %q", got, searchAlias)
	}
	if !strings.HasSuffix(searchAlias, "_search_web") || !strings.HasSuffix(caseAlias, "_Search_Web") {
		t.Fatalf("generated aliases lost semantic suffixes: %q, %q", searchAlias, caseAlias)
	}
	if len(searchAlias) > 64 || len(caseAlias) > 64 {
		t.Fatalf("generated aliases exceed 64 characters: %q, %q", searchAlias, caseAlias)
	}
	if got := gjson.GetBytes(out, "tools.4.description").String(); got != "unknown one" {
		t.Fatalf("description = %q, want preserved", got)
	}
	if got := gjson.GetBytes(out, "tools.4.input_schema.required.0").String(); got != "q" {
		t.Fatalf("input schema was not preserved: %s", out)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != searchAlias {
		t.Fatalf("tool_choice.name = %q, want %q", got, searchAlias)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != searchAlias {
		t.Fatalf("historical tool_use.name = %q, want %q", got, searchAlias)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.id").String(); got != "toolu_unknown" {
		t.Fatalf("tool_use.id = %q, want unchanged", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.tool_name").String(); got != caseAlias {
		t.Fatalf("tool_reference.tool_name = %q, want %q", got, caseAlias)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.content.0.tool_name").String(); got != searchAlias {
		t.Fatalf("nested tool_reference.tool_name = %q, want %q", got, searchAlias)
	}
	if reverseMap[searchAlias] != "search_web" || reverseMap[caseAlias] != "Search_Web" ||
		reverseMap[bashAlias] != "bash" || reverseMap[readAlias] != "Read" {
		t.Fatalf("reverseMap = %v, want exact client names", reverseMap)
	}

	response := []byte(fmt.Sprintf(`{"content":[
		{"type":"tool_use","id":"toolu_unknown","name":%q,"input":{}},
		{"type":"tool_reference","tool_name":%q},
		{"type":"tool_result","tool_use_id":"toolu_unknown","content":[{"type":"tool_reference","tool_name":%q}]}
	]}`, searchAlias, caseAlias, searchAlias))
	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "search_web" {
		t.Fatalf("restored tool_use.name = %q, want search_web", got)
	}
	if got := gjson.GetBytes(restored, "content.1.tool_name").String(); got != "Search_Web" {
		t.Fatalf("restored tool_reference.tool_name = %q, want Search_Web", got)
	}
	if got := gjson.GetBytes(restored, "content.2.content.0.tool_name").String(); got != "search_web" {
		t.Fatalf("restored nested tool_reference = %q, want search_web", got)
	}

	streamLine := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_unknown","name":%q,"input":{}}}`, searchAlias))
	restoredLine, errReverse := reverseRemapOAuthToolNamesFromStreamLine(streamLine, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errReverse)
	}
	if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != "search_web" {
		t.Fatalf("restored stream name = %q, want search_web: %s", got, restoredLine)
	}
}

func TestRemapOAuthToolNames_TypedCustomUsesMCPAlias(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"custom","name":"client_custom","description":"keep","input_schema":{"type":"object","properties":{"value":{"type":"string"}}}},
			{"type":"web_search_20250305","name":"web_search","max_uses":2},
			{"type":"client_extension_v1","name":"client_extension","description":"extension","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"client_custom"},
		"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_custom","name":"client_custom","input":{}}]}]
	}`)
	out, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "caller-secret"})

	alias := gjson.GetBytes(out, "tools.0.name").String()
	if !helps.IsClaudeMCPToolName(alias) {
		t.Fatalf("typed custom alias = %q, want MCP name", alias)
	}
	if gjson.GetBytes(out, "tools.0.type").Exists() {
		t.Fatalf("typed custom type was not normalized away: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "keep" {
		t.Fatalf("typed custom description = %q, want preserved", got)
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "web_search" {
		t.Fatalf("server builtin name = %q, want unchanged", got)
	}
	extensionAlias := gjson.GetBytes(out, "tools.2.name").String()
	if !helps.IsClaudeMCPToolName(extensionAlias) || gjson.GetBytes(out, "tools.2.type").Exists() {
		t.Fatalf("unknown typed client tool was not normalized: %s", out)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != alias {
		t.Fatalf("tool_choice.name = %q, want %q", got, alias)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != alias {
		t.Fatalf("historical tool_use.name = %q, want %q", got, alias)
	}
	if reverseMap[alias] != "client_custom" || reverseMap[extensionAlias] != "client_extension" {
		t.Fatalf("reverseMap = %v, want exact typed client names", reverseMap)
	}
}

func TestRemapOAuthToolNames_MCPAliasAvoidsClientCollision(t *testing.T) {
	const secret = "credential-secret"
	initialCandidate := helps.ClaudeMCPToolAlias(secret, "fetch_url", 0)
	body := []byte(fmt.Sprintf(`{"tools":[
		{"name":%q,"input_schema":{"type":"object"}},
		{"name":"fetch_url","input_schema":{"type":"object"}}
	]}`, initialCandidate))

	out, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: secret})
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != initialCandidate {
		t.Fatalf("existing MCP tool = %q, want %q", got, initialCandidate)
	}
	alias := gjson.GetBytes(out, "tools.1.name").String()
	if alias == initialCandidate {
		t.Fatalf("generated alias collided with client MCP name %q", alias)
	}
	if reverseMap[alias] != "fetch_url" {
		t.Fatalf("reverseMap = %v, want %q -> fetch_url", reverseMap, alias)
	}
}

func TestRemapOAuthToolNames_MCPAliasIsMandatory(t *testing.T) {
	body := []byte(`{"tools":[{"name":"search_web","input_schema":{"type":"object"}}]}`)
	out, reverseMap := remapOAuthToolNames(body)
	alias := gjson.GetBytes(out, "tools.0.name").String()
	if !helps.IsClaudeMCPToolName(alias) {
		t.Fatalf("tools.0.name = %q, want mandatory MCP alias", alias)
	}
	if reverseMap[alias] != "search_web" {
		t.Fatalf("reverseMap = %v, want alias -> search_web", reverseMap)
	}
}

func TestRemapOAuthToolNames_SemanticAliasRestoresLongOriginal(t *testing.T) {
	original := "Read.file/with a very long semantic name and Unicode 网页内容 that exceeds the wire limit"
	body := []byte(`{"tools":[{"name":` + fmt.Sprintf("%q", original) + `,"input_schema":{"type":"object"}}]}`)
	options := claudeMCPAliasOptions{secret: "stable-caller"}

	out, reverseMap := remapOAuthToolNamesWithOptions(body, options)
	alias := gjson.GetBytes(out, "tools.0.name").String()
	if !helps.IsClaudeMCPToolName(alias) || len(alias) > 64 {
		t.Fatalf("semantic alias is invalid or too long: len=%d name=%q", len(alias), alias)
	}
	if !strings.Contains(alias, "_Read_file_with_a_very_long") {
		t.Fatalf("semantic alias %q does not expose the truncated original meaning", alias)
	}
	if reverseMap[alias] != original {
		t.Fatalf("reverseMap lost exact original: got %q, want %q", reverseMap[alias], original)
	}

	second, _ := remapOAuthToolNamesWithOptions(body, options)
	if got := gjson.GetBytes(second, "tools.0.name").String(); got != alias {
		t.Fatalf("semantic alias is not stable across requests: %q != %q", got, alias)
	}
	response := []byte(`{"content":[{"type":"tool_use","id":"toolu_1","name":` + fmt.Sprintf("%q", alias) + `,"input":{}}]}`)
	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != original {
		t.Fatalf("restored tool name = %q, want exact original %q", got, original)
	}
}

func TestPrepareClaudeOAuthToolNamesForUpstream_PreservesMCPConvention(t *testing.T) {
	body := []byte(`{"tools":[
		{"name":"search_web","input_schema":{"type":"object"}},
		{"name":"mcp__context7__query-docs","input_schema":{"type":"object"}},
		{"name":"bash","input_schema":{"type":"object"}}
	],"tool_choice":{"type":"tool","name":"search_web"}}`)
	out, reverseMap := prepareClaudeOAuthToolNamesForUpstream(body, claudeMCPAliasOptions{secret: "credential-secret"})

	alias := gjson.GetBytes(out, "tools.0.name").String()
	if !helps.IsClaudeMCPToolName(alias) || strings.HasPrefix(alias, "proxy_") {
		t.Fatalf("unknown alias = %q, want bare mcp__ name", alias)
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "mcp__context7__query-docs" {
		t.Fatalf("existing MCP name = %q, want unchanged", got)
	}
	bashAlias := gjson.GetBytes(out, "tools.2.name").String()
	if !helps.IsClaudeMCPToolName(bashAlias) || strings.HasPrefix(bashAlias, "proxy_") {
		t.Fatalf("former vetted tool = %q, want bare MCP alias", bashAlias)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != alias {
		t.Fatalf("tool_choice.name = %q, want %q", got, alias)
	}
	if reverseMap[alias] != "search_web" || reverseMap[bashAlias] != "bash" {
		t.Fatalf("reverseMap = %v, want exact alias restoration", reverseMap)
	}
}

func TestResolveClaudeMCPAliasOptions(t *testing.T) {
	if options := resolveClaudeMCPAliasOptions(context.Background()); options.secret == "" {
		t.Fatal("default caller alias secret is empty")
	}

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("userApiKey", "downstream-caller-one")
	callerCtx := context.WithValue(context.Background(), "gin", ginCtx)
	firstSecret := resolveClaudeMCPAliasOptions(callerCtx).secret
	secondSecret := resolveClaudeMCPAliasOptions(callerCtx).secret
	if firstSecret == "" || secondSecret != firstSecret {
		t.Fatalf("caller alias secret is unstable: %q != %q", firstSecret, secondSecret)
	}
	otherGinCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	otherGinCtx.Set("userApiKey", "downstream-caller-two")
	otherCtx := context.WithValue(context.Background(), "gin", otherGinCtx)
	if otherSecret := resolveClaudeMCPAliasOptions(otherCtx).secret; otherSecret == firstSecret {
		t.Fatalf("different downstream callers shared alias secret %q", firstSecret)
	}
}

func TestRemapOAuthToolNames_MixedCaseNamesRemainDistinct(t *testing.T) {
	body := []byte(`{"tools":[` +
		`{"name":"Bash","input_schema":{"type":"object"}},` +
		`{"name":"bash","input_schema":{"type":"object"}}` +
		`]}`)
	out, reverseMap := remapOAuthToolNames(body)
	upperAlias := gjson.GetBytes(out, "tools.0.name").String()
	lowerAlias := gjson.GetBytes(out, "tools.1.name").String()
	if !helps.IsClaudeMCPToolName(upperAlias) || !helps.IsClaudeMCPToolName(lowerAlias) || upperAlias == lowerAlias {
		t.Fatalf("mixed-case aliases = %q, %q, want distinct MCP names", upperAlias, lowerAlias)
	}
	if reverseMap[upperAlias] != "Bash" || reverseMap[lowerAlias] != "bash" {
		t.Fatalf("reverseMap = %v, want exact mixed-case names", reverseMap)
	}
}

// TestReverseRemapOAuthToolNamesFromStreamLine_HonorsPerRequestMap guards the
// SSE streaming code path against the same mixed-case bug.
func TestReverseRemapOAuthToolNamesFromStreamLine_HonorsPerRequestMap(t *testing.T) {
	reverseMap := map[string]string{"Glob": "glob"}

	// Bash block was never renamed, must pass through as-is.
	bashLine := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}}`)
	out, errReverse := reverseRemapOAuthToolNamesFromStreamLine(bashLine, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errReverse)
	}
	if !bytes.Contains(out, []byte(`"name":"Bash"`)) {
		t.Fatalf("Bash should be preserved, got: %s", string(out))
	}
	if bytes.Contains(out, []byte(`"name":"bash"`)) {
		t.Fatalf("Bash must not be lowercased, got: %s", string(out))
	}

	// Glob block IS in the reverseMap, must be restored to `glob`.
	globLine := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"Glob","input":{}}}`)
	out, errReverse = reverseRemapOAuthToolNamesFromStreamLine(globLine, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errReverse)
	}
	if !bytes.Contains(out, []byte(`"name":"glob"`)) {
		t.Fatalf("Glob should be restored to glob, got: %s", string(out))
	}
}

func TestPrepareClaudeOAuthToolNamesForUpstream_AllCustomToolsWithHistory(t *testing.T) {
	body := []byte(`{"tools":[` +
		`{"name":"Bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}},` +
		`{"name":"glob","input_schema":{"type":"object","properties":{"filePattern":{"type":"string"}}}}` +
		`],"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}},` +
		`{"type":"tool_use","id":"toolu_02","name":"glob","input":{}}` +
		`]}]}`)

	out, reverseMap := prepareClaudeOAuthToolNamesForUpstream(body, claudeMCPAliasOptions{secret: "mixed-case-caller"})
	bashAlias := gjson.GetBytes(out, "tools.0.name").String()
	globAlias := gjson.GetBytes(out, "tools.1.name").String()
	if !helps.IsClaudeMCPToolName(bashAlias) || !helps.IsClaudeMCPToolName(globAlias) || bashAlias == globAlias {
		t.Fatalf("tool aliases = %q, %q, want distinct bare MCP names", bashAlias, globAlias)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != bashAlias {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, bashAlias)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != globAlias {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, globAlias)
	}
	if reverseMap[bashAlias] != "Bash" || reverseMap[globAlias] != "glob" {
		t.Fatalf("reverseMap = %v, want exact client names", reverseMap)
	}
}

func TestClaudeExecutor_ExecuteOpenAINonStreamRestoresOAuthToolNames(t *testing.T) {
	upstreamBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"echo hi\"}"}}`,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	type upstreamRequest struct {
		toolName string
		stream   bool
	}
	upstreamRequests := make(chan upstreamRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, errRead.Error(), http.StatusBadRequest)
			return
		}
		toolName := gjson.GetBytes(body, "tools.0.name").String()
		upstreamRequests <- upstreamRequest{
			toolName: toolName,
			stream:   gjson.GetBytes(body, "stream").Bool(),
		}
		w.Header().Set("Content-Type", "text/event-stream")
		responseBody := strings.Replace(upstreamBody, `"name":"Bash"`, `"name":`+fmt.Sprintf("%q", toolName), 1)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat01-test",
			"base_url": server.URL,
		},
		Metadata: claudeOAuthTestMetadata(),
	}
	payload := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"run echo hi"}],` +
		`"tools":[{"type":"function","function":{"name":"bash","description":"run shell",` +
		`"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	upstream := <-upstreamRequests
	if !upstream.stream {
		t.Fatal("upstream stream = false, want true")
	}
	if !helps.IsClaudeMCPToolName(upstream.toolName) || !strings.HasSuffix(upstream.toolName, "_bash") {
		t.Fatalf("upstream tools.0.name = %q, want semantic MCP alias", upstream.toolName)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool_calls.0.function.name = %q, want %q; payload=%s", got, "bash", string(resp.Payload))
	}
}

func TestClaudeExecutor_ExecuteOAuthCustomToolMCPAliasRoundTrip(t *testing.T) {
	var upstreamAlias string
	var upstreamBody []byte
	var upstreamHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = bytes.Clone(body)
		upstreamHeaders = r.Header.Clone()
		upstreamAlias = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{"query":"go"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, upstreamAlias)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "oauth-mcp-round-trip",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-mcp-round-trip",
			"base_url": server.URL,
		},
		Metadata: claudeOAuthTestMetadata(),
	}
	payload := []byte(`{"model":"claude-opus-5","system":"messages-system-prompt","messages":[{"role":"user","content":"search"}],"tools":[{"name":"search_web","description":"search","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`)
	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if !helps.IsClaudeMCPToolName(upstreamAlias) || strings.HasPrefix(upstreamAlias, "proxy_") || !strings.HasSuffix(upstreamAlias, "_search_web") {
		t.Fatalf("upstream tool name = %q, want semantic mcp__ alias", upstreamAlias)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.name").String(); got != "search_web" {
		t.Fatalf("client response tool name = %q, want search_web; payload=%s", got, resp.Payload)
	}
	if _, ok := claudeBillingCCHDigitsOffset(upstreamBody); !ok {
		t.Fatalf("Claude OAuth custom BaseURL body is missing CCH: %s", upstreamBody)
	}
	if got := upstreamHeaders.Get("User-Agent"); got != "claude-cli/2.1.220 (external, cli)" {
		t.Fatalf("Messages User-Agent = %q, want CLI identity", got)
	}
	wantBetas := claudeCodeCLIBetas(payload, nil, true)
	if got := upstreamHeaders.Get("Anthropic-Beta"); got != wantBetas {
		t.Fatalf("Messages Anthropic-Beta = %q, want %q", got, wantBetas)
	}
	if got := gjson.GetBytes(upstreamBody, "system.1.text").String(); got != claudeCodeCLIIdentity {
		t.Fatalf("Messages system.1.text = %q, want official CLI identity", got)
	}
	if got := gjson.GetBytes(upstreamBody, "system.#").Int(); got != 2 {
		t.Fatalf("Messages top-level system block count = %d, want 2", got)
	}
	content := gjson.GetBytes(upstreamBody, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("Messages first user content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "search", "1h")
	assertClaudeMidConversationSystemMessage(t, upstreamBody, 1, "messages-system-prompt", "1h")
}

func TestClaudeExecutor_ExecuteStreamOAuthCustomToolMCPAliasRoundTrip(t *testing.T) {
	var upstreamAlias string
	var upstreamBody []byte
	var upstreamHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = bytes.Clone(body)
		upstreamHeaders = r.Header.Clone()
		upstreamAlias = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":%q,\"input\":{}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", upstreamAlias)
	}))
	defer server.Close()

	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "oauth-mcp-stream-round-trip",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-mcp-stream-round-trip",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: deviceIDs,
		},
	}
	payload := []byte(`{"model":"claude-opus-5","system":"stream-system-prompt","messages":[{"role":"user","content":"fetch"}],"tools":[{"name":"fetch_url","description":"fetch","input_schema":{"type":"object"}}],"stream":true}`)
	result, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "stream-agent-conversation",
		},
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var downstream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		downstream.Write(chunk.Payload)
	}
	if !helps.IsClaudeMCPToolName(upstreamAlias) || !strings.HasSuffix(upstreamAlias, "_fetch_url") {
		t.Fatalf("upstream tool name = %q, want semantic mcp__ alias", upstreamAlias)
	}
	if _, ok := claudeBillingCCHDigitsOffset(upstreamBody); !ok {
		t.Fatalf("streaming Claude OAuth custom BaseURL body is missing CCH: %s", upstreamBody)
	}
	if got := upstreamHeaders.Get("User-Agent"); got != "claude-cli/2.1.220 (external, cli)" {
		t.Fatalf("streaming User-Agent = %q, want CLI identity", got)
	}
	wantBetas := claudeCodeCLIBetas(payload, nil, true)
	if got := upstreamHeaders.Get("Anthropic-Beta"); got != wantBetas {
		t.Fatalf("streaming Anthropic-Beta = %q, want %q", got, wantBetas)
	}
	if got := gjson.GetBytes(upstreamBody, "system.1.text").String(); got != claudeCodeCLIIdentity {
		t.Fatalf("streaming system.1.text = %q, want official CLI identity", got)
	}
	if got := gjson.GetBytes(upstreamBody, "system.#").Int(); got != 2 {
		t.Fatalf("streaming top-level system block count = %d, want 2", got)
	}
	content := gjson.GetBytes(upstreamBody, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("streaming first user content has %d blocks, want currentDate and user text", len(content))
	}
	assertClaudeCodeCurrentDateBlock(t, content[0])
	assertEphemeralUserTextBlock(t, content[1], "fetch", "1h")
	assertClaudeMidConversationSystemMessage(t, upstreamBody, 1, "stream-system-prompt", "1h")
	assertClaudeCredentialIdentity(t, upstreamBody, upstreamHeaders, deviceIDs, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if !strings.Contains(downstream.String(), `"name":"fetch_url"`) {
		t.Fatalf("downstream stream did not restore fetch_url: %s", downstream.String())
	}
	if strings.Contains(downstream.String(), upstreamAlias) {
		t.Fatalf("downstream leaked upstream alias %q: %s", upstreamAlias, downstream.String())
	}
}

func TestPrependClaudeSystemReminders_FollowsToolResultsAndIsIdempotent(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},{"type":"text","text":"continue"}]}` +
		`]}`)

	texts := []string{"first guidance", "second guidance"}
	first := prependClaudeSystemRemindersToFirstUserMessage(payload, texts)
	second := prependClaudeSystemRemindersToFirstUserMessage(first, texts)
	if !bytes.Equal(first, second) {
		t.Fatalf("caller reminder insertion is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
	content := gjson.GetBytes(first, "messages.1.content").Array()
	if len(content) != 4 {
		t.Fatalf("content has %d blocks, want tool_result, two caller reminders, and user text", len(content))
	}
	if got := content[0].Get("type").String(); got != "tool_result" {
		t.Fatalf("content[0].type = %q, want tool_result", got)
	}
	for idx, text := range texts {
		if got := content[idx+1].Get("text").String(); got != claudeCallerSystemReminder(text) {
			t.Fatalf("content[%d].text = %q, want caller reminder %q", idx+1, got, text)
		}
	}
	if got := content[3].Get("text").String(); got != "continue" {
		t.Fatalf("content[3].text = %q, want user text", got)
	}
}

func TestInsertClaudeMidConversationSystemMessages_FollowsToolResultUserTurn(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}` +
		`]}`)

	out := insertClaudeMidConversationSystemMessages(payload, []string{"guidance"})
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want 3: %s", got, out)
	}
	blocks := gjson.GetBytes(out, "messages.1.content")
	if got := blocks.Get("0.type").String(); got != "tool_result" {
		t.Fatalf("first block type = %q, want tool_result: %s", got, out)
	}
	if got := blocks.Get("0.tool_use_id").String(); got != "toolu_1" {
		t.Fatalf("tool_use_id = %q, want toolu_1: %s", got, out)
	}
	assertClaudeMidConversationSystemMessage(t, out, 2, "guidance", "")
}

func TestInsertClaudeMidConversationSystemMessages_PrecedesExistingAssistantTurn(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"answer"},` +
		`{"role":"user","content":"continue"}` +
		`]}`)

	out := insertClaudeMidConversationSystemMessages(payload, []string{"guidance"})
	roles := gjson.GetBytes(out, "messages.#.role").Array()
	wantRoles := []string{"user", "system", "assistant", "user"}
	if len(roles) != len(wantRoles) {
		t.Fatalf("message count = %d, want %d: %s", len(roles), len(wantRoles), out)
	}
	for idx, wantRole := range wantRoles {
		if got := roles[idx].String(); got != wantRole {
			t.Fatalf("messages[%d].role = %q, want %q", idx, got, wantRole)
		}
	}
	assertClaudeMidConversationSystemMessage(t, out, 1, "guidance", "")
}

func TestInsertClaudeMidConversationSystemMessages_FollowsConsecutiveUserRun(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"user","content":"first"},` +
		`{"role":"user","content":"second"},` +
		`{"role":"assistant","content":"answer"}` +
		`]}`)

	out := insertClaudeMidConversationSystemMessages(payload, []string{"guidance"})
	roles := gjson.GetBytes(out, "messages.#.role").Array()
	wantRoles := []string{"user", "user", "system", "assistant"}
	if len(roles) != len(wantRoles) {
		t.Fatalf("message count = %d, want %d: %s", len(roles), len(wantRoles), out)
	}
	for idx, wantRole := range wantRoles {
		if got := roles[idx].String(); got != wantRole {
			t.Fatalf("messages[%d].role = %q, want %q", idx, got, wantRole)
		}
	}
	assertClaudeMidConversationSystemMessage(t, out, 2, "guidance", "")
}

func TestInsertClaudeMidConversationSystemMessages_IsIdempotent(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	texts := []string{"first guidance", "second guidance"}
	first := insertClaudeMidConversationSystemMessages(payload, texts)
	second := insertClaudeMidConversationSystemMessages(first, texts)
	if !bytes.Equal(first, second) {
		t.Fatalf("mid-conversation system insertion is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
	if got := gjson.GetBytes(first, "messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want user and two system messages: %s", got, first)
	}
	assertClaudeMidConversationSystemMessage(t, first, 1, texts[0], "")
	assertClaudeMidConversationSystemMessage(t, first, 2, texts[1], "")
}

// TestClaudeCodeCLIBetas_MatchesObservedClientMatrix pins the Anthropic-Beta
// baseline to Claude Code 2.1.220 behavior captured against api.anthropic.com.
// The OAuth profile was reverified on 2026-08-03 with two distinct accounts.
func TestClaudeCodeCLIBetas_MatchesObservedClientMatrix(t *testing.T) {
	const constants = "claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05"

	tests := []struct {
		name      string
		body      string
		requested map[string]bool
		oauth     bool
		want      string
	}{
		{
			name: "legacy model without tools omits both conditional betas",
			body: `{"model":"claude-opus-4-6"}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name:      "context 1m sits right after claude-code, not at the end",
			body:      `{"model":"claude-opus-4-6"}`,
			requested: map[string]bool{claudeContext1MBeta: true},
			want: "claude-code-20250219,context-1m-2025-08-07," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24",
		},
		{
			name: "opus-5 1m variant reproduces the full observed order",
			body: `{"model":"claude-opus-5","tools":[{"name":"Read"}]}`,
			requested: map[string]bool{
				claudeContext1MBeta:          true,
				claudeServerSideFallbackBeta: true,
				claudeFallbackCreditBeta:     true,
			},
			want: "claude-code-20250219,context-1m-2025-08-07," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07," +
				"advanced-tool-use-2025-11-20,effort-2025-11-24," +
				"server-side-fallback-2026-06-01,fallback-credit-2026-06-01",
		},
		{
			name:      "structured outputs trails effort",
			body:      `{"model":"claude-opus-4-6"}`,
			requested: map[string]bool{claudeStructuredOutputsBeta: true},
			want:      constants + ",effort-2025-11-24,structured-outputs-2025-12-15",
		},
		{
			name:      "unknown caller beta is not smuggled into the baseline",
			body:      `{"model":"claude-opus-4-6"}`,
			requested: map[string]bool{"totally-made-up-2030-01-01": true},
			want:      constants + ",effort-2025-11-24",
		},
		{
			name: "claude-sonnet-5 accepts role=system",
			body: `{"model":"claude-sonnet-5"}`,
			want: constants + ",mid-conversation-system-2026-04-07,effort-2025-11-24",
		},
		{
			name: "claude-opus-4-8 accepts role=system",
			body: `{"model":"claude-opus-4-8"}`,
			want: constants + ",mid-conversation-system-2026-04-07,effort-2025-11-24",
		},
		{
			name: "claude-fable-5 accepts role=system",
			body: `{"model":"claude-fable-5"}`,
			want: constants + ",mid-conversation-system-2026-04-07,effort-2025-11-24",
		},
		{
			name: "claude-opus-4-7 stays on the reminder path",
			body: `{"model":"claude-opus-4-7"}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name:  "oauth uses advanced tools and the current cache TTL trailer",
			body:  `{"model":"claude-opus-4-6","tools":[{"name":"Read"}]}`,
			oauth: true,
			want: "claude-code-20250219,oauth-2025-04-20," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20," +
				"effort-2025-11-24,fallback-credit-2026-06-01," +
				"extended-cache-ttl-2025-04-11",
		},
		{
			name:  "oauth precedes context-1m",
			body:  `{"model":"claude-opus-5","tools":[{"name":"Read"}]}`,
			oauth: true,
			requested: map[string]bool{
				claudeContext1MBeta:          true,
				claudeServerSideFallbackBeta: true,
				claudeFallbackCreditBeta:     true,
			},
			want: "claude-code-20250219,oauth-2025-04-20,context-1m-2025-08-07," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07," +
				"advanced-tool-use-2025-11-20,effort-2025-11-24," +
				"server-side-fallback-2026-06-01,fallback-credit-2026-06-01," +
				"extended-cache-ttl-2025-04-11",
		},
		{
			name: "api key path sends neither oauth beta",
			body: `{"model":"claude-opus-4-6"}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "claude-haiku-4-5-20251001 stays on the reminder path",
			body: `{"model":"claude-haiku-4-5-20251001"}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "legacy model with tools adds advanced tool use only",
			body: `{"model":"claude-sonnet-4-6","tools":[{"name":"Read"}]}`,
			want: constants + ",advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "role=system model without tools adds mid conversation system only",
			body: `{"model":"claude-opus-5"}`,
			want: constants + ",mid-conversation-system-2026-04-07,effort-2025-11-24",
		},
		{
			name: "role=system model with tools adds both in wire order",
			body: `{"model":"claude-opus-5","tools":[{"name":"Read"}]}`,
			want: constants + ",mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "empty tools array does not add advanced tool use",
			body: `{"model":"claude-opus-4-6","tools":[]}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "unknown future model keeps the optimistic role=system default",
			body: `{"model":"claude-future-9"}`,
			want: constants + ",mid-conversation-system-2026-04-07,effort-2025-11-24",
		},
		{
			name: "thinking display summarized drops redact-thinking",
			body: `{"model":"claude-opus-5","thinking":{"type":"adaptive","display":"summarized"}}`,
			want: "claude-code-20250219,interleaved-thinking-2025-05-14," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07," +
				"effort-2025-11-24",
		},
		{
			name: "thinking display omitted drops redact-thinking as well",
			body: `{"model":"claude-opus-4-6","thinking":{"type":"enabled","budget_tokens":2048,"display":"omitted"}}`,
			want: "claude-code-20250219,interleaved-thinking-2025-05-14," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24",
		},
		{
			name: "thinking without display keeps redact-thinking",
			body: `{"model":"claude-opus-4-6","thinking":{"type":"adaptive"}}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "blank display value keeps redact-thinking",
			body: `{"model":"claude-opus-4-6","thinking":{"type":"adaptive","display":"  "}}`,
			want: constants + ",effort-2025-11-24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeCodeCLIBetas([]byte(tt.body), tt.requested, tt.oauth); got != tt.want {
				t.Fatalf("claudeCodeCLIBetas() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyClaudeHeaders_StreamTransportNegotiation pins the observed 2.1.220
// behaviour: a streaming request to api.anthropic.com negotiates exactly like a
// non-streaming one, because Anthropic selects SSE from the body. Other
// Anthropic-compatible upstreams keep the conservative SSE contract.
func TestApplyClaudeHeaders_StreamTransportNegotiation(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-stream-accept"}}
	body := []byte(`{"model":"claude-opus-4-6","stream":true}`)

	directReq := newClaudeHeaderTestRequest(t, http.Header{})
	if errApply := applyClaudeHeaders(directReq, auth, "key-stream-accept", true, nil, body, nil, http.Header{}, false); errApply != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errApply)
	}
	if got, want := directReq.Header.Get("Accept"), "application/json"; got != want {
		t.Fatalf("streaming Accept = %q, want %q to match the real client", got, want)
	}
	if got, want := directReq.Header.Get("Accept-Encoding"), "gzip, deflate, br, zstd"; got != want {
		t.Fatalf("streaming Accept-Encoding = %q, want %q to match the real client", got, want)
	}

	gatewayReq := httptest.NewRequest(http.MethodPost, "https://api.kimi.com/coding/v1/messages", nil)
	gatewayReq = gatewayReq.WithContext(directReq.Context())
	if errApply := applyClaudeHeaders(gatewayReq, auth, "key-stream-accept", true, nil, body, nil, http.Header{}, false); errApply != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errApply)
	}
	if got, want := gatewayReq.Header.Get("Accept"), "text/event-stream"; got != want {
		t.Fatalf("gateway streaming Accept = %q, want %q", got, want)
	}
	if got, want := gatewayReq.Header.Get("Accept-Encoding"), "identity"; got != want {
		t.Fatalf("gateway streaming Accept-Encoding = %q, want %q", got, want)
	}
}

func TestApplyClaudeHeaders_CallerBetasScopedByUpstream(t *testing.T) {
	incoming := http.Header{"Anthropic-Beta": []string{"caller-only-beta"}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-caller-betas"}}
	body := []byte(`{"model":"claude-opus-4-6"}`)

	// Direct Anthropic must not echo a beta real Claude Code never sends.
	directReq := newClaudeHeaderTestRequest(t, incoming)
	if errApply := applyClaudeHeaders(directReq, auth, "key-caller-betas", false, nil, body, nil, incoming, false); errApply != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errApply)
	}
	if got := directReq.Header.Get("Anthropic-Beta"); strings.Contains(got, "caller-only-beta") {
		t.Fatalf("Anthropic-Beta = %q, want caller beta dropped on api.anthropic.com", got)
	}
	if got, want := directReq.Header.Get("Anthropic-Beta"), claudeCodeCLIBetas(body, nil, false); got != want {
		t.Fatalf("Anthropic-Beta = %q, want exactly the CLI baseline %q", got, want)
	}

	// Other Anthropic-compatible upstreams keep caller betas functional.
	gatewayReq := httptest.NewRequest(http.MethodPost, "https://api.kimi.com/coding/v1/messages", nil)
	gatewayReq = gatewayReq.WithContext(directReq.Context())
	if errApply := applyClaudeHeaders(gatewayReq, auth, "key-caller-betas", false, nil, body, nil, incoming, false); errApply != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errApply)
	}
	if got := gatewayReq.Header.Get("Anthropic-Beta"); !strings.Contains(got, "caller-only-beta") {
		t.Fatalf("Anthropic-Beta = %q, want caller beta preserved on non-Anthropic upstream", got)
	}
}

// TestInjectClaudeCodeContextManagement pins the captured 2.1.220 object and
// the thinking and caller-ownership rules that control automatic injection.
func TestInjectClaudeCodeContextManagement(t *testing.T) {
	const captured = `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`

	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "enabled thinking", payload: `{"model":"claude-opus-5","thinking":{"type":"enabled"}}`},
		{name: "adaptive thinking", payload: `{"model":"claude-opus-5","thinking":{"type":"adaptive"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, automaticallyInjected := injectClaudeCodeContextManagement([]byte(test.payload))
			if !automaticallyInjected {
				t.Fatal("automatic context_management injection was not reported")
			}
			if diff := gjson.GetBytes(got, "context_management").Raw; diff != captured {
				t.Fatalf("context_management = %s, want the captured object %s", diff, captured)
			}
		})
	}

	callerOwned := []byte(`{"model":"claude-opus-4-6","context_management":{"edits":[]}}`)
	callerOwnedGot, automaticallyInjected := injectClaudeCodeContextManagement(callerOwned)
	if automaticallyInjected {
		t.Error("caller context_management was reported as automatically injected")
	}
	if !bytes.Equal(callerOwnedGot, callerOwned) {
		t.Fatalf("caller context_management was modified: %s", callerOwnedGot)
	}

	// Anthropic rejects clear_thinking_20251015 unless thinking is enabled or
	// adaptive, so an omitted thinking field is as ineligible as an explicit
	// disabled one.
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "disabled thinking", payload: `{"model":"claude-opus-5","thinking":{"type":"disabled"}}`},
		{name: "omitted thinking", payload: `{"model":"claude-opus-4-6"}`},
		{name: "unknown thinking", payload: `{"model":"claude-opus-5","thinking":{"type":"unexpected"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ineligible := []byte(test.payload)
			got, automaticallyInjected := injectClaudeCodeContextManagement(ineligible)
			if automaticallyInjected {
				t.Error("ineligible thinking context_management was reported as automatically injected")
			}
			if !bytes.Equal(got, ineligible) {
				t.Errorf("ineligible payload was modified: %s", got)
			}
			if cm := gjson.GetBytes(got, "context_management"); cm.Exists() {
				t.Errorf("context_management = %s, want absent", cm.Raw)
			}
		})
	}
}

// Anthropic rejects a request carrying the clear_thinking_20251015 strategy
// without enabled/adaptive thinking:
//
//	`clear_thinking_20251015` strategy requires `thinking` to be enabled or adaptive
//
// This walks the real execute.go ordering, where disableThinkingIfToolChoiceForced
// deletes the thinking field between injection and reconciliation.
func TestClaudeCodeContextManagementNeverOutlivesEligibleThinking(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		wantCM  bool
	}{
		{
			name:    "thinking omitted from the start",
			payload: `{"model":"claude-opus-5","messages":[]}`,
		},
		{
			name:    "forced tool_choice strips thinking after injection",
			payload: `{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":1024},"tool_choice":{"type":"any"},"messages":[]}`,
		},
		{
			name:    "thinking survives without forced tool_choice",
			payload: `{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":1024},"messages":[]}`,
			wantCM:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, injected := injectClaudeCodeContextManagement([]byte(test.payload))
			state := claudeCodeContextManagementState{eligible: true, automaticallyInjected: injected}
			body = disableThinkingIfToolChoiceForced(body)
			body = reconcileClaudeCodeContextManagement(body, state)

			thinkingEligible := gjson.GetBytes(body, "thinking.type").String() == "enabled" ||
				gjson.GetBytes(body, "thinking.type").String() == "adaptive"
			cm := gjson.GetBytes(body, "context_management")
			if cm.Exists() && !thinkingEligible {
				t.Fatalf("context_management = %s survived ineligible thinking; Anthropic would reject this: %s", cm.Raw, body)
			}
			if cm.Exists() != test.wantCM {
				t.Fatalf("context_management present = %v, want %v; body=%s", cm.Exists(), test.wantCM, body)
			}
		})
	}
}

func TestReconcileClaudeCodeContextManagement(t *testing.T) {
	withAutomatic := func(thinkingType string) string {
		return `{"thinking":{"type":"` + thinkingType + `"},"context_management":` + claudeCodeContextManagement + `}`
	}

	for _, test := range []struct {
		name    string
		payload string
		state   claudeCodeContextManagementState
		wantRaw string
	}{
		{
			name:    "removes unchanged automatic object when disabled",
			payload: withAutomatic("disabled"),
			state:   claudeCodeContextManagementState{eligible: true, automaticallyInjected: true},
		},
		{
			name:    "preserves rule owned automatic object when disabled",
			payload: withAutomatic("disabled"),
			state:   claudeCodeContextManagementState{eligible: true, automaticallyInjected: true, payloadRuleTouched: true},
			wantRaw: claudeCodeContextManagement,
		},
		{
			name:    "preserves changed automatic object when disabled",
			payload: `{"thinking":{"type":"disabled"},"context_management":{"edits":[{"type":"custom"}]}}`,
			state:   claudeCodeContextManagementState{eligible: true, automaticallyInjected: true},
			wantRaw: `{"edits":[{"type":"custom"}]}`,
		},
		{
			name:    "adds automatic object when enabled",
			payload: `{"thinking":{"type":"enabled"}}`,
			state:   claudeCodeContextManagementState{eligible: true},
			wantRaw: claudeCodeContextManagement,
		},
		{
			name:    "adds automatic object when adaptive",
			payload: `{"thinking":{"type":"adaptive"}}`,
			state:   claudeCodeContextManagementState{eligible: true},
			wantRaw: claudeCodeContextManagement,
		},
		{
			name:    "caller ownership prevents addition",
			payload: `{"thinking":{"type":"enabled"}}`,
			state:   claudeCodeContextManagementState{eligible: true, callerOwned: true},
		},
		{
			name:    "payload rule ownership prevents addition",
			payload: `{"thinking":{"type":"enabled"}}`,
			state:   claudeCodeContextManagementState{eligible: true, payloadRuleTouched: true},
		},
		{
			name:    "ineligible request prevents addition",
			payload: `{"thinking":{"type":"enabled"}}`,
		},
		{
			name:    "omitted thinking prevents addition",
			payload: `{}`,
			state:   claudeCodeContextManagementState{eligible: true},
		},
		{
			name:    "removes automatic object when thinking was stripped entirely",
			payload: `{"context_management":` + claudeCodeContextManagement + `}`,
			state:   claudeCodeContextManagementState{eligible: true, automaticallyInjected: true},
		},
		{
			name:    "keeps caller object when thinking was stripped entirely",
			payload: `{"context_management":` + claudeCodeContextManagement + `}`,
			state:   claudeCodeContextManagementState{eligible: true, callerOwned: true},
			wantRaw: claudeCodeContextManagement,
		},
		{
			name:    "unknown thinking prevents addition",
			payload: `{"thinking":{"type":"unexpected"}}`,
			state:   claudeCodeContextManagementState{eligible: true},
		},
		{
			name:    "invalid thinking prevents addition",
			payload: `{"thinking":{"type":123}}`,
			state:   claudeCodeContextManagementState{eligible: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := reconcileClaudeCodeContextManagement([]byte(test.payload), test.state)
			if raw := gjson.GetBytes(got, "context_management").Raw; raw != test.wantRaw {
				t.Fatalf("context_management = %s, want %s; body=%s", raw, test.wantRaw, got)
			}
		})
	}
}

func TestClaudeExecutorPayloadOverrideDisabledThinking(t *testing.T) {
	const model = "claude-opus-5"
	modelRules := []config.PayloadModelRule{{Name: model, Protocol: "claude"}}
	basePayload := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

	for _, test := range []struct {
		name   string
		stream bool
	}{
		{name: "execute"},
		{name: "execute stream", stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"thinking.type": "disabled"},
			}}}}
			upstreamBody := executeClaudeContextManagementRequest(t, cfg, basePayload, test.stream)
			if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "disabled" {
				t.Fatalf("final upstream thinking.type = %q, want disabled; body=%s", got, upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
				t.Errorf("final upstream context_management = %s with disabled thinking, want absent", got.Raw)
			}
		})
	}

	t.Run("caller context management is preserved", func(t *testing.T) {
		cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: modelRules,
			Params: map[string]any{"thinking.type": "disabled"},
		}}}}
		payload := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"caller_owned"}]}}`)
		upstreamBody := executeClaudeContextManagementRequest(t, cfg, payload, false)
		if got := gjson.GetBytes(upstreamBody, "context_management.edits.0.type").String(); got != "caller_owned" {
			t.Fatalf("caller context_management type = %q, want caller_owned; body=%s", got, upstreamBody)
		}
	})

	t.Run("payload override replacement is preserved", func(t *testing.T) {
		cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: modelRules,
			Params: map[string]any{
				"thinking.type":      "disabled",
				"context_management": map[string]any{"edits": []any{map[string]any{"type": "payload_rule"}}},
			},
		}}}}
		upstreamBody := executeClaudeContextManagementRequest(t, cfg, basePayload, false)
		if got := gjson.GetBytes(upstreamBody, "context_management.edits.0.type").String(); got != "payload_rule" {
			t.Fatalf("payload-rule context_management type = %q, want payload_rule; body=%s", got, upstreamBody)
		}
	})

	t.Run("exact automatic value remains payload rule owned", func(t *testing.T) {
		ownershipConfigs := []struct {
			name string
			cfg  *config.Config
		}{
			{
				name: "default",
				cfg: &config.Config{Payload: config.PayloadConfig{
					Default: []config.PayloadRule{{
						Models: modelRules,
						Params: map[string]any{"context_management": json.RawMessage(claudeCodeContextManagement)},
					}},
					Override: []config.PayloadRule{{
						Models: modelRules,
						Params: map[string]any{"thinking.type": "disabled"},
					}},
				}},
			},
			{
				name: "raw default",
				cfg: &config.Config{Payload: config.PayloadConfig{
					DefaultRaw: []config.PayloadRule{{
						Models: modelRules,
						Params: map[string]any{"context_management": claudeCodeContextManagement},
					}},
					Override: []config.PayloadRule{{
						Models: modelRules,
						Params: map[string]any{"thinking.type": "disabled"},
					}},
				}},
			},
			{
				name: "override",
				cfg: &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
					Models: modelRules,
					Params: map[string]any{
						"thinking.type":      "disabled",
						"context_management": json.RawMessage(claudeCodeContextManagement),
					},
				}}}},
			},
			{
				name: "raw override",
				cfg: &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{
						Models: modelRules,
						Params: map[string]any{"thinking.type": "disabled"},
					}},
					OverrideRaw: []config.PayloadRule{{
						Models: modelRules,
						Params: map[string]any{"context_management": claudeCodeContextManagement},
					}},
				}},
			},
		}
		for _, ownership := range ownershipConfigs {
			for _, stream := range []bool{false, true} {
				name := ownership.name + " execute"
				if stream {
					name += " stream"
				}
				t.Run(name, func(t *testing.T) {
					upstreamBody := executeClaudeContextManagementRequest(t, ownership.cfg, basePayload, stream)
					if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "disabled" {
						t.Fatalf("final upstream thinking.type = %q, want disabled; body=%s", got, upstreamBody)
					}
					if got := gjson.GetBytes(upstreamBody, "context_management").Raw; got != claudeCodeContextManagement {
						t.Fatalf("%s context_management = %s, want payload-rule-owned %s; body=%s", ownership.name, got, claudeCodeContextManagement, upstreamBody)
					}
				})
			}
		}
	})

	t.Run("payload filter remains effective", func(t *testing.T) {
		cfg := &config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
			Models: modelRules,
			Params: []string{"context_management"},
		}}}}
		upstreamBody := executeClaudeContextManagementRequest(t, cfg, basePayload, false)
		if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
			t.Fatalf("filtered context_management = %s, want absent", got.Raw)
		}
	})

	for _, stream := range []bool{false, true} {
		// Anthropic rejects the automatic strategy once forced tool choice has
		// stripped thinking:
		//
		//	`clear_thinking_20251015` strategy requires `thinking` to be enabled or adaptive
		name := "forced tool choice drops automatic context management execute"
		if stream {
			name += " stream"
		}
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"}}`)
			upstreamBody := executeClaudeContextManagementRequest(t, &config.Config{}, payload, stream)
			if got := gjson.GetBytes(upstreamBody, "thinking"); got.Exists() {
				t.Fatalf("forced tool choice thinking = %s, want absent", got.Raw)
			}
			if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
				t.Fatalf("forced tool choice context_management = %s, want absent because Anthropic rejects it without thinking", got.Raw)
			}
			if got := gjson.GetBytes(upstreamBody, "tool_choice.type").String(); got != "any" {
				t.Fatalf("forced tool_choice.type = %q, want any", got)
			}
		})
	}
}

func TestClaudeExecutorPayloadOverrideReenablesThinking(t *testing.T) {
	const model = "claude-opus-5"
	modelRules := []config.PayloadModelRule{{Name: model, Protocol: "claude"}}
	basePayload := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)

	for _, test := range []struct {
		name         string
		thinkingType string
		stream       bool
	}{
		{name: "execute enabled", thinkingType: "enabled"},
		{name: "execute adaptive", thinkingType: "adaptive"},
		{name: "execute stream enabled", thinkingType: "enabled", stream: true},
		{name: "execute stream adaptive", thinkingType: "adaptive", stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"thinking.type": test.thinkingType},
			}}}}
			upstreamBody := executeClaudeContextManagementRequest(t, cfg, basePayload, test.stream)
			if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != test.thinkingType {
				t.Fatalf("final upstream thinking.type = %q, want %q; body=%s", got, test.thinkingType, upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "context_management").Raw; got != claudeCodeContextManagement {
				t.Fatalf("final upstream context_management = %s, want %s after payload override to %s; body=%s", got, claudeCodeContextManagement, test.thinkingType, upstreamBody)
			}
		})
	}

	for _, stream := range []bool{false, true} {
		nameSuffix := "execute"
		if stream {
			nameSuffix = "execute stream"
		}

		t.Run("caller context management is preserved after re-enabling "+nameSuffix, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{"thinking.type": "enabled"},
			}}}}
			payload := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"context_management":{"edits":[{"type":"caller_owned"}]}}`)
			upstreamBody := executeClaudeContextManagementRequest(t, cfg, payload, stream)
			if got := gjson.GetBytes(upstreamBody, "context_management.edits.0.type").String(); got != "caller_owned" {
				t.Fatalf("caller context_management type = %q, want caller_owned; body=%s", got, upstreamBody)
			}
		})

		t.Run("custom payload rule object is preserved after re-enabling "+nameSuffix, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: modelRules,
				Params: map[string]any{
					"thinking.type":      "adaptive",
					"context_management": map[string]any{"edits": []any{map[string]any{"type": "payload_rule"}}},
				},
			}}}}
			upstreamBody := executeClaudeContextManagementRequest(t, cfg, basePayload, stream)
			if got := gjson.GetBytes(upstreamBody, "context_management.edits.0.type").String(); got != "payload_rule" {
				t.Fatalf("payload-rule context_management type = %q, want payload_rule; body=%s", got, upstreamBody)
			}
		})

		t.Run("context management filter remains authoritative after re-enabling "+nameSuffix, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{
					Models: modelRules,
					Params: map[string]any{"thinking.type": "enabled"},
				}},
				Filter: []config.PayloadFilterRule{{
					Models: modelRules,
					Params: []string{"context_management"},
				}},
			}}
			upstreamBody := executeClaudeContextManagementRequest(t, cfg, basePayload, stream)
			if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "enabled" {
				t.Fatalf("final upstream thinking.type = %q, want enabled; body=%s", got, upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
				t.Fatalf("filtered context_management = %s after re-enabling, want absent; body=%s", got.Raw, upstreamBody)
			}
		})
	}
}

func executeClaudeContextManagementRequest(t *testing.T, cfg *config.Config, payload []byte, stream bool) []byte {
	t.Helper()

	var upstreamBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		contentType := "application/json"
		responseBody := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		if stream {
			contentType = "text/event-stream"
			responseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-payload-rule"}}
	request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	if stream {
		result, errStream := executor.ExecuteStream(ctx, auth, request, options)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
		return upstreamBody
	}
	if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	return upstreamBody
}

func TestValidateClaudeCallerSystemBlocksAcceptsTextOnly(t *testing.T) {
	tests := []struct {
		name   string
		system string
	}{
		{name: "string", system: `"S1"`},
		{name: "text blocks", system: `[{"type":"text","text":"S1"},{"type":"text","text":"S2"}]`},
		{name: "absent", system: ``},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := `{"model":"claude-opus-5"}`
			if test.system != "" {
				payload = `{"model":"claude-opus-5","system":` + test.system + `}`
			}
			if err := validateClaudeCallerSystemBlocks(gjson.Get(payload, "system")); err != nil {
				t.Fatalf("validateClaudeCallerSystemBlocks() error = %v, want nil", err)
			}
		})
	}
}

// Anthropic rejects every non-text block in both system slots, verified live on
// 2026-08-03: the top-level field answers "system.<i>.type: Input should be
// 'text'" and a role=system message answers "role 'system' supports text,
// tool_addition, and tool_removal blocks only". Cloaking has no third slot, so
// the request has to fail here instead of losing the caller's instructions.
func TestValidateClaudeCallerSystemBlocksRejectsNonTextBlock(t *testing.T) {
	tests := []struct {
		name      string
		system    string
		wantIndex string
		wantType  string
	}{
		{
			name:      "image",
			system:    `[{"type":"text","text":"S1"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]`,
			wantIndex: "system.1.type",
			wantType:  `"image"`,
		},
		{
			name:      "responses marker",
			system:    `[{"type":"input_file"}]`,
			wantIndex: "system.0.type",
			wantType:  `"input_file"`,
		},
		{
			name:      "missing type",
			system:    `[{"text":"S1"}]`,
			wantIndex: "system.0.type",
			wantType:  `"unknown"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateClaudeCallerSystemBlocks(gjson.Parse(test.system))
			if err == nil {
				t.Fatal("validateClaudeCallerSystemBlocks() error = nil, want rejection")
			}
			var statusCoder interface{ StatusCode() int }
			if !errors.As(err, &statusCoder) || statusCoder.StatusCode() != http.StatusBadRequest {
				t.Fatalf("error status = %v, want 400", err)
			}
			var scoped interface{ IsRequestScoped() bool }
			if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
				t.Fatalf("error %v must be request scoped so no other credential is tried", err)
			}
			if got := err.Error(); !strings.Contains(got, test.wantIndex) || !strings.Contains(got, test.wantType) {
				t.Fatalf("error = %q, want it to name %s and %s", got, test.wantIndex, test.wantType)
			}
		})
	}
}

func TestApplyCloakingRejectsNonTextCallerSystemBlock(t *testing.T) {
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"S1"},{"type":"input_image"}],"messages":[{"role":"user","content":[{"type":"text","text":"U1"}]}]}`)

	out, cloaked, errCloaking := applyCloaking(context.Background(), cfg, auth, payload, "key-123", false, true)
	if errCloaking == nil {
		t.Fatal("applyCloaking() error = nil, want rejection")
	}
	if out != nil {
		t.Fatalf("applyCloaking() payload = %s, want nil", out)
	}
	if cloaked {
		t.Fatal("applyCloaking() cloaked = true, want false")
	}
}

// Strict mode never forwards caller system prompts, so an unusable block cannot
// lose information and must not fail the request.
func TestApplyCloakingStrictModeIgnoresNonTextCallerSystemBlock(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{StrictMode: true},
		}},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"input_image"}],"messages":[{"role":"user","content":[{"type":"text","text":"U1"}]}]}`)

	out, cloaked, errCloaking := applyCloaking(context.Background(), cfg, auth, payload, "key-123", false, true)
	if errCloaking != nil {
		t.Fatalf("applyCloaking() error = %v, want nil", errCloaking)
	}
	if !cloaked {
		t.Fatal("applyCloaking() cloaked = false, want true")
	}
	if got := len(gjson.GetBytes(out, "system").Array()); got != 2 {
		t.Fatalf("system blocks = %d, want the 2 Claude Code blocks", got)
	}
}

// A cloaked direct-Anthropic count_tokens request relocates caller system blocks
// into messages, so a non-text block has no destination there either and must be
// rejected before any upstream call.
func TestClaudeExecutor_CountTokensRejectsNonTextCallerSystemBlock(t *testing.T) {
	upstreamCalled := false
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalled = true
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-oat-count-system-block"}}
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"S1"},{"type":"input_image"}],"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`)

	_, errCount := NewClaudeExecutor(&config.Config{}).countTokensUpstream(ctx, auth,
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount == nil {
		t.Fatal("countTokensUpstream() error = nil, want rejection")
	}
	var statusCoder interface{ StatusCode() int }
	if !errors.As(errCount, &statusCoder) || statusCoder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("countTokensUpstream() error = %v, want 400", errCount)
	}
	if upstreamCalled {
		t.Fatal("countTokensUpstream() called upstream, want local rejection")
	}
}

// The native gate selects the 1h cache pool only for OAuth credentials and pushes
// extended-cache-ttl-2025-04-11 exactly when that selection produced a 1h body ttl.
// Body ttl and the beta must therefore always travel together.
func TestClaudeExecutor_CacheTTLIsPairedWithExtendedCacheTTLBeta(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantTTL  string
		wantBeta bool
	}{
		{
			name:     "oauth credential selects the 1h pool",
			apiKey:   "sk-ant-oat-cache-ttl-pairing",
			wantTTL:  "1h",
			wantBeta: true,
		},
		{
			name:     "api key credential keeps the default pool",
			apiKey:   "key-cache-ttl-pairing",
			wantTTL:  "",
			wantBeta: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var seenBody []byte
			var seenHeaders http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenBody, _ = io.ReadAll(r.Body)
				seenHeaders = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID: "cache-ttl-pairing",
				Attributes: map[string]string{
					"api_key":  test.apiKey,
					"base_url": server.URL,
				},
				Metadata: claudeOAuthTestMetadata(),
			}
			_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-opus-4-6",
				Payload: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}

			gotTTL := gjson.GetBytes(seenBody, "system.1.cache_control.ttl").String()
			if gotTTL != test.wantTTL {
				t.Fatalf("system[1].cache_control.ttl = %q, want %q: %s", gotTTL, test.wantTTL, seenBody)
			}
			if got := gjson.GetBytes(seenBody, "system.1.cache_control.type").String(); got != "ephemeral" {
				t.Fatalf("system[1].cache_control.type = %q, want ephemeral: %s", got, seenBody)
			}
			gotBeta := strings.Contains(seenHeaders.Get("Anthropic-Beta"), claudeExtendedCacheTTLBeta)
			if gotBeta != test.wantBeta {
				t.Fatalf("extended-cache-ttl declared = %v, want %v: %s", gotBeta, test.wantBeta, seenHeaders.Get("Anthropic-Beta"))
			}
			// The pairing invariant itself: a 1h body ttl without the beta, or the beta
			// without a 1h body ttl, is a combination native never produces.
			if (gotTTL == "1h") != gotBeta {
				t.Fatalf("body ttl %q and extended-cache-ttl beta %v disagree", gotTTL, gotBeta)
			}
		})
	}
}
