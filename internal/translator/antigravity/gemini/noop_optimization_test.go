package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRewriteGeminiFunctionNamesReusesNormalizedPayload(t *testing.T) {
	input := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"ok"}}}]}],"toolConfig":{"functionCallingConfig":{"allowedFunctionNames":["lookup"]}}}}`)

	output := rewriteGeminiFunctionNames(input, nil)

	if &output[0] != &input[0] {
		t.Fatal("normalized function names caused a payload copy")
	}
}

func TestRemoveEmptyGeminiFunctionToolsReusesNormalizedPayload(t *testing.T) {
	input := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"lookup"}]}]}}`)

	output := removeEmptyGeminiFunctionTools(input)

	if &output[0] != &input[0] {
		t.Fatal("non-empty tools caused a payload copy")
	}
}

func TestRemoveEmptyGeminiFunctionToolsDeletesEmptyArray(t *testing.T) {
	input := []byte(`{"request":{"tools":[]}}`)

	output := removeEmptyGeminiFunctionTools(input)

	if gjson.GetBytes(output, "request.tools").Exists() {
		t.Fatalf("empty tools should be removed: %s", output)
	}
}

func TestRewriteGeminiFunctionNamesNormalizesNonStringNames(t *testing.T) {
	input := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":true,"args":{}}}]}],"toolConfig":{"functionCallingConfig":{"allowedFunctionNames":[true]}}}}`)

	output := rewriteGeminiFunctionNames(input, nil)

	if name := gjson.GetBytes(output, "request.contents.0.parts.0.functionCall.name"); name.Type != gjson.String || name.String() != "true" {
		t.Fatalf("functionCall.name = %s, want string true", name.Raw)
	}
	if name := gjson.GetBytes(output, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0"); name.Type != gjson.String || name.String() != "true" {
		t.Fatalf("allowedFunctionNames.0 = %s, want string true", name.Raw)
	}
}

func TestFixCLIToolResponseReusesHistoryWithoutFunctionResponses(t *testing.T) {
	input := `{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"world"}]}]}}`

	output, errFix := fixCLIToolResponse(input)
	if errFix != nil {
		t.Fatalf("fixCLIToolResponse returned an error: %v", errFix)
	}
	if output != input {
		t.Fatalf("history changed:\n got: %s\nwant: %s", output, input)
	}
}

func TestFixCLIToolResponsePreservesObjectNormalization(t *testing.T) {
	input := `{"request":{"contents":{"first":{"role":"user","parts":[{"text":"hello"}]}}}}`

	output, errFix := fixCLIToolResponse(input)
	if errFix != nil {
		t.Fatalf("fixCLIToolResponse returned an error: %v", errFix)
	}
	if !gjson.Get(output, "request.contents").IsArray() {
		t.Fatalf("contents should be normalized to an array: %s", output)
	}
}
