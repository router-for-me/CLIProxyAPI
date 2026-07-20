package interactions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRewriteInteractionsFunctionNamesReusesNormalizedPayload(t *testing.T) {
	input := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"ok"}}}]}],"toolConfig":{"functionCallingConfig":{"allowedFunctionNames":["lookup"]}}}}`)

	output := rewriteInteractionsFunctionNames(input, nil)

	if &output[0] != &input[0] {
		t.Fatal("normalized function names caused a payload copy")
	}
}

func TestRewriteInteractionsFunctionNamesNormalizesNonStringNames(t *testing.T) {
	input := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":true,"args":{}}}]}],"toolConfig":{"functionCallingConfig":{"allowedFunctionNames":[true]}}}}`)

	output := rewriteInteractionsFunctionNames(input, nil)

	if name := gjson.GetBytes(output, "request.contents.0.parts.0.functionCall.name"); name.Type != gjson.String || name.String() != "true" {
		t.Fatalf("functionCall.name = %s, want string true", name.Raw)
	}
	if name := gjson.GetBytes(output, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0"); name.Type != gjson.String || name.String() != "true" {
		t.Fatalf("allowedFunctionNames.0 = %s, want string true", name.Raw)
	}
}
