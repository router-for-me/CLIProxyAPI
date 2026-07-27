package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestCleanGeminiCodexToolParametersPreservesCanonicalSchema(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`)

	output := cleanGeminiCodexToolParameters(gjson.ParseBytes(input))

	if string(output) != string(input) {
		t.Fatalf("canonical schema changed:\n got: %s\nwant: %s", output, input)
	}
}

func TestSetCodexToolChoiceFromGeminiToolConfigReusesAutoChoice(t *testing.T) {
	input := []byte(`{"tool_choice":"auto","input":[]}`)
	config := gjson.Parse(`{"mode":"AUTO"}`)

	output := setCodexToolChoiceFromGeminiToolConfig(input, config)

	if &output[0] != &input[0] {
		t.Fatal("AUTO tool choice caused a payload copy")
	}
}

func TestCleanGeminiCodexToolParametersNormalizesSchema(t *testing.T) {
	input := []byte(`{"type":"object","$schema":"draft","additionalProperties":true}`)

	output := cleanGeminiCodexToolParameters(gjson.ParseBytes(input))

	if gjson.GetBytes(output, "$schema").Exists() {
		t.Fatal("$schema should be removed")
	}
	if additionalProperties := gjson.GetBytes(output, "additionalProperties"); additionalProperties.Type != gjson.False {
		t.Fatalf("additionalProperties = %s, want false", additionalProperties.Raw)
	}
}
