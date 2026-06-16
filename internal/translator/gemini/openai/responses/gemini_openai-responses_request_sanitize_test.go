package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// TestConvertOpenAIResponsesRequestToGemini_SanitizesStaleRequiredEntries verifies
// that stale `required` entries (declared in the OpenAI tool schema but missing
// from `properties`) are stripped before the request is forwarded to Gemini,
// preventing
//
//	"AI_APICallError: GenerateContentRequest.tools[0].function_declarations[N].parameters.required[M]: property is not defined".
func TestConvertOpenAIResponsesRequestToGemini_SanitizesStaleRequiredEntries(t *testing.T) {
	input := []byte(`{
  "model": "gpt-5",
  "input": [{"type": "message", "role": "user", "content": "hi"}],
  "tools": [{
    "type": "function",
    "name": "search_company",
    "description": "Search",
    "parameters": {
      "type": "object",
      "properties": {
        "country": {"type": "string"},
        "industry": {"type": "string"}
      },
      "required": ["country", "industry", "stale_field", "another_stale"]
    }
  }]
}`)

	out := ConvertOpenAIResponsesRequestToGemini("gpt-5", input, false)
	schema := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("expected parametersJsonSchema to exist, got: %s", string(out))
	}

	reqArr := schema.Get("required")
	if !reqArr.IsArray() {
		t.Fatalf("expected required to be an array, got %v", reqArr.Type)
	}
	got := []string{}
	for _, r := range reqArr.Array() {
		got = append(got, r.String())
	}
	want := []string{"country", "industry"}
	if len(got) != len(want) {
		t.Fatalf("required mismatch: got %v, want %v (full: %s)", got, want, schema.Raw)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("required[%d]: got %q, want %q", i, got[i], v)
		}
	}
}
