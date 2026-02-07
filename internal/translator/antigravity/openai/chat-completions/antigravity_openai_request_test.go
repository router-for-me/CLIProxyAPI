package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravity_GoogleSearchEnablesWebSearchMode(t *testing.T) {
	input := []byte(`{
		"model":"gemini-3-flash-preview",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"type":"function","function":{"name":"add_memory","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}},
			{"google_search":{}}
		]
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash-preview", input, false)

	if got := gjson.GetBytes(out, "requestType").String(); got != "web_search" {
		t.Fatalf("requestType = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "model").String(); got != "gemini-2.5-flash" {
		t.Fatalf("model = %q, want %q", got, "gemini-2.5-flash")
	}
	if got := gjson.GetBytes(out, "request.generationConfig.candidateCount").Int(); got != 1 {
		t.Fatalf("candidateCount = %d, want %d", got, 1)
	}
	if got := len(gjson.GetBytes(out, "request.tools").Array()); got != 2 {
		t.Fatalf("len(request.tools) = %d, want %d", got, 2)
	}
	if !gjson.GetBytes(out, "request.tools.0.functionDeclarations").Exists() {
		t.Fatalf("request.tools.0.functionDeclarations missing")
	}
	if !gjson.GetBytes(out, "request.tools.1.googleSearch").Exists() {
		t.Fatalf("request.tools.1.googleSearch missing")
	}
}

func TestConvertOpenAIRequestToAntigravity_WebSearchToolOnlyWhenMixed(t *testing.T) {
	input := []byte(`{
		"model":"gemini-3-flash-preview",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"type":"function","function":{"name":"add_memory","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}},
			{"google_search":{}},
			{"code_execution":{}}
		]
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash-preview", input, false)

	tools := gjson.GetBytes(out, "request.tools").Array()
	if got := len(tools); got != 3 {
		t.Fatalf("len(request.tools) = %d, want %d", got, 3)
	}
	if !gjson.GetBytes(out, "request.tools.0.functionDeclarations").Exists() {
		t.Fatalf("request.tools.0.functionDeclarations missing")
	}
	if !gjson.GetBytes(out, "request.tools.1.googleSearch").Exists() {
		t.Fatalf("request.tools.1.googleSearch missing")
	}
	if !gjson.GetBytes(out, "request.tools.2.codeExecution").Exists() {
		t.Fatalf("request.tools.2.codeExecution missing")
	}
}
