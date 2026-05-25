package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	xxHash64 "github.com/pierrec/xxHash/xxHash64"
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
	applyClaudeHeaders(req, auth, "key-baseline", false, nil, cfg)

	assertClaudeFingerprint(t, req.Header, "evil-client/9.9", "9.9.9", "v24.5.0", "Linux", "x64")
	if got := req.Header.Get("X-Stainless-Timeout"); got != "900" {
		t.Fatalf("X-Stainless-Timeout = %q, want %q", got, "900")
	}
}

func TestApplyClaudeHeaders_TracksHighestClaudeCLIFingerprint(t *testing.T) {
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
	applyClaudeHeaders(firstReq, auth, "key-upgrade", false, nil, cfg)
	assertClaudeFingerprint(t, firstReq.Header, "claude-cli/2.1.62 (external, cli)", "0.74.0", "v24.3.0", "MacOS", "arm64")

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"lobe-chat/1.0"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-upgrade", false, nil, cfg)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.62 (external, cli)", "0.74.0", "v24.3.0", "MacOS", "arm64")

	higherReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.63 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.75.0"},
		"X-Stainless-Runtime-Version": []string{"v24.4.0"},
		"X-Stainless-Os":              []string{"MacOS"},
		"X-Stainless-Arch":            []string{"arm64"},
	})
	applyClaudeHeaders(higherReq, auth, "key-upgrade", false, nil, cfg)
	assertClaudeFingerprint(t, higherReq.Header, "claude-cli/2.1.63 (external, cli)", "0.75.0", "v24.4.0", "MacOS", "arm64")

	lowerReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.61 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.73.0"},
		"X-Stainless-Runtime-Version": []string{"v24.2.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(lowerReq, auth, "key-upgrade", false, nil, cfg)
	assertClaudeFingerprint(t, lowerReq.Header, "claude-cli/2.1.63 (external, cli)", "0.75.0", "v24.4.0", "MacOS", "arm64")
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
	applyClaudeHeaders(olderClaudeReq, auth, "key-baseline-floor", false, nil, cfg)
	assertClaudeFingerprint(t, olderClaudeReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")

	newerClaudeReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.71 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.81.0"},
		"X-Stainless-Runtime-Version": []string{"v24.6.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(newerClaudeReq, auth, "key-baseline-floor", false, nil, cfg)
	assertClaudeFingerprint(t, newerClaudeReq.Header, "claude-cli/2.1.71 (external, cli)", "0.81.0", "v24.6.0", "MacOS", "arm64")
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
	applyClaudeHeaders(officialReq, auth, "key-baseline-reload", false, nil, oldCfg)
	assertClaudeFingerprint(t, officialReq.Header, "claude-cli/2.1.71 (external, cli)", "0.81.0", "v24.6.0", "MacOS", "arm64")

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-baseline-reload", false, nil, newCfg)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.77 (external, cli)", "0.87.0", "v24.8.0", "MacOS", "arm64")
}

func TestValidateMiniMaxToolResultAdjacencyRejectsIncompleteSequence(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type":"tool_use","id":"call_1","name":"read","input":{}},
					{"type":"tool_use","id":"call_2","name":"glob","input":{}},
					{"type":"tool_use","id":"call_3","name":"grep","input":{}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"text","text":"continue"}
				]
			}
		]
	}`)

	err := validateMiniMaxToolResultAdjacency(body)
	if err == nil {
		t.Fatal("expected invalid MiniMax tool sequence error")
	}
	statusProvider, ok := err.(interface{ StatusCode() int })
	if !ok || statusProvider.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected bad request status error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "tool_result must immediately follow tool_use") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMiniMaxToolResultAdjacencyAcceptsCompletedSequence(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type":"tool_use","id":"call_1","name":"read","input":{}},
					{"type":"tool_use","id":"call_2","name":"glob","input":{}},
					{"type":"tool_use","id":"call_3","name":"grep","input":{}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"tool_result","tool_use_id":"call_2","content":"ok"}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"tool_result","tool_use_id":"call_3","content":"ok"}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"text","text":"continue"}
				]
			}
		]
	}`)

	if err := validateMiniMaxToolResultAdjacency(body); err != nil {
		t.Fatalf("expected completed MiniMax tool sequence to pass, got %v", err)
	}
}

func TestValidateMiniMaxToolResultAdjacencyAcceptsOpenAIToolCycleAfterTranslation(t *testing.T) {
	t.Parallel()

	input := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role":"system","content":"Decide whether to continue."},
			{
				"role":"assistant",
				"content":"Analysis: no reply is needed.",
				"tool_calls":[{"id":"call_no_reply","type":"function","function":{"name":"no_reply","arguments":"{}"}}]
			},
			{"role":"tool","tool_call_id":"call_no_reply","content":"Wait for the next message."},
			{"role":"user","content":"New message arrived."}
		],
		"tools":[
			{"type":"function","function":{"name":"no_reply","parameters":{"type":"object","properties":{}}}}
		]
	}`)

	body := sdktranslator.TranslateRequest(sdktranslator.FromString("openai"), sdktranslator.FromString("claude"), "MiniMax-M2.7-highspeed", input, true)
	var err error
	body, err = repairClaudeToolUseHistory(body, "test")
	if err != nil {
		t.Fatalf("repairClaudeToolUseHistory() error = %v", err)
	}
	body = ensureCacheControl(body)
	body = enforceCacheControlLimit(body, 4)
	body = normalizeCacheControlTTL(body)

	if err := validateMiniMaxToolResultAdjacency(body); err != nil {
		t.Fatalf("expected translated OpenAI tool cycle to pass, got %v\nbody: %s", err, body)
	}
}

func TestClaudeCompatKindPrefersAuthMetadataForMiniMaxAliases(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"compat_kind": "MiniMax",
		},
	}
	if got := claudeCompatKind(auth, "https://proxy.example.com/anthropic"); got != "minimax" {
		t.Fatalf("claudeCompatKind() = %q, want minimax", got)
	}
}

func TestRepairMiniMaxToolResultAdjacencySplitsMixedUserContent(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type":"tool_use","id":"call_1","name":"read","input":{}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"text","text":"new user content"},
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			}
		]
	}`)

	out, repairs, err := repairMiniMaxToolResultAdjacency(body)
	if err != nil {
		t.Fatalf("repairMiniMaxToolResultAdjacency() error = %v", err)
	}
	if repairs != 1 {
		t.Fatalf("repairs = %d, want 1", repairs)
	}
	if err := validateMiniMaxToolResultAdjacency(out); err != nil {
		t.Fatalf("expected repaired sequence to pass, got %v\nbody: %s", err, out)
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 3 {
		t.Fatalf("messages length = %d, want 3: %s", len(msgs), gjson.GetBytes(out, "messages").Raw)
	}
	if got := msgs[1].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("message 1 content type = %q, want tool_result: %s", got, msgs[1].Raw)
	}
	if got := msgs[2].Get("content.0.type").String(); got != "text" {
		t.Fatalf("message 2 content type = %q, want text: %s", got, msgs[2].Raw)
	}
}

func TestRepairClaudeToolAdjacencyForDeepSeekCompat(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type":"tool_use","id":"browser_back","name":"browser_back","input":{}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"text","text":"next user instruction"},
					{"type":"tool_result","tool_use_id":"browser_back","content":"ok"}
				]
			}
		]
	}`)

	out, err := repairMiniMaxClaudeToolAdjacencyForCompat("deepseek", body)
	if err != nil {
		t.Fatalf("repairMiniMaxClaudeToolAdjacencyForCompat() error = %v", err)
	}
	if err := validateMiniMaxToolResultAdjacency(out); err != nil {
		t.Fatalf("expected repaired DeepSeek sequence to pass, got %v\nbody: %s", err, out)
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 3 {
		t.Fatalf("messages length = %d, want 3: %s", len(msgs), gjson.GetBytes(out, "messages").Raw)
	}
	if got := msgs[1].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("message 1 content type = %q, want tool_result: %s", got, msgs[1].Raw)
	}
	if got := msgs[2].Get("content.0.type").String(); got != "text" {
		t.Fatalf("message 2 content type = %q, want text: %s", got, msgs[2].Raw)
	}
}

func TestRepairMiniMaxToolResultAdjacencyMovesAssistantToolUseLast(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type":"text","text":"before"},
					{"type":"tool_use","id":"call_1","name":"read","input":{}},
					{"type":"text","text":"after"}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			}
		]
	}`)

	out, repairs, err := repairMiniMaxToolResultAdjacency(body)
	if err != nil {
		t.Fatalf("repairMiniMaxToolResultAdjacency() error = %v", err)
	}
	if repairs != 1 {
		t.Fatalf("repairs = %d, want 1", repairs)
	}
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if got := content[len(content)-1].Get("type").String(); got != "tool_use" {
		t.Fatalf("last assistant content type = %q, want tool_use: %s", got, gjson.GetBytes(out, "messages.0.content").Raw)
	}
	if err := validateMiniMaxToolResultAdjacency(out); err != nil {
		t.Fatalf("expected repaired sequence to pass, got %v\nbody: %s", err, out)
	}
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
	applyClaudeHeaders(thirdPartyReq, auth, "key-custom-baseline-learning", false, nil, cfg)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "my-gateway/1.0", "custom-pkg", "custom-runtime", "MacOS", "arm64")

	officialReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.77 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.87.0"},
		"X-Stainless-Runtime-Version": []string{"v24.8.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(officialReq, auth, "key-custom-baseline-learning", false, nil, cfg)
	assertClaudeFingerprint(t, officialReq.Header, "claude-cli/2.1.77 (external, cli)", "0.87.0", "v24.8.0", "MacOS", "arm64")

	postLearningThirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"curl/8.7.1"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(postLearningThirdPartyReq, auth, "key-custom-baseline-learning", false, nil, cfg)
	assertClaudeFingerprint(t, postLearningThirdPartyReq.Header, "claude-cli/2.1.77 (external, cli)", "0.87.0", "v24.8.0", "MacOS", "arm64")
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
		if candidate.UserAgent != "claude-cli/2.1.62 (external, cli)" {
			return
		}
		pauseOnce.Do(func() { close(lowPaused) })
		<-releaseLow
	}
	t.Cleanup(func() {
		helps.ClaudeDeviceProfileBeforeCandidateStore = nil
		releaseOnce.Do(func() { close(releaseLow) })
	})

	lowResultCh := make(chan helps.ClaudeDeviceProfile, 1)
	go func() {
		lowResultCh <- helps.ResolveClaudeDeviceProfile(auth, "key-racy-upgrade", http.Header{
			"User-Agent":                  []string{"claude-cli/2.1.62 (external, cli)"},
			"X-Stainless-Package-Version": []string{"0.74.0"},
			"X-Stainless-Runtime-Version": []string{"v24.3.0"},
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
		"User-Agent":                  []string{"claude-cli/2.1.63 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.75.0"},
		"X-Stainless-Runtime-Version": []string{"v24.4.0"},
		"X-Stainless-Os":              []string{"MacOS"},
		"X-Stainless-Arch":            []string{"arm64"},
	}, cfg)
	releaseOnce.Do(func() { close(releaseLow) })

	select {
	case lowResult := <-lowResultCh:
		if lowResult.UserAgent != "claude-cli/2.1.63 (external, cli)" {
			t.Fatalf("lowResult.UserAgent = %q, want %q", lowResult.UserAgent, "claude-cli/2.1.63 (external, cli)")
		}
		if lowResult.PackageVersion != "0.75.0" {
			t.Fatalf("lowResult.PackageVersion = %q, want %q", lowResult.PackageVersion, "0.75.0")
		}
		if lowResult.OS != "MacOS" || lowResult.Arch != "arm64" {
			t.Fatalf("lowResult platform = %s/%s, want %s/%s", lowResult.OS, lowResult.Arch, "MacOS", "arm64")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lower candidate result")
	}

	if highResult.UserAgent != "claude-cli/2.1.63 (external, cli)" {
		t.Fatalf("highResult.UserAgent = %q, want %q", highResult.UserAgent, "claude-cli/2.1.63 (external, cli)")
	}
	if highResult.OS != "MacOS" || highResult.Arch != "arm64" {
		t.Fatalf("highResult platform = %s/%s, want %s/%s", highResult.OS, highResult.Arch, "MacOS", "arm64")
	}

	cached := helps.ResolveClaudeDeviceProfile(auth, "key-racy-upgrade", http.Header{
		"User-Agent": []string{"curl/8.7.1"},
	}, cfg)
	if cached.UserAgent != "claude-cli/2.1.63 (external, cli)" {
		t.Fatalf("cached.UserAgent = %q, want %q", cached.UserAgent, "claude-cli/2.1.63 (external, cli)")
	}
	if cached.PackageVersion != "0.75.0" {
		t.Fatalf("cached.PackageVersion = %q, want %q", cached.PackageVersion, "0.75.0")
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
	applyClaudeHeaders(thirdPartyReq, auth, "key-third-party-then-official", false, nil, cfg)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.70 (external, cli)", "0.80.0", "v24.5.0", "MacOS", "arm64")

	officialReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.77 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.87.0"},
		"X-Stainless-Runtime-Version": []string{"v24.8.0"},
		"X-Stainless-Os":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(officialReq, auth, "key-third-party-then-official", false, nil, cfg)
	assertClaudeFingerprint(t, officialReq.Header, "claude-cli/2.1.77 (external, cli)", "0.87.0", "v24.8.0", "MacOS", "arm64")
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
	applyClaudeHeaders(firstReq, auth, "key-disable-stability", false, nil, cfg)
	assertClaudeFingerprint(t, firstReq.Header, "claude-cli/2.1.62 (external, cli)", "0.74.0", "v24.3.0", "Linux", "x64")

	thirdPartyReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"lobe-chat/1.0"},
		"X-Stainless-Package-Version": []string{"0.10.0"},
		"X-Stainless-Runtime-Version": []string{"v18.0.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(thirdPartyReq, auth, "key-disable-stability", false, nil, cfg)
	assertClaudeFingerprint(t, thirdPartyReq.Header, "claude-cli/2.1.60 (external, cli)", "0.10.0", "v18.0.0", "Windows", "x64")

	lowerReq := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.61 (external, cli)"},
		"X-Stainless-Package-Version": []string{"0.73.0"},
		"X-Stainless-Runtime-Version": []string{"v24.2.0"},
		"X-Stainless-Os":              []string{"Windows"},
		"X-Stainless-Arch":            []string{"x64"},
	})
	applyClaudeHeaders(lowerReq, auth, "key-disable-stability", false, nil, cfg)
	assertClaudeFingerprint(t, lowerReq.Header, "claude-cli/2.1.61 (external, cli)", "0.73.0", "v24.2.0", "Windows", "x64")
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
	applyClaudeHeaders(req, auth, "key-legacy-ua-override", false, nil, cfg)

	assertClaudeFingerprint(t, req.Header, "config-ua/1.0", "0.74.0", "v24.3.0", "Linux", "x64")
}

func TestApplyClaudeHeaders_LegacyModeFallsBackToRuntimeOSArchWhenMissing(t *testing.T) {
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
		ID: "auth-legacy-runtime-os-arch",
		Attributes: map[string]string{
			"api_key": "key-legacy-runtime-os-arch",
		},
	}

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"curl/8.7.1"},
	})
	applyClaudeHeaders(req, auth, "key-legacy-runtime-os-arch", false, nil, cfg)

	assertClaudeFingerprint(t, req.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", helps.MapStainlessOS(), helps.MapStainlessArch())
}

func TestApplyClaudeHeaders_UnsetStabilizationAlsoUsesLegacyRuntimeOSArchFallback(t *testing.T) {
	resetClaudeDeviceProfileCache()

	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:      "claude-cli/2.1.60 (external, cli)",
			PackageVersion: "0.70.0",
			RuntimeVersion: "v22.0.0",
			OS:             "MacOS",
			Arch:           "arm64",
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
	applyClaudeHeaders(req, auth, "key-unset-runtime-os-arch", false, nil, cfg)

	assertClaudeFingerprint(t, req.Header, "claude-cli/2.1.60 (external, cli)", "0.70.0", "v22.0.0", helps.MapStainlessOS(), helps.MapStainlessArch())
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

func TestApplyClaudeToolPrefix_SkipsBuiltinTools(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"my_custom_tool","input_schema":{"type":"object"}}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("tools.0.name = %q, want %q (built-in should not be prefixed)", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_my_custom_tool" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_my_custom_tool")
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

func TestGeminiToAntigravity_RequestTypeDetectsGoogleSearchAnywhere(t *testing.T) {
	t.Run("googleSearch at index 1 sets web_search", func(t *testing.T) {
		input := []byte(`{"model":"gemini-3-flash","request":{"tools":[{"functionDeclarations":[{"name":"f"}]},{"googleSearch":{}}]}}`)
		out := geminiToAntigravity("gemini-3-flash", input, "")
		if got := gjson.GetBytes(out, "requestType").String(); got != "web_search" {
			t.Fatalf("requestType = %q, want %q", got, "web_search")
		}
	})

	t.Run("no googleSearch keeps agent", func(t *testing.T) {
		input := []byte(`{"model":"gemini-3-flash","request":{"tools":[{"functionDeclarations":[{"name":"f"}]}]}}`)
		out := geminiToAntigravity("gemini-3-flash", input, "")
		if got := gjson.GetBytes(out, "requestType").String(); got != "agent" {
			t.Fatalf("requestType = %q, want %q", got, "agent")
		}
	})
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

func TestApplyClaudeToolPrefix_NestedToolReference(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"mcp__nia__manage_resource"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "proxy_mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want %q", got, "proxy_mcp__nia__manage_resource")
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

func TestSanitizeClaudeToolNamesForUpstream_RewritesAndRestores(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"name":"skill:pet_animals","input_schema":{"type":"object"}},
			{"type":"web_search_20250305","name":"web_search"}
		],
		"tool_choice":{"type":"tool","name":"skill:pet_animals"},
		"messages":[{"role":"assistant","content":[
			{"type":"tool_use","name":"skill:pet_animals","id":"toolu_1","input":{}},
			{"type":"tool_reference","tool_name":"skill:pet_animals"},
			{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"skill:pet_animals"}]}
		]}]
	}`)

	out, mapping := sanitizeClaudeToolNamesForUpstream(input)
	if mapping == nil {
		t.Fatal("expected invalid tool name to be sanitized")
	}
	for _, path := range []string{
		"tools.0.name",
		"tool_choice.name",
		"messages.0.content.0.name",
		"messages.0.content.1.tool_name",
		"messages.0.content.2.content.0.tool_name",
	} {
		if got := gjson.GetBytes(out, path).String(); got != "skill_pet_animals" {
			t.Fatalf("%s = %q, want %q", path, got, "skill_pet_animals")
		}
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "web_search" {
		t.Fatalf("built-in tool name = %q, want web_search", got)
	}

	response := restoreClaudeToolNamesFromResponse([]byte(`{"content":[
		{"type":"tool_use","name":"skill_pet_animals","id":"toolu_2","input":{}},
		{"type":"tool_reference","tool_name":"skill_pet_animals"},
		{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"tool_reference","tool_name":"skill_pet_animals"}]}
	]}`), mapping)
	for _, path := range []string{
		"content.0.name",
		"content.1.tool_name",
		"content.2.content.0.tool_name",
	} {
		if got := gjson.GetBytes(response, path).String(); got != "skill:pet_animals" {
			t.Fatalf("%s = %q, want %q", path, got, "skill:pet_animals")
		}
	}

	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"skill_pet_animals","id":"toolu_3"},"index":0}`)
	restoredLine := restoreClaudeToolNamesFromStreamLine(line, mapping)
	payload := bytes.TrimSpace(restoredLine)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.name").String(); got != "skill:pet_animals" {
		t.Fatalf("stream content_block.name = %q, want %q", got, "skill:pet_animals")
	}
}

func TestDowngradeClaudeToolSearchForCompat(t *testing.T) {
	payload := []byte(`{
		"tools":[
			{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},
			{"name":"mcp__files__read","description":"Read files","defer_loading":true,"input_schema":{"type":"object"}}
		],
		"messages":[
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_regex","input":{"query":"read"}},
				{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"mcp__files__read"}]}},
				{"type":"tool_reference","tool_name":"mcp__files__read"},
				{"type":"tool_use","id":"toolu_1","name":"mcp__files__read","input":{"path":"README.md"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"mcp__files__read"},{"type":"text","text":"ok"}]}]}
		]
	}`)

	out := downgradeClaudeToolSearchForCompat("https://api.kimi.com/coding", payload)

	if got := len(gjson.GetBytes(out, "tools").Array()); got != 1 {
		t.Fatalf("tools length = %d, want 1: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.defer_loading").Exists() {
		t.Fatalf("defer_loading should be removed: %s", string(out))
	}
	for _, partType := range []string{"server_tool_use", "tool_search_tool_result", "tool_reference"} {
		if gjson.GetBytes(out, `messages.0.content.#(type=="`+partType+`")`).Exists() {
			t.Fatalf("%s should be downgraded away: %s", partType, string(out))
		}
	}
	if got := gjson.GetBytes(out, `messages.0.content.#(type=="tool_use").name`).String(); got != "mcp__files__read" {
		t.Fatalf("tool_use name = %q, want mcp__files__read: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.content.0.type").String(); got != "text" {
		t.Fatalf("nested tool_reference should become text, got %q: %s", got, string(out))
	}
}

func TestDowngradeClaudeUnsupportedServerToolsForMiniMax(t *testing.T) {
	payload := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":8},
			{"name":"read_file","description":"Read files","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"search"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},
				{"type":"mcp_tool_result","content":[{"type":"text","text":"mcp ok"}]}
			]},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"current date"}},
				{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}
			]}
		]
	}`)

	out := downgradeClaudeToolSearchForCompat("https://api.minimax.io/anthropic", payload)

	if got := len(gjson.GetBytes(out, "tools").Array()); got != 1 {
		t.Fatalf("tools length = %d, want 1: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("remaining tool = %q, want read_file: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice for removed server tool should be removed: %s", string(out))
	}
	userContent := gjson.GetBytes(out, "messages.0.content").Array()
	if hasClaudePartType(userContent, "image_url") || hasClaudePartType(userContent, "mcp_tool_result") {
		t.Fatalf("MiniMax unsupported content block remained: %s", string(out))
	}
	if !hasClaudeText(userContent, "search") || !hasClaudeText(userContent, "mcp ok") {
		t.Fatalf("MiniMax compatible text should be preserved: %s", string(out))
	}
	for _, partType := range []string{"server_tool_use", "web_search_tool_result"} {
		if gjson.GetBytes(out, `messages.1.content.#(type=="`+partType+`")`).Exists() {
			t.Fatalf("%s should be downgraded away: %s", partType, string(out))
		}
	}
	if err := validateClaudeUpstreamPayload("https://api.minimax.io/anthropic", out); err != nil {
		t.Fatalf("downgraded MiniMax payload should pass validation: %v", err)
	}
}

func TestDowngradeClaudeUnsupportedBlocksForXiaomi(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":8},
			{"name":"read_file","description":"Read files","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}
		],
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"search"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},
				{"type":"mcp_tool_result","content":[{"type":"text","text":"mcp ok"}]}
			]},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"current date"}},
				{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"text","text":"code ok"}]},
				{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"README.md"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"text","text":"file ok"},
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BBBB"}}
				]}
			]}
		]
	}`)

	out := downgradeClaudeToolSearchForCompat("https://token-plan-cn.xiaomimimo.com/anthropic", payload)

	if got := len(gjson.GetBytes(out, "tools").Array()); got != 1 {
		t.Fatalf("tools length = %d, want 1: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("remaining tool = %q, want read_file: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice for removed server tool should be removed: %s", string(out))
	}

	userContent := gjson.GetBytes(out, "messages.0.content").Array()
	if hasClaudePartType(userContent, "image_url") || hasClaudePartType(userContent, "mcp_tool_result") {
		t.Fatalf("Xiaomi unsupported content block remained: %s", string(out))
	}
	if !hasClaudeText(userContent, "search") || !hasClaudeText(userContent, "mcp ok") {
		t.Fatalf("Xiaomi compatible text should be preserved: %s", string(out))
	}

	assistantContent := gjson.GetBytes(out, "messages.1.content").Array()
	for _, partType := range []string{"server_tool_use", "code_execution_tool_result"} {
		if hasClaudePartType(assistantContent, partType) {
			t.Fatalf("%s should be downgraded away: %s", partType, string(out))
		}
	}
	if !hasClaudePartType(assistantContent, "tool_use") {
		t.Fatalf("custom tool_use should be preserved: %s", string(out))
	}

	toolResultContent := gjson.GetBytes(out, "messages.2.content.0.content").Array()
	if hasClaudePartType(toolResultContent, "image") {
		t.Fatalf("unsupported image inside tool_result should be removed: %s", string(out))
	}
	if !hasClaudeText(toolResultContent, "file ok") {
		t.Fatalf("tool_result text should be preserved: %s", string(out))
	}
	if err := validateClaudeUpstreamPayload("https://token-plan-cn.xiaomimimo.com/anthropic", out); err != nil {
		t.Fatalf("downgraded Xiaomi payload should pass validation: %v", err)
	}
}

func TestApplyMiniMaxStreamingThinkingDefaultForCompat(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"model":"MiniMax-M2.7","messages":[{"role":"user","content":"hi"}]}`)
	out := applyMiniMaxStreamingThinkingDefaultForCompat("minimax", payload, true)
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled: %s", got, string(out))
	}

	explicit := []byte(`{"model":"MiniMax-M2.7","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`)
	out = applyMiniMaxStreamingThinkingDefaultForCompat("minimax", explicit, true)
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("explicit thinking.type = %q, want enabled: %s", got, string(out))
	}

	forcedToolChoice := []byte(`{"tool_choice":{"type":"tool","name":"read"},"messages":[{"role":"user","content":"hi"}]}`)
	out = applyMiniMaxStreamingThinkingDefaultForCompat("minimax", forcedToolChoice, true)
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("forced tool_choice should not receive implicit thinking: %s", string(out))
	}

	nonStream := applyMiniMaxStreamingThinkingDefaultForCompat("minimax", payload, false)
	if gjson.GetBytes(nonStream, "thinking").Exists() {
		t.Fatalf("non-stream MiniMax request should not be changed: %s", string(nonStream))
	}
}

func TestSanitizeClaudeHTTPRequestToolNames_DisablesImplicitMiniMaxStreamingThinking(t *testing.T) {
	t.Parallel()

	payload := `{"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.minimax.io/anthropic/v1/messages?beta=true", strings.NewReader(payload))

	if _, err := sanitizeClaudeHTTPRequestToolNames(req); err != nil {
		t.Fatalf("sanitizeClaudeHTTPRequestToolNames() error = %v", err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := gjson.GetBytes(body, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled: %s", got, string(body))
	}
}

func TestSanitizeClaudeHTTPRequestToolNames_DowngradesXiaomiAnthropicBody(t *testing.T) {
	t.Parallel()

	payload := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"mcp_tool_result","content":[{"type":"text","text":"mcp ok"}]}]}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.xiaomimimo.com/anthropic/v1/messages?beta=true", strings.NewReader(payload))

	if _, err := sanitizeClaudeHTTPRequestToolNames(req); err != nil {
		t.Fatalf("sanitizeClaudeHTTPRequestToolNames() error = %v", err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	content := gjson.GetBytes(body, "messages.0.content").Array()
	if hasClaudePartType(content, "image_url") || hasClaudePartType(content, "mcp_tool_result") {
		t.Fatalf("Xiaomi direct HttpRequest body should remove unsupported blocks: %s", string(body))
	}
	if !hasClaudeText(content, "hi") || !hasClaudeText(content, "mcp ok") {
		t.Fatalf("text should be preserved: %s", string(body))
	}
}

func TestDowngradeClaudeToolSearchForCompatSkipsOfficialAnthropic(t *testing.T) {
	payload := []byte(`{"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}]}`)
	out := downgradeClaudeToolSearchForCompat("https://api.anthropic.com", payload)
	if !bytes.Equal(out, payload) {
		t.Fatalf("official Anthropic payload should not be changed: %s", string(out))
	}
}

func TestFilterClaudeBetasForCompatDropsToolSearch(t *testing.T) {
	out := filterClaudeBetasForCompat("claude-code-20250219, tool-search-2025-11-19,tool_search_tool_regex_20251119,oauth-2025-04-20,provider-beta-2099-01-01")
	for _, dropped := range []string{"claude-code", "tool-search", "tool_search", "oauth"} {
		if strings.Contains(out, dropped) {
			t.Fatalf("%s beta should be removed for compat endpoints, got %q", dropped, out)
		}
	}
	if !strings.Contains(out, "provider-beta-2099-01-01") {
		t.Fatalf("unknown provider beta should be preserved, got %q", out)
	}
}

func TestSanitizeClaudeToolNamesForUpstream_AvoidsCollisions(t *testing.T) {
	input := []byte(`{"tools":[{"name":"skill_pet_animals"},{"name":"skill:pet_animals"}]}`)

	out, mapping := sanitizeClaudeToolNamesForUpstream(input)
	if mapping == nil {
		t.Fatal("expected invalid tool name to be sanitized")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "skill_pet_animals" {
		t.Fatalf("valid tool name changed to %q", got)
	}
	sanitized := gjson.GetBytes(out, "tools.1.name").String()
	if sanitized == "skill_pet_animals" {
		t.Fatal("sanitized invalid tool name collided with an existing valid tool")
	}
	if !strings.HasPrefix(sanitized, "skill_pet_animals_") {
		t.Fatalf("sanitized collision name = %q, want skill_pet_animals_<hash>", sanitized)
	}
	if !isValidClaudeToolName(sanitized) {
		t.Fatalf("sanitized collision name %q is not Anthropic-compatible", sanitized)
	}

	response := restoreClaudeToolNamesFromResponse([]byte(fmt.Sprintf(`{"content":[{"type":"tool_use","name":%q,"id":"toolu_1","input":{}}]}`, sanitized)), mapping)
	if got := gjson.GetBytes(response, "content.0.name").String(); got != "skill:pet_animals" {
		t.Fatalf("restored tool name = %q, want %q", got, "skill:pet_animals")
	}
}

func TestClaudeExecutor_Execute_SanitizesInvalidToolNamesForAPIKeyUpstream(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		name := gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"tool_use","name":%q,"id":"toolu_1","input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}`, name)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"tools":[{"name":"skill:pet_animals","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"skill:pet_animals"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); got != "skill_pet_animals" {
		t.Fatalf("upstream tools.0.name = %q, want %q", got, "skill_pet_animals")
	}
	if got := gjson.GetBytes(seenBody, "tool_choice.name").String(); got != "skill_pet_animals" {
		t.Fatalf("upstream tool_choice.name = %q, want %q", got, "skill_pet_animals")
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.name").String(); got != "skill:pet_animals" {
		t.Fatalf("downstream content.0.name = %q, want original name", got)
	}
}

func TestClaudeExecutor_Execute_DropsUnansweredToolUseHistory(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"start"}]},
			{"role":"assistant","content":[
				{"type":"text","text":"will use tools"},
				{"type":"tool_use","id":"call_01_vp6YvKjZbis7ayYMkHUTn76a","name":"read_file","input":{}},
				{"type":"tool_use","id":"call_02_HV14reYsv1LuKOdMKLX3dKMM","name":"glob","input":{}}
			]},
			{"role":"user","content":[{"type":"text","text":"continue without tool results"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if strings.Contains(string(seenBody), "call_01_vp6YvKjZbis7ayYMkHUTn76a") {
		t.Fatalf("upstream body still has unanswered call_01 tool_use: %s", seenBody)
	}
	if strings.Contains(string(seenBody), "call_02_HV14reYsv1LuKOdMKLX3dKMM") {
		t.Fatalf("upstream body still has unanswered call_02 tool_use: %s", seenBody)
	}
	if !strings.Contains(string(seenBody), "will use tools") {
		t.Fatalf("upstream body should keep original assistant text: %s", seenBody)
	}
	if !strings.Contains(string(seenBody), "continue without tool results") {
		t.Fatalf("upstream body should keep original user text: %s", seenBody)
	}
}

func TestClaudeExecutor_HttpRequest_SanitizesDirectMessagesToolNames(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"skill_pet_animals","id":"toolu_1"},"index":0}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key": "key-123",
	}}
	payload := []byte(`{
		"tools":[{"name":"skill:pet_animals","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"skill:pet_animals"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"stream":true
	}`)
	req, errReq := http.NewRequest(http.MethodPost, server.URL+"/v1/messages?beta=true", bytes.NewReader(payload))
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := executor.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest error: %v", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("response body close error: %v", errClose)
		}
	}()
	data, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("read response body: %v", errRead)
	}

	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); got != "skill_pet_animals" {
		t.Fatalf("upstream tools.0.name = %q, want %q", got, "skill_pet_animals")
	}
	if got := gjson.GetBytes(seenBody, "tool_choice.name").String(); got != "skill_pet_animals" {
		t.Fatalf("upstream tool_choice.name = %q, want %q", got, "skill_pet_animals")
	}
	if !bytes.Contains(data, []byte(`"name":"skill:pet_animals"`)) {
		t.Fatalf("downstream stream did not restore tool name: %s", string(data))
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

func TestClaudeExecutor_CountTokens_AppliesCacheControlGuards(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}}
		],
		"system": [
			{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"type":"text","text":"s2","cache_control":{"type":"ephemeral","ttl":"1h"}}
		],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]},
			{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral","ttl":"1h"}}]}
		]
	}`)

	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-haiku-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if len(seenBody) == 0 {
		t.Fatal("expected count_tokens request body to be captured")
	}
	if got := countCacheControls(seenBody); got > 4 {
		t.Fatalf("count_tokens body has %d cache_control blocks, want <= 4", got)
	}
	if hasTTLOrderingViolation(seenBody) {
		t.Fatalf("count_tokens body still has ttl ordering violations: %s", string(seenBody))
	}
}

func hasTTLOrderingViolation(payload []byte) bool {
	seen5m := false
	violates := false

	checkCC := func(cc gjson.Result) {
		if !cc.Exists() || violates {
			return
		}
		ttl := cc.Get("ttl").String()
		if ttl != "1h" {
			seen5m = true
			return
		}
		if seen5m {
			violates = true
		}
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			checkCC(tool.Get("cache_control"))
			return !violates
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			checkCC(item.Get("cache_control"))
			return !violates
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					checkCC(item.Get("cache_control"))
					return !violates
				})
			}
			return !violates
		})
	}

	return violates
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

func TestClaudeExecutor_CountTokens_InvalidGzipErrorBodyReturnsDecodeMessage(t *testing.T) {
	testClaudeExecutorInvalidCompressedErrorBody(t, func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error {
		_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
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

func TestClaudeExecutor_ExecuteStream_PatchesQianfanStartUsageForProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"as_123","type":"message","role":"assistant","model":"qianfan-code-latest","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":42,"output_tokens":1}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":     "key-123",
		"base_url":    server.URL,
		"compat_kind": "qianfan",
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qianfan-code-latest",
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
	startPayload := findClaudeSSEPayload(t, combined.String(), "message_start")
	if got := gjson.GetBytes(startPayload, "message.usage.input_tokens").Int(); got <= 0 {
		t.Fatalf("message_start input_tokens = %d, want patched positive value; payload=%s", got, string(startPayload))
	}
	deltaPayload := findClaudeSSEPayload(t, combined.String(), "message_delta")
	if got := gjson.GetBytes(deltaPayload, "usage.input_tokens").Int(); got != 42 {
		t.Fatalf("message_delta input_tokens = %d, want upstream value 42; payload=%s", got, string(deltaPayload))
	}
}

func TestPatchClaudeMessageStartUsageForProgressKeepsExistingUsage(t *testing.T) {
	line := []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":99,"output_tokens":0}}}`)
	out := patchClaudeMessageStartUsageForProgress(line, 1234)
	payload, ok := sseDataPayload(out)
	if !ok {
		t.Fatal("expected patched line to remain an SSE data line")
	}
	if got := gjson.GetBytes(payload, "message.usage.input_tokens").Int(); got != 99 {
		t.Fatalf("input_tokens = %d, want existing value 99", got)
	}
}

func findClaudeSSEPayload(t *testing.T, stream, eventType string) []byte {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		payload, ok := sseDataPayload([]byte(line))
		if !ok || len(payload) == 0 || !gjson.ValidBytes(payload) {
			continue
		}
		if gjson.GetBytes(payload, "type").String() == eventType {
			return payload
		}
	}
	t.Fatalf("stream did not contain event type %q: %s", eventType, stream)
	return nil
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

func expectedClaudeCodeStaticPrompt() string {
	return strings.Join([]string{
		helps.ClaudeCodeIntro,
		helps.ClaudeCodeSystem,
		helps.ClaudeCodeDoingTasks,
		helps.ClaudeCodeToneAndStyle,
		helps.ClaudeCodeOutputEfficiency,
	}, "\n\n")
}

func expectedForwardedSystemReminder(text string) string {
	return fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context from the system:
%s

IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>
`, text)
}

// Test case 1: String system prompt is preserved by forwarding it to the first user message
func TestCheckSystemInstructionsWithMode_StringSystemPreserved(t *testing.T) {
	payload := []byte(`{"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	system := gjson.GetBytes(out, "system")
	if !system.IsArray() {
		t.Fatalf("system should be an array, got %s", system.Type)
	}

	blocks := system.Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}

	if !strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") {
		t.Fatalf("blocks[0] should be billing header, got %q", blocks[0].Get("text").String())
	}
	if blocks[1].Get("text").String() != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Fatalf("blocks[1] should be agent block, got %q", blocks[1].Get("text").String())
	}
	if blocks[2].Get("text").String() != expectedClaudeCodeStaticPrompt() {
		t.Fatalf("blocks[2] should be static Claude Code prompt, got %q", blocks[2].Get("text").String())
	}
	if blocks[2].Get("cache_control").Exists() {
		t.Fatalf("blocks[2] should not have cache_control, got %s", blocks[2].Get("cache_control").Raw)
	}

	if got := gjson.GetBytes(out, "messages.0.content").String(); got != expectedForwardedSystemReminder("You are a helpful assistant.")+"hi" {
		t.Fatalf("messages[0].content should include forwarded system prompt, got %q", got)
	}
}

// Test case 2: Strict mode keeps only the injected Claude Code system blocks
func TestCheckSystemInstructionsWithMode_StringSystemStrict(t *testing.T) {
	payload := []byte(`{"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, true)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("strict mode should produce 3 injected blocks, got %d", len(blocks))
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "hi" {
		t.Fatalf("strict mode should not forward system prompt into messages, got %q", got)
	}
}

// Test case 3: Empty string system prompt does not alter the first user message
func TestCheckSystemInstructionsWithMode_EmptyStringSystemIgnored(t *testing.T) {
	payload := []byte(`{"system":"","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("empty string system should still produce 3 injected blocks, got %d", len(blocks))
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "hi" {
		t.Fatalf("empty string system should not alter messages, got %q", got)
	}
}

// Test case 4: Array system prompt is forwarded to the first user message
func TestCheckSystemInstructionsWithMode_ArraySystemStillWorks(t *testing.T) {
	payload := []byte(`{"system":[{"type":"text","text":"Be concise."}],"messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}
	if blocks[2].Get("text").String() != expectedClaudeCodeStaticPrompt() {
		t.Fatalf("blocks[2] should be static Claude Code prompt, got %q", blocks[2].Get("text").String())
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != expectedForwardedSystemReminder("Be concise.")+"hi" {
		t.Fatalf("messages[0].content should include forwarded array system prompt, got %q", got)
	}
}

// Test case 5: Special characters in string system prompt survive forwarding
func TestCheckSystemInstructionsWithMode_StringWithSpecialChars(t *testing.T) {
	payload := []byte(`{"system":"Use <xml> tags & \"quotes\" in output.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != expectedForwardedSystemReminder(`Use <xml> tags & "quotes" in output.`)+"hi" {
		t.Fatalf("forwarded system prompt text mangled, got %q", got)
	}
}

func TestClaudeExecutor_ExperimentalCCHSigningDisabledByDefaultKeepsLegacyHeader(t *testing.T) {
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
	if strings.Contains(billingHeader, "cch=00000;") {
		t.Fatalf("legacy mode should not forward cch placeholder, got %q", billingHeader)
	}
}

func TestClaudeExecutor_ExperimentalCCHSigningOptInSignsFinalBody(t *testing.T) {
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
	if got := gjson.GetBytes(seenBody, "messages.0.content.0.text").String(); got != messageText {
		t.Fatalf("message text = %q, want %q", got, messageText)
	}

	billingPattern := regexp.MustCompile(`(x-anthropic-billing-header:[^"]*?\bcch=)([0-9a-f]{5})(;)`)
	match := billingPattern.FindSubmatch(seenBody)
	if match == nil {
		t.Fatalf("expected signed billing header in body: %s", string(seenBody))
	}
	actualCCH := string(match[2])
	unsignedBody := billingPattern.ReplaceAll(seenBody, []byte(`${1}00000${3}`))
	wantCCH := fmt.Sprintf("%05x", xxHash64.Checksum(unsignedBody, 0x6E52736AC806831E)&0xFFFFF)
	if actualCCH != wantCCH {
		t.Fatalf("cch = %q, want %q\nbody: %s", actualCCH, wantCCH, string(seenBody))
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

	out := applyCloaking(context.Background(), cfg, auth, payload, "claude-3-5-sonnet-20241022", "key-123")

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected strict mode to keep the 3 injected Claude Code system blocks, got %d", len(blocks))
	}
	if got := gjson.GetBytes(out, "messages.0.content.#").Int(); got != 1 {
		t.Fatalf("strict mode should not prepend a forwarded system reminder block, got %d content blocks", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); !strings.Contains(got, "\u200B") {
		t.Fatalf("expected configured sensitive word obfuscation to apply, got %q", got)
	}
}

func TestNormalizeClaudeTemperatureForThinking_AdaptiveCoercesToOne(t *testing.T) {
	payload := []byte(`{"temperature":0,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	out := normalizeClaudeTemperatureForThinking(payload)

	if got := gjson.GetBytes(out, "temperature").Float(); got != 1 {
		t.Fatalf("temperature = %v, want 1", got)
	}
}

func TestNormalizeClaudeTemperatureForThinking_EnabledCoercesToOne(t *testing.T) {
	payload := []byte(`{"temperature":0.2,"thinking":{"type":"enabled","budget_tokens":2048}}`)
	out := normalizeClaudeTemperatureForThinking(payload)

	if got := gjson.GetBytes(out, "temperature").Float(); got != 1 {
		t.Fatalf("temperature = %v, want 1", got)
	}
}

func TestNormalizeClaudeTemperatureForThinking_NoThinkingLeavesTemperatureAlone(t *testing.T) {
	payload := []byte(`{"temperature":0,"messages":[{"role":"user","content":"hi"}]}`)
	out := normalizeClaudeTemperatureForThinking(payload)

	if got := gjson.GetBytes(out, "temperature").Float(); got != 0 {
		t.Fatalf("temperature = %v, want 0", got)
	}
}

func TestNormalizeClaudeTemperatureForThinking_AfterForcedToolChoiceKeepsOriginalTemperature(t *testing.T) {
	payload := []byte(`{"temperature":0,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"tool_choice":{"type":"any"}}`)
	out := disableThinkingIfToolChoiceForced(payload)
	out = normalizeClaudeTemperatureForThinking(out)

	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be removed when tool_choice forces tool use")
	}
	if got := gjson.GetBytes(out, "temperature").Float(); got != 0 {
		t.Fatalf("temperature = %v, want 0", got)
	}
}

func TestRemapOAuthToolNames_TitleCase_NoReverseNeeded(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash","description":"Run shell commands","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	out, reverseMap := remapOAuthToolNames(body)
	if len(reverseMap) != 0 {
		t.Fatalf("reverseMap = %v, want empty", reverseMap)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Bash" {
		t.Fatalf("tools.0.name = %q, want %q", got, "Bash")
	}

	resp := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"cmd":"ls"}}]}`)
	reversed := reverseRemapOAuthToolNames(resp, reverseMap)
	if got := gjson.GetBytes(reversed, "content.0.name").String(); got != "Bash" {
		t.Fatalf("content.0.name = %q, want %q", got, "Bash")
	}
}

func TestRemapOAuthToolNames_Lowercase_ReverseApplied(t *testing.T) {
	body := []byte(`{"tools":[{"name":"bash","description":"Run shell commands","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	out, reverseMap := remapOAuthToolNames(body)
	if reverseMap["Bash"] != "bash" {
		t.Fatalf("reverseMap = %v, want entry Bash->bash", reverseMap)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Bash" {
		t.Fatalf("tools.0.name = %q, want %q", got, "Bash")
	}

	resp := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"cmd":"ls"}}]}`)
	reversed := reverseRemapOAuthToolNames(resp, reverseMap)
	if got := gjson.GetBytes(reversed, "content.0.name").String(); got != "bash" {
		t.Fatalf("content.0.name = %q, want %q", got, "bash")
	}
}

func TestValidateClaudeUpstreamPayload_MiniMaxRejectsStructuredOutputFormat(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"MiniMax-M2.5",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"output_config":{
			"format":{
				"type":"json_schema",
				"json_schema":{"name":"result","schema":{"type":"object"}}
			}
		}
	}`)

	err := validateClaudeUpstreamPayload("https://api.minimaxi.io/anthropic", payload)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if se.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", se.StatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "request_feature_unsupported:") {
		t.Fatalf("error = %q, want request_feature_unsupported prefix", err.Error())
	}
}

func TestDowngradeClaudeStructuredOutputForCompat_NonAnthropicRemovesFormatAndInjectsSchema(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"MiniMax-M2.5",
		"system":"Be concise.",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"output_config":{
			"format":{
				"type":"json_schema",
				"json_schema":{"name":"result","schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}
			}
		}
	}`)

	out := downgradeClaudeStructuredOutputForCompat("https://api.minimax.io/anthropic", payload)
	if gjson.GetBytes(out, "output_config.format").Exists() {
		t.Fatalf("output_config.format should be removed, got %s", string(out))
	}
	system := gjson.GetBytes(out, "system").String()
	if !strings.Contains(system, "Structured output compatibility mode") {
		t.Fatalf("system prompt missing compatibility instruction: %q", system)
	}
	if !strings.Contains(system, `"json_schema"`) || !strings.Contains(system, `"ok"`) {
		t.Fatalf("system prompt missing schema: %q", system)
	}
	if err := validateClaudeUpstreamPayload("https://api.minimax.io/anthropic", out); err != nil {
		t.Fatalf("downgraded payload should pass MiniMax validation, got %v", err)
	}
}

func TestDowngradeClaudeStructuredOutputForCompat_OfficialAnthropicPreservesFormat(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"claude-sonnet-4-6",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"output_config":{
			"format":{"type":"json_schema","json_schema":{"name":"result","schema":{"type":"object"}}}
		}
	}`)

	out := downgradeClaudeStructuredOutputForCompat("https://api.anthropic.com", payload)
	if !gjson.GetBytes(out, "output_config.format").Exists() {
		t.Fatalf("official Anthropic payload should preserve output_config.format, got %s", string(out))
	}
}

func TestDowngradeClaudeStructuredOutputForCompat_AppendsArraySystemBlock(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"system":[{"type":"text","text":"Existing system."}],
		"output_config":{"format":{"type":"json_object"}}
	}`)

	out := downgradeClaudeStructuredOutputForCompat("https://api.moonshot.cn/anthropic", payload)
	system := gjson.GetBytes(out, "system")
	if got := len(system.Array()); got != 2 {
		t.Fatalf("system block count = %d, want 2; payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "system.1.type").String(); got != "text" {
		t.Fatalf("system.1.type = %q, want text", got)
	}
	if got := gjson.GetBytes(out, "system.1.text").String(); !strings.Contains(got, "json_object") {
		t.Fatalf("system.1.text missing format: %q", got)
	}
}

func TestDowngradeClaudeToolSearchForCompatKind_DeepSeekRemovesUnsupportedBlocks(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"name":"read_file","description":"Read file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}
		],
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"hi"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,BBBB"}},
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"CCCC"}},
				{"type":"search_result","content":[{"type":"text","text":"search ok"}]},
				{"type":"mcp_tool_result","content":[{"type":"text","text":"mcp ok"}]}
			]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"plan"},
				{"type":"redacted_thinking","data":"secret"},
				{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"README.md"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"text","text":"tool ok"},
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"DDDD"}}
				]}
			]}
		]
	}`)

	out := downgradeClaudeToolSearchForCompatKind("deepseek", "https://api.deepseek.com/anthropic", payload)

	if got := len(gjson.GetBytes(out, "tools").Array()); got != 1 {
		t.Fatalf("tools count = %d, want 1: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("kept tool name = %q, want read_file: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice for removed server tool should be removed: %s", string(out))
	}

	userContent := gjson.GetBytes(out, "messages.0.content").Array()
	if hasClaudePartType(userContent, "image") || hasClaudePartType(userContent, "image_url") || hasClaudePartType(userContent, "document") || hasClaudePartType(userContent, "search_result") || hasClaudePartType(userContent, "mcp_tool_result") {
		t.Fatalf("DeepSeek unsupported user content block remained: %s", string(out))
	}
	for _, wantText := range []string{"hi", "search ok", "mcp ok"} {
		if !hasClaudeText(userContent, wantText) {
			t.Fatalf("expected text %q in downgraded content: %s", wantText, string(out))
		}
	}

	assistantContent := gjson.GetBytes(out, "messages.1.content").Array()
	if hasClaudePartType(assistantContent, "redacted_thinking") {
		t.Fatalf("redacted_thinking should be removed for DeepSeek: %s", string(out))
	}
	if !hasClaudePartType(assistantContent, "thinking") || !hasClaudePartType(assistantContent, "tool_use") {
		t.Fatalf("supported thinking/tool_use blocks should be preserved: %s", string(out))
	}

	toolResultContent := gjson.GetBytes(out, "messages.2.content.0.content").Array()
	if hasClaudePartType(toolResultContent, "image") {
		t.Fatalf("unsupported image inside tool_result should be removed: %s", string(out))
	}
	if !hasClaudeText(toolResultContent, "tool ok") {
		t.Fatalf("tool_result text should be preserved: %s", string(out))
	}
}

func TestDowngradeClaudeToolSearchForCompatKind_DeepSeekSanitizesToolSchema(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"deepseek-v4-flash",
		"tools":[{
			"name":"browser_back",
			"description":"Navigate back in the browser",
			"input_schema":{
				"type":"object",
				"properties":{
					"sessions":{"type":"array","items":null}
				},
				"required":null
			}
		}],
		"messages":[{"role":"user","content":[{"type":"text","text":"go back"}]}]
	}`)

	out := downgradeClaudeToolSearchForCompatKind("deepseek", "https://api.deepseek.com/anthropic", payload)

	if gjson.GetBytes(out, "tools.0.input_schema.required").Exists() {
		t.Fatalf("required=null should be removed for DeepSeek: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.properties.sessions.items.type").String(); got == "" {
		t.Fatalf("array items should be filled for DeepSeek: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("object schema should include additionalProperties=false for DeepSeek: %s", string(out))
	}
}

func TestDeepSeekClaudeCompatNormalizesThinkingBudgetByModelName(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"thinking":{"type":"enabled","budget_tokens":50},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	out := scrubDeepSeekThinkingBudgetForCompat(payload, "deepseek-v4-pro", "https://tokenrai.com", "")

	if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != 100 {
		t.Fatalf("thinking.budget_tokens = %d, want 100: %s", got, string(out))
	}
}

func TestSanitizeClaudeHTTPRequestToolNames_DowngradesDeepSeekAnthropicBody(t *testing.T) {
	t.Parallel()

	payload := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.deepseek.com/anthropic/v1/messages?beta=true", strings.NewReader(payload))

	if _, err := sanitizeClaudeHTTPRequestToolNames(req); err != nil {
		t.Fatalf("sanitizeClaudeHTTPRequestToolNames() error = %v", err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if hasClaudePartType(gjson.GetBytes(body, "messages.0.content").Array(), "image") {
		t.Fatalf("DeepSeek direct HttpRequest body should remove image blocks: %s", string(body))
	}
	if !hasClaudeText(gjson.GetBytes(body, "messages.0.content").Array(), "hi") {
		t.Fatalf("text should be preserved: %s", string(body))
	}
}

func TestSanitizeClaudeHTTPRequestToolNames_NormalizesDeepSeekThinkingBudget(t *testing.T) {
	t.Parallel()

	payload := `{"model":"deepseek-v4-pro","thinking":{"type":"enabled","budget_tokens":"50"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.deepseek.com/anthropic/v1/messages?beta=true", strings.NewReader(payload))

	if _, err := sanitizeClaudeHTTPRequestToolNames(req); err != nil {
		t.Fatalf("sanitizeClaudeHTTPRequestToolNames() error = %v", err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := gjson.GetBytes(body, "thinking.budget_tokens").Int(); got != 100 {
		t.Fatalf("thinking.budget_tokens = %d, want 100: %s", got, string(body))
	}
}

func hasClaudePartType(parts []gjson.Result, partType string) bool {
	for _, part := range parts {
		if part.Get("type").String() == partType {
			return true
		}
	}
	return false
}

func hasClaudeText(parts []gjson.Result, text string) bool {
	for _, part := range parts {
		if part.Get("type").String() == "text" && part.Get("text").String() == text {
			return true
		}
	}
	return false
}

func TestValidateClaudeUpstreamPayload_NonMiniMaxAllowsStructuredOutputFormat(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"claude-sonnet-4-6",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"output_config":{
			"format":{
				"type":"json_schema",
				"json_schema":{"name":"result","schema":{"type":"object"}}
			}
		}
	}`)

	if err := validateClaudeUpstreamPayload("https://api.anthropic.com", payload); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateClaudeUpstreamPayload_MiniMaxRejectsServerTool(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"MiniMax-M2.5",
		"messages":[{"role":"user","content":[{"type":"text","text":"search"}]}],
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}]
	}`)

	err := validateClaudeUpstreamPayload("https://api.minimax.io/anthropic", payload)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if se.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", se.StatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "request_feature_unsupported:") {
		t.Fatalf("error = %q, want request_feature_unsupported prefix", err.Error())
	}
}

func TestValidateClaudeUpstreamPayload_MiniMaxAllowsCustomTypedToolWithSchema(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"model":"MiniMax-M2.5",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{"type":"custom","name":"lookup","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]
	}`)

	if err := validateClaudeUpstreamPayload("https://api.minimax.io/anthropic", payload); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestRemapOAuthToolNames_MixedCase_OnlyRenamedToolsReversed is the regression
// test for a case where a single request contains both a TitleCase tool (which
// must pass through unchanged) and a lowercase tool that we forward-rename.
// Before the fix, triggering ANY forward rename caused the reverse pass to
// lowercase every TitleCase tool in the response using a global reverse map,
// corrupting tool names the client originally sent in TitleCase (notably Amp
// CLI's `Bash`, which its registry lookup cannot find as `bash`).
func TestRemapOAuthToolNames_MixedCase_OnlyRenamedToolsReversed(t *testing.T) {
	body := []byte(`{"tools":[` +
		`{"name":"Bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}},` +
		`{"name":"glob","input_schema":{"type":"object","properties":{"filePattern":{"type":"string"}}}}` +
		`]}`)

	out, reverseMap := remapOAuthToolNames(body)

	// Forward: TitleCase `Bash` is not a forward-map key, must pass through.
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Bash" {
		t.Fatalf("tools.0.name = %q, want %q (TitleCase tool must not be renamed)", got, "Bash")
	}
	// Forward: `glob` is a forward-map key, upstream sees `Glob`.
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "Glob" {
		t.Fatalf("tools.1.name = %q, want %q", got, "Glob")
	}

	// Reverse map records ONLY the rename that happened.
	if len(reverseMap) != 1 || reverseMap["Glob"] != "glob" {
		t.Fatalf("reverseMap = %v, want {Glob:glob}", reverseMap)
	}

	// Upstream responds with a `Bash` tool_use. Since we never renamed `Bash`,
	// reverseRemap MUST leave it alone.
	bashResp := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"cmd":"ls"}}]}`)
	reversed := reverseRemapOAuthToolNames(bashResp, reverseMap)
	if got := gjson.GetBytes(reversed, "content.0.name").String(); got != "Bash" {
		t.Fatalf("content.0.name = %q, want %q (Bash must be preserved; was never forward-renamed)", got, "Bash")
	}

	// Upstream responds with a `Glob` tool_use. Since we renamed `glob`→`Glob`,
	// reverseRemap MUST restore the original `glob`.
	globResp := []byte(`{"content":[{"type":"tool_use","id":"toolu_02","name":"Glob","input":{"filePattern":"**/*.go"}}]}`)
	reversed = reverseRemapOAuthToolNames(globResp, reverseMap)
	if got := gjson.GetBytes(reversed, "content.0.name").String(); got != "glob" {
		t.Fatalf("content.0.name = %q, want %q (Glob must be restored to client's original `glob`)", got, "glob")
	}
}

// TestReverseRemapOAuthToolNamesFromStreamLine_HonorsPerRequestMap guards the
// SSE streaming code path against the same mixed-case bug.
func TestReverseRemapOAuthToolNamesFromStreamLine_HonorsPerRequestMap(t *testing.T) {
	reverseMap := map[string]string{"Glob": "glob"}

	// Bash block was never renamed, must pass through as-is.
	bashLine := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}}`)
	out := reverseRemapOAuthToolNamesFromStreamLine(bashLine, reverseMap)
	if !bytes.Contains(out, []byte(`"name":"Bash"`)) {
		t.Fatalf("Bash should be preserved, got: %s", string(out))
	}
	if bytes.Contains(out, []byte(`"name":"bash"`)) {
		t.Fatalf("Bash must not be lowercased, got: %s", string(out))
	}

	// Glob block IS in the reverseMap, must be restored to `glob`.
	globLine := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"Glob","input":{}}}`)
	out = reverseRemapOAuthToolNamesFromStreamLine(globLine, reverseMap)
	if !bytes.Contains(out, []byte(`"name":"glob"`)) {
		t.Fatalf("Glob should be restored to glob, got: %s", string(out))
	}
}

func TestPrepareClaudeOAuthToolNamesForUpstream_MixedCaseWithPrefix(t *testing.T) {
	body := []byte(`{"tools":[` +
		`{"name":"Bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}},` +
		`{"name":"glob","input_schema":{"type":"object","properties":{"filePattern":{"type":"string"}}}}` +
		`],"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}},` +
		`{"type":"tool_use","id":"toolu_02","name":"glob","input":{}}` +
		`]}]}`)

	out, reverseMap := prepareClaudeOAuthToolNamesForUpstream(body, "proxy_", false)

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Bash" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Bash")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Glob" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Glob")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_Bash" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_Bash")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Glob" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Glob")
	}
	if len(reverseMap) != 1 || reverseMap["Glob"] != "glob" {
		t.Fatalf("reverseMap = %v, want {Glob:glob}", reverseMap)
	}
}

func TestRestoreClaudeOAuthToolNamesFromResponse_MixedCaseWithPrefix(t *testing.T) {
	reverseMap := map[string]string{"Glob": "glob"}
	resp := []byte(`{"content":[` +
		`{"type":"tool_use","id":"toolu_01","name":"proxy_Bash","input":{}},` +
		`{"type":"tool_use","id":"toolu_02","name":"proxy_Glob","input":{}}` +
		`]}`)

	out := restoreClaudeOAuthToolNamesFromResponse(resp, "proxy_", false, reverseMap)

	if got := gjson.GetBytes(out, "content.0.name").String(); got != "Bash" {
		t.Fatalf("content.0.name = %q, want %q", got, "Bash")
	}
	if got := gjson.GetBytes(out, "content.1.name").String(); got != "glob" {
		t.Fatalf("content.1.name = %q, want %q", got, "glob")
	}
}

func TestRestoreClaudeOAuthToolNamesFromStreamLine_MixedCaseWithPrefix(t *testing.T) {
	reverseMap := map[string]string{"Glob": "glob"}

	bashLine := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"proxy_Bash","input":{}}}`)
	out := restoreClaudeOAuthToolNamesFromStreamLine(bashLine, "proxy_", false, reverseMap)
	if !bytes.Contains(out, []byte(`"name":"Bash"`)) {
		t.Fatalf("Bash should be preserved, got: %s", string(out))
	}
	if bytes.Contains(out, []byte(`"name":"bash"`)) {
		t.Fatalf("Bash must not be lowercased, got: %s", string(out))
	}

	globLine := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"proxy_Glob","input":{}}}`)
	out = restoreClaudeOAuthToolNamesFromStreamLine(globLine, "proxy_", false, reverseMap)
	if !bytes.Contains(out, []byte(`"name":"glob"`)) {
		t.Fatalf("Glob should be restored to glob, got: %s", string(out))
	}
}
