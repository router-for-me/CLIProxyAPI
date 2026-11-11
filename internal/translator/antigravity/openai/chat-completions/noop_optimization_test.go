package chat_completions

import "testing"

func TestNormalizeAntigravityOpenAIThinkingConfigReusesCanonicalConfig(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high","thinkingBudget":8192}}}}`)

	output := normalizeAntigravityOpenAIThinkingConfig(input)

	if &output[0] != &input[0] {
		t.Fatal("canonical thinking config caused a payload copy")
	}
}
