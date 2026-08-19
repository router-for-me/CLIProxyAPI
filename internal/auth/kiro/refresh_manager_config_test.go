package kiro

import (
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
)

func resetKiroRuntimeConfigTestState() {
	globalRateLimiter = nil
	globalRateLimiterCfg = nil
	globalRateLimiterOnce = sync.Once{}
	kirocommon.SetSystemPromptInjectEnabled(false)
	kirocommon.SetTruncationDetectorEnabled(false)
	kirocommon.SetExtractThinkingTagEnabled(false)
}

func TestInitSystemPromptInjectConfig_AppliesAndResets(t *testing.T) {
	resetKiroRuntimeConfigTestState()
	t.Cleanup(resetKiroRuntimeConfigTestState)

	enabled := true
	InitSystemPromptInjectConfig(&config.Config{
		KiroSystemPromptInjectEnable: &enabled,
	})
	if !kirocommon.IsSystemPromptInjectEnabled() {
		t.Fatal("expected system prompt injection to be enabled")
	}

	InitSystemPromptInjectConfig(&config.Config{})
	if kirocommon.IsSystemPromptInjectEnabled() {
		t.Fatal("expected system prompt injection to reset to disabled")
	}
}

func TestInitTruncationDetectorConfig_AppliesAndResets(t *testing.T) {
	resetKiroRuntimeConfigTestState()
	t.Cleanup(resetKiroRuntimeConfigTestState)

	enabled := true
	InitTruncationDetectorConfig(&config.Config{
		KiroTruncationDetectorEnable: &enabled,
	})
	if !kirocommon.IsTruncationDetectorEnabled() {
		t.Fatal("expected truncation detector to be enabled")
	}

	InitTruncationDetectorConfig(&config.Config{})
	if kirocommon.IsTruncationDetectorEnabled() {
		t.Fatal("expected truncation detector to reset to disabled")
	}
}

func TestInitExtractThinkingTagConfig_AppliesAndResets(t *testing.T) {
	resetKiroRuntimeConfigTestState()
	t.Cleanup(resetKiroRuntimeConfigTestState)

	enabled := true
	InitExtractThinkingTagConfig(&config.Config{
		KiroExtractThinkingTagEnable: &enabled,
	})
	if !kirocommon.IsExtractThinkingTagEnabled() {
		t.Fatal("expected extract thinking tag to be enabled")
	}

	InitExtractThinkingTagConfig(&config.Config{})
	if kirocommon.IsExtractThinkingTagEnabled() {
		t.Fatal("expected extract thinking tag to reset to disabled")
	}
}

func TestInitRateLimiterConfig_AppliesAndResets(t *testing.T) {
	resetKiroRuntimeConfigTestState()
	t.Cleanup(resetKiroRuntimeConfigTestState)

	enabled := true
	InitRateLimiterConfig(&config.Config{
		KiroRateLimit: &config.KiroRateLimitConfig{
			Enabled:          &enabled,
			MinTokenInterval: "5s",
			MaxTokenInterval: "7s",
		},
	})

	rl := GetGlobalRateLimiter()
	if !rl.enabled {
		t.Fatal("expected rate limiter to be enabled")
	}
	if rl.minTokenInterval != 5*time.Second || rl.maxTokenInterval != 7*time.Second {
		t.Fatalf("unexpected interval config: min=%v max=%v", rl.minTokenInterval, rl.maxTokenInterval)
	}

	InitRateLimiterConfig(&config.Config{})
	if rl.enabled {
		t.Fatal("expected rate limiter to reset to disabled")
	}
	if rl.minTokenInterval != DefaultMinTokenInterval || rl.maxTokenInterval != DefaultMaxTokenInterval {
		t.Fatalf("expected defaults after reset, got min=%v max=%v", rl.minTokenInterval, rl.maxTokenInterval)
	}

	rl.MarkTokenFailed("disabled-token")
	if state := rl.GetTokenState("disabled-token"); state != nil {
		t.Fatal("expected disabled rate limiter to ignore failure tracking")
	}
}
