package interactions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToAntigravityWithToolMessagesDirect(t *testing.T) {
	out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","system_instruction":"be brief","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`), false)
	if got := gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("request.systemInstruction.parts.0.text = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String(); got != "lookup" {
		t.Fatalf("functionCall.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "lookup" {
		t.Fatalf("functionResponse.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.name").String(); got != "lookup" {
		t.Fatalf("request.tools.0.functionDeclarations.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.q.type").String(); got != "string" {
		t.Fatalf("tool parameters schema was not preserved. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToAntigravityPreservesGenerationConfig(t *testing.T) {
	out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","input":"hi","generation_config":{"max_output_tokens":16,"top_p":0.8,"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"reasoning":{"summary":"auto"},"stream":true}`), true)
	if gjson.GetBytes(out, "input").Exists() {
		t.Fatalf("raw interactions input exists in translated request. Output: %s", string(out))
	}
	for _, path := range []string{
		"request.generationConfig.toolChoice",
		"request.generationConfig.thinkingLevel",
		"request.generationConfig.thinkingSummaries",
	} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s exists, want omitted. Output: %s", path, string(out))
		}
	}
	if got := gjson.GetBytes(out, "request.stream").Bool(); !got {
		t.Fatalf("request.stream = false, want true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 16 {
		t.Fatalf("request.generationConfig.maxOutputTokens = %d, want 16. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.topP").Float(); got != 0.8 {
		t.Fatalf("request.generationConfig.topP = %v, want 0.8. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("request.generationConfig.thinkingConfig.thinkingLevel = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("request.generationConfig.thinkingConfig.includeThoughts = false, want true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("request.toolConfig.functionCallingConfig.mode = %q, want AUTO. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsReasoningToAntigravityKeepsSummaryIndependent(t *testing.T) {
	tests := []struct {
		name       string
		reasoning  string
		want       bool
		wantExists bool
	}{
		{name: "effort only leaves summaries unspecified", reasoning: `{"effort":"high"}`},
		{name: "explicit auto enables summaries", reasoning: `{"effort":"high","summary":"auto"}`, want: true, wantExists: true},
		{name: "explicit none disables summaries", reasoning: `{"effort":"high","summary":"none"}`, wantExists: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"antigravity-test","input":"hi","reasoning":` + test.reasoning + `}`)
			out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", body, false)
			if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
				t.Fatalf("thinkingLevel = %q, want high. Output: %s", got, out)
			}
			includeThoughts := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts")
			if includeThoughts.Exists() != test.wantExists {
				t.Fatalf("includeThoughts exists = %v, want %v. Output: %s", includeThoughts.Exists(), test.wantExists, out)
			}
			if test.wantExists && includeThoughts.Bool() != test.want {
				t.Fatalf("includeThoughts = %v, want %v. Output: %s", includeThoughts.Bool(), test.want, out)
			}
		})
	}
}

func TestConvertAntigravityResponseToInteractionsNonStream(t *testing.T) {
	raw := []byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"ok"},{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`)
	out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.0.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.1.type").String(); got != "function_call" {
		t.Fatalf("steps.1.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertAntigravityResponseToInteractionsStream(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any
	events := ConvertAntigravityResponseToInteractions(ctx, "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}}`), &param)
	payload := findAntigravityInteractionsEventPayload(events, "step.delta")
	if len(payload) == 0 {
		t.Fatalf("step.delta event not found: %q", events)
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "ok" {
		t.Fatalf("delta.text = %q, want ok. Payload: %s", got, string(payload))
	}
}

func TestConvertAntigravityResponseToInteractionsStreamFunctionCallStartHasCallID(t *testing.T) {
	var param any
	events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]}}]}}`), &param)
	payload := findAntigravityInteractionsEventPayload(events, "step.start")
	if got := gjson.GetBytes(payload, "step.call_id").String(); got != "call_1" {
		t.Fatalf("step.call_id = %q, want call_1. Payload: %s", got, string(payload))
	}
}

func TestConvertInteractionsRequestToAntigravityDeduplicatesAndDisambiguatesTools(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	inputJSON := []byte(`{
		"input":[
			{"type":"function_call","name":"` + second + `","call_id":"call_1","arguments":{}},
			{"type":"function_result","name":"` + second + `","call_id":"call_1","result":{}}
		],
		"tools":[
			{"functionDeclarations":[{"name":"lookup"},{"name":"` + first + `"}]},
			{"function_declarations":[{"name":"lookup"},{"name":"` + second + `"}]}
		],
		"tool_choice":{"type":"function","function":{"name":"` + second + `"}}
	}`)

	out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", inputJSON, false)
	declarations := gjson.GetBytes(out, "request.tools.0.functionDeclarations").Array()
	if len(declarations) != 3 {
		t.Fatalf("declaration count = %d, want 3. Output: %s", len(declarations), out)
	}
	firstMapped := declarations[1].Get("name").String()
	secondMapped := declarations[2].Get("name").String()
	if firstMapped == secondMapped || len(secondMapped) > 64 {
		t.Fatalf("collision names = %q and %q, want distinct names <= 64 chars", firstMapped, secondMapped)
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.functionCall.name").String(); got != secondMapped {
		t.Fatalf("functionCall.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse.name").String(); got != secondMapped {
		t.Fatalf("functionResponse.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != secondMapped {
		t.Fatalf("allowedFunctionNames.0 = %q, want %q. Output: %s", got, secondMapped, out)
	}
}

func TestConvertInteractionsRequestToAntigravityPreservesNameMappingWhitespace(t *testing.T) {
	inputJSON := []byte(`{
		"input":[{"type":"function_call","name":" read/file ","arguments":{}}],
		"tools":[{"type":"function","name":" read/file ","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","function":{"name":" read/file "}}
	}`)

	out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", inputJSON, false)
	declarationName := gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.name").String()
	callName := gjson.GetBytes(out, "request.contents.0.parts.0.functionCall.name").String()
	allowedName := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String()
	if declarationName == "" || callName != declarationName || allowedName != declarationName {
		t.Fatalf("mapped names declaration=%q call=%q allowed=%q. Output: %s", declarationName, callName, allowedName, out)
	}
}

func TestConvertAntigravityResponseToInteractionsRestoresDisambiguatedName(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	original := []byte(`{"tools":[{"name":"` + first + `"},{"name":"` + second + `"}]}`)
	mapped := util.SanitizedFunctionNameMap(original)[second]
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + mapped + `","args":{}}}]}}]}}`)

	out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", original, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.name").String(); got != second {
		t.Fatalf("function call name = %q, want %q. Output: %s", got, second, out)
	}
}

func findAntigravityInteractionsEventPayload(events [][]byte, eventType string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, prefix) {
				continue
			}
			payload := bytes.TrimSpace(line[len(prefix):])
			if gjson.GetBytes(payload, "type").String() == eventType || gjson.GetBytes(payload, "event_type").String() == eventType {
				return payload
			}
		}
	}
	return nil
}

func TestConvertInteractionsRequestToAntigravityToolChoiceNoneOmitsTools(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolChoice string
	}{
		{name: "string none", toolChoice: `"none"`},
		{name: "object none", toolChoice: `{"type":"none"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputJSON := []byte(`{
				"model":"antigravity-test",
				"input":[{"type":"text","text":"hi"}],
				"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],
				"tool_choice":` + tc.toolChoice + `
			}`)
			out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", inputJSON, false)
			if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
				t.Fatalf("expected mode NONE, got %q", got)
			}
			if gjson.GetBytes(out, "request.tools").Exists() {
				t.Fatalf("expected request.tools to be omitted, got %s", gjson.GetBytes(out, "request.tools").Raw)
			}
		})
	}
}

func TestConvertInteractionsRequestToAntigravityBuiltinTools(t *testing.T) {
	t.Run("url_context only", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"read url",
			"tools":[{"type":"url_context"}]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("urlContext").Exists() {
			t.Fatalf("expected urlContext tool, got %s", tools[0].Raw)
		}
		if tools[0].Get("type").Exists() {
			t.Fatalf("expected type field to be omitted from upstream tool, got %s", tools[0].Raw)
		}
	})

	t.Run("code_execution only", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"execute code",
			"tools":[{"type":"code_execution"}]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("codeExecution").Exists() {
			t.Fatalf("expected codeExecution tool, got %s", tools[0].Raw)
		}
		if tools[0].Get("type").Exists() {
			t.Fatalf("expected type field to be omitted from upstream tool, got %s", tools[0].Raw)
		}
	})

	t.Run("google_search only", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"search web",
			"tools":[{"type":"google_search"}]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("googleSearch").Exists() {
			t.Fatalf("expected googleSearch tool, got %s", tools[0].Raw)
		}
		if tools[0].Get("type").Exists() {
			t.Fatalf("expected type field to be omitted from upstream tool, got %s", tools[0].Raw)
		}
	})

	t.Run("mixed function and builtin tools", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"mixed tools",
			"tools":[
				{"type":"function","name":"lookup","parameters":{"type":"object"}},
				{"type":"url_context"},
				{"type":"code_execution"}
			]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 3 {
			t.Fatalf("expected 3 tools, got %d. Output: %s", len(tools), string(out))
		}
		hasFunc := false
		hasURL := false
		hasCode := false
		for _, tool := range tools {
			if tool.Get("type").Exists() {
				t.Fatalf("expected type field to be omitted from all upstream tools, got %s", tool.Raw)
			}
			if tool.Get("functionDeclarations").Exists() {
				hasFunc = true
			}
			if tool.Get("urlContext").Exists() {
				hasURL = true
			}
			if tool.Get("codeExecution").Exists() {
				hasCode = true
			}
		}
		if !hasFunc || !hasURL || !hasCode {
			t.Fatalf("expected functionDeclarations, urlContext, and codeExecution; got func=%v, url=%v, code=%v. Output: %s", hasFunc, hasURL, hasCode, string(out))
		}
	})

	t.Run("nested parameters and aliases preserved", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"test options",
			"tools":[
				{"type":"url_context","url_context":{"max_urls":3}},
				{"code_execution":{"environment":"sandbox"}},
				{"type":"web_search","google_search":{"mode":"search"}}
			]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 3 {
			t.Fatalf("expected 3 tools, got %d. Output: %s", len(tools), string(out))
		}
		if got := tools[0].Get("urlContext.max_urls").Int(); got != 3 {
			t.Fatalf("expected urlContext.max_urls=3, got %d. Tool: %s", got, tools[0].Raw)
		}
		if got := tools[1].Get("codeExecution.environment").String(); got != "sandbox" {
			t.Fatalf("expected codeExecution.environment=sandbox, got %s. Tool: %s", got, tools[1].Raw)
		}
		if got := tools[2].Get("googleSearch.mode").String(); got != "search" {
			t.Fatalf("expected googleSearch.mode=search, got %s. Tool: %s", got, tools[2].Raw)
		}
	})

	t.Run("native composite tools preserved without truncation", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"native composite tools",
			"tools":[{"googleSearch":{},"urlContext":{}}]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool node, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("googleSearch").Exists() {
			t.Fatalf("expected googleSearch preserved in composite tool, got %s", tools[0].Raw)
		}
		if !tools[0].Get("urlContext").Exists() {
			t.Fatalf("expected urlContext preserved in composite tool, got %s", tools[0].Raw)
		}
	})

	t.Run("unrecognized tool retained and not silently dropped", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.8-flash-high",
			"input":"unrecognized tool",
			"tools":[{"type":"file_search","file_search":{"max_results":5}}]
		}`)
		out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
		tools := gjson.GetBytes(out, "request.tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool retained, got %d. Output: %s", len(tools), string(out))
		}
		if got := tools[0].Get("type").String(); got != "file_search" {
			t.Fatalf("expected tool type file_search retained, got %s", tools[0].Raw)
		}
	})
}

func TestConvertInteractionsRequestToAntigravity_FunctionResponseJSONRef(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{
				"type": "function_result",
				"name": "lookup",
				"call_id": "call_1",
				"result": {
					"schema": {
						"$ref": "#/components/schemas/ErrorModel"
					}
				}
			}
		]
	}`)
	out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
	val := gjson.GetBytes(out, "request.contents.0.parts.0.functionResponse.response.result")
	if val.Type != gjson.String {
		t.Fatalf("expected functionResponse.response.result to be string, got %s (raw: %s)", val.Type, val.Raw)
	}
	if !strings.Contains(val.String(), "#/components/schemas/ErrorModel") {
		t.Fatalf("expected string result to contain ref target, got %q", val.String())
	}
}

func TestConvertInteractionsRequestToAntigravity_ParallelToolCallsHistory(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type": "user_input", "content": [{"type": "text", "text": "run tools"}]},
			{"type": "thought", "signature": "sig_turn1"},
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {"a": 1}},
			{"type": "function_call", "name": "f2", "call_id": "c2", "arguments": {"b": 2}},
			{"type": "function_call", "name": "f3", "call_id": "c3", "arguments": {"c": 3}},
			{"type": "function_result", "name": "f1", "call_id": "c1", "result": {"r": 1}},
			{"type": "function_result", "name": "f2", "call_id": "c2", "result": {"r": 2}},
			{"type": "function_result", "name": "f3", "call_id": "c3", "result": {"r": 3}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents (user, model, user), got %d: %s", len(contents), string(out))
	}
	if err := signature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", err)
	}
	modelParts := contents[1].Get("parts").Array()
	if len(modelParts) != 3 {
		t.Fatalf("expected 3 model parts, got %d", len(modelParts))
	}
	if sig := modelParts[0].Get("thoughtSignature").String(); sig != "sig_turn1" {
		t.Fatalf("expected first functionCall thoughtSignature to be %q, got %q", "sig_turn1", sig)
	}
	userParts := contents[2].Get("parts").Array()
	if len(userParts) != 3 {
		t.Fatalf("expected 3 user functionResponse parts, got %d", len(userParts))
	}
}

func TestConvertInteractionsRequestToAntigravity_ThoughtSummaryAndSignature(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type": "thought", "summary": [{"type": "text", "text": "my thinking"}], "signature": "sig_thought"},
			{"type": "model_output", "content": [{"type": "text", "text": "my answer"}]}
		]
	}`)
	out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(out))
	}
	parts := contents[0].Get("parts").Array()
	hasThoughtText := false
	hasSig := false
	for _, p := range parts {
		if p.Get("thought").Bool() && strings.Contains(p.Get("text").String(), "my thinking") {
			hasThoughtText = true
		}
		if p.Get("thoughtSignature").String() == "sig_thought" {
			hasSig = true
		}
	}
	if !hasThoughtText {
		t.Fatalf("expected thought text from summary in parts, got: %s", string(out))
	}
	if !hasSig {
		t.Fatalf("expected thoughtSignature in parts, got: %s", string(out))
	}
}

func TestConvertAntigravityResponseToInteractionsNonStream_ThoughtSignature(t *testing.T) {
	raw := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"functionCall": {"name": "lookup", "id": "call_1", "args": {"q": "x"}}, "thoughtSignature": "sig_fc"},
						{"functionCall": {"name": "search", "id": "call_2", "args": {"q": "y"}}}
					]
				}
			}]
		}
	}`)
	out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "gemini-3.8-flash-high", nil, nil, raw, nil)
	steps := gjson.GetBytes(out, "steps").Array()
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps (thought, function_call, function_call), got %d: %s", len(steps), string(out))
	}
	if steps[0].Get("type").String() != "thought" || steps[0].Get("signature").String() != "sig_fc" {
		t.Fatalf("step[0] expected thought with signature sig_fc, got: %s", steps[0].Raw)
	}
	if steps[1].Get("type").String() != "function_call" || steps[1].Get("name").String() != "lookup" {
		t.Fatalf("step[1] expected function_call lookup, got: %s", steps[1].Raw)
	}
	if steps[2].Get("type").String() != "function_call" || steps[2].Get("name").String() != "search" {
		t.Fatalf("step[2] expected function_call search, got: %s", steps[2].Raw)
	}
}

func TestConvertAntigravityResponseToInteractionsStream_TrailingThoughtSignature(t *testing.T) {
	var param any
	chunk := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"sig_trailing"}]}}]}}`)
	out := ConvertAntigravityResponseToInteractions(context.Background(), "gemini-3.8-flash-high", nil, nil, chunk, &param)
	foundSig := false
	for _, frame := range out {
		if bytes.Contains(frame, []byte("thought_signature")) && bytes.Contains(frame, []byte("sig_trailing")) {
			foundSig = true
			break
		}
	}
	if !foundSig {
		t.Fatalf("expected thought_signature delta in stream output, got: %v", string(bytes.Join(out, []byte("\n"))))
	}
}

func TestConvertInteractionsRequestToAntigravity_InterleavedModelTurnSteps(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {"a": 1}, "signature": "sig1"},
			{"type": "model_output", "content": [{"type": "text", "text": "explanation"}]},
			{"type": "function_call", "name": "f2", "call_id": "c2", "arguments": {"b": 2}},
			{"type": "function_result", "name": "f1", "call_id": "c1", "result": {"r": 1}},
			{"type": "function_result", "name": "f2", "call_id": "c2", "result": {"r": 2}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents (model, user), got %d: %s", len(contents), string(out))
	}
	if err := signature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", err)
	}
	modelParts := contents[0].Get("parts").Array()
	if len(modelParts) != 3 {
		t.Fatalf("expected 3 model parts (fc1, text, fc2), got %d: %s", len(modelParts), contents[0].Raw)
	}
	if sig := modelParts[0].Get("thoughtSignature").String(); sig != "sig1" {
		t.Fatalf("expected fc1 thoughtSignature = sig1, got %q", sig)
	}
	userParts := contents[1].Get("parts").Array()
	if len(userParts) != 2 {
		t.Fatalf("expected 2 user functionResponse parts, got %d: %s", len(userParts), contents[1].Raw)
	}
}

func TestConvertInteractionsRequestToAntigravity_ResponseToRequestRoundTrip(t *testing.T) {
	upstreamResponse := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"functionCall": {"name": "lookup", "id": "call_1", "args": {"q": "x"}}, "thoughtSignature": "sig_rt_fc"},
						{"functionCall": {"name": "search", "id": "call_2", "args": {"q": "y"}}}
					]
				}
			}]
		}
	}`)
	respOut := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "gemini-3.8-flash-high", nil, nil, upstreamResponse, nil)
	steps := gjson.GetBytes(respOut, "steps").Raw

	// Build Turn 2 input: user question + Turn 1 steps + tool results
	turn2Input := `{"model":"gemini-3.8-flash-high","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}`
	for _, s := range gjson.Get(steps, "@this").Array() {
		turn2Input += "," + s.Raw
	}
	turn2Input += `,{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}},{"type":"function_result","name":"search","call_id":"call_2","result":{"found":true}}]}`

	reqOut, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", []byte(turn2Input), false)
	contents := gjson.GetBytes(reqOut, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents (user, model, user), got %d: %s", len(contents), string(reqOut))
	}
	if err := signature.ValidateGeminiFunctionCallPairing(reqOut); err != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", err)
	}
	modelParts := contents[1].Get("parts").Array()
	if len(modelParts) != 2 {
		t.Fatalf("expected 2 model parts, got %d", len(modelParts))
	}
	if sig := modelParts[0].Get("thoughtSignature").String(); sig != "sig_rt_fc" {
		t.Fatalf("expected round-tripped thoughtSignature = sig_rt_fc, got %q", sig)
	}
}

func TestConvertInteractionsRequestToAntigravity_MultipleSignaturesPreserved(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type": "thought", "summary": [{"type": "text", "text": "thought 1"}], "signature": "sig_thought_1"},
			{"type": "thought", "signature": "sig_fc_1"},
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(out))
	}
	parts := contents[0].Get("parts").Array()
	var foundS1, foundS2 bool
	for _, p := range parts {
		if p.Get("thoughtSignature").String() == "sig_thought_1" {
			foundS1 = true
		}
		if p.Get("functionCall").Exists() && p.Get("thoughtSignature").String() == "sig_fc_1" {
			foundS2 = true
		}
	}
	if !foundS1 || !foundS2 {
		t.Fatalf("expected both sig_thought_1 and sig_fc_1 preserved, got parts: %s", contents[0].Get("parts").Raw)
	}
}

func TestConvertInteractionsRequestToAntigravity_TrailingSignatureRoundTrip(t *testing.T) {
	upstreamResponse := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "answer"},
						{"text": "", "thoughtSignature": "sig_trailing_rt"}
					]
				}
			}]
		}
	}`)
	respOut := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "gemini-3.8-flash-high", nil, nil, upstreamResponse, nil)
	steps := gjson.GetBytes(respOut, "steps").Raw

	turn2Input := `{"model":"gemini-3.8-flash-high","input":` + steps + `}`
	reqOut, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", []byte(turn2Input), false)
	contents := gjson.GetBytes(reqOut, "request.contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(reqOut))
	}
	parts := contents[0].Get("parts").Array()
	var foundText, foundSig bool
	for _, p := range parts {
		if p.Get("text").String() == "answer" {
			foundText = true
		}
		if p.Get("thoughtSignature").String() == "sig_trailing_rt" {
			foundSig = true
		}
	}
	if !foundText || !foundSig {
		t.Fatalf("expected both answer and sig_trailing_rt preserved, got parts: %s", contents[0].Get("parts").Raw)
	}
}

func TestConvertInteractionsRequestToAntigravity_ExplicitSignatureClearsPending(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type": "thought", "signature": "sig_shared"},
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {}, "signature": "sig_shared"},
			{"type": "function_call", "name": "f2", "call_id": "c2", "arguments": {}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", inputJSON, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(out))
	}
	parts := contents[0].Get("parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected exactly 2 parts (no extra carrier), got %d: %s", len(parts), contents[0].Raw)
	}
	if sig := parts[0].Get("thoughtSignature").String(); sig != "sig_shared" {
		t.Fatalf("expected f1 thoughtSignature = sig_shared, got %q", sig)
	}
	if sig := parts[1].Get("thoughtSignature").String(); sig != "" {
		t.Fatalf("expected f2 thoughtSignature to be empty, got %q", sig)
	}
}
