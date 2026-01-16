package test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	// Import provider packages to trigger init() registration of ProviderAppliers
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/geminicli"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/iflow"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestThinkingE2EMatrix tests the thinking configuration transformation using the real data flow path.
// Data flow: Input JSON → TranslateRequest → ApplyThinking → Validate Output
// No helper functions are used; all test data is inline.
func TestThinkingE2EMatrix(t *testing.T) {
	// Register test models directly
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-%d", time.Now().UnixNano())

	testModels := []*registry.ModelInfo{
		{
			ID:          "level-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "Level Model",
			Thinking: &registry.ThinkingSupport{
				Levels:         []string{"minimal", "low", "medium", "high"},
				ZeroAllowed:    false,
				DynamicAllowed: false,
			},
		},
		{
			ID:          "gemini-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Budget Model",
			Thinking: &registry.ThinkingSupport{
				Min:            128,
				Max:            20000,
				ZeroAllowed:    false,
				DynamicAllowed: true,
			},
		},
		{
			ID:          "gemini-mixed-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Mixed Model",
			Thinking: &registry.ThinkingSupport{
				Min:            128,
				Max:            32768,
				Levels:         []string{"low", "high"},
				ZeroAllowed:    false,
				DynamicAllowed: true,
			},
		},
		{
			ID:          "claude-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "claude",
			DisplayName: "Claude Budget Model",
			Thinking: &registry.ThinkingSupport{
				Min:            1024,
				Max:            128000,
				ZeroAllowed:    true,
				DynamicAllowed: false,
			},
		},
		{
			ID:          "antigravity-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini-cli",
			DisplayName: "Antigravity Budget Model",
			Thinking: &registry.ThinkingSupport{
				Min:            128,
				Max:            20000,
				ZeroAllowed:    true,
				DynamicAllowed: true,
			},
		},
		{
			ID:          "no-thinking-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "No Thinking Model",
			Thinking:    nil,
		},
		{
			ID:          "user-defined-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "User Defined Model",
			UserDefined: true,
			Thinking:    nil,
		},
	}

	reg.RegisterClient(uid, "test", testModels)
	defer reg.UnregisterClient(uid)

	type testCase struct {
		name            string
		from            string
		to              string
		modelSuffix     string
		inputJSON       string
		expectField     string
		expectValue     string
		includeThoughts string
		expectErr       bool
	}

	cases := []testCase{
		// level-model (Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false)
		// Case 1: No suffix, translator adds default medium for codex
		{
			name:        "1",
			from:        "openai",
			to:          "codex",
			modelSuffix: "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 2: Explicit medium level
		{
			name:        "2",
			from:        "openai",
			to:          "codex",
			modelSuffix: "level-model(medium)",
			inputJSON:   `{"model":"level-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 3: xhigh not in Levels=[minimal,low,medium,high] → ValidateConfig returns error
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			modelSuffix: "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 4: none → ModeNone, ZeroAllowed=false → clamp to min level (minimal)
		{
			name:        "4",
			from:        "openai",
			to:          "codex",
			modelSuffix: "level-model(none)",
			inputJSON:   `{"model":"level-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 5: auto → ModeAuto, DynamicAllowed=false → convert to mid-range (medium)
		{
			name:        "5",
			from:        "openai",
			to:          "codex",
			modelSuffix: "level-model(auto)",
			inputJSON:   `{"model":"level-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 6: No suffix from gemini → translator injects default reasoning.effort: medium
		{
			name:        "6",
			from:        "gemini",
			to:          "codex",
			modelSuffix: "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 7: 8192 → medium (1025-8192)
		{
			name:        "7",
			from:        "gemini",
			to:          "codex",
			modelSuffix: "level-model(8192)",
			inputJSON:   `{"model":"level-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 8: 64000 → xhigh → not supported → error
		{
			name:        "8",
			from:        "gemini",
			to:          "codex",
			modelSuffix: "level-model(64000)",
			inputJSON:   `{"model":"level-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 9: 0 → ModeNone, ZeroAllowed=false → clamp to min level (minimal)
		{
			name:        "9",
			from:        "gemini",
			to:          "codex",
			modelSuffix: "level-model(0)",
			inputJSON:   `{"model":"level-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 10: -1 → ModeAuto, DynamicAllowed=false → convert to mid-range (medium)
		{
			name:        "10",
			from:        "gemini",
			to:          "codex",
			modelSuffix: "level-model(-1)",
			inputJSON:   `{"model":"level-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 11: No suffix from claude → no thinking config
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			modelSuffix: "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 12: 8192 → medium
		{
			name:        "12",
			from:        "claude",
			to:          "openai",
			modelSuffix: "level-model(8192)",
			inputJSON:   `{"model":"level-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 13: 64000 → xhigh → not supported → error
		{
			name:        "13",
			from:        "claude",
			to:          "openai",
			modelSuffix: "level-model(64000)",
			inputJSON:   `{"model":"level-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 14: 0 → ModeNone, ZeroAllowed=false → clamp to min level (minimal)
		{
			name:        "14",
			from:        "claude",
			to:          "openai",
			modelSuffix: "level-model(0)",
			inputJSON:   `{"model":"level-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 15: -1 → ModeAuto, DynamicAllowed=false → convert to mid-range (medium)
		{
			name:        "15",
			from:        "claude",
			to:          "openai",
			modelSuffix: "level-model(-1)",
			inputJSON:   `{"model":"level-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},

		// gemini-budget-model (Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true)
		{
			name:        "16",
			from:        "openai",
			to:          "gemini",
			modelSuffix: "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// medium → 8192
		{
			name:            "17",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(medium)",
			inputJSON:       `{"model":"gemini-budget-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// xhigh → 32768 → clamp to 20000
		{
			name:            "18",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(xhigh)",
			inputJSON:       `{"model":"gemini-budget-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// none → 0 → ZeroAllowed=false → clamp to 128, includeThoughts=false
		{
			name:            "19",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(none)",
			inputJSON:       `{"model":"gemini-budget-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "128",
			includeThoughts: "false",
			expectErr:       false,
		},
		// auto → -1 dynamic allowed
		{
			name:            "20",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(auto)",
			inputJSON:       `{"model":"gemini-budget-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:        "21",
			from:        "claude",
			to:          "gemini",
			modelSuffix: "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:            "22",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(8192)",
			inputJSON:       `{"model":"gemini-budget-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:            "23",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:            "24",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(0)",
			inputJSON:       `{"model":"gemini-budget-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "128",
			includeThoughts: "false",
			expectErr:       false,
		},
		{
			name:            "25",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-budget-model(-1)",
			inputJSON:       `{"model":"gemini-budget-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// gemini-mixed-model (Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true)
		{
			name:        "26",
			from:        "openai",
			to:          "gemini",
			modelSuffix: "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// high → use thinkingLevel
		{
			name:            "27",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(high)",
			inputJSON:       `{"model":"gemini-mixed-model(high)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "high",
			includeThoughts: "true",
			expectErr:       false,
		},
		// xhigh → not in Levels=[low,high] → error
		{
			name:        "28",
			from:        "openai",
			to:          "gemini",
			modelSuffix: "gemini-mixed-model(xhigh)",
			inputJSON:   `{"model":"gemini-mixed-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// none → ModeNone, ZeroAllowed=false → set Level to lowest (low), includeThoughts=false
		{
			name:            "29",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(none)",
			inputJSON:       `{"model":"gemini-mixed-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "false",
			expectErr:       false,
		},
		// auto → dynamic allowed, use thinkingBudget=-1
		{
			name:            "30",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(auto)",
			inputJSON:       `{"model":"gemini-mixed-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:        "31",
			from:        "claude",
			to:          "gemini",
			modelSuffix: "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 8192 → ModeBudget → clamp (in range) → thinkingBudget: 8192
		{
			name:            "32",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(8192)",
			inputJSON:       `{"model":"gemini-mixed-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// 64000 → ModeBudget → clamp to 32768 → thinkingBudget: 32768
		{
			name:            "33",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(64000)",
			inputJSON:       `{"model":"gemini-mixed-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "32768",
			includeThoughts: "true",
			expectErr:       false,
		},
		// 0 → ModeNone, ZeroAllowed=false → set Level to lowest (low), includeThoughts=false
		{
			name:            "34",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(0)",
			inputJSON:       `{"model":"gemini-mixed-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "false",
			expectErr:       false,
		},
		// -1 → auto, dynamic allowed
		{
			name:            "35",
			from:            "claude",
			to:              "gemini",
			modelSuffix:     "gemini-mixed-model(-1)",
			inputJSON:       `{"model":"gemini-mixed-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// claude-budget-model (Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false)
		{
			name:        "36",
			from:        "openai",
			to:          "claude",
			modelSuffix: "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// medium → 8192
		{
			name:        "37",
			from:        "openai",
			to:          "claude",
			modelSuffix: "claude-budget-model(medium)",
			inputJSON:   `{"model":"claude-budget-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// xhigh → 32768
		{
			name:        "38",
			from:        "openai",
			to:          "claude",
			modelSuffix: "claude-budget-model(xhigh)",
			inputJSON:   `{"model":"claude-budget-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "32768",
			expectErr:   false,
		},
		// none → ZeroAllowed=true → disabled
		{
			name:        "39",
			from:        "openai",
			to:          "claude",
			modelSuffix: "claude-budget-model(none)",
			inputJSON:   `{"model":"claude-budget-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.type",
			expectValue: "disabled",
			expectErr:   false,
		},
		// auto → ModeAuto, DynamicAllowed=false → convert to mid-range
		{
			name:        "40",
			from:        "openai",
			to:          "claude",
			modelSuffix: "claude-budget-model(auto)",
			inputJSON:   `{"model":"claude-budget-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "64512",
			expectErr:   false,
		},
		{
			name:        "41",
			from:        "gemini",
			to:          "claude",
			modelSuffix: "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "42",
			from:        "gemini",
			to:          "claude",
			modelSuffix: "claude-budget-model(8192)",
			inputJSON:   `{"model":"claude-budget-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		{
			name:        "43",
			from:        "gemini",
			to:          "claude",
			modelSuffix: "claude-budget-model(200000)",
			inputJSON:   `{"model":"claude-budget-model(200000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "128000",
			expectErr:   false,
		},
		// 0 → ZeroAllowed=true → disabled
		{
			name:        "44",
			from:        "gemini",
			to:          "claude",
			modelSuffix: "claude-budget-model(0)",
			inputJSON:   `{"model":"claude-budget-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.type",
			expectValue: "disabled",
			expectErr:   false,
		},
		// -1 → auto → DynamicAllowed=false → mid-range
		{
			name:        "45",
			from:        "gemini",
			to:          "claude",
			modelSuffix: "claude-budget-model(-1)",
			inputJSON:   `{"model":"claude-budget-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "64512",
			expectErr:   false,
		},

		// antigravity-budget-model (Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true)
		{
			name:        "46",
			from:        "gemini",
			to:          "antigravity",
			modelSuffix: "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:            "47",
			from:            "gemini",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(medium)",
			inputJSON:       `{"model":"antigravity-budget-model(medium)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:            "48",
			from:            "gemini",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(xhigh)",
			inputJSON:       `{"model":"antigravity-budget-model(xhigh)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:            "49",
			from:            "gemini",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(none)",
			inputJSON:       `{"model":"antigravity-budget-model(none)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "0",
			includeThoughts: "false",
			expectErr:       false,
		},
		{
			name:            "50",
			from:            "gemini",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(auto)",
			inputJSON:       `{"model":"antigravity-budget-model(auto)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:        "51",
			from:        "claude",
			to:          "antigravity",
			modelSuffix: "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:            "52",
			from:            "claude",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(8192)",
			inputJSON:       `{"model":"antigravity-budget-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:            "53",
			from:            "claude",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(64000)",
			inputJSON:       `{"model":"antigravity-budget-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:            "54",
			from:            "claude",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(0)",
			inputJSON:       `{"model":"antigravity-budget-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "0",
			includeThoughts: "false",
			expectErr:       false,
		},
		{
			name:            "55",
			from:            "claude",
			to:              "antigravity",
			modelSuffix:     "antigravity-budget-model(-1)",
			inputJSON:       `{"model":"antigravity-budget-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// no-thinking-model (Thinking=nil)
		{
			name:        "46",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "47",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "no-thinking-model(8192)",
			inputJSON:   `{"model":"no-thinking-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "48",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "no-thinking-model(0)",
			inputJSON:   `{"model":"no-thinking-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "49",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "no-thinking-model(-1)",
			inputJSON:   `{"model":"no-thinking-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "50",
			from:        "claude",
			to:          "openai",
			modelSuffix: "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "51",
			from:        "claude",
			to:          "openai",
			modelSuffix: "no-thinking-model(8192)",
			inputJSON:   `{"model":"no-thinking-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "52",
			from:        "claude",
			to:          "openai",
			modelSuffix: "no-thinking-model(0)",
			inputJSON:   `{"model":"no-thinking-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		{
			name:        "53",
			from:        "claude",
			to:          "openai",
			modelSuffix: "no-thinking-model(-1)",
			inputJSON:   `{"model":"no-thinking-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model (UserDefined=true, Thinking=nil)
		{
			name:        "54",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 8192 → medium (passthrough for UserDefined)
		{
			name:        "55",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// 64000 → xhigh
		{
			name:        "56",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "user-defined-model(64000)",
			inputJSON:   `{"model":"user-defined-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// 0 → none
		{
			name:        "57",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "user-defined-model(0)",
			inputJSON:   `{"model":"user-defined-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "none",
			expectErr:   false,
		},
		// -1 → auto
		{
			name:        "58",
			from:        "gemini",
			to:          "openai",
			modelSuffix: "user-defined-model(-1)",
			inputJSON:   `{"model":"user-defined-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 59: No suffix from claude → translator injects default reasoning.effort: medium
		{
			name:        "59",
			from:        "claude",
			to:          "codex",
			modelSuffix: "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// 8192 → medium
		{
			name:        "60",
			from:        "claude",
			to:          "codex",
			modelSuffix: "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// 64000 → xhigh
		{
			name:        "61",
			from:        "claude",
			to:          "codex",
			modelSuffix: "user-defined-model(64000)",
			inputJSON:   `{"model":"user-defined-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// 0 → none
		{
			name:        "62",
			from:        "claude",
			to:          "codex",
			modelSuffix: "user-defined-model(0)",
			inputJSON:   `{"model":"user-defined-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "none",
			expectErr:   false,
		},
		// -1 → auto
		{
			name:        "63",
			from:        "claude",
			to:          "codex",
			modelSuffix: "user-defined-model(-1)",
			inputJSON:   `{"model":"user-defined-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// openai/codex → gemini/claude for user-defined-model
		{
			name:            "64",
			from:            "openai",
			to:              "gemini",
			modelSuffix:     "user-defined-model(8192)",
			inputJSON:       `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:        "65",
			from:        "openai",
			to:          "claude",
			modelSuffix: "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		{
			name:            "66",
			from:            "codex",
			to:              "gemini",
			modelSuffix:     "user-defined-model(8192)",
			inputJSON:       `{"model":"user-defined-model(8192)","input":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		{
			name:        "67",
			from:        "codex",
			to:          "claude",
			modelSuffix: "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
	}

	for _, tc := range cases {
		tc := tc
		testName := fmt.Sprintf("Case%s_%s->%s_%s", tc.name, tc.from, tc.to, tc.modelSuffix)
		t.Run(testName, func(t *testing.T) {
			// Real data flow path:
			// 1. Parse suffix to get base model
			suffixResult := thinking.ParseSuffix(tc.modelSuffix)
			baseModel := suffixResult.ModelName

			// 2. Translate request from source format to target format
			body := sdktranslator.TranslateRequest(
				sdktranslator.FromString(tc.from),
				sdktranslator.FromString(tc.to),
				baseModel,
				[]byte(tc.inputJSON),
				true,
			)

			// 3. Apply thinking configuration (main entry point)
			body, err := thinking.ApplyThinking(body, tc.modelSuffix, tc.to)

			// Validate results
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error but got none, body=%s", string(body))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v, body=%s", err, string(body))
			}

			// Check for expected field absence
			if tc.expectField == "" {
				var hasThinking bool
				switch tc.to {
				case "gemini":
					hasThinking = gjson.GetBytes(body, "generationConfig.thinkingConfig").Exists()
				case "claude":
					hasThinking = gjson.GetBytes(body, "thinking").Exists()
				case "openai":
					hasThinking = gjson.GetBytes(body, "reasoning_effort").Exists()
				case "codex":
					hasThinking = gjson.GetBytes(body, "reasoning.effort").Exists() || gjson.GetBytes(body, "reasoning").Exists()
				}
				if hasThinking {
					t.Fatalf("expected no thinking field but found one, body=%s", string(body))
				}
				return
			}

			// Check expected field value
			val := gjson.GetBytes(body, tc.expectField)
			if !val.Exists() {
				t.Fatalf("expected field %s not found, body=%s", tc.expectField, string(body))
			}

			actualValue := val.String()
			if val.Type == gjson.Number {
				actualValue = fmt.Sprintf("%d", val.Int())
			}
			if actualValue != tc.expectValue {
				t.Fatalf("field %s: expected %q, got %q, body=%s", tc.expectField, tc.expectValue, actualValue, string(body))
			}

			// Check includeThoughts for Gemini/Antigravity
			if tc.includeThoughts != "" && (tc.to == "gemini" || tc.to == "antigravity") {
				path := "generationConfig.thinkingConfig.includeThoughts"
				if tc.to == "antigravity" {
					path = "request.generationConfig.thinkingConfig.includeThoughts"
				}
				itVal := gjson.GetBytes(body, path)
				if !itVal.Exists() {
					t.Fatalf("expected includeThoughts field not found, body=%s", string(body))
				}
				actual := fmt.Sprintf("%v", itVal.Bool())
				if actual != tc.includeThoughts {
					t.Fatalf("includeThoughts: expected %s, got %s, body=%s", tc.includeThoughts, actual, string(body))
				}
			}
		})
	}
}
