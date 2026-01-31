package test

import (
	"fmt"
	"strings"
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
	"github.com/tidwall/sjson"
)

// thinkingTestCase represents a common test case structure for both suffix and body tests.
type thinkingTestCase struct {
	name            string
	from            string
	to              string
	model           string
	inputJSON       string
	expectField     string
	expectValue     string
	includeThoughts string
	expectErr       bool
}

// TestThinkingE2EMatrix_Suffix tests the thinking configuration transformation using model name suffix.
// Data flow: Input JSON → TranslateRequest → ApplyThinking → Validate Output
// No helper functions are used; all test data is inline.
func TestThinkingE2EMatrix_Suffix(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-suffix-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// level-model (Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 1: No suffix → injected default → medium
		{
			name:        "1",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 2: Specified medium → medium
		{
			name:        "2",
			from:        "openai",
			to:          "codex",
			model:       "level-model(medium)",
			inputJSON:   `{"model":"level-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 3: Specified xhigh → out of range error
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 4: Level none → clamped to minimal (ZeroAllowed=false)
		{
			name:        "4",
			from:        "openai",
			to:          "codex",
			model:       "level-model(none)",
			inputJSON:   `{"model":"level-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 5: Level auto → DynamicAllowed=false → medium (mid-range)
		{
			name:        "5",
			from:        "openai",
			to:          "codex",
			model:       "level-model(auto)",
			inputJSON:   `{"model":"level-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 6: No suffix from gemini → injected default → medium
		{
			name:        "6",
			from:        "gemini",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 7: Budget 8192 → medium
		{
			name:        "7",
			from:        "gemini",
			to:          "codex",
			model:       "level-model(8192)",
			inputJSON:   `{"model":"level-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 8: Budget 64000 → clamped to high
		{
			name:        "8",
			from:        "gemini",
			to:          "codex",
			model:       "level-model(64000)",
			inputJSON:   `{"model":"level-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 9: Budget 0 → clamped to minimal (ZeroAllowed=false)
		{
			name:        "9",
			from:        "gemini",
			to:          "codex",
			model:       "level-model(0)",
			inputJSON:   `{"model":"level-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 10: Budget -1 → auto → DynamicAllowed=false → medium (mid-range)
		{
			name:        "10",
			from:        "gemini",
			to:          "codex",
			model:       "level-model(-1)",
			inputJSON:   `{"model":"level-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 11: Claude source no suffix → passthrough (no thinking)
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 12: Budget 8192 → medium
		{
			name:        "12",
			from:        "claude",
			to:          "openai",
			model:       "level-model(8192)",
			inputJSON:   `{"model":"level-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 13: Budget 64000 → clamped to high
		{
			name:        "13",
			from:        "claude",
			to:          "openai",
			model:       "level-model(64000)",
			inputJSON:   `{"model":"level-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 14: Budget 0 → clamped to minimal (ZeroAllowed=false)
		{
			name:        "14",
			from:        "claude",
			to:          "openai",
			model:       "level-model(0)",
			inputJSON:   `{"model":"level-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 15: Budget -1 → auto → DynamicAllowed=false → medium (mid-range)
		{
			name:        "15",
			from:        "claude",
			to:          "openai",
			model:       "level-model(-1)",
			inputJSON:   `{"model":"level-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},

		// level-subset-model (Levels=low/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 16: Budget 8192 → medium → rounded down to low
		{
			name:        "16",
			from:        "gemini",
			to:          "openai",
			model:       "level-subset-model(8192)",
			inputJSON:   `{"model":"level-subset-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "low",
			expectErr:   false,
		},
		// Case 17: Budget 1 → minimal → clamped to low (min supported)
		{
			name:            "17",
			from:            "claude",
			to:              "gemini",
			model:           "level-subset-model(1)",
			inputJSON:       `{"model":"level-subset-model(1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "true",
			expectErr:       false,
		},

		// gemini-budget-model (Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true)

		// Case 18: No suffix → passthrough
		{
			name:        "18",
			from:        "openai",
			to:          "gemini",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 19: Effort medium → 8192
		{
			name:            "19",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model(medium)",
			inputJSON:       `{"model":"gemini-budget-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 20: Effort xhigh → clamped to 20000 (max)
		{
			name:            "20",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model(xhigh)",
			inputJSON:       `{"model":"gemini-budget-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 21: Effort none → clamped to 128 (min) → includeThoughts=false
		{
			name:            "21",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model(none)",
			inputJSON:       `{"model":"gemini-budget-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "128",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 22: Effort auto → DynamicAllowed=true → -1
		{
			name:            "22",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model(auto)",
			inputJSON:       `{"model":"gemini-budget-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 23: Claude source no suffix → passthrough
		{
			name:        "23",
			from:        "claude",
			to:          "gemini",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 24: Budget 8192 → 8192
		{
			name:            "24",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model(8192)",
			inputJSON:       `{"model":"gemini-budget-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 25: Budget 64000 → clamped to 20000 (max)
		{
			name:            "25",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 26: Budget 0 → clamped to 128 (min) → includeThoughts=false
		{
			name:            "26",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model(0)",
			inputJSON:       `{"model":"gemini-budget-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "128",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 27: Budget -1 → DynamicAllowed=true → -1
		{
			name:            "27",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model(-1)",
			inputJSON:       `{"model":"gemini-budget-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// gemini-mixed-model (Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true)

		// Case 28: OpenAI source no suffix → passthrough
		{
			name:        "28",
			from:        "openai",
			to:          "gemini",
			model:       "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 29: Effort high → low/high supported → high
		{
			name:            "29",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model(high)",
			inputJSON:       `{"model":"gemini-mixed-model(high)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "high",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 30: Effort xhigh → not in low/high → error
		{
			name:        "30",
			from:        "openai",
			to:          "gemini",
			model:       "gemini-mixed-model(xhigh)",
			inputJSON:   `{"model":"gemini-mixed-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 31: Effort none → clamped to low (min supported) → includeThoughts=false
		{
			name:            "31",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model(none)",
			inputJSON:       `{"model":"gemini-mixed-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 32: Effort auto → DynamicAllowed=true → -1 (budget)
		{
			name:            "32",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model(auto)",
			inputJSON:       `{"model":"gemini-mixed-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 33: Claude source no suffix → passthrough
		{
			name:        "33",
			from:        "claude",
			to:          "gemini",
			model:       "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 34: Budget 8192 → 8192 (keep budget)
		{
			name:            "34",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model(8192)",
			inputJSON:       `{"model":"gemini-mixed-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 35: Budget 64000 → clamped to 32768 (max)
		{
			name:            "35",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model(64000)",
			inputJSON:       `{"model":"gemini-mixed-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "32768",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 36: Budget 0 → minimal → clamped to low (min level) → includeThoughts=false
		{
			name:            "36",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model(0)",
			inputJSON:       `{"model":"gemini-mixed-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 37: Budget -1 → DynamicAllowed=true → -1 (budget)
		{
			name:            "37",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model(-1)",
			inputJSON:       `{"model":"gemini-mixed-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// claude-budget-model (Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false)

		// Case 38: OpenAI source no suffix → passthrough
		{
			name:        "38",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 39: Effort medium → 8192
		{
			name:        "39",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model(medium)",
			inputJSON:   `{"model":"claude-budget-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 40: Effort xhigh → clamped to 32768 (matrix value)
		{
			name:        "40",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model(xhigh)",
			inputJSON:   `{"model":"claude-budget-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "32768",
			expectErr:   false,
		},
		// Case 41: Effort none → ZeroAllowed=true → disabled
		{
			name:        "41",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model(none)",
			inputJSON:   `{"model":"claude-budget-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.type",
			expectValue: "disabled",
			expectErr:   false,
		},
		// Case 42: Effort auto → DynamicAllowed=false → 64512 (mid-range)
		{
			name:        "42",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model(auto)",
			inputJSON:   `{"model":"claude-budget-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "64512",
			expectErr:   false,
		},
		// Case 43: Gemini source no suffix → passthrough
		{
			name:        "43",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 44: Budget 8192 → 8192
		{
			name:        "44",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model(8192)",
			inputJSON:   `{"model":"claude-budget-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 45: Budget 200000 → clamped to 128000 (max)
		{
			name:        "45",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model(200000)",
			inputJSON:   `{"model":"claude-budget-model(200000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "128000",
			expectErr:   false,
		},
		// Case 46: Budget 0 → ZeroAllowed=true → disabled
		{
			name:        "46",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model(0)",
			inputJSON:   `{"model":"claude-budget-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.type",
			expectValue: "disabled",
			expectErr:   false,
		},
		// Case 47: Budget -1 → auto → DynamicAllowed=false → 64512 (mid-range)
		{
			name:        "47",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model(-1)",
			inputJSON:   `{"model":"claude-budget-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "64512",
			expectErr:   false,
		},

		// antigravity-budget-model (Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true)

		// Case 48: Gemini to Antigravity no suffix → passthrough
		{
			name:        "48",
			from:        "gemini",
			to:          "antigravity",
			model:       "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 49: Effort medium → 8192
		{
			name:            "49",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model(medium)",
			inputJSON:       `{"model":"antigravity-budget-model(medium)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 50: Effort xhigh → clamped to 20000 (max)
		{
			name:            "50",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model(xhigh)",
			inputJSON:       `{"model":"antigravity-budget-model(xhigh)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 51: Effort none → ZeroAllowed=true → 0 → includeThoughts=false
		{
			name:            "51",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model(none)",
			inputJSON:       `{"model":"antigravity-budget-model(none)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "0",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 52: Effort auto → DynamicAllowed=true → -1
		{
			name:            "52",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model(auto)",
			inputJSON:       `{"model":"antigravity-budget-model(auto)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 53: Claude to Antigravity no suffix → passthrough
		{
			name:        "53",
			from:        "claude",
			to:          "antigravity",
			model:       "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 54: Budget 8192 → 8192
		{
			name:            "54",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model(8192)",
			inputJSON:       `{"model":"antigravity-budget-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 55: Budget 64000 → clamped to 20000 (max)
		{
			name:            "55",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model(64000)",
			inputJSON:       `{"model":"antigravity-budget-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 56: Budget 0 → ZeroAllowed=true → 0 → includeThoughts=false
		{
			name:            "56",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model(0)",
			inputJSON:       `{"model":"antigravity-budget-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "0",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 57: Budget -1 → DynamicAllowed=true → -1
		{
			name:            "57",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model(-1)",
			inputJSON:       `{"model":"antigravity-budget-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// no-thinking-model (Thinking=nil)

		// Case 58: No thinking support → no configuration
		{
			name:        "58",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 59: Budget 8192 → no thinking support → suffix stripped → no configuration
		{
			name:        "59",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model(8192)",
			inputJSON:   `{"model":"no-thinking-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 60: Budget 0 → suffix stripped → no configuration
		{
			name:        "60",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model(0)",
			inputJSON:   `{"model":"no-thinking-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 61: Budget -1 → suffix stripped → no configuration
		{
			name:        "61",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model(-1)",
			inputJSON:   `{"model":"no-thinking-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 62: Claude source no suffix → no configuration
		{
			name:        "62",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 63: Budget 8192 → suffix stripped → no configuration
		{
			name:        "63",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(8192)",
			inputJSON:   `{"model":"no-thinking-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 64: Budget 0 → suffix stripped → no configuration
		{
			name:        "64",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(0)",
			inputJSON:   `{"model":"no-thinking-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 65: Budget -1 → suffix stripped → no configuration
		{
			name:        "65",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(-1)",
			inputJSON:   `{"model":"no-thinking-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model (UserDefined=true, Thinking=nil)

		// Case 66: User defined model no suffix → passthrough
		{
			name:        "66",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 67: Budget 8192 → passthrough logic → medium
		{
			name:        "67",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 68: Budget 64000 → passthrough logic → xhigh
		{
			name:        "68",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model(64000)",
			inputJSON:   `{"model":"user-defined-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 69: Budget 0 → passthrough logic → none
		{
			name:        "69",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model(0)",
			inputJSON:   `{"model":"user-defined-model(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 70: Budget -1 → passthrough logic → auto
		{
			name:        "70",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model(-1)",
			inputJSON:   `{"model":"user-defined-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 71: Claude to Codex no suffix → injected default → medium
		{
			name:        "71",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 72: Budget 8192 → passthrough logic → medium
		{
			name:        "72",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 73: Budget 64000 → passthrough logic → xhigh
		{
			name:        "73",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(64000)",
			inputJSON:   `{"model":"user-defined-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 74: Budget 0 → passthrough logic → none
		{
			name:        "74",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(0)",
			inputJSON:   `{"model":"user-defined-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 75: Budget -1 → passthrough logic → auto
		{
			name:        "75",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(-1)",
			inputJSON:   `{"model":"user-defined-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 76: OpenAI to Gemini budget 8192 → passthrough → 8192
		{
			name:            "76",
			from:            "openai",
			to:              "gemini",
			model:           "user-defined-model(8192)",
			inputJSON:       `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 77: OpenAI to Claude budget 8192 → passthrough → 8192
		{
			name:        "77",
			from:        "openai",
			to:          "claude",
			model:       "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 78: OpenAI-Response to Gemini budget 8192 → passthrough → 8192
		{
			name:            "78",
			from:            "openai-response",
			to:              "gemini",
			model:           "user-defined-model(8192)",
			inputJSON:       `{"model":"user-defined-model(8192)","input":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 79: OpenAI-Response to Claude budget 8192 → passthrough → 8192
		{
			name:        "79",
			from:        "openai-response",
			to:          "claude",
			model:       "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},

		// Same-protocol passthrough tests (80-89)

		// Case 80: OpenAI to OpenAI, level high → passthrough reasoning_effort
		{
			name:        "80",
			from:        "openai",
			to:          "openai",
			model:       "level-model(high)",
			inputJSON:   `{"model":"level-model(high)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 81: OpenAI to OpenAI, level xhigh → out of range error
		{
			name:        "81",
			from:        "openai",
			to:          "openai",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 82: OpenAI-Response to Codex, level high → passthrough reasoning.effort
		{
			name:        "82",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model(high)",
			inputJSON:   `{"model":"level-model(high)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 83: OpenAI-Response to Codex, level xhigh → out of range error
		{
			name:        "83",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 84: Gemini to Gemini, budget 8192 → passthrough thinkingBudget
		{
			name:            "84",
			from:            "gemini",
			to:              "gemini",
			model:           "gemini-budget-model(8192)",
			inputJSON:       `{"model":"gemini-budget-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 85: Gemini to Gemini, budget 64000 → clamped to Max
		{
			name:            "85",
			from:            "gemini",
			to:              "gemini",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 86: Claude to Claude, budget 8192 → passthrough thinking.budget_tokens
		{
			name:        "86",
			from:        "claude",
			to:          "claude",
			model:       "claude-budget-model(8192)",
			inputJSON:   `{"model":"claude-budget-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 87: Claude to Claude, budget 200000 → clamped to Max
		{
			name:        "87",
			from:        "claude",
			to:          "claude",
			model:       "claude-budget-model(200000)",
			inputJSON:   `{"model":"claude-budget-model(200000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "thinking.budget_tokens",
			expectValue: "128000",
			expectErr:   false,
		},
		// Case 88: Gemini-CLI to Antigravity, budget 8192 → passthrough thinkingBudget
		{
			name:            "88",
			from:            "gemini-cli",
			to:              "antigravity",
			model:           "antigravity-budget-model(8192)",
			inputJSON:       `{"model":"antigravity-budget-model(8192)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 89: Gemini-CLI to Antigravity, budget 64000 → clamped to Max
		{
			name:            "89",
			from:            "gemini-cli",
			to:              "antigravity",
			model:           "antigravity-budget-model(64000)",
			inputJSON:       `{"model":"antigravity-budget-model(64000)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},

		// iflow tests: glm-test and minimax-test (Cases 90-105)

		// glm-test (from: openai, claude)
		// Case 90: OpenAI to iflow, no suffix → passthrough
		{
			name:        "90",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 91: OpenAI to iflow, (medium) → enable_thinking=true
		{
			name:        "91",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test(medium)",
			inputJSON:   `{"model":"glm-test(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 92: OpenAI to iflow, (auto) → enable_thinking=true
		{
			name:        "92",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test(auto)",
			inputJSON:   `{"model":"glm-test(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 93: OpenAI to iflow, (none) → enable_thinking=false
		{
			name:        "93",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test(none)",
			inputJSON:   `{"model":"glm-test(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "false",
			expectErr:   false,
		},
		// Case 94: Claude to iflow, no suffix → passthrough
		{
			name:        "94",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 95: Claude to iflow, (8192) → enable_thinking=true
		{
			name:        "95",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test(8192)",
			inputJSON:   `{"model":"glm-test(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 96: Claude to iflow, (-1) → enable_thinking=true
		{
			name:        "96",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test(-1)",
			inputJSON:   `{"model":"glm-test(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 97: Claude to iflow, (0) → enable_thinking=false
		{
			name:        "97",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test(0)",
			inputJSON:   `{"model":"glm-test(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "false",
			expectErr:   false,
		},

		// minimax-test (from: openai, gemini)
		// Case 98: OpenAI to iflow, no suffix → passthrough
		{
			name:        "98",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 99: OpenAI to iflow, (medium) → reasoning_split=true
		{
			name:        "99",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test(medium)",
			inputJSON:   `{"model":"minimax-test(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 100: OpenAI to iflow, (auto) → reasoning_split=true
		{
			name:        "100",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test(auto)",
			inputJSON:   `{"model":"minimax-test(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 101: OpenAI to iflow, (none) → reasoning_split=false
		{
			name:        "101",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test(none)",
			inputJSON:   `{"model":"minimax-test(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_split",
			expectValue: "false",
			expectErr:   false,
		},
		// Case 102: Gemini to iflow, no suffix → passthrough
		{
			name:        "102",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 103: Gemini to iflow, (8192) → reasoning_split=true
		{
			name:        "103",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test(8192)",
			inputJSON:   `{"model":"minimax-test(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 104: Gemini to iflow, (-1) → reasoning_split=true
		{
			name:        "104",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test(-1)",
			inputJSON:   `{"model":"minimax-test(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 105: Gemini to iflow, (0) → reasoning_split=false
		{
			name:        "105",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test(0)",
			inputJSON:   `{"model":"minimax-test(0)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_split",
			expectValue: "false",
			expectErr:   false,
		},

		// Gemini Family Cross-Channel Consistency (Cases 106-114)
		// Tests that gemini/gemini-cli/antigravity as same API family should have consistent validation behavior

		// Case 106: Gemini to Antigravity, budget 64000 (suffix) → clamped to Max
		{
			name:            "106",
			from:            "gemini",
			to:              "antigravity",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 107: Gemini to Gemini-CLI, budget 64000 (suffix) → clamped to Max
		{
			name:            "107",
			from:            "gemini",
			to:              "gemini-cli",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 108: Gemini-CLI to Antigravity, budget 64000 (suffix) → clamped to Max
		{
			name:            "108",
			from:            "gemini-cli",
			to:              "antigravity",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 109: Gemini-CLI to Gemini, budget 64000 (suffix) → clamped to Max
		{
			name:            "109",
			from:            "gemini-cli",
			to:              "gemini",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 110: Gemini to Antigravity, budget 8192 → passthrough (normal value)
		{
			name:            "110",
			from:            "gemini",
			to:              "antigravity",
			model:           "gemini-budget-model(8192)",
			inputJSON:       `{"model":"gemini-budget-model(8192)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 111: Gemini-CLI to Antigravity, budget 8192 → passthrough (normal value)
		{
			name:            "111",
			from:            "gemini-cli",
			to:              "antigravity",
			model:           "gemini-budget-model(8192)",
			inputJSON:       `{"model":"gemini-budget-model(8192)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
	}

	runThinkingTests(t, cases)
}

// TestThinkingE2EMatrix_Body tests the thinking configuration transformation using request body parameters.
// Data flow: Input JSON with thinking params → TranslateRequest → ApplyThinking → Validate Output
func TestThinkingE2EMatrix_Body(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-body-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// level-model (Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 1: No param → injected default → medium
		{
			name:        "1",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 2: reasoning_effort=medium → medium
		{
			name:        "2",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 3: reasoning_effort=xhigh → out of range error
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 4: reasoning_effort=none → clamped to minimal
		{
			name:        "4",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 5: reasoning_effort=auto → medium (DynamicAllowed=false)
		{
			name:        "5",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 6: No param from gemini → injected default → medium
		{
			name:        "6",
			from:        "gemini",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 7: thinkingBudget=8192 → medium
		{
			name:        "7",
			from:        "gemini",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 8: thinkingBudget=64000 → clamped to high
		{
			name:        "8",
			from:        "gemini",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 9: thinkingBudget=0 → clamped to minimal
		{
			name:        "9",
			from:        "gemini",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 10: thinkingBudget=-1 → medium (DynamicAllowed=false)
		{
			name:        "10",
			from:        "gemini",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 11: Claude no param → passthrough (no thinking)
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 12: thinking.budget_tokens=8192 → medium
		{
			name:        "12",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 13: thinking.budget_tokens=64000 → clamped to high
		{
			name:        "13",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 14: thinking.budget_tokens=0 → clamped to minimal
		{
			name:        "14",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "reasoning_effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 15: thinking.budget_tokens=-1 → medium (DynamicAllowed=false)
		{
			name:        "15",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},

		// level-subset-model (Levels=low/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 16: thinkingBudget=8192 → medium → rounded down to low
		{
			name:        "16",
			from:        "gemini",
			to:          "openai",
			model:       "level-subset-model",
			inputJSON:   `{"model":"level-subset-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField: "reasoning_effort",
			expectValue: "low",
			expectErr:   false,
		},
		// Case 17: thinking.budget_tokens=1 → minimal → clamped to low
		{
			name:            "17",
			from:            "claude",
			to:              "gemini",
			model:           "level-subset-model",
			inputJSON:       `{"model":"level-subset-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "true",
			expectErr:       false,
		},

		// gemini-budget-model (Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true)

		// Case 18: No param → passthrough
		{
			name:        "18",
			from:        "openai",
			to:          "gemini",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 19: reasoning_effort=medium → 8192
		{
			name:            "19",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 20: reasoning_effort=xhigh → clamped to 20000
		{
			name:            "20",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 21: reasoning_effort=none → clamped to 128 → includeThoughts=false
		{
			name:            "21",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "128",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 22: reasoning_effort=auto → -1 (DynamicAllowed=true)
		{
			name:            "22",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 23: Claude no param → passthrough
		{
			name:        "23",
			from:        "claude",
			to:          "gemini",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 24: thinking.budget_tokens=8192 → 8192
		{
			name:            "24",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 25: thinking.budget_tokens=64000 → clamped to 20000
		{
			name:            "25",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 26: thinking.budget_tokens=0 → clamped to 128 → includeThoughts=false
		{
			name:            "26",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "128",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 27: thinking.budget_tokens=-1 → -1 (DynamicAllowed=true)
		{
			name:            "27",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// gemini-mixed-model (Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true)

		// Case 28: No param → passthrough
		{
			name:        "28",
			from:        "openai",
			to:          "gemini",
			model:       "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 29: reasoning_effort=high → high
		{
			name:            "29",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "high",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 30: reasoning_effort=xhigh → error (not in low/high)
		{
			name:        "30",
			from:        "openai",
			to:          "gemini",
			model:       "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 31: reasoning_effort=none → clamped to low → includeThoughts=false
		{
			name:            "31",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 32: reasoning_effort=auto → -1 (DynamicAllowed=true)
		{
			name:            "32",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 33: Claude no param → passthrough
		{
			name:        "33",
			from:        "claude",
			to:          "gemini",
			model:       "gemini-mixed-model",
			inputJSON:   `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 34: thinking.budget_tokens=8192 → 8192 (keeps budget)
		{
			name:            "34",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 35: thinking.budget_tokens=64000 → clamped to 32768 (keeps budget)
		{
			name:            "35",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "32768",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 36: thinking.budget_tokens=0 → clamped to low → includeThoughts=false
		{
			name:            "36",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "low",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 37: thinking.budget_tokens=-1 → -1 (DynamicAllowed=true)
		{
			name:            "37",
			from:            "claude",
			to:              "gemini",
			model:           "gemini-mixed-model",
			inputJSON:       `{"model":"gemini-mixed-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// claude-budget-model (Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false)

		// Case 38: No param → passthrough
		{
			name:        "38",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 39: reasoning_effort=medium → 8192
		{
			name:        "39",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 40: reasoning_effort=xhigh → clamped to 32768
		{
			name:        "40",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "thinking.budget_tokens",
			expectValue: "32768",
			expectErr:   false,
		},
		// Case 41: reasoning_effort=none → disabled
		{
			name:        "41",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField: "thinking.type",
			expectValue: "disabled",
			expectErr:   false,
		},
		// Case 42: reasoning_effort=auto → 64512 (mid-range)
		{
			name:        "42",
			from:        "openai",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField: "thinking.budget_tokens",
			expectValue: "64512",
			expectErr:   false,
		},
		// Case 43: Gemini no param → passthrough
		{
			name:        "43",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 44: thinkingBudget=8192 → 8192
		{
			name:        "44",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 45: thinkingBudget=200000 → clamped to 128000
		{
			name:        "45",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":200000}}}`,
			expectField: "thinking.budget_tokens",
			expectValue: "128000",
			expectErr:   false,
		},
		// Case 46: thinkingBudget=0 → disabled
		{
			name:        "46",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			expectField: "thinking.type",
			expectValue: "disabled",
			expectErr:   false,
		},
		// Case 47: thinkingBudget=-1 → 64512 (mid-range)
		{
			name:        "47",
			from:        "gemini",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			expectField: "thinking.budget_tokens",
			expectValue: "64512",
			expectErr:   false,
		},

		// antigravity-budget-model (Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true)

		// Case 48: Gemini no param → passthrough
		{
			name:        "48",
			from:        "gemini",
			to:          "antigravity",
			model:       "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 49: thinkingLevel=medium → 8192
		{
			name:            "49",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 50: thinkingLevel=xhigh → clamped to 20000
		{
			name:            "50",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"xhigh"}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 51: thinkingLevel=none → 0 (ZeroAllowed=true)
		{
			name:            "51",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"none"}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "0",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 52: thinkingBudget=-1 → -1 (DynamicAllowed=true)
		{
			name:            "52",
			from:            "gemini",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 53: Claude no param → passthrough
		{
			name:        "53",
			from:        "claude",
			to:          "antigravity",
			model:       "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 54: thinking.budget_tokens=8192 → 8192
		{
			name:            "54",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 55: thinking.budget_tokens=64000 → clamped to 20000
		{
			name:            "55",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 56: thinking.budget_tokens=0 → 0 (ZeroAllowed=true)
		{
			name:            "56",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "0",
			includeThoughts: "false",
			expectErr:       false,
		},
		// Case 57: thinking.budget_tokens=-1 → -1 (DynamicAllowed=true)
		{
			name:            "57",
			from:            "claude",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "-1",
			includeThoughts: "true",
			expectErr:       false,
		},

		// no-thinking-model (Thinking=nil)

		// Case 58: Gemini no param → passthrough
		{
			name:        "58",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 59: thinkingBudget=8192 → stripped
		{
			name:        "59",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 60: thinkingBudget=0 → stripped
		{
			name:        "60",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 61: thinkingBudget=-1 → stripped
		{
			name:        "61",
			from:        "gemini",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 62: Claude no param → passthrough
		{
			name:        "62",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 63: thinking.budget_tokens=8192 → stripped
		{
			name:        "63",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 64: thinking.budget_tokens=0 → stripped
		{
			name:        "64",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 65: thinking.budget_tokens=-1 → stripped
		{
			name:        "65",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model (UserDefined=true, Thinking=nil)

		// Case 66: Gemini no param → passthrough
		{
			name:        "66",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 67: thinkingBudget=8192 → medium
		{
			name:        "67",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 68: thinkingBudget=64000 → xhigh (passthrough)
		{
			name:        "68",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}`,
			expectField: "reasoning_effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 69: thinkingBudget=0 → none
		{
			name:        "69",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			expectField: "reasoning_effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 70: thinkingBudget=-1 → auto
		{
			name:        "70",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			expectField: "reasoning_effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 71: Claude no param → injected default → medium
		{
			name:        "71",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 72: thinking.budget_tokens=8192 → medium
		{
			name:        "72",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 73: thinking.budget_tokens=64000 → xhigh (passthrough)
		{
			name:        "73",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField: "reasoning.effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 74: thinking.budget_tokens=0 → none
		{
			name:        "74",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "reasoning.effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 75: thinking.budget_tokens=-1 → auto
		{
			name:        "75",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "reasoning.effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 76: OpenAI reasoning_effort=medium to Gemini → 8192
		{
			name:            "76",
			from:            "openai",
			to:              "gemini",
			model:           "user-defined-model",
			inputJSON:       `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 77: OpenAI reasoning_effort=medium to Claude → 8192
		{
			name:        "77",
			from:        "openai",
			to:          "claude",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 78: OpenAI-Response reasoning.effort=medium to Gemini → 8192
		{
			name:            "78",
			from:            "openai-response",
			to:              "gemini",
			model:           "user-defined-model",
			inputJSON:       `{"model":"user-defined-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"medium"}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 79: OpenAI-Response reasoning.effort=medium to Claude → 8192
		{
			name:        "79",
			from:        "openai-response",
			to:          "claude",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"medium"}}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},

		// Same-protocol passthrough tests (80-89)

		// Case 80: OpenAI to OpenAI, reasoning_effort=high → passthrough
		{
			name:        "80",
			from:        "openai",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 81: OpenAI to OpenAI, reasoning_effort=xhigh → out of range error
		{
			name:        "81",
			from:        "openai",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 82: OpenAI-Response to Codex, reasoning.effort=high → passthrough
		{
			name:        "82",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high"}}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 83: OpenAI-Response to Codex, reasoning.effort=xhigh → out of range error
		{
			name:        "83",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"xhigh"}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 84: Gemini to Gemini, thinkingBudget=8192 → passthrough
		{
			name:            "84",
			from:            "gemini",
			to:              "gemini",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField:     "generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 85: Gemini to Gemini, thinkingBudget=64000 → exceeds Max error
		{
			name:        "85",
			from:        "gemini",
			to:          "gemini",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 86: Claude to Claude, thinking.budget_tokens=8192 → passthrough
		{
			name:        "86",
			from:        "claude",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "thinking.budget_tokens",
			expectValue: "8192",
			expectErr:   false,
		},
		// Case 87: Claude to Claude, thinking.budget_tokens=200000 → exceeds Max error
		{
			name:        "87",
			from:        "claude",
			to:          "claude",
			model:       "claude-budget-model",
			inputJSON:   `{"model":"claude-budget-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":200000}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 88: Gemini-CLI to Antigravity, thinkingBudget=8192 → passthrough
		{
			name:            "88",
			from:            "gemini-cli",
			to:              "antigravity",
			model:           "antigravity-budget-model",
			inputJSON:       `{"model":"antigravity-budget-model","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 89: Gemini-CLI to Antigravity, thinkingBudget=64000 → exceeds Max error
		{
			name:        "89",
			from:        "gemini-cli",
			to:          "antigravity",
			model:       "antigravity-budget-model",
			inputJSON:   `{"model":"antigravity-budget-model","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}}`,
			expectField: "",
			expectErr:   true,
		},

		// iflow tests: glm-test and minimax-test (Cases 90-105)

		// glm-test (from: openai, claude)
		// Case 90: OpenAI to iflow, no param → passthrough
		{
			name:        "90",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 91: OpenAI to iflow, reasoning_effort=medium → enable_thinking=true
		{
			name:        "91",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 92: OpenAI to iflow, reasoning_effort=auto → enable_thinking=true
		{
			name:        "92",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 93: OpenAI to iflow, reasoning_effort=none → enable_thinking=false
		{
			name:        "93",
			from:        "openai",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "false",
			expectErr:   false,
		},
		// Case 94: Claude to iflow, no param → passthrough
		{
			name:        "94",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 95: Claude to iflow, thinking.budget_tokens=8192 → enable_thinking=true
		{
			name:        "95",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 96: Claude to iflow, thinking.budget_tokens=-1 → enable_thinking=true
		{
			name:        "96",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 97: Claude to iflow, thinking.budget_tokens=0 → enable_thinking=false
		{
			name:        "97",
			from:        "claude",
			to:          "iflow",
			model:       "glm-test",
			inputJSON:   `{"model":"glm-test","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "chat_template_kwargs.enable_thinking",
			expectValue: "false",
			expectErr:   false,
		},

		// minimax-test (from: openai, gemini)
		// Case 98: OpenAI to iflow, no param → passthrough
		{
			name:        "98",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 99: OpenAI to iflow, reasoning_effort=medium → reasoning_split=true
		{
			name:        "99",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 100: OpenAI to iflow, reasoning_effort=auto → reasoning_split=true
		{
			name:        "100",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 101: OpenAI to iflow, reasoning_effort=none → reasoning_split=false
		{
			name:        "101",
			from:        "openai",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField: "reasoning_split",
			expectValue: "false",
			expectErr:   false,
		},
		// Case 102: Gemini to iflow, no param → passthrough
		{
			name:        "102",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 103: Gemini to iflow, thinkingBudget=8192 → reasoning_split=true
		{
			name:        "103",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 104: Gemini to iflow, thinkingBudget=-1 → reasoning_split=true
		{
			name:        "104",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			expectField: "reasoning_split",
			expectValue: "true",
			expectErr:   false,
		},
		// Case 105: Gemini to iflow, thinkingBudget=0 → reasoning_split=false
		{
			name:        "105",
			from:        "gemini",
			to:          "iflow",
			model:       "minimax-test",
			inputJSON:   `{"model":"minimax-test","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			expectField: "reasoning_split",
			expectValue: "false",
			expectErr:   false,
		},

		// Gemini Family Cross-Channel Consistency (Cases 106-114)
		// Tests that gemini/gemini-cli/antigravity as same API family should have consistent validation behavior

		// Case 106: Gemini to Antigravity, thinkingBudget=64000 → exceeds Max error (same family strict validation)
		{
			name:        "106",
			from:        "gemini",
			to:          "antigravity",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 107: Gemini to Gemini-CLI, thinkingBudget=64000 → exceeds Max error (same family strict validation)
		{
			name:        "107",
			from:        "gemini",
			to:          "gemini-cli",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 108: Gemini-CLI to Antigravity, thinkingBudget=64000 → exceeds Max error (same family strict validation)
		{
			name:        "108",
			from:        "gemini-cli",
			to:          "antigravity",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 109: Gemini-CLI to Gemini, thinkingBudget=64000 → exceeds Max error (same family strict validation)
		{
			name:        "109",
			from:        "gemini-cli",
			to:          "gemini",
			model:       "gemini-budget-model",
			inputJSON:   `{"model":"gemini-budget-model","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 110: Gemini to Antigravity, thinkingBudget=8192 → passthrough (normal value)
		{
			name:            "110",
			from:            "gemini",
			to:              "antigravity",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 111: Gemini-CLI to Antigravity, thinkingBudget=8192 → passthrough (normal value)
		{
			name:            "111",
			from:            "gemini-cli",
			to:              "antigravity",
			model:           "gemini-budget-model",
			inputJSON:       `{"model":"gemini-budget-model","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
	}

	runThinkingTests(t, cases)
}

// getTestModels returns the shared model definitions for E2E tests.
func getTestModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{
			ID:          "level-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "Level Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}, ZeroAllowed: false, DynamicAllowed: false},
		},
		{
			ID:          "level-subset-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Level Subset Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "high"}, ZeroAllowed: false, DynamicAllowed: false},
		},
		{
			ID:          "gemini-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Budget Model",
			Thinking:    &registry.ThinkingSupport{Min: 128, Max: 20000, ZeroAllowed: false, DynamicAllowed: true},
		},
		{
			ID:          "gemini-mixed-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Mixed Model",
			Thinking:    &registry.ThinkingSupport{Min: 128, Max: 32768, Levels: []string{"low", "high"}, ZeroAllowed: false, DynamicAllowed: true},
		},
		{
			ID:          "claude-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "claude",
			DisplayName: "Claude Budget Model",
			Thinking:    &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: false},
		},
		{
			ID:          "antigravity-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini-cli",
			DisplayName: "Antigravity Budget Model",
			Thinking:    &registry.ThinkingSupport{Min: 128, Max: 20000, ZeroAllowed: true, DynamicAllowed: true},
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
		{
			ID:          "glm-test",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "iflow",
			DisplayName: "GLM Test Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"none", "auto", "minimal", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:          "minimax-test",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "iflow",
			DisplayName: "MiniMax Test Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"none", "auto", "minimal", "low", "medium", "high", "xhigh"}},
		},
	}
}

// runThinkingTests runs thinking test cases using the real data flow path.
func runThinkingTests(t *testing.T, cases []thinkingTestCase) {
	for _, tc := range cases {
		tc := tc
		testName := fmt.Sprintf("Case%s_%s->%s_%s", tc.name, tc.from, tc.to, tc.model)
		t.Run(testName, func(t *testing.T) {
			suffixResult := thinking.ParseSuffix(tc.model)
			baseModel := suffixResult.ModelName

			translateTo := tc.to
			applyTo := tc.to
			if tc.to == "iflow" {
				translateTo = "openai"
				applyTo = "iflow"
			}

			body := sdktranslator.TranslateRequest(
				sdktranslator.FromString(tc.from),
				sdktranslator.FromString(translateTo),
				baseModel,
				[]byte(tc.inputJSON),
				true,
			)
			if applyTo == "claude" {
				body, _ = sjson.SetBytes(body, "max_tokens", 200000)
			}

			body, err := thinking.ApplyThinking(body, tc.model, tc.from, applyTo, applyTo)

			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error but got none, body=%s", string(body))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v, body=%s", err, string(body))
			}

			if tc.expectField == "" {
				var hasThinking bool
				switch tc.to {
				case "gemini":
					hasThinking = gjson.GetBytes(body, "generationConfig.thinkingConfig").Exists()
				case "gemini-cli":
					hasThinking = gjson.GetBytes(body, "request.generationConfig.thinkingConfig").Exists()
				case "antigravity":
					hasThinking = gjson.GetBytes(body, "request.generationConfig.thinkingConfig").Exists()
				case "claude":
					hasThinking = gjson.GetBytes(body, "thinking").Exists()
				case "openai":
					hasThinking = gjson.GetBytes(body, "reasoning_effort").Exists()
				case "codex":
					hasThinking = gjson.GetBytes(body, "reasoning.effort").Exists() || gjson.GetBytes(body, "reasoning").Exists()
				case "iflow":
					hasThinking = gjson.GetBytes(body, "chat_template_kwargs.enable_thinking").Exists() || gjson.GetBytes(body, "reasoning_split").Exists()
				}
				if hasThinking {
					t.Fatalf("expected no thinking field but found one, body=%s", string(body))
				}
				return
			}

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

			if tc.includeThoughts != "" && (tc.to == "gemini" || tc.to == "gemini-cli" || tc.to == "antigravity") {
				path := "generationConfig.thinkingConfig.includeThoughts"
				if tc.to == "gemini-cli" || tc.to == "antigravity" {
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

			// Verify clear_thinking for iFlow GLM models when enable_thinking=true
			if tc.to == "iflow" && tc.expectField == "chat_template_kwargs.enable_thinking" && tc.expectValue == "true" {
				baseModel := thinking.ParseSuffix(tc.model).ModelName
				isGLM := strings.HasPrefix(strings.ToLower(baseModel), "glm")
				ctVal := gjson.GetBytes(body, "chat_template_kwargs.clear_thinking")
				if isGLM {
					if !ctVal.Exists() {
						t.Fatalf("expected clear_thinking field not found for GLM model, body=%s", string(body))
					}
					if ctVal.Bool() != false {
						t.Fatalf("clear_thinking: expected false, got %v, body=%s", ctVal.Bool(), string(body))
					}
				} else if ctVal.Exists() {
					t.Fatalf("expected no clear_thinking field for non-GLM enable_thinking model, body=%s", string(body))
				}
			}
		})
	}
}
