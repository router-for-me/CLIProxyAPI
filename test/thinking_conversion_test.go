package test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator"

	// Import provider packages to trigger init() registration of ProviderAppliers
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/antigravity"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/claude"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/codex"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/gemini"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/geminicli"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/iflow"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/kimi"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/minimax"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking/provider/openai"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/registry"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
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
	expectField2    string
	expectValue2    string
	includeThoughts string
	expectErr       bool
}

func getTestModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{ID: "level-model"},
		{ID: "budget-model"},
		{ID: "dynamic-model"},
		{ID: "zero-model"},
		{ID: "gpt-5-codex"},
		{ID: "gpt-5.2-codex"},
	}
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
		// Case 14: Budget 0 → clamped to low (OpenAI doesn't support minimal)
		{
			name:        "14",
			from:        "claude",
			to:          "openai",
			model:       "level-model(0)",
			inputJSON:   `{"model":"level-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "low",
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
		// Case 30: Effort xhigh → clamped to high
		{
			name:            "30",
			from:            "openai",
			to:              "gemini",
			model:           "gemini-mixed-model(xhigh)",
			inputJSON:       `{"model":"gemini-mixed-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField:     "generationConfig.thinkingConfig.thinkingLevel",
			expectValue:     "high",
			includeThoughts: "true",
			expectErr:       false,
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
		// Case 68: Budget 64000 → high (OpenAI doesn't support xhigh)
		{
			name:        "68",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model(64000)",
			inputJSON:   `{"model":"user-defined-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "high",
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
		// Case 70: Budget -1 → medium (OpenAI maps auto to medium)
		{
			name:        "70",
			from:        "gemini",
			to:          "openai",
			model:       "user-defined-model(-1)",
			inputJSON:   `{"model":"user-defined-model(-1)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
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
		// Case 88: Antigravity to Antigravity, budget 8192 → passthrough thinkingBudget
		{
			name:            "88",
			from:            "antigravity",
			to:              "antigravity",
			model:           "antigravity-budget-model(8192)",
			inputJSON:       `{"model":"antigravity-budget-model(8192)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "8192",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 89: Antigravity to Antigravity, budget 64000 → clamped to Max
		{
			name:            "89",
			from:            "antigravity",
			to:              "antigravity",
			model:           "antigravity-budget-model(64000)",
			inputJSON:       `{"model":"antigravity-budget-model(64000)","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},

		// Gemini Family Cross-Channel Consistency (Cases 90-95)
		// Tests that gemini/antigravity as same API family should have consistent validation behavior

		// Case 90: Gemini to Antigravity, budget 64000 (suffix) → clamped to Max
		{
			name:            "90",
			from:            "gemini",
			to:              "antigravity",
			model:           "gemini-budget-model(64000)",
			inputJSON:       `{"model":"gemini-budget-model(64000)","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			expectField:     "request.generationConfig.thinkingConfig.thinkingBudget",
			expectValue:     "20000",
			includeThoughts: "true",
			expectErr:       false,
		},
		// Case 94: Gemini to Antigravity, budget 8192 → passthrough (normal value)
		{
			name:            "94",
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

		// GitHub Copilot tests: gpt-5, gpt-5.1, gpt-5.2 (Levels=low/medium/high, some with none/xhigh)
		// Testing /chat/completions endpoint (openai format) - with suffix

		// Case 112: OpenAI to gpt-5, level high → high
		{
			name:        "112",
			from:        "openai",
			to:          "github-copilot",
			model:       "gpt-5(high)",
			inputJSON:   `{"model":"gpt-5(high)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 113: OpenAI to gpt-5, level none → clamped to low (ZeroAllowed=false)
		{
			name:        "113",
			from:        "openai",
			to:          "github-copilot",
			model:       "gpt-5(none)",
			inputJSON:   `{"model":"gpt-5(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "low",
			expectErr:   false,
		},
		// Case 114: OpenAI to gpt-5.1, level none → none (ZeroAllowed=true)
		{
			name:        "114",
			from:        "openai",
			to:          "github-copilot",
			model:       "gpt-5.1(none)",
			inputJSON:   `{"model":"gpt-5.1(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 115: OpenAI to gpt-5.2, level xhigh → xhigh (gpt-5.2 supports xhigh)
		{
			name:        "115",
			from:        "openai",
			to:          "github-copilot",
			model:       "gpt-5.2(xhigh)",
			inputJSON:   `{"model":"gpt-5.2(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 116: OpenAI to gpt-5, level xhigh (out of range) → error
		{
			name:        "116",
			from:        "openai",
			to:          "github-copilot",
			model:       "gpt-5(xhigh)",
			inputJSON:   `{"model":"gpt-5(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 117: Claude to gpt-5.1, budget 0 → none (ZeroAllowed=true)
		{
			name:        "117",
			from:        "claude",
			to:          "github-copilot",
			model:       "gpt-5.1(0)",
			inputJSON:   `{"model":"gpt-5.1(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "none",
			expectErr:   false,
		},

		// GitHub Copilot tests: /responses endpoint (codex format) - with suffix

		// Case 118: OpenAI-Response to gpt-5-codex, level high → high
		{
			name:        "118",
			from:        "openai-response",
			to:          "github-copilot",
			model:       "gpt-5-codex(high)",
			inputJSON:   `{"model":"gpt-5-codex(high)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 119: OpenAI-Response to gpt-5.2-codex, level xhigh → xhigh
		{
			name:        "119",
			from:        "openai-response",
			to:          "github-copilot",
			model:       "gpt-5.2-codex(xhigh)",
			inputJSON:   `{"model":"gpt-5.2-codex(xhigh)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 120: OpenAI-Response to gpt-5.2-codex, level none → none
		{
			name:        "120",
			from:        "openai-response",
			to:          "github-copilot",
			model:       "gpt-5.2-codex(none)",
			inputJSON:   `{"model":"gpt-5.2-codex(none)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 121: OpenAI-Response to gpt-5-codex, level none → clamped to low (ZeroAllowed=false)
		{
			name:        "121",
			from:        "openai-response",
			to:          "github-copilot",
			model:       "gpt-5-codex(none)",
			inputJSON:   `{"model":"gpt-5-codex(none)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "low",
			expectErr:   false,
		},
	}
	runThinkingTests(t, cases)
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
			switch applyTo {
			case "kimi":
				translateTo = "openai"
			case "xai":
				translateTo = "codex"
			}
			if tc.to == "github-copilot" {
				if tc.from == "openai-response" {
					translateTo = "codex"
					applyTo = "codex"
				} else {
					translateTo = "openai"
					applyTo = "openai"
				}
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
				case "antigravity":
					hasThinking = gjson.GetBytes(body, "request.generationConfig.thinkingConfig").Exists()
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

			assertField := func(fieldPath, expected string) {
				val := gjson.GetBytes(body, fieldPath)
				if !val.Exists() {
					t.Fatalf("expected field %s not found, body=%s", fieldPath, string(body))
				}
				actualValue := val.String()
				if val.Type == gjson.Number {
					actualValue = fmt.Sprintf("%d", val.Int())
				}
				if actualValue != expected {
					t.Fatalf("field %s: expected %q, got %q, body=%s", fieldPath, expected, actualValue, string(body))
				}
			}

			assertField(tc.expectField, tc.expectValue)
			if tc.expectField2 != "" {
				assertField(tc.expectField2, tc.expectValue2)
			}

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
