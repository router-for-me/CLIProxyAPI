package responses

import (
	"encoding/json"
	"testing"

	"github.com/MoeForge/moego/ojson"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/gemini"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
)

func TestAntigravityResponses_TruncatedArgumentsPreservesValidJSON(t *testing.T) {
	truncatedArgs := `{"cmd": "cd 'D:\\foo' && apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: foo.rs\n@@\n- old\n+ new\n*** End Patch\nEOF", "workdir": "D:\\foo", "yield_time_ms": 120000`

	doc := ojson.NewDocString(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{
				"type": "function_call",
				"id": "fc_chatcmpl-tool-a1074280d66905c6",
				"call_id": "chatcmpl-tool-a1074280d66905c6",
				"name": "exec_command",
				"arguments": ""
			},
			{
				"type": "function_call_output",
				"id": "fco_1",
				"call_id": "chatcmpl-tool-a1074280d66905c6",
				"output": "failed to parse function arguments"
			}
		]
	}`)
	_ = doc.Set("input.0.arguments", truncatedArgs)

	geminiJSON, err := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", doc.Bytes(), false)
	if err != nil {
		t.Fatalf("ConvertOpenAIResponsesRequestToGemini failed: %v", err)
	}
	antigravityJSON, err := ConvertGeminiRequestToAntigravity("gemini-3.8-flash-high", geminiJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToAntigravity failed: %v", err)
	}
	if !json.Valid(antigravityJSON) {
		t.Fatalf("antigravityJSON is not valid JSON: %s", string(antigravityJSON))
	}

	var parsed any
	if err := json.Unmarshal(antigravityJSON, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed on antigravityJSON: %v", err)
	}
}

