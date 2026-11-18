package gemini

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestCleanGeminiCodexToolParametersPreservesCanonicalSchema(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`)

	output := cleanGeminiCodexToolParameters(util.ParseGJSONBytesNoCopy(input))

	if string(output) != string(input) {
		t.Fatalf("canonical schema changed:\n got: %s\nwant: %s", output, input)
	}
}

func TestSetCodexToolChoiceFromGeminiToolConfigReusesAutoChoice(t *testing.T) {
	input := []byte(`{"tool_choice":"auto","input":[]}`)
	config := ojson.Parse(`{"mode":"AUTO"}`)

	output := setCodexToolChoiceFromGeminiToolConfig(input, config)

	if &output[0] != &input[0] {
		t.Fatal("AUTO tool choice caused a payload copy")
	}
}

func TestCleanGeminiCodexToolParametersNormalizesSchema(t *testing.T) {
	input := []byte(`{"type":"object","$schema":"draft","additionalProperties":true}`)

	output := cleanGeminiCodexToolParameters(util.ParseGJSONBytesNoCopy(input))

	if ojson.GetBytesNoCopy(output, "$schema").Exists() {
		t.Fatal("$schema should be removed")
	}
	if additionalProperties := ojson.GetBytesNoCopy(output, "additionalProperties"); additionalProperties.Type != ojson.False {
		t.Fatalf("additionalProperties = %s, want false", additionalProperties.Raw)
	}
}
