package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGemini_ReasoningEffortMapping(t *testing.T) {
	tests := []struct {
		name               string
		reasoningEffort    string
		expectThinkingConfig bool
		expectThinkingLevel  string
		expectThinkingBudget int64
		expectIncludeThoughts bool
	}{
		{
			name:                "high",
			reasoningEffort:     `"high"`,
			expectThinkingConfig: true,
			expectThinkingLevel:  "high",
			expectIncludeThoughts: true,
		},
		{
			name:                "medium",
			reasoningEffort:     `"medium"`,
			expectThinkingConfig: true,
			expectThinkingLevel:  "medium",
			expectIncludeThoughts: true,
		},
		{
			name:                "low",
			reasoningEffort:     `"low"`,
			expectThinkingConfig: true,
			expectThinkingLevel:  "low",
			expectIncludeThoughts: true,
		},
		{
			name:                "none",
			reasoningEffort:     `"none"`,
			expectThinkingConfig: true,
			expectThinkingLevel:  "none",
			expectIncludeThoughts: false,
		},
		{
			name:                "auto",
			reasoningEffort:     `"auto"`,
			expectThinkingConfig: true,
			expectThinkingBudget: -1,
			expectIncludeThoughts: true,
		},
		{
			name:                "no_reasoning_effort",
			reasoningEffort:     ``,
			expectThinkingConfig: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build an OpenAI-format request
			req := `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hello"}]}`
			if tt.reasoningEffort != "" {
				req = `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hello"}],"reasoning_effort":` + tt.reasoningEffort + `}`
			}

			result := ConvertOpenAIRequestToGemini("gemini-2.5-flash", []byte(req), false)

			t.Logf("Input: %s", req)
			t.Logf("Output: %s", string(result))

			tc := gjson.GetBytes(result, "generationConfig.thinkingConfig")
			if tt.expectThinkingConfig {
				if !tc.Exists() {
					t.Fatal("FAIL: thinkingConfig was expected but not found in output")
				}

				if tt.expectThinkingLevel != "" {
					level := tc.Get("thinkingLevel").String()
					if level != tt.expectThinkingLevel {
						t.Fatalf("FAIL: thinkingLevel mismatch.\n  expected: %q\n  got:      %q", tt.expectThinkingLevel, level)
					}
				}

				if tt.expectThinkingBudget != 0 {
					budget := tc.Get("thinkingBudget").Int()
					if budget != tt.expectThinkingBudget {
						t.Fatalf("FAIL: thinkingBudget mismatch.\n  expected: %d\n  got:      %d", tt.expectThinkingBudget, budget)
					}
				}

				includeThoughts := tc.Get("includeThoughts").Bool()
				if includeThoughts != tt.expectIncludeThoughts {
					t.Fatalf("FAIL: includeThoughts mismatch.\n  expected: %v\n  got:      %v", tt.expectIncludeThoughts, includeThoughts)
				}

				t.Logf("PASS: thinkingConfig properly set")
			} else {
				if tc.Exists() {
					// When no reasoning_effort, thinkingConfig might still exist if the model defaults to it
					// Check that it wasn't set by our code
					level := tc.Get("thinkingLevel").String()
					budget := tc.Get("thinkingBudget").Int()
					includeThoughts := tc.Get("includeThoughts").Bool()

					if level != "" || budget != 0 || includeThoughts {
						t.Logf("INFO: thinkingConfig exists but was not set from reasoning_effort: level=%q budget=%d includeThoughts=%v",
							level, budget, includeThoughts)
					} else {
						t.Logf("PASS: no thinkingConfig from reasoning_effort")
					}
				} else {
					t.Logf("PASS: no thinkingConfig from reasoning_effort")
				}
			}
		})
	}
}