package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

// TestConvertOpenAIRequestToGemini_SanitizesStaleRequiredEntries verifies that the
// OpenAI -> Gemini translator strips required entries that are missing from
// properties. Without this fix, Gemini API returns
//
//	"AI_APICallError: GenerateContentRequest.tools[0].function_declarations[N].parameters.required[M]: property is not defined"
//
// even though the original OpenAI request was accepted by the client.
func TestConvertOpenAIRequestToGemini_SanitizesStaleRequiredEntries(t *testing.T) {
	input := []byte(`{
  "model": "gemini-2.0-flash",
  "messages": [{"role":"user","content":"hi"}],
  "tools": [{
    "type": "function",
    "function": {
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
    }
  }]
}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.0-flash", input, false)

	// Find the renamed parametersJsonSchema
	schema := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("expected parametersJsonSchema to exist after conversion, got: %s", string(out))
	}

	// Required array should be filtered to only the keys present in properties.
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

// TestConvertOpenAIRequestToGemini_DropsEmptyRequiredArray ensures that a required
// array whose entries were all stale is fully removed (not left as []).
func TestConvertOpenAIRequestToGemini_DropsEmptyRequiredArray(t *testing.T) {
	input := []byte(`{
  "model": "gemini-2.0-flash",
  "messages": [{"role":"user","content":"hi"}],
  "tools": [{
    "type": "function",
    "function": {
      "name": "noop",
      "description": "Noop",
      "parameters": {
        "type": "object",
        "properties": {
          "x": {"type": "string"}
        },
        "required": ["missing_one", "missing_two"]
      }
    }
  }]
}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.0-flash", input, false)
	schema := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("expected parametersJsonSchema to exist")
	}
	if schema.Get("required").Exists() {
		t.Fatalf("expected required to be removed when all entries are stale, got: %s", schema.Raw)
	}
}

// TestConvertOpenAIRequestToGemini_AllRequiredValidPassesThrough ensures we do
// not regress valid OpenAI requests where every required entry is declared.
func TestConvertOpenAIRequestToGemini_AllRequiredValidPassesThrough(t *testing.T) {
	input := []byte(`{
  "model": "gemini-2.0-flash",
  "messages": [{"role":"user","content":"hi"}],
  "tools": [{
    "type": "function",
    "function": {
      "name": "ok_tool",
      "description": "ok",
      "parameters": {
        "type": "object",
        "properties": {
          "a": {"type": "string"},
          "b": {"type": "number"}
        },
        "required": ["a", "b"]
      }
    }
  }]
}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.0-flash", input, false)
	schema := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema")
	req := schema.Get("required")
	if !req.IsArray() {
		t.Fatalf("expected required array, got %v", req.Type)
	}
	if len(req.Array()) != 2 {
		t.Fatalf("expected 2 required entries, got %d: %s", len(req.Array()), schema.Raw)
	}
}
