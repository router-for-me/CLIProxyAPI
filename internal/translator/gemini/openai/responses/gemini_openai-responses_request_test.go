package responses

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeveloperRoleToSystem(t *testing.T) {
	in := mustJSON(t, map[string]any{
		"input": []map[string]any{
			{"role": "developer", "content": "dev"},
			{"role": "user", "content": "hi"},
		},
	})
	out := ConvertOpenAIResponsesRequestToGemini("m", in, false)
	got := gjson.ParseBytes(out)
	if got.Get("systemInstruction.parts.0.text").String() != "dev" {
		t.Fatalf("missing systemInstruction: %s", out)
	}
	if got.Get("contents.#(role=developer)").Exists() {
		t.Fatalf("developer role leaked: %s", out)
	}
}

func TestFunctionIDsNotForwarded(t *testing.T) {
	in := mustJSON(t, map[string]any{
		"input": []map[string]any{
			{"role": "user", "content": "call"},
			{
				"type":      "function_call",
				"call_id":   "c1",
				"name":      "echo",
				"arguments": `{"v":"x"}`,
			},
			{"type": "function_call_output", "call_id": "c1", "output": "x"},
		},
	})
	out := ConvertOpenAIResponsesRequestToGemini("m", in, false)
	got := gjson.ParseBytes(out)
	if got.Get("contents.1.parts.0.functionCall.id").Exists() {
		t.Fatalf("functionCall id leaked: %s", out)
	}
	if got.Get("contents.2.parts.0.functionResponse.id").Exists() {
		t.Fatalf("functionResponse id leaked: %s", out)
	}
	if got.Get("contents.1.parts.0.functionCall.name").String() != "echo" {
		t.Fatalf("missing function call: %s", out)
	}
}
