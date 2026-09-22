package gemini

import (
	"runtime"
	"strings"
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
	input := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"world"}]}]}}`)

	output, errFix := fixCLIToolResponse(input)
	if errFix != nil {
		t.Fatalf("fixCLIToolResponse returned an error: %v", errFix)
	}
	if string(output) != string(input) {
		t.Fatalf("history changed:\n got: %s\nwant: %s", output, input)
	}
	if &output[0] != &input[0] {
		t.Fatal("history without function responses caused a payload copy")
	}
}

func TestFixCLIToolResponsePreservesObjectNormalization(t *testing.T) {
	input := []byte(`{"request":{"contents":{"first":{"role":"user","parts":[{"text":"hello"}]}}}}`)

	output, errFix := fixCLIToolResponse(input)
	if errFix != nil {
		t.Fatalf("fixCLIToolResponse returned an error: %v", errFix)
	}
	if !gjson.GetBytes(output, "request.contents").IsArray() {
		t.Fatalf("contents should be normalized to an array: %s", output)
	}
}

// TestConvertGeminiRequestToAntigravityBoundsLargePayloadCopies keeps the number of
// full-payload copies bounded for large inline data. The assertions run directly in
// the test (not inside testing.Benchmark) so a regression fails loudly instead of
// being swallowed by a discarded benchmark result.
func TestConvertGeminiRequestToAntigravityBoundsLargePayloadCopies(t *testing.T) {
	const inlineDataSize = 4 << 20
	input := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` +
		strings.Repeat("A", inlineDataSize) + `"}},{"text":"describe"}]}]}`)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	output := ConvertGeminiRequestToAntigravity("gemini-3-flash", input, false)
	runtime.ReadMemStats(&after)

	if got := gjson.GetBytes(output, "request.contents.0.parts.0.inlineData.data").String(); len(got) != inlineDataSize {
		t.Fatalf("inline data length = %d, want %d", len(got), inlineDataSize)
	}
	if got := gjson.GetBytes(output, "model").String(); got != "gemini-3-flash" {
		t.Fatalf("model = %q, want gemini-3-flash", got)
	}
	if got := gjson.GetBytes(output, "request.safetySettings"); !got.IsArray() {
		t.Fatalf("request.safetySettings = %s, want array", got.Raw)
	}

	// Wrapping the request in the Antigravity envelope and setting the model each
	// allocate one payload-sized buffer; everything beyond that is a regression.
	const allowedCopies = 3
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > allowedCopies*inlineDataSize {
		t.Fatalf("conversion allocated %d bytes for a %d byte payload, want at most %d",
			allocated, inlineDataSize, allowedCopies*inlineDataSize)
	}
}
