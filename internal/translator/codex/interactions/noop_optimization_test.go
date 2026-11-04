package interactions

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestCleanedCodexToolParametersPreservesCanonicalSchema(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`)

	output := []byte(cleanedCodexToolParameters(util.ParseGJSONBytesNoCopy(input)))

	if string(output) != string(input) {
		t.Fatalf("canonical schema changed:\n got: %s\nwant: %s", output, input)
	}
}

func TestSetInteractionsCodexRawIfDifferentReusesMatchingValue(t *testing.T) {
	input := []byte(`{"tool_choice":"auto","input":[]}`)
	value := ojson.Parse(`"auto"`)

	output := setInteractionsCodexRawIfDifferent(input, "tool_choice", value)

	if &output[0] != &input[0] {
		t.Fatal("matching raw value caused a payload copy")
	}
}
