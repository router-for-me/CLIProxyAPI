package config

import "testing"

func TestClaudePromptCacheEffectiveMode(t *testing.T) {
	testCases := []struct {
		name       string
		configured string
		want       string
	}{
		{name: "omitted", configured: "", want: ClaudePromptCacheModeLegacy},
		{name: "legacy", configured: "legacy", want: ClaudePromptCacheModeLegacy},
		{name: "adaptive normalized", configured: " ADAPTIVE ", want: ClaudePromptCacheModeAdaptive},
		{name: "passthrough normalized", configured: "PassThrough", want: ClaudePromptCacheModePassthrough},
		{name: "unknown falls back", configured: "unsupported", want: ClaudePromptCacheModeLegacy},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cacheConfig := ClaudePromptCacheConfig{Mode: testCase.configured}
			if got := cacheConfig.EffectiveMode(); got != testCase.want {
				t.Fatalf("EffectiveMode() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestClaudePromptCacheEffectiveColdStartMaxWaitSeconds(t *testing.T) {
	negativeWait := -1
	disabledWait := 0
	shortWait := 1
	maximumWait := 60
	overMaximumWait := 61
	testCases := []struct {
		name       string
		configured *int
		want       int
	}{
		{name: "omitted uses default", configured: nil, want: 15},
		{name: "negative uses default", configured: &negativeWait, want: 15},
		{name: "zero disables waiting", configured: &disabledWait, want: 0},
		{name: "short wait", configured: &shortWait, want: 1},
		{name: "maximum wait", configured: &maximumWait, want: 60},
		{name: "over maximum is clamped", configured: &overMaximumWait, want: 60},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cacheConfig := ClaudePromptCacheConfig{ColdStartMaxWaitSeconds: testCase.configured}
			if got := cacheConfig.EffectiveColdStartMaxWaitSeconds(); got != testCase.want {
				t.Fatalf("EffectiveColdStartMaxWaitSeconds() = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestParseConfigBytesClaudePromptCache(t *testing.T) {
	configuredWait := 12
	cfg, errParse := ParseConfigBytes([]byte(`
claude-prompt-cache:
  mode: adaptive
  cold-start-max-wait-seconds: 12
  diagnostics: true
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if cfg.ClaudePromptCache.EffectiveMode() != ClaudePromptCacheModeAdaptive {
		t.Fatalf("effective mode = %q, want %q", cfg.ClaudePromptCache.EffectiveMode(), ClaudePromptCacheModeAdaptive)
	}
	if cfg.ClaudePromptCache.ColdStartMaxWaitSeconds == nil {
		t.Fatal("ColdStartMaxWaitSeconds = nil, want non-nil")
	}
	if *cfg.ClaudePromptCache.ColdStartMaxWaitSeconds != configuredWait {
		t.Fatalf("ColdStartMaxWaitSeconds = %d, want %d", *cfg.ClaudePromptCache.ColdStartMaxWaitSeconds, configuredWait)
	}
	if !cfg.ClaudePromptCache.Diagnostics {
		t.Fatal("Diagnostics = false, want true")
	}
}
