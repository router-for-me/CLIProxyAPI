package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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

func TestApplyClaudeToolPrefix_PrefixesCustomToolType(t *testing.T) {
	input := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_apply_patch")
	}
}

func TestApplyClaudeToolPrefix_SkipsExplicitNonCustomTypedToolAndReferences(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}},
			{"type":"custom","name":"apply_patch","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"future_one"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"future_one","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"future_one"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"future_one"}]}]},
			{"role":"assistant","content":[{"type":"tool_use","name":"apply_patch","id":"t2","input":{}}]}
		]
	}`)

	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "future_one" {
		t.Fatalf("tools.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "future_one" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "future_one" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_apply_patch")
	}
	if got := gjson.GetBytes(out, "messages.3.content.0.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("messages.3.content.0.name = %q, want %q", got, "proxy_apply_patch")
	}
}

func TestApplyClaudeToolPrefix_SkipsRawWrapperToolWithoutTopLevelNameAndReferences(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"custom","function":{"name":"raw_wrapper","description":"wrapped","parameters":{"type":"object"}}},
			{"type":"custom","name":"apply_patch","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"raw_wrapper"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"raw_wrapper","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"raw_wrapper"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"raw_wrapper"}]}]},
			{"role":"assistant","content":[{"type":"tool_use","name":"apply_patch","id":"t2","input":{}}]}
		]
	}`)

	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "raw_wrapper" {
		t.Fatalf("tools.0.function.name = %q, want %q", got, "raw_wrapper")
	}
	if got := gjson.GetBytes(out, "tools.0.name"); got.Exists() {
		t.Fatalf("tools.0.name should remain absent for raw wrapper tool, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "raw_wrapper" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "raw_wrapper")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "raw_wrapper" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "raw_wrapper")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "raw_wrapper" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "raw_wrapper")
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != "raw_wrapper" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "raw_wrapper")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_apply_patch")
	}
	if got := gjson.GetBytes(out, "messages.3.content.0.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("messages.3.content.0.name = %q, want %q", got, "proxy_apply_patch")
	}
}

func TestApplyClaudeToolPrefix_PreservesAmbiguousSharedNameAcrossRawWrapperAndCustomTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"custom","function":{"name":"shared_tool","description":"wrapped","parameters":{"type":"object"}}},
			{"type":"custom","name":"shared_tool","input_schema":{"type":"object"}},
			{"type":"custom","name":"apply_patch","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"shared_tool"},
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","name":"shared_tool","id":"t1","input":{}},
				{"type":"tool_use","name":"apply_patch","id":"t2","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"shared_tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"shared_tool"}]}]}
		]
	}`)

	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "shared_tool" {
		t.Fatalf("tools.0.function.name = %q, want %q", got, "shared_tool")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "shared_tool" {
		t.Fatalf("tools.1.name = %q, want %q", got, "shared_tool")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "shared_tool" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "shared_tool")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "shared_tool" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "shared_tool")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "shared_tool" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "shared_tool")
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != "shared_tool" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "shared_tool")
	}
	if got := gjson.GetBytes(out, "tools.2.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("tools.2.name = %q, want %q", got, "proxy_apply_patch")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_apply_patch")
	}
}

func TestNormalizeClaudeToolsForAnthropic_CustomTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark"}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search_20250305" {
		t.Fatalf("tools.0.type = %q, want %q", got, "web_search_20250305")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "apply_patch" {
		t.Fatalf("tools.1.name = %q, want %q", got, "apply_patch")
	}
	if got := gjson.GetBytes(out, "tools.1.description").String(); got != "Custom tool" {
		t.Fatalf("tools.1.description = %q, want %q", got, "Custom tool")
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.1.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.1.type"); got.Exists() {
		t.Fatalf("tools.1.type should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.format"); got.Exists() {
		t.Fatalf("tools.1.format should be removed, got %s", got.Raw)
	}
}

func TestNormalizeClaudeToolsForAnthropic_PreservesDocumentedCustomMetadata(t *testing.T) {
	input := []byte(`{
		"tools":[
			{
				"type":"custom",
				"name":"apply_patch",
				"cache_control":{"type":"ephemeral","ttl":"1h"},
				"input_examples":[{"input":{"path":"README.md"}}],
				"strict":true,
				"format":{"type":"grammar","syntax":"lark"}
			}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "apply_patch" {
		t.Fatalf("tools.0.name = %q, want %q", got, "apply_patch")
	}
	if got := gjson.GetBytes(out, "tools.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("tools.0.cache_control.ttl = %q, want %q", got, "1h")
	}
	if got := gjson.GetBytes(out, "tools.0.input_examples.#").Int(); got != 1 {
		t.Fatalf("tools.0.input_examples length = %d, want 1", got)
	}
	if !gjson.GetBytes(out, "tools.0.strict").Bool() {
		t.Fatalf("tools.0.strict should be true, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type"); got.Exists() {
		t.Fatalf("tools.0.type should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.format"); got.Exists() {
		t.Fatalf("tools.0.format should be removed, got %s", got.Raw)
	}
}

func TestNormalizeClaudeToolsForAnthropic_FunctionFallbacks(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]}],
		"tools":[
			{"type":"custom","function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if strings.Contains(normalizedName, " ") {
		t.Fatalf("normalized tool name should be sanitized, got %q", normalizedName)
	}
	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "dangerous" {
		t.Fatalf("tools.0.description = %q, want %q", got, "dangerous")
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.0.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_OpenAIFunctionToolNormalizesInsteadOfPassingThrough(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]}],
		"tools":[
			{"type":"function","function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if strings.Contains(normalizedName, " ") {
		t.Fatalf("normalized tool name should be sanitized, got %q", normalizedName)
	}
	if got := gjson.GetBytes(out, "tools.0.type"); got.Exists() {
		t.Fatalf("tools.0.type should be removed after normalization, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "dangerous" {
		t.Fatalf("tools.0.description = %q, want %q", got, "dangerous")
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.0.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_FunctionFallbacksPreserveTopLevelMetadata(t *testing.T) {
	input := []byte(`{
		"tools":[
			{
				"type":"custom",
				"cache_control":{"type":"ephemeral"},
				"input_examples":[{"input":{"command":"run"}}],
				"strict":true,
				"function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}
			}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "dangerous" {
		t.Fatalf("tools.0.description = %q, want %q", got, "dangerous")
	}
	if got := gjson.GetBytes(out, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tools.0.cache_control.type = %q, want %q", got, "ephemeral")
	}
	if got := gjson.GetBytes(out, "tools.0.input_examples.#").Int(); got != 1 {
		t.Fatalf("tools.0.input_examples length = %d, want 1", got)
	}
	if !gjson.GetBytes(out, "tools.0.strict").Bool() {
		t.Fatalf("tools.0.strict should be true, body=%s", string(out))
	}
}

func TestNormalizeClaudeToolsForAnthropic_RenameMapUpdatesAllReferenceSites(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"very bad tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"very bad tool"}]}]}
		],
		"tools":[
			{"type":"custom","function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_RenameMapPreservesLargeIntegers(t *testing.T) {
	input := []byte(`{
		"request_id": 9007199254740993,
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"tool_use","name":"very bad tool","id":"t1","input":{"large_id":9007199254740995}},
					{"type":"text","text":"unchanged"}
				]
			},
			{
				"role":"user",
				"content":[
					{"type":"tool_result","tool_use_id":"t1","content":[
						{"type":"tool_reference","tool_name":"very bad tool"},
						{"type":"text","text":"meta","data":{"ticket":9223372036854775806}}
					]}
				]
			}
		],
		"tools":[
			{"type":"custom","function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.1.content.0.content.0.tool_name = %q, want %q", got, normalizedName)
	}

	// Ensure unrelated large integer values are preserved exactly (no float64 coercion).
	if got := gjson.GetBytes(out, "request_id").Raw; got != "9007199254740993" {
		t.Fatalf("request_id raw = %q, want %q", got, "9007199254740993")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.input.large_id").Raw; got != "9007199254740995" {
		t.Fatalf("messages.0.content.0.input.large_id raw = %q, want %q", got, "9007199254740995")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.content.1.data.ticket").Raw; got != "9223372036854775806" {
		t.Fatalf("messages.1.content.0.content.1.data.ticket raw = %q, want %q", got, "9223372036854775806")
	}
}

func TestNormalizeClaudeToolsForAnthropic_RejectsDuplicateOriginalCustomToolNames(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"duplicate tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"duplicate tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"duplicate tool"}]}
		],
		"tools":[
			{"type":"custom","name":"duplicate tool","description":"first","input_schema":{"type":"object"}},
			{"type":"custom","name":"duplicate tool","description":"second","input_schema":{"type":"object"}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want duplicate-name rejection")
	}
	if !errors.Is(err, errAnthropicDuplicateToolName) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicDuplicateToolName", err)
	}
}

func TestNormalizeClaudeToolsForAnthropic_RejectsUnsanitizableCustomToolName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"!!!"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"!!!","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"!!!"}]}
		],
		"tools":[
			{"type":"custom","name":"!!!","description":"bad","input_schema":{"type":"object"}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want invalid-name rejection")
	}
	if !errors.Is(err, errAnthropicToolNameUnsanitizable) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicToolNameUnsanitizable", err)
	}
}

func TestFinalizeClaudeRequestBody_RejectsDuplicateOriginalCustomToolNames(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	_, err := executor.finalizeClaudeRequestBody([]byte(`{
		"tools":[
			{"type":"custom","name":"duplicate tool","description":"first","input_schema":{"type":"object"}},
			{"type":"custom","name":"duplicate tool","description":"second","input_schema":{"type":"object"}}
		]
	}`), "claude-opus-4-6", sdktranslator.FromString("claude"), sdktranslator.FromString("claude"), "claude-opus-4-6", "", nil, claudeBodyFinalizeOptions{})
	if err == nil {
		t.Fatal("finalizeClaudeRequestBody error = nil, want bad-request rejection")
	}
	sErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("finalizeClaudeRequestBody error type = %T, want statusErr", err)
	}
	if sErr.code != http.StatusBadRequest {
		t.Fatalf("statusErr.code = %d, want %d", sErr.code, http.StatusBadRequest)
	}
}

func TestFinalizeClaudeRequestBody_RejectsUnsanitizableCustomToolName(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	_, err := executor.finalizeClaudeRequestBody([]byte(`{
		"tools":[
			{"type":"custom","name":"!!!","description":"bad","input_schema":{"type":"object"}}
		]
	}`), "claude-opus-4-6", sdktranslator.FromString("claude"), sdktranslator.FromString("claude"), "claude-opus-4-6", "", nil, claudeBodyFinalizeOptions{})
	if err == nil {
		t.Fatal("finalizeClaudeRequestBody error = nil, want bad-request rejection")
	}
	sErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("finalizeClaudeRequestBody error type = %T, want statusErr", err)
	}
	if sErr.code != http.StatusBadRequest {
		t.Fatalf("statusErr.code = %d, want %d", sErr.code, http.StatusBadRequest)
	}
}

func TestFinalizeClaudeRequestBody_PreservesPreNormalizationTranslationBody(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	input := []byte(`{
		"tools":[
			{
				"type":"function",
				"name":"very bad tool",
				"description":"Search",
				"parameters":{"type":"object","properties":{"q":{"type":"string"}}},
				"x_vendor":{"mode":"strict"}
			}
		],
		"tool_choice":{"type":"function","function":{"name":"very bad tool"}}
	}`)
	body := sdktranslator.TranslateRequest(sdktranslator.FromString("openai-response"), sdktranslator.FromString("claude"), "claude-opus-4-6", input, true)
	prepared, err := executor.finalizeClaudeRequestBody(body, "claude-opus-4-6", sdktranslator.FromString("openai-response"), sdktranslator.FromString("claude"), "claude-opus-4-6", "", nil, claudeBodyFinalizeOptions{})
	if err != nil {
		t.Fatalf("finalizeClaudeRequestBody error: %v", err)
	}

	if got := gjson.GetBytes(prepared.bodyForTranslation, "tools.0.name").String(); got != "very bad tool" {
		t.Fatalf("bodyForTranslation tools.0.name = %q, want %q", got, "very bad tool")
	}
	if got := gjson.GetBytes(prepared.bodyForTranslation, "tools.0.input_schema.properties.q.type").String(); got != "string" {
		t.Fatalf("bodyForTranslation tools.0.input_schema.properties.q.type = %q, want %q", got, "string")
	}
	if got := gjson.GetBytes(prepared.bodyForTranslation, "tool_choice.name").String(); got != "very bad tool" {
		t.Fatalf("bodyForTranslation tool_choice.name = %q, want %q", got, "very bad tool")
	}

	upstreamName := gjson.GetBytes(prepared.bodyForUpstream, "tools.0.name").String()
	if upstreamName == "" || upstreamName == "very bad tool" {
		t.Fatalf("bodyForUpstream tools.0.name = %q, want normalized name", upstreamName)
	}
	if got := gjson.GetBytes(prepared.bodyForUpstream, "tools.0.type"); got.Exists() {
		t.Fatalf("bodyForUpstream tools.0.type should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(prepared.bodyForUpstream, "tool_choice.name").String(); got != upstreamName {
		t.Fatalf("bodyForUpstream tool_choice.name = %q, want %q", got, upstreamName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_ReservesBuiltinNamesForCustomTools(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"web search"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"web search","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"web search"}]}
		],
		"tools":[
			{"type":"custom","name":"web search","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if normalizedName == "web_search" {
		t.Fatalf("custom tool should not normalize to reserved built-in name, got %q", normalizedName)
	}
	if !strings.HasPrefix(normalizedName, "web_search_") {
		t.Fatalf("custom tool should be suffixed off reserved name, got %q", normalizedName)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_AllowsDistinctOriginalNamesThatSanitizeToSameBase(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"very@bad tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"tool_reference","tool_name":"very@bad tool"}]}]}
		],
		"tools":[
			{"type":"custom","name":"very bad tool","description":"first","input_schema":{"type":"object"}},
			{"type":"custom","name":"very@bad tool","description":"second","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	firstName := gjson.GetBytes(out, "tools.0.name").String()
	secondName := gjson.GetBytes(out, "tools.1.name").String()
	if firstName == "" || secondName == "" {
		t.Fatalf("normalized names should not be empty, got first=%q second=%q", firstName, secondName)
	}
	if firstName == secondName {
		t.Fatalf("normalized names should be unique, got %q", firstName)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != firstName {
		t.Fatalf("tool_choice.name = %q, want %q", got, firstName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != firstName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, firstName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != secondName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, secondName)
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != secondName {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, secondName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_RejectsNonObjectSchemas(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"custom","name":"array_schema","input_schema":{"type":"array","items":{"type":"string"}}},
			{"type":"custom","name":"bad_properties","input_schema":{"type":"object","properties":[]}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.0.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("tools.0.input_schema.properties should be object, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.items"); got.Exists() {
		t.Fatalf("tools.0.input_schema.items should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.1.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("tools.1.input_schema.properties should be object, got %s", got.Raw)
	}
}

func TestNormalizeClaudeToolsForAnthropic_PreservesUnknownExplicitTypedTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}},
			{"type":"custom","name":"apply patch","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "future_tool" {
		t.Fatalf("tools.0.type = %q, want %q", got, "future_tool")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "future_one" {
		t.Fatalf("tools.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "tools.0.extra.a").Int(); got != 1 {
		t.Fatalf("tools.0.extra.a = %d, want 1", got)
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got == "apply patch" || got == "" {
		t.Fatalf("tools.1.name = %q, want sanitized non-empty custom name", got)
	}
}

func TestNormalizeClaudeToolsForAnthropic_TreatsExplicitTypedAndCustomNameCollisionAsAmbiguous(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"future_one"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"future_one","id":"t1","input":{"n":9007199254740995}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"future_one"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"future_one"}]}]}
		],
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}},
			{"type":"custom","name":"future_one","description":"custom duplicate","input_schema":{"type":"object"}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want ambiguous-name rejection")
	}
	if !errors.Is(err, errAnthropicDuplicateToolName) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicDuplicateToolName", err)
	}
}

func TestNormalizeClaudeToolsForAnthropic_TreatsBuiltinAndCustomNameCollisionAsAmbiguous(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"web_search","id":"ws1","input":{"n":9007199254740995}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"web_search"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"ws1","content":[{"type":"tool_reference","tool_name":"web_search"}]}]}
		],
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":5},
			{"type":"custom","name":"web_search","description":"custom duplicate","input_schema":{"type":"object"}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want ambiguous-name rejection")
	}
	if !errors.Is(err, errAnthropicDuplicateToolName) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicDuplicateToolName", err)
	}
}

func TestNormalizeClaudeToolsForAnthropic_SkipsEmptyNameNoTypeTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"name":"shell","description":"Run commands","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}},
			{"name":"","description":"","input_schema":{}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	toolCount := gjson.GetBytes(out, "tools.#").Int()
	if toolCount != 1 {
		t.Fatalf("tools count = %d, want 1; body=%s", toolCount, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "shell" {
		t.Fatalf("tools.0.name = %q, want %q", got, "shell")
	}
}

func TestNormalizeClaudeToolsForAnthropic_SkipsMultipleEmptyNameNoTypeTools(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"name":"","description":"","input_schema":{}},
			{"name":"shell","description":"Run commands","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}},
			{"name":"","description":"code interpreter","input_schema":{}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	toolCount := gjson.GetBytes(out, "tools.#").Int()
	if toolCount != 1 {
		t.Fatalf("tools count = %d, want 1; body=%s", toolCount, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "shell" {
		t.Fatalf("tools.0.name = %q, want %q", got, "shell")
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

func TestClaudeExecutor_CountTokens_AppliesThinkingFromModelSuffix(t *testing.T) {
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

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet(2048)",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if got := gjson.GetBytes(seenBody, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "enabled", string(seenBody))
	}
	if got := gjson.GetBytes(seenBody, "thinking.budget_tokens").Int(); got <= 0 {
		t.Fatalf("thinking.budget_tokens = %d, want > 0, body=%s", got, string(seenBody))
	}
}

func TestClaudeExecutor_CountTokens_DisablesThinkingWhenForcedToolChoice(t *testing.T) {
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
		"tool_choice":{"type":"tool","name":"apply_patch"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)
	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet(2048)",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if gjson.GetBytes(seenBody, "thinking").Exists() {
		t.Fatalf("thinking should be removed for forced tool choice, body=%s", string(seenBody))
	}
}

func TestApplyClaudeHeaders_MergesBetasWithDefaultsAndClaude1M(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginReq := httptest.NewRequest(http.MethodPost, "http://localhost/v1/messages", nil)
	ginReq.Header.Set("Anthropic-Beta", "custom-beta-1,oauth-2025-04-20")
	ginReq.Header.Set("X-CPA-CLAUDE-1M", "1")
	ginCtx.Request = ginReq

	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), "gin", ginCtx))

	applyClaudeHeaders(req, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}, "key-123", false, []string{"beta-from-body", "custom-beta-1"}, &config.Config{})

	got := req.Header.Get("Anthropic-Beta")
	for _, want := range []string{
		"custom-beta-1",
		"oauth-2025-04-20",
		"beta-from-body",
		"context-1m-2025-08-07",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Anthropic-Beta = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "custom-beta-1") != 1 {
		t.Fatalf("Anthropic-Beta should de-duplicate custom-beta-1, got %q", got)
	}
}

func TestClaudeExecutor_Execute_PreservesCustomToolCacheControl(t *testing.T) {
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
		"tools": [
			{
				"type":"custom",
				"name":"tool_one",
				"cache_control":{"type":"ephemeral","ttl":"1h"},
				"input_schema":{"type":"object"}
			},
			{
				"type":"custom",
				"name":"tool_two",
				"input_schema":{"type":"object"}
			}
		],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
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
	if got := gjson.GetBytes(seenBody, "tools.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("tools.0.cache_control.ttl = %q, want %q, body=%s", got, "1h", string(seenBody))
	}
	if gjson.GetBytes(seenBody, "tools.1.cache_control").Exists() {
		t.Fatalf("tools.1.cache_control should not be injected when caller already provided tool cache_control, body=%s", string(seenBody))
	}
	if got := countCacheControls(seenBody); got != 1 {
		t.Fatalf("cache_control count = %d, want 1, body=%s", got, string(seenBody))
	}
}

func TestClaudeExecutor_Execute_RestoresOriginalToolNamesAfterNormalization(t *testing.T) {
	var upstreamRequestName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamRequestName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"id":"msg_1",
			"type":"message",
			"model":"claude-3-5-sonnet",
			"role":"assistant",
			"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{"q":"hi"}}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`, upstreamRequestName)))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"tools":[{"type":"custom","name":"very bad tool","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if upstreamRequestName == "" || upstreamRequestName == "very bad tool" {
		t.Fatalf("upstream request tool name = %q, want normalized name", upstreamRequestName)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.name").String(); got != "very bad tool" {
		t.Fatalf("content.0.name = %q, want %q, payload=%s", got, "very bad tool", string(resp.Payload))
	}
}

func TestClaudeExecutor_ExecuteStream_RestoresOriginalToolNamesAfterNormalization(t *testing.T) {
	var upstreamRequestName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamRequestName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet\",\"content\":[]}}\n\n"))
		_, _ = w.Write([]byte(fmt.Sprintf("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":%q,\"input\":{}}}\n\n", upstreamRequestName)))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"tools":[{"type":"custom","name":"very bad tool","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var lines []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		lines = append(lines, string(chunk.Payload))
	}

	if upstreamRequestName == "" || upstreamRequestName == "very bad tool" {
		t.Fatalf("upstream request tool name = %q, want normalized name", upstreamRequestName)
	}
	streamBody := strings.Join(lines, "")
	if !strings.Contains(streamBody, `"name":"very bad tool"`) {
		t.Fatalf("stream output should restore original tool name, got %s", streamBody)
	}
	if strings.Contains(streamBody, fmt.Sprintf(`"name":"%s"`, upstreamRequestName)) {
		t.Fatalf("stream output should not expose normalized tool name %q, got %s", upstreamRequestName, streamBody)
	}
}

func TestClaudeExecutor_ExecuteStream_OpenAIResponsesWithoutOriginalRequest_RestoresEchoedToolNames(t *testing.T) {
	var upstreamRequestName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamRequestName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet\",\"content\":[]}}\n\n"))
		_, _ = w.Write([]byte(fmt.Sprintf("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":%q,\"input\":{\"q\":\"hi\"}}}\n\n", upstreamRequestName)))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[{"type":"function","name":"very bad tool","description":"Search","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","function":{"name":"very bad tool"}}
	}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}

	if upstreamRequestName == "" || upstreamRequestName == "very bad tool" {
		t.Fatalf("upstream request tool name = %q, want normalized name", upstreamRequestName)
	}

	var (
		sawOutputName     bool
		sawEchoedToolName bool
		sawToolChoiceName bool
	)
	for _, payload := range collectSSEDataPayloads(chunks) {
		if got := payload.Get("item.name").String(); got == "very bad tool" {
			sawOutputName = true
		} else if got == upstreamRequestName {
			t.Fatalf("stream output item exposed normalized tool name %q in payload=%s", got, payload.Raw)
		}
		if got := payload.Get("response.tools.0.name").String(); got == "very bad tool" {
			sawEchoedToolName = true
		} else if got == upstreamRequestName {
			t.Fatalf("stream response.tools exposed normalized tool name %q in payload=%s", got, payload.Raw)
		}
		if got := openAIResponseToolChoiceName(payload); got == "very bad tool" {
			sawToolChoiceName = true
		} else if got == upstreamRequestName {
			t.Fatalf("stream tool_choice exposed normalized tool name %q in payload=%s", got, payload.Raw)
		}
	}

	if !sawOutputName {
		t.Fatalf("expected streamed output item to restore original tool name, got %v", chunks)
	}
	if !sawEchoedToolName {
		t.Fatalf("expected streamed response.tools to restore original tool name, got %v", chunks)
	}
	if !sawToolChoiceName {
		t.Fatalf("expected streamed tool_choice to restore original tool name, got %v", chunks)
	}
}

func TestClaudeExecutor_Execute_OpenAIResponsesWithoutOriginalRequest_PreservesEchoedToolSchema(t *testing.T) {
	var upstreamRequestName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamRequestName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet\",\"content\":[]}}\n\n"))
		_, _ = w.Write([]byte(fmt.Sprintf("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":%q,\"input\":{\"q\":\"hi\"}}}\n\n", upstreamRequestName)))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[{"type":"function","name":"very bad tool","description":"Search","parameters":{"type":"object","properties":{"q":{"type":"string"}}},"x_vendor":{"mode":"strict"}}],
		"tool_choice":{"type":"function","function":{"name":"very bad tool"}}
	}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if upstreamRequestName == "" || upstreamRequestName == "very bad tool" {
		t.Fatalf("upstream request tool name = %q, want normalized name", upstreamRequestName)
	}
	if got := gjson.GetBytes(resp.Payload, "tools.0.type").String(); got != "function" {
		t.Fatalf("response tools.0.type = %q, want %q, payload=%s", got, "function", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "tools.0.name").String(); got != "very bad tool" {
		t.Fatalf("response tools.0.name = %q, want %q, payload=%s", got, "very bad tool", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "tools.0.parameters.properties.q.type").String(); got != "string" {
		t.Fatalf("response tools.0.parameters.properties.q.type = %q, want %q, payload=%s", got, "string", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "tools.0.x_vendor.mode").String(); got != "strict" {
		t.Fatalf("response tools.0.x_vendor.mode = %q, want %q, payload=%s", got, "strict", string(resp.Payload))
	}
	if got := openAIResponseToolChoiceName(gjson.ParseBytes(resp.Payload)); got != "very bad tool" {
		t.Fatalf("response tool_choice name = %q, want %q, payload=%s", got, "very bad tool", string(resp.Payload))
	}
}

func TestClaudeExecutor_Execute_OpenAIResponsesWithoutOriginalRequest_UsesEffectiveRequestEcho(t *testing.T) {
	var upstreamTemperature float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamTemperature = gjson.GetBytes(body, "temperature").Float()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet\",\"content\":[]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: map[string]any{
						"temperature": 0.25,
					},
				},
			},
		},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"temperature":0.9
	}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if upstreamTemperature != 0.25 {
		t.Fatalf("upstream temperature = %v, want %v", upstreamTemperature, 0.25)
	}
	if got := gjson.GetBytes(resp.Payload, "temperature").Float(); got != 0.9 {
		t.Fatalf("response temperature = %v, want %v, payload=%s", got, 0.9, string(resp.Payload))
	}
}

func TestClaudeExecutor_ErrorPaths_RestoreOriginalToolNamesAfterNormalization(t *testing.T) {
	var upstreamRequestName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamRequestName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"type":"error",
			"error":{
				"type":"invalid_request_error",
				"message":"tool %s failed validation"
			}
		}`, upstreamRequestName)))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"tools":[{"type":"custom","name":"very bad tool","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	checkErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var status statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: expected statusErr, got %T: %v", name, err, err)
		}
		if status.StatusCode() != http.StatusBadRequest {
			t.Fatalf("%s: status code = %d, want %d", name, status.StatusCode(), http.StatusBadRequest)
		}
		if upstreamRequestName == "" || upstreamRequestName == "very bad tool" {
			t.Fatalf("%s: upstream request tool name = %q, want normalized name", name, upstreamRequestName)
		}
		if !strings.Contains(err.Error(), "very bad tool") {
			t.Fatalf("%s: expected restored original tool name in error, got %q", name, err.Error())
		}
		if strings.Contains(err.Error(), upstreamRequestName) {
			t.Fatalf("%s: error should not expose normalized tool name %q, got %q", name, upstreamRequestName, err.Error())
		}
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "Execute", err)

	_, err = executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "ExecuteStream", err)

	_, err = executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "CountTokens", err)
}

func TestClaudeExecutor_ErrorPaths_LeavePlainTextErrorsVerbatim(t *testing.T) {
	var upstreamRequestName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamRequestName = gjson.GetBytes(body, "tools.0.name").String()
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("read timeout while calling upstream"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"tools":[{"type":"custom","name":"read!","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	checkErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var status statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: expected statusErr, got %T: %v", name, err, err)
		}
		if status.StatusCode() != http.StatusBadRequest {
			t.Fatalf("%s: status code = %d, want %d", name, status.StatusCode(), http.StatusBadRequest)
		}
		if upstreamRequestName != "read" {
			t.Fatalf("%s: upstream request tool name = %q, want %q", name, upstreamRequestName, "read")
		}
		if got := err.Error(); got != "read timeout while calling upstream" {
			t.Fatalf("%s: error text = %q, want %q", name, got, "read timeout while calling upstream")
		}
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "Execute", err)

	_, err = executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "ExecuteStream", err)

	_, err = executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "CountTokens", err)
}

func collectSSEDataPayloads(chunks []string) []gjson.Result {
	var payloads []gjson.Result
	for _, chunk := range chunks {
		for _, line := range strings.Split(chunk, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if raw == "" || !gjson.Valid(raw) {
				continue
			}
			payloads = append(payloads, gjson.Parse(raw))
		}
	}
	return payloads
}

func openAIResponseToolChoiceName(payload gjson.Result) string {
	for _, path := range []string{
		"tool_choice.function.name",
		"tool_choice.name",
		"response.tool_choice.function.name",
		"response.tool_choice.name",
	} {
		if value := payload.Get(path); value.Exists() {
			return value.String()
		}
	}
	return ""
}

func TestNormalizeThenPrefix_KeepsCustomNameConsistentWhenSanitizedNameMatchesBuiltin(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"web search"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"web search","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"web search"}]}
		],
		"tools":[
			{"type":"custom","name":"web search","input_schema":{"type":"object"}}
		]
	}`)

	normalized, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	prefixed := applyClaudeToolPrefix(normalized, "proxy_")

	normalizedName := gjson.GetBytes(normalized, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	expectedPrefixedName := "proxy_" + normalizedName
	if got := gjson.GetBytes(prefixed, "tools.0.name").String(); got != expectedPrefixedName {
		t.Fatalf("tools.0.name = %q, want %q", got, expectedPrefixedName)
	}
	if got := gjson.GetBytes(prefixed, "tool_choice.name").String(); got != expectedPrefixedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, expectedPrefixedName)
	}
	if got := gjson.GetBytes(prefixed, "messages.0.content.0.name").String(); got != expectedPrefixedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, expectedPrefixedName)
	}
	if got := gjson.GetBytes(prefixed, "messages.1.content.0.tool_name").String(); got != expectedPrefixedName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, expectedPrefixedName)
	}
}

func TestNormalizeThenPrefix_PreservesUnknownExplicitTypedToolName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"future_one"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"future_one","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"future_one"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"future_one"}]}]}
		],
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}}
		]
	}`)

	normalized, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	prefixed := applyClaudeToolPrefix(normalized, "proxy_")

	if got := gjson.GetBytes(prefixed, "tools.0.name").String(); got != "future_one" {
		t.Fatalf("tools.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "tool_choice.name").String(); got != "future_one" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "messages.0.content.0.name").String(); got != "future_one" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "messages.1.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "messages.2.content.0.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "future_one")
	}
}

func TestNormalizeThenPrefix_RejectsAmbiguousExplicitTypedAndCustomSharedName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"future_one"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"future_one","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"future_one"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"future_one"}]}]}
		],
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}},
			{"type":"custom","name":"future_one","input_schema":{"type":"object"}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want ambiguous-name rejection")
	}
	if !errors.Is(err, errAnthropicDuplicateToolName) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicDuplicateToolName", err)
	}
}

func TestNormalizeThenPrefix_RejectsAmbiguousBuiltinAndCustomSharedName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"web_search","id":"ws1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"web_search"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"ws1","content":[{"type":"tool_reference","tool_name":"web_search"}]}]}
		],
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"custom","name":"web_search","input_schema":{"type":"object"}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want ambiguous-name rejection")
	}
	if !errors.Is(err, errAnthropicDuplicateToolName) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicDuplicateToolName", err)
	}
}

func TestNormalizeThenPrefix_RejectsDuplicateOpenAIFunctionFallbackName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"duplicate tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"duplicate tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"duplicate tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"duplicate tool"}]}]}
		],
		"tools":[
			{"type":"function","function":{"name":"duplicate tool","description":"first","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"duplicate tool","description":"second","parameters":{"type":"object"}}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want duplicate-name rejection")
	}
	if !errors.Is(err, errAnthropicDuplicateToolName) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicDuplicateToolName", err)
	}
}

func TestNormalizeThenPrefix_RejectsUnsanitizableOpenAIFunctionFallbackName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"!!!"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"!!!","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"!!!"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"!!!"}]}]}
		],
		"tools":[
			{"type":"function","function":{"name":"!!!","description":"bad","parameters":{"type":"object"}}}
		]
	}`)

	_, err := normalizeClaudeToolsForAnthropic(input)
	if err == nil {
		t.Fatal("normalizeClaudeToolsForAnthropic error = nil, want invalid-name rejection")
	}
	if !errors.Is(err, errAnthropicToolNameUnsanitizable) {
		t.Fatalf("normalizeClaudeToolsForAnthropic error = %v, want errAnthropicToolNameUnsanitizable", err)
	}
}

func TestRestoreClaudeNormalizedToolNamesInRequest_RestoresToolsAndReferences(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"search_tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"search_tool","id":"toolu_1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"search_tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"search_tool"}]}]}
		],
		"tools":[
			{"name":"search_tool","type":"custom","input_schema":{"type":"object"}}
		]
	}`)

	out := restoreClaudeNormalizedToolNamesInRequest(input, map[string]string{
		"search_tool": "search tool",
	})

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "search tool" {
		t.Fatalf("tools.0.name = %q, want %q", got, "search tool")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "search tool" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "search tool")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "search tool" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "search tool")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "search tool" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "search tool")
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != "search tool" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "search tool")
	}
}

func TestRestoreClaudeToolNamesInErrorText_PrefersLongerNormalizedNames(t *testing.T) {
	text := "tool read_more failed validation while read stayed valid"
	out := restoreClaudeToolNamesInErrorText(text, "", map[string]string{
		"read":      "read!",
		"read_more": "read more!",
	})

	if strings.Contains(out, "read!_more") {
		t.Fatalf("restored text = %q, should not partially replace overlapping normalized names", out)
	}
	if got := out; got != "tool read more! failed validation while read! stayed valid" {
		t.Fatalf("restored text = %q, want %q", got, "tool read more! failed validation while read! stayed valid")
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

	checkErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var status statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: expected statusErr, got %T: %v", name, err, err)
		}
		if got := status.StatusCode(); got != http.StatusBadRequest {
			t.Fatalf("%s: status code = %d, want %d", name, got, http.StatusBadRequest)
		}
		errText := strings.ToLower(err.Error())
		if !strings.Contains(errText, "failed to read upstream error body") &&
			!strings.Contains(errText, "failed to decode upstream error body") &&
			!strings.Contains(errText, "failed to decode error response body") {
			t.Fatalf("%s: expected read/decode failure context, got %q", name, err.Error())
		}
		if !strings.Contains(errText, "gzip") && !strings.Contains(errText, "eof") && !strings.Contains(errText, "checksum") {
			t.Fatalf("%s: expected gzip read failure, got %q", name, err.Error())
		}
	}

	err := invoke(executor, auth, payload)
	checkErr(t, "invoke", err)
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

	out, renamed := remapOAuthToolNames(body)
	if renamed {
		t.Fatalf("renamed = true, want false")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Bash" {
		t.Fatalf("tools.0.name = %q, want %q", got, "Bash")
	}

	resp := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"cmd":"ls"}}]}`)
	reversed := resp
	if renamed {
		reversed = reverseRemapOAuthToolNames(resp)
	}
	if got := gjson.GetBytes(reversed, "content.0.name").String(); got != "Bash" {
		t.Fatalf("content.0.name = %q, want %q", got, "Bash")
	}
}

func TestRemapOAuthToolNames_Lowercase_ReverseApplied(t *testing.T) {
	body := []byte(`{"tools":[{"name":"bash","description":"Run shell commands","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	out, renamed := remapOAuthToolNames(body)
	if !renamed {
		t.Fatalf("renamed = false, want true")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Bash" {
		t.Fatalf("tools.0.name = %q, want %q", got, "Bash")
	}

	resp := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"cmd":"ls"}}]}`)
	reversed := resp
	if renamed {
		reversed = reverseRemapOAuthToolNames(resp)
	}
	if got := gjson.GetBytes(reversed, "content.0.name").String(); got != "bash" {
		t.Fatalf("content.0.name = %q, want %q", got, "bash")
	}
}

func TestClaudeExecutor_PreservesStatusOnCompressedErrorDecodeFailure(t *testing.T) {
	invalidGzip := []byte("not-a-gzip-stream")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(invalidGzip)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	checkErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var status statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: expected statusErr, got %T: %v", name, err, err)
		}
		if got := status.StatusCode(); got != http.StatusBadRequest {
			t.Fatalf("%s: status code = %d, want %d", name, got, http.StatusBadRequest)
		}
		errText := strings.ToLower(err.Error())
		if !strings.Contains(errText, "failed to decode upstream error body") &&
			!strings.Contains(errText, "failed to decode error response body") {
			t.Fatalf("%s: expected decode failure context, got %q", name, err.Error())
		}
		if !strings.Contains(errText, "gzip") {
			t.Fatalf("%s: expected gzip decode failure details, got %q", name, err.Error())
		}
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "Execute", err)

	_, err = executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "ExecuteStream", err)

	_, err = executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "CountTokens", err)
}
