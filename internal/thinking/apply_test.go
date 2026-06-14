package thinking

import (
	"testing"
)

func TestParseSuffixToConfig(t *testing.T) {
	tests := []struct {
		name       string
		rawSuffix  string
		provider   string
		model      string
		wantMode   ThinkingMode
		wantBudget int
		wantLevel  ThinkingLevel
	}{
		{name: "none special value", rawSuffix: "none", wantMode: ModeNone, wantBudget: 0, wantLevel: ""},
		{name: "auto special value", rawSuffix: "auto", wantMode: ModeAuto, wantBudget: -1, wantLevel: ""},
		{name: "-1 as auto", rawSuffix: "-1", wantMode: ModeAuto, wantBudget: -1, wantLevel: ""},
		{name: "level high", rawSuffix: "high", wantMode: ModeLevel, wantBudget: 0, wantLevel: "high"},
		{name: "level medium", rawSuffix: "medium", wantMode: ModeLevel, wantBudget: 0, wantLevel: "medium"},
		{name: "numeric budget", rawSuffix: "8192", wantMode: ModeBudget, wantBudget: 8192, wantLevel: ""},
		{name: "numeric zero", rawSuffix: "0", wantMode: ModeNone, wantBudget: 0, wantLevel: ""},
		{name: "unknown returns empty config", rawSuffix: "unknown", wantMode: ModeBudget, wantBudget: 0, wantLevel: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSuffixToConfig(tt.rawSuffix, tt.provider, tt.model)
			if got.Mode != tt.wantMode {
				t.Errorf("Mode = %v, want %v", got.Mode, tt.wantMode)
			}
			if got.Budget != tt.wantBudget {
				t.Errorf("Budget = %d, want %d", got.Budget, tt.wantBudget)
			}
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
		})
	}
}

func TestHasThinkingConfig(t *testing.T) {
	tests := []struct {
		name   string
		config ThinkingConfig
		want   bool
	}{
		{name: "empty config", config: ThinkingConfig{}, want: false},
		{name: "ModeNone with Budget=0", config: ThinkingConfig{Mode: ModeNone, Budget: 0}, want: true},
		{name: "ModeBudget with Budget=0", config: ThinkingConfig{Mode: ModeBudget, Budget: 0}, want: false},
		{name: "ModeBudget with Budget>0", config: ThinkingConfig{Mode: ModeBudget, Budget: 8192}, want: true},
		{name: "ModeLevel with Level set", config: ThinkingConfig{Mode: ModeLevel, Level: "high"}, want: true},
		{name: "ModeAuto with Budget=-1", config: ThinkingConfig{Mode: ModeAuto, Budget: -1}, want: true},
		{name: "ModeNone with Level set", config: ThinkingConfig{Mode: ModeNone, Level: "none"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasThinkingConfig(tt.config); got != tt.want {
				t.Errorf("hasThinkingConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeUserDefinedConfig(t *testing.T) {
	tests := []struct {
		name       string
		config     ThinkingConfig
		fromFormat string
		toFormat   string
		wantMode   ThinkingMode
		wantBudget int
		wantLevel  ThinkingLevel
	}{
		{
			name:       "ModeBudget preserved",
			config:     ThinkingConfig{Mode: ModeBudget, Budget: 8192},
			toFormat:   "openai",
			wantMode:   ModeBudget,
			wantBudget: 8192,
			wantLevel:  "",
		},
		{
			name:       "ModeLevel to claude preserved",
			config:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
			toFormat:   "claude",
			wantMode:   ModeLevel,
			wantBudget: 0,
			wantLevel:  LevelHigh,
		},
		{
			name:       "ModeLevel to non-budget provider preserved",
			config:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
			toFormat:   "openai",
			wantMode:   ModeLevel,
			wantBudget: 0,
			wantLevel:  LevelHigh,
		},
		{
			name:       "ModeLevel to gemini converts to budget",
			config:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
			toFormat:   "gemini",
			wantMode:   ModeBudget,
			wantBudget: 24576,
			wantLevel:  "",
		},
		{
			name:       "ModeLevel to gemini-cli converts to budget",
			config:     ThinkingConfig{Mode: ModeLevel, Level: LevelMedium},
			toFormat:   "gemini-cli",
			wantMode:   ModeBudget,
			wantBudget: 8192,
			wantLevel:  "",
		},
		{
			name:       "ModeLevel to antigravity converts to budget",
			config:     ThinkingConfig{Mode: ModeLevel, Level: LevelLow},
			toFormat:   "antigravity",
			wantMode:   ModeBudget,
			wantBudget: 1024,
			wantLevel:  "",
		},
		{
			name:       "ModeNone preserved",
			config:     ThinkingConfig{Mode: ModeNone, Budget: 0},
			toFormat:   "gemini",
			wantMode:   ModeNone,
			wantBudget: 0,
			wantLevel:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeUserDefinedConfig(tt.config, tt.fromFormat, tt.toFormat)
			if got.Mode != tt.wantMode {
				t.Errorf("Mode = %v, want %v", got.Mode, tt.wantMode)
			}
			if got.Budget != tt.wantBudget {
				t.Errorf("Budget = %d, want %d", got.Budget, tt.wantBudget)
			}
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
		})
	}
}

func TestExtractThinkingConfig(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		provider string
		wantMode ThinkingMode
		wantVal  int // budget for ModeBudget, ignored otherwise
		wantLvl  ThinkingLevel
	}{
		// Claude
		{name: "claude disabled", body: []byte(`{"thinking":{"type":"disabled"}}`), provider: "claude", wantMode: ModeNone, wantVal: 0},
		{name: "claude enabled with budget", body: []byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`), provider: "claude", wantMode: ModeBudget, wantVal: 8192},
		{name: "claude enabled auto", body: []byte(`{"thinking":{"type":"enabled","budget_tokens":-1}}`), provider: "claude", wantMode: ModeAuto, wantVal: -1},
		{name: "claude enabled no budget", body: []byte(`{"thinking":{"type":"enabled"}}`), provider: "claude", wantMode: ModeAuto, wantVal: -1},
		{name: "claude adaptive with effort", body: []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`), provider: "claude", wantMode: ModeLevel, wantLvl: "high"},
		{name: "claude adaptive no effort", body: []byte(`{"thinking":{"type":"adaptive"},"output_config":{}}`), provider: "claude", wantMode: ModeBudget, wantVal: 0},
		{name: "claude no thinking", body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`), provider: "claude", wantMode: ModeBudget, wantVal: 0},
		// Gemini
		{name: "gemini thinkingLevel high", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`), provider: "gemini", wantMode: ModeLevel, wantLvl: "high"},
		{name: "gemini thinkingLevel none", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"none"}}}`), provider: "gemini", wantMode: ModeNone, wantVal: 0},
		{name: "gemini thinkingBudget 8192", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`), provider: "gemini", wantMode: ModeBudget, wantVal: 8192},
		{name: "gemini thinkingBudget -1", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`), provider: "gemini", wantMode: ModeAuto, wantVal: -1},
		{name: "gemini snake_case thinking_level", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinking_level":"medium"}}}`), provider: "gemini", wantMode: ModeLevel, wantLvl: "medium"},
		{name: "gemini snake_case thinking_budget", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinking_budget":4096}}}`), provider: "gemini", wantMode: ModeBudget, wantVal: 4096},
		// Gemini CLI / Antigravity
		{name: "gemini-cli thinkingLevel", body: []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}}`), provider: "gemini-cli", wantMode: ModeLevel, wantLvl: "low"},
		{name: "gemini-cli thinkingBudget", body: []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":16384}}}}`), provider: "gemini-cli", wantMode: ModeBudget, wantVal: 16384},
		{name: "antigravity thinkingLevel", body: []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}}`), provider: "antigravity", wantMode: ModeLevel, wantLvl: "high"},
		// OpenAI
		{name: "openai reasoning_effort high", body: []byte(`{"reasoning_effort":"high"}`), provider: "openai", wantMode: ModeLevel, wantLvl: "high"},
		{name: "openai reasoning_effort none", body: []byte(`{"reasoning_effort":"none"}`), provider: "openai", wantMode: ModeNone, wantVal: 0},
		{name: "openai reasoning_effort auto", body: []byte(`{"reasoning_effort":"auto"}`), provider: "openai", wantMode: ModeAuto, wantVal: -1},
		// Codex / xAI
		{name: "codex reasoning.effort medium", body: []byte(`{"reasoning":{"effort":"medium"}}`), provider: "codex", wantMode: ModeLevel, wantLvl: "medium"},
		{name: "codex reasoning.effort none", body: []byte(`{"reasoning":{"effort":"none"}}`), provider: "codex", wantMode: ModeNone, wantVal: 0},
		{name: "xai reasoning.effort high", body: []byte(`{"reasoning":{"effort":"high"}}`), provider: "xai", wantMode: ModeLevel, wantLvl: "high"},
		// Kimi / DeepSeek (OpenAI-compatible)
		{name: "kimi reasoning_effort high", body: []byte(`{"reasoning_effort":"high"}`), provider: "kimi", wantMode: ModeLevel, wantLvl: "high"},
		{name: "deepseek reasoning_effort medium", body: []byte(`{"reasoning_effort":"medium"}`), provider: "deepseek", wantMode: ModeLevel, wantLvl: "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractThinkingConfig(tt.body, tt.provider)
			if got.Mode != tt.wantMode {
				t.Errorf("Mode = %v, want %v", got.Mode, tt.wantMode)
			}
			if tt.wantMode == ModeBudget && got.Budget != tt.wantVal {
				t.Errorf("Budget = %d, want %d", got.Budget, tt.wantVal)
			}
			if tt.wantMode == ModeLevel && got.Level != tt.wantLvl {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLvl)
			}
		})
	}
}

func TestExtractThinkingConfig_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		provider string
	}{
		{name: "empty body", body: []byte{}, provider: "claude"},
		{name: "invalid JSON", body: []byte(`{invalid}`), provider: "claude"},
		{name: "unknown provider", body: []byte(`{"reasoning_effort":"high"}`), provider: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractThinkingConfig(tt.body, tt.provider)
			if got.Mode != ModeBudget || got.Budget != 0 || got.Level != "" {
				t.Errorf("expected empty config, got Mode=%v Budget=%d Level=%q", got.Mode, got.Budget, got.Level)
			}
		})
	}
}

func TestReasoningEffortFromConfig(t *testing.T) {
	tests := []struct {
		name   string
		config ThinkingConfig
		want   string
	}{
		{name: "empty config", config: ThinkingConfig{}, want: ""},
		{name: "ModeNone", config: ThinkingConfig{Mode: ModeNone, Budget: 0}, want: "none"},
		{name: "ModeAuto", config: ThinkingConfig{Mode: ModeAuto, Budget: -1}, want: "auto"},
		{name: "ModeLevel high", config: ThinkingConfig{Mode: ModeLevel, Level: "high"}, want: "high"},
		{name: "ModeLevel Max", config: ThinkingConfig{Mode: ModeLevel, Level: "Max"}, want: "max"},
		{name: "ModeBudget 8192", config: ThinkingConfig{Mode: ModeBudget, Budget: 8192}, want: "medium"},
		{name: "ModeBudget 512", config: ThinkingConfig{Mode: ModeBudget, Budget: 512}, want: "minimal"},
		{name: "ModeBudget 0 (empty sentinel)", config: ThinkingConfig{Mode: ModeBudget, Budget: 0}, want: ""},
		{name: "ModeBudget -1", config: ThinkingConfig{Mode: ModeBudget, Budget: -1}, want: "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reasoningEffortFromConfig(tt.config); got != tt.want {
				t.Errorf("reasoningEffortFromConfig() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReasoningEffortFromSuffix(t *testing.T) {
	tests := []struct {
		name   string
		suffix SuffixResult
		want   string
	}{
		{name: "no suffix", suffix: SuffixResult{HasSuffix: false}, want: ""},
		{name: "level high", suffix: SuffixResult{HasSuffix: true, RawSuffix: "high"}, want: "high"},
		{name: "budget 8192", suffix: SuffixResult{HasSuffix: true, RawSuffix: "8192"}, want: "medium"},
		{name: "none", suffix: SuffixResult{HasSuffix: true, RawSuffix: "none"}, want: "none"},
		{name: "auto", suffix: SuffixResult{HasSuffix: true, RawSuffix: "auto"}, want: "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reasoningEffortFromSuffix(tt.suffix); got != tt.want {
				t.Errorf("reasoningEffortFromSuffix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractReasoningEffort(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		provider string
		model    string
		want     string
	}{
		{name: "suffix overrides body", body: []byte(`{"reasoning_effort":"low"}`), provider: "openai", model: "gpt-5.4(high)", want: "high"},
		{name: "claude budget to level", body: []byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`), provider: "claude", model: "claude-sonnet-4-5", want: "medium"},
		{name: "openai-response codex format", body: []byte(`{"reasoning":{"effort":"medium"}}`), provider: "openai-response", model: "gpt-5.4", want: "medium"},
		{name: "no config", body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`), provider: "openai", model: "gpt-5.4", want: ""},
		{name: "openai body effort", body: []byte(`{"reasoning_effort":"high"}`), provider: "openai", model: "gpt-5.4", want: "high"},
		{name: "codex body effort", body: []byte(`{"reasoning":{"effort":"low"}}`), provider: "codex", model: "codex-model", want: "low"},
		{name: "gemini body budget", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":24576}}}`), provider: "gemini", model: "gemini-model", want: "high"},
		{name: "gemini body level", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`), provider: "gemini", model: "gemini-model", want: "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractReasoningEffort(tt.body, tt.provider, tt.model); got != tt.want {
				t.Errorf("ExtractReasoningEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractTranslatedReasoningEffort(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		provider string
		want     string
	}{
		{name: "claude budget to level", body: []byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`), provider: "claude", want: "medium"},
		{name: "gemini level", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`), provider: "gemini", want: "high"},
		{name: "openai reasoning_effort", body: []byte(`{"reasoning_effort":"low"}`), provider: "openai", want: "low"},
		{name: "codex reasoning.effort", body: []byte(`{"reasoning":{"effort":"medium"}}`), provider: "codex", want: "medium"},
		{name: "openai-response from codex falls back", body: []byte(`{"reasoning":{"effort":"high"}}`), provider: "openai-response", want: "high"},
		{name: "no config", body: []byte(`{"messages":[]}`), provider: "claude", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractTranslatedReasoningEffort(tt.body, tt.provider); got != tt.want {
				t.Errorf("ExtractTranslatedReasoningEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}
