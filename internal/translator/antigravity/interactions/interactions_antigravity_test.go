package interactions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConvertInteractionsRequestToAntigravityWithToolMessagesDirect(t *testing.T) {
	out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","system_instruction":"be brief","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "request.systemInstruction.role").String(); got != "user" {
		t.Fatalf("request.systemInstruction.role = %q, want user. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("request.systemInstruction.parts.0.text = %q, want be brief. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0.functionCall.name").String(); got != "lookup" {
		t.Fatalf("functionCall.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "lookup" {
		t.Fatalf("functionResponse.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.0.functionDeclarations.0.name").String(); got != "lookup" {
		t.Fatalf("request.tools.0.functionDeclarations.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.q.type").String(); got != "string" {
		t.Fatalf("tool parameters schema was not preserved. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToAntigravityPreservesGenerationConfig(t *testing.T) {
	out, _ := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","input":"hi","generation_config":{"max_output_tokens":16,"top_p":0.8,"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"reasoning":{"summary":"auto"},"stream":true}`), true)
	if ojson.GetBytesNoCopy(out, "input").Exists() {
		t.Fatalf("raw interactions input exists in translated request. Output: %s", string(out))
	}
	for _, path := range []string{
		"request.generationConfig.toolChoice",
		"request.generationConfig.thinkingLevel",
		"request.generationConfig.thinkingSummaries",
	} {
		if ojson.GetBytesNoCopy(out, path).Exists() {
			t.Fatalf("%s exists, want omitted. Output: %s", path, string(out))
		}
	}
	if got := ojson.GetBytesNoCopy(out, "request.stream").Bool(); !got {
		t.Fatalf("request.stream = false, want true. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.generationConfig.maxOutputTokens").Int(); got != 16 {
		t.Fatalf("request.generationConfig.maxOutputTokens = %d, want 16. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.generationConfig.topP").Float(); got != 0.8 {
		t.Fatalf("request.generationConfig.topP = %v, want 0.8. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("request.generationConfig.thinkingConfig.thinkingLevel = %q, want high. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("request.generationConfig.thinkingConfig.includeThoughts = false, want true. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "request.toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
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
			if got := ojson.GetBytesNoCopy(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
				t.Fatalf("thinkingLevel = %q, want high. Output: %s", got, out)
			}
			includeThoughts := ojson.GetBytesNoCopy(out, "request.generationConfig.thinkingConfig.includeThoughts")
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
	if got := ojson.GetBytesNoCopy(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.0.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "steps.1.type").String(); got != "function_call" {
		t.Fatalf("steps.1.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "usage.total_tokens").Int(); got != 5 {
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
	if got := ojson.GetBytesNoCopy(payload, "delta.text").String(); got != "ok" {
		t.Fatalf("delta.text = %q, want ok. Payload: %s", got, string(payload))
	}
}

func countAntigravityInteractionsEvents(events [][]byte, eventType string) int {
	count := 0
	for _, event := range events {
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("event:")) && string(bytes.TrimSpace(line[len("event:"):])) == eventType {
				count++
			}
		}
	}
	return count
}

func TestConvertAntigravityResponseToInteractionsStreamEmitsDoneAfterUsage(t *testing.T) {
	var param any
	events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`), &param)

	joined := string(bytes.Join(events, nil))
	if !strings.Contains(joined, "event: done") || !strings.Contains(joined, "data: [DONE]") {
		t.Fatalf("done event missing after a terminal usage frame: %q", joined)
	}
	if got := countAntigravityInteractionsEvents(events, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Events: %q", got, joined)
	}
	if got := countAntigravityInteractionsEvents(events, "done"); got != 1 {
		t.Fatalf("done count = %d, want 1. Events: %q", got, joined)
	}
	completed := findAntigravityInteractionsEventPayload(events, "interaction.completed")
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_tokens").Int(); got != 5 {
		t.Fatalf("completed total_tokens = %d, want 5. Payload: %s", got, string(completed))
	}
}

func TestConvertAntigravityResponseToInteractionsStreamSplitFinishAndUsage(t *testing.T) {
	var param any
	finishOut := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`), &param)
	if got := countAntigravityInteractionsEvents(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countAntigravityInteractionsEvents(finishOut, "done"); got != 0 {
		t.Fatalf("finish done count = %d, want 0", got)
	}

	usageOut := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"cpaUsageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}}`), &param)
	if got := countAntigravityInteractionsEvents(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1", got)
	}
	if got := countAntigravityInteractionsEvents(usageOut, "done"); got != 1 {
		t.Fatalf("usage done count = %d, want 1", got)
	}
	completed := findAntigravityInteractionsEventPayload(usageOut, "interaction.completed")
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_tokens").Int(); got != 33 {
		t.Fatalf("completed total_tokens = %d, want 33. Payload: %s", got, string(completed))
	}

	doneOut := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`[DONE]`), &param)
	if len(doneOut) != 0 {
		t.Fatalf("literal [DONE] after completion emitted %q, want no events", doneOut)
	}
}

func TestConvertAntigravityResponseToInteractionsStreamWaitsForPostFinishUsage(t *testing.T) {
	var param any
	contentOut := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1}}}`), &param)
	finishOut := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}}`), &param)
	for _, out := range [][][]byte{contentOut, finishOut} {
		joined := bytes.Join(out, nil)
		if bytes.Contains(joined, []byte("interaction.completed")) || bytes.Contains(joined, []byte("[DONE]")) {
			t.Fatalf("stream completed before the post-finish usage frame: %q", joined)
		}
	}
	usageOut := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}}`), &param)
	if got := countAntigravityInteractionsEvents(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1", got)
	}
	if got := countAntigravityInteractionsEvents(usageOut, "done"); got != 1 {
		t.Fatalf("usage done count = %d, want 1", got)
	}
	completed := findAntigravityInteractionsEventPayload(usageOut, "interaction.completed")
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_tokens").Int(); got != 33 {
		t.Fatalf("completed total_tokens = %d, want 33 (final usage, not the intermediate frame). Payload: %s", got, string(completed))
	}
}

func TestConvertAntigravityResponseToInteractionsStreamIgnoresEmptyFinishReason(t *testing.T) {
	for _, finishReason := range []string{"", " "} {
		var param any
		out := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"`+finishReason+`"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`), &param)
		if got := countAntigravityInteractionsEvents(out, "interaction.completed"); got != 0 {
			t.Fatalf("finishReason %q: interaction.completed count = %d, want 0", finishReason, got)
		}
		if got := countAntigravityInteractionsEvents(out, "done"); got != 0 {
			t.Fatalf("finishReason %q: done count = %d, want 0", finishReason, got)
		}
	}
}

func TestConvertAntigravityResponseToInteractionsStreamDoesNotCompleteOnNonTerminalUsage(t *testing.T) {
	var param any
	out := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking"}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1}}}`), &param)
	if got := countAntigravityInteractionsEvents(out, "interaction.completed"); got != 0 {
		t.Fatalf("interaction.completed count = %d, want 0. Events: %q", got, out)
	}
	if got := countAntigravityInteractionsEvents(out, "done"); got != 0 {
		t.Fatalf("done count = %d, want 0. Events: %q", got, out)
	}
}

func TestConvertAntigravityResponseToInteractionsStreamFunctionCallStartHasCallID(t *testing.T) {
	var param any
	events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]}}]}}`), &param)
	payload := findAntigravityInteractionsEventPayload(events, "step.start")
	if got := ojson.GetBytesNoCopy(payload, "step.call_id").String(); got != "call_1" {
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
	declarations := ojson.GetBytesNoCopy(out, "request.tools.0.functionDeclarations").Array()
	if len(declarations) != 3 {
		t.Fatalf("declaration count = %d, want 3. Output: %s", len(declarations), out)
	}
	firstMapped := declarations[1].Get("name").String()
	secondMapped := declarations[2].Get("name").String()
	if firstMapped == secondMapped || len(secondMapped) > 64 {
		t.Fatalf("collision names = %q and %q, want distinct names <= 64 chars", firstMapped, secondMapped)
	}
	if got := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionCall.name").String(); got != secondMapped {
		t.Fatalf("functionCall.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0.functionResponse.name").String(); got != secondMapped {
		t.Fatalf("functionResponse.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := ojson.GetBytesNoCopy(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != secondMapped {
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
	declarationName := ojson.GetBytesNoCopy(out, "request.tools.0.functionDeclarations.0.name").String()
	callName := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionCall.name").String()
	allowedName := ojson.GetBytesNoCopy(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String()
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
	if got := ojson.GetBytesNoCopy(out, "steps.0.name").String(); got != second {
		t.Fatalf("function call name = %q, want %q. Output: %s", got, second, out)
	}
}

func findAntigravityInteractionsEventPayload(events [][]byte, eventType string) []byte {
	payloads := findAntigravityInteractionsEventPayloads(events, eventType)
	if len(payloads) == 0 {
		return nil
	}
	return payloads[0]
}

func findAntigravityInteractionsEventPayloads(events [][]byte, eventType string) [][]byte {
	prefix := []byte("data:")
	var out [][]byte
	for _, event := range events {
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, prefix) {
				continue
			}
			payload := bytes.TrimSpace(line[len(prefix):])
			if ojson.GetBytesNoCopy(payload, "type").String() == eventType || ojson.GetBytesNoCopy(payload, "event_type").String() == eventType {
				out = append(out, payload)
			}
		}
	}
	return out
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
			if got := ojson.GetBytesNoCopy(out, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
				t.Fatalf("expected mode NONE, got %q", got)
			}
			if ojson.GetBytesNoCopy(out, "request.tools").Exists() {
				t.Fatalf("expected request.tools to be omitted, got %s", ojson.GetBytesNoCopy(out, "request.tools").Raw)
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
		tools := ojson.GetBytesNoCopy(out, "request.tools").Array()
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
	val := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionResponse.response.output")
	if val.Type != ojson.String {
		t.Fatalf("expected functionResponse.response.output to be string, got %s (raw: %s)", val.Type, val.Raw)
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
	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
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
	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
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
	steps := ojson.GetBytesNoCopy(out, "steps").Array()
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
	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
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
	steps := ojson.GetBytesNoCopy(respOut, "steps").Raw

	// Build Turn 2 input: user question + Turn 1 steps + tool results
	turn2Input := `{"model":"gemini-3.8-flash-high","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}`
	for _, s := range ojson.Get(steps, "@this").Array() {
		turn2Input += "," + s.Raw
	}
	turn2Input += `,{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}},{"type":"function_result","name":"search","call_id":"call_2","result":{"found":true}}]}`

	reqOut, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", []byte(turn2Input), false)
	contents := ojson.GetBytesNoCopy(reqOut, "request.contents").Array()
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
	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
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
	steps := ojson.GetBytesNoCopy(respOut, "steps").Raw

	turn2Input := `{"model":"gemini-3.8-flash-high","input":` + steps + `}`
	reqOut, _ := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", []byte(turn2Input), false)
	contents := ojson.GetBytesNoCopy(reqOut, "request.contents").Array()
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
	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
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

func TestConvertInteractionsRequestToAntigravityMergesMultipartUserInput(t *testing.T) {
	input := []byte(`{"model":"gemini-3-flash","input":[{"type":"user_input","content":[{"type":"text","text":"What is in this image?"},{"type":"image","mime_type":"image/png","data":"iVBORw0KGgo="}]}]}`)

	out, errConvert := ConvertInteractionsRequestToAntigravity("gemini-3-flash", input, false)
	if errConvert != nil {
		t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
	}
	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
	if len(contents) != 1 {
		t.Fatalf("request.contents length = %d, want 1. Output: %s", len(contents), string(out))
	}
	if role := contents[0].Get("role").String(); role != "user" {
		t.Fatalf("request.contents.0.role = %q, want user. Output: %s", role, string(out))
	}
	parts := contents[0].Get("parts").Array()
	if len(parts) != 2 {
		t.Fatalf("request.contents.0.parts length = %d, want 2. Output: %s", len(parts), string(out))
	}
	if text := parts[0].Get("text").String(); text != "What is in this image?" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want question text. Output: %s", text, string(out))
	}
	if data := parts[1].Get("inlineData.data").String(); data != "iVBORw0KGgo=" {
		t.Fatalf("request.contents.0.parts.1.inlineData.data = %q, want image data. Output: %s", data, string(out))
	}
}

func TestConvertInteractionsRequestToAntigravityAcceptsBareContentBlockList(t *testing.T) {
	bareInput := []byte(`{"model":"antigravity-test","input":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="},{"type":"text","text":"describe"}]}`)
	stepInput := []byte(`{"model":"antigravity-test","input":[{"type":"user_input","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="},{"type":"text","text":"describe"}]}]}`)
	out, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", bareInput, false)
	if errConvert != nil {
		t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
	}
	stepOut, errStep := ConvertInteractionsRequestToAntigravity("antigravity-test", stepInput, false)
	if errStep != nil {
		t.Fatalf("ConvertInteractionsRequestToAntigravity step form error: %v", errStep)
	}
	if got, want := ojson.GetBytesNoCopy(out, "request.contents").Raw, ojson.GetBytesNoCopy(stepOut, "request.contents").Raw; got != want {
		t.Fatalf("bare content block contents = %s, want step form %s", got, want)
	}

	contents := ojson.GetBytesNoCopy(out, "request.contents").Array()
	if len(contents) == 0 {
		t.Fatalf("no contents in output: %s", string(out))
	}
	var inlineData, mimeType, text string
	for _, content := range contents {
		if role := content.Get("role").String(); role != "user" {
			t.Fatalf("content role = %q, want user. Output: %s", role, string(out))
		}
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
			if data := part.Get("inlineData.data").String(); data != "" {
				inlineData = data
				mimeType = part.Get("inlineData.mimeType").String()
			}
			if value := part.Get("text").String(); value != "" {
				text = value
			}
			return true
		})
	}
	if inlineData != "aGVsbG8=" {
		t.Fatalf("image content block was dropped. Output: %s", string(out))
	}
	if mimeType != "image/png" {
		t.Fatalf("image mimeType = %q, want image/png. Output: %s", mimeType, string(out))
	}
	if text != "describe" {
		t.Fatalf("text content block was dropped. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToAntigravityBareInstructionTextKeepsEmptiedTurnRefusal(t *testing.T) {
	for _, role := range []string{"system", "developer"} {
		t.Run(role, func(t *testing.T) {
			for _, order := range []string{"before", "after"} {
				t.Run(order, func(t *testing.T) {
					instruction := `{"type":"text","role":"` + role + `","text":"note"}`
					unsupported := `{"type":"user_input","content":[{"type":"document","uri":"gs://b/a.pdf"}]}`
					input := unsupported + "," + instruction
					if order == "before" {
						input = instruction + "," + unsupported
					}
					_, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","input":[`+input+`]}`), false)
					if errConvert == nil {
						t.Fatalf("expected unsupported document refusal to be preserved for role %s (%s), got nil", role, order)
					}
				})
			}
		})
	}
}

func TestConvertInteractionsRequestToAntigravityGenerationConfigPreservesArraysEmptyContainersAndEscapedKeys(t *testing.T) {
	input := []byte(`{
		"model": "antigravity-test",
		"input": "hi",
		"generation_config": {
			"response_schema": {
				"type": "object",
				"properties": {},
				"required": [],
				"any_of": [
					{"type": "string", "min_length": 1},
					{"type": "integer", "minimum": 0}
				]
			},
			"speech_config": {
				"multi_speaker_voice_config": {
					"speaker_voice_configs": [
						{
							"speaker": "Alice",
							"voice_config": {"prebuilt_voice_config": {"voice_name": "Kore"}}
						}
					]
				}
			},
			"nested_matrix": [[1, 2], [3, 4]],
			"special_keys": {
				"user.name": "a",
				"wild*card": "b",
				":leading": "c",
				"": "d",
				"0": "e"
			}
		}
	}`)

	out, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", input, false)
	if errConvert != nil {
		t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
	}

	genCfg := ojson.GetBytesNoCopy(out, "request.generationConfig")

	anyOf := genCfg.Get("responseSchema.anyOf").Array()
	if len(anyOf) != 2 {
		t.Fatalf("responseSchema.anyOf len = %d, want 2. Output: %s", len(anyOf), genCfg.Raw)
	}
	if anyOf[0].Get("type").String() != "string" || anyOf[0].Get("minLength").Int() != 1 {
		t.Fatalf("responseSchema.anyOf[0] = %s, want both type and minLength", anyOf[0].Raw)
	}
	if minField := anyOf[1].Get("minimum"); anyOf[1].Get("type").String() != "integer" || !minField.Exists() || minField.Raw != "0" {
		t.Fatalf("responseSchema.anyOf[1] = %s, want both type and minimum", anyOf[1].Raw)
	}

	speakers := genCfg.Get("speechConfig.multiSpeakerVoiceConfig.speakerVoiceConfigs").Array()
	if len(speakers) != 1 {
		t.Fatalf("speakerVoiceConfigs len = %d, want 1. Output: %s", len(speakers), genCfg.Raw)
	}
	if speakers[0].Get("speaker").String() != "Alice" || speakers[0].Get("voiceConfig.prebuiltVoiceConfig.voiceName").String() != "Kore" {
		t.Fatalf("speakerVoiceConfigs[0] = %s, want both speaker and voiceConfig", speakers[0].Raw)
	}

	props := genCfg.Get("responseSchema.properties")
	if !props.Exists() || !props.IsObject() || len(props.Map()) != 0 {
		t.Fatalf("responseSchema.properties = %q (exists=%v), want empty object {}", props.Raw, props.Exists())
	}
	req := genCfg.Get("responseSchema.required")
	if !req.Exists() || !req.IsArray() || len(req.Array()) != 0 {
		t.Fatalf("responseSchema.required = %q (exists=%v), want empty array []", req.Raw, req.Exists())
	}

	matrix := genCfg.Get("nestedMatrix").Array()
	if len(matrix) != 2 || len(matrix[0].Array()) != 2 || len(matrix[1].Array()) != 2 {
		t.Fatalf("nestedMatrix = %s, want [[1,2],[3,4]]", genCfg.Get("nestedMatrix").Raw)
	}

	special := genCfg.Get("specialKeys").Map()
	for k, want := range map[string]string{
		"user.name": "a",
		"wild*card": "b",
		":leading":  "c",
		"":          "d",
		"0":         "e",
	} {
		if got, ok := special[k]; !ok || got.String() != want {
			t.Fatalf("specialKeys[%q] = %q (ok=%v), want %q. Raw: %s", k, got.String(), ok, want, genCfg.Get("specialKeys").Raw)
		}
	}
}

func TestConvertInteractionsRequestToAntigravity_FunctionCallArgsNormalizedToObject(t *testing.T) {
	tests := []struct {
		name      string
		argsField string
		wantRaw   string
	}{
		{
			name:      "valid JSON object preserved",
			argsField: `,"arguments":{"city":"Tokyo","unit":"c"}`,
			wantRaw:   `{"city":"Tokyo","unit":"c"}`,
		},
		{
			name:      "JSON-encoded object string unwrapped",
			argsField: `,"arguments":" {\"city\":\"Tokyo\"} "`,
			wantRaw:   `{"city":"Tokyo"}`,
		},
		{
			name:      "JSON-encoded array string falls back to empty object",
			argsField: `,"arguments":"[1,2,3]"`,
			wantRaw:   `{}`,
		},
		{
			name:      "JSON-encoded scalar string falls back to empty object",
			argsField: `,"arguments":"123"`,
			wantRaw:   `{}`,
		},
		{
			name:      "JSON-encoded null string falls back to empty object",
			argsField: `,"arguments":"null"`,
			wantRaw:   `{}`,
		},
		{
			name:      "empty string falls back to empty object",
			argsField: `,"arguments":""`,
			wantRaw:   `{}`,
		},
		{
			name:      "invalid JSON string falls back to empty object",
			argsField: `,"arguments":"{broken"`,
			wantRaw:   `{}`,
		},
		{
			name:      "null value falls back to empty object",
			argsField: `,"arguments":null`,
			wantRaw:   `{}`,
		},
		{
			name:      "array value falls back to empty object",
			argsField: `,"arguments":[1,2]`,
			wantRaw:   `{}`,
		},
		{
			name:      "number scalar falls back to empty object",
			argsField: `,"arguments":42`,
			wantRaw:   `{}`,
		},
		{
			name:      "boolean scalar falls back to empty object",
			argsField: `,"arguments":true`,
			wantRaw:   `{}`,
		},
		{
			name:      "missing arguments defaults to empty object",
			argsField: "",
			wantRaw:   `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "antigravity-test",
				"input": [
					{"type": "user_input", "content": [{"type": "text", "text": "check weather"}]},
					{"type": "function_call", "call_id": "call_1", "name": "get_weather"` + tt.argsField + `},
					{"type": "function_result", "call_id": "call_1", "name": "get_weather", "result": {"temp": 22}}
				]
			}`)
			out, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", input, false)
			if errConvert != nil {
				t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
			}
			args := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0.functionCall.args")
			if !args.Exists() || !args.IsObject() {
				t.Fatalf("functionCall.args = %s (type=%s), want JSON object. Output: %s", args.Raw, args.Type, string(out))
			}
			if args.Raw != tt.wantRaw {
				t.Fatalf("functionCall.args = %s, want %s. Output: %s", args.Raw, tt.wantRaw, string(out))
			}
		})
	}
}

func TestConvertInteractionsRequestToAntigravity_NativeFunctionCallArgsNormalizedToObject(t *testing.T) {
	tests := []struct {
		name      string
		argsField string
		wantRaw   string
	}{
		{
			name:      "valid object preserved",
			argsField: `,"args":{"q":"go"}`,
			wantRaw:   `{"q":"go"}`,
		},
		{
			name:      "JSON-encoded object string unwrapped",
			argsField: `,"args":"{\"q\":\"go\"}"`,
			wantRaw:   `{"q":"go"}`,
		},
		{
			name:      "array falls back to empty object",
			argsField: `,"args":[]`,
			wantRaw:   `{}`,
		},
		{
			name:      "null falls back to empty object",
			argsField: `,"args":null`,
			wantRaw:   `{}`,
		},
		{
			name:      "scalar falls back to empty object",
			argsField: `,"args":42`,
			wantRaw:   `{}`,
		},
		{
			name:      "invalid JSON string falls back to empty object",
			argsField: `,"args":"{bad"`,
			wantRaw:   `{}`,
		},
		{
			name:      "missing args defaults to empty object",
			argsField: "",
			wantRaw:   `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "antigravity-test",
				"input": [
					{"role": "user", "parts": [{"text": "lookup"}]},
					{"role": "model", "parts": [{"functionCall": {"name": "lookup", "id": "call_1"` + tt.argsField + `}, "thoughtSignature": "sig_keep"}]},
					{"role": "user", "parts": [{"functionResponse": {"name": "lookup", "id": "call_1", "response": {"ok": true}}}]}
				]
			}`)
			out, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", input, false)
			if errConvert != nil {
				t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
			}
			part := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0")
			args := part.Get("functionCall.args")
			if !args.Exists() || !args.IsObject() {
				t.Fatalf("functionCall.args = %s (type=%s), want JSON object. Output: %s", args.Raw, args.Type, string(out))
			}
			if args.Raw != tt.wantRaw {
				t.Fatalf("functionCall.args = %s, want %s. Output: %s", args.Raw, tt.wantRaw, string(out))
			}
			if got := part.Get("functionCall.name").String(); got != "lookup" {
				t.Fatalf("functionCall.name = %q, want lookup", got)
			}
			if got := part.Get("thoughtSignature").String(); got != "sig_keep" {
				t.Fatalf("thoughtSignature = %q, want sig_keep", got)
			}
		})
	}
}

func TestConvertAntigravityResponseToInteractionsStream_InlineData(t *testing.T) {
	tests := []struct {
		name        string
		partJSON    string
		wantType    string
		wantMime    string
		wantData    string
		wantThought string
	}{
		{
			name:        "camelCase inlineData image with thoughtSignature",
			partJSON:    `{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="},"thoughtSignature":"sig_img"}`,
			wantType:    "image",
			wantMime:    "image/png",
			wantData:    "aGVsbG8=",
			wantThought: "sig_img",
		},
		{
			name:     "snake_case inline_data audio",
			partJSON: `{"inline_data":{"mime_type":"audio/wav","data":"YXVkaW8="}}`,
			wantType: "audio",
			wantMime: "audio/wav",
			wantData: "YXVkaW8=",
		},
		{
			name:     "inlineData video",
			partJSON: `{"inlineData":{"mimeType":"video/mp4","data":"dmlkZW8="}}`,
			wantType: "video",
			wantMime: "video/mp4",
			wantData: "dmlkZW8=",
		},
		{
			name:     "inlineData document fallback",
			partJSON: `{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}`,
			wantType: "document",
			wantMime: "application/pdf",
			wantData: "JVBERi0=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[` + tt.partJSON + `]}}]}}`)
			events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk, &param)

			starts := findAntigravityInteractionsEventPayloads(events, "step.start")
			if len(starts) == 0 {
				t.Fatalf("step.start event not found: %q", events)
			}
			if got := ojson.GetBytesNoCopy(starts[0], "step.type").String(); got != "model_output" {
				t.Fatalf("step.start[0].step.type = %q, want model_output. Payload: %s", got, starts[0])
			}

			deltas := findAntigravityInteractionsEventPayloads(events, "step.delta")
			if len(deltas) == 0 {
				t.Fatalf("step.delta event not found: %q", events)
			}
			if ojson.GetBytesNoCopy(deltas[0], "delta.content").Exists() {
				t.Fatalf("delta.content must not wrap media delta. Payload: %s", deltas[0])
			}
			if got := ojson.GetBytesNoCopy(deltas[0], "delta.type").String(); got != tt.wantType {
				t.Fatalf("delta.type = %q, want %q. Payload: %s", got, tt.wantType, deltas[0])
			}
			if got := ojson.GetBytesNoCopy(deltas[0], "delta.mime_type").String(); got != tt.wantMime {
				t.Fatalf("delta.mime_type = %q, want %q. Payload: %s", got, tt.wantMime, deltas[0])
			}
			if got := ojson.GetBytesNoCopy(deltas[0], "delta.data").String(); got != tt.wantData {
				t.Fatalf("delta.data = %q, want %q. Payload: %s", got, tt.wantData, deltas[0])
			}

			if tt.wantThought != "" {
				if len(deltas) < 2 {
					t.Fatalf("expected trailing thought_signature delta, got %d deltas: %q", len(deltas), events)
				}
				if got := ojson.GetBytesNoCopy(deltas[1], "delta.type").String(); got != "thought_signature" {
					t.Fatalf("deltas[1].delta.type = %q, want thought_signature. Payload: %s", got, deltas[1])
				}
				if got := ojson.GetBytesNoCopy(deltas[1], "delta.signature").String(); got != tt.wantThought {
					t.Fatalf("deltas[1].delta.signature = %q, want %q. Payload: %s", got, tt.wantThought, deltas[1])
				}
			}
		})
	}
}

func TestConvertAntigravityResponseToInteractionsNonStream_FunctionCallEmitsIDAndCallID(t *testing.T) {
	tests := []struct {
		name   string
		fcJSON string
		wantID string
	}{
		{
			name:   "upstream id populates both id and call_id",
			fcJSON: `{"name":"lookup","id":"call_1","args":{"q":"x"}}`,
			wantID: "call_1",
		},
		{
			name:   "upstream call_id populates both id and call_id",
			fcJSON: `{"name":"lookup","call_id":"call_2","args":{"q":"x"}}`,
			wantID: "call_2",
		},
		{
			name:   "empty upstream id falls back to call_id",
			fcJSON: `{"name":"lookup","id":"","call_id":"call_3","args":{"q":"x"}}`,
			wantID: "call_3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"functionCall":` + tt.fcJSON + `}]},"finishReason":"STOP"}]}}`)
			out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", nil, nil, raw, nil)
			if got := ojson.GetBytesNoCopy(out, "steps.0.id").String(); got != tt.wantID {
				t.Fatalf("steps.0.id = %q, want %q. Output: %s", got, tt.wantID, string(out))
			}
			if got := ojson.GetBytesNoCopy(out, "steps.0.call_id").String(); got != tt.wantID {
				t.Fatalf("steps.0.call_id = %q, want %q. Output: %s", got, tt.wantID, string(out))
			}
		})
	}
}

func TestConvertAntigravityResponseToInteractions_FunctionCallArgsNormalizedToObject(t *testing.T) {
	tests := []struct {
		name      string
		argsField string
		wantRaw   string
	}{
		{
			name:      "valid JSON object preserved",
			argsField: `,"args":{"q":"go"}`,
			wantRaw:   `{"q":"go"}`,
		},
		{
			name:      "JSON-encoded object string unwrapped",
			argsField: `,"args":"{\"q\":\"go\"}"`,
			wantRaw:   `{"q":"go"}`,
		},
		{
			name:      "null falls back to empty object",
			argsField: `,"args":null`,
			wantRaw:   `{}`,
		},
		{
			name:      "array falls back to empty object",
			argsField: `,"args":[1,2]`,
			wantRaw:   `{}`,
		},
		{
			name:      "number scalar falls back to empty object",
			argsField: `,"args":42`,
			wantRaw:   `{}`,
		},
		{
			name:      "boolean scalar falls back to empty object",
			argsField: `,"args":true`,
			wantRaw:   `{}`,
		},
		{
			name:      "non-object string falls back to empty object",
			argsField: `,"args":"not-an-object"`,
			wantRaw:   `{}`,
		},
		{
			name:      "missing args defaults to empty object",
			argsField: "",
			wantRaw:   `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			part := `{"functionCall":{"name":"lookup","id":"call_1"` + tt.argsField + `}}`
			nonStreamRaw := []byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[` + part + `]},"finishReason":"STOP"}]}}`)
			out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", nil, nil, nonStreamRaw, nil)
			args := ojson.GetBytesNoCopy(out, "steps.0.arguments")
			if !args.Exists() || !args.IsObject() {
				t.Fatalf("non-stream steps.0.arguments = %s (type=%s), want JSON object. Output: %s", args.Raw, args.Type, string(out))
			}
			if args.Raw != tt.wantRaw {
				t.Fatalf("non-stream steps.0.arguments = %s, want %s. Output: %s", args.Raw, tt.wantRaw, string(out))
			}

			var param any
			streamRaw := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[` + part + `]}}]}}`)
			events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, streamRaw, &param)
			delta := findAntigravityInteractionsEventPayload(events, "step.delta")
			if len(delta) == 0 {
				t.Fatalf("stream step.delta not found: %q", events)
			}
			gotStreamArgs := ojson.GetBytesNoCopy(delta, "delta.arguments").String()
			if gotStreamArgs != tt.wantRaw {
				t.Fatalf("stream delta.arguments = %q, want %q. Payload: %s", gotStreamArgs, tt.wantRaw, string(delta))
			}
		})
	}
}

func TestConvertAntigravityResponseToInteractionsStream_ParallelFunctionCallFallbackIDsUnique(t *testing.T) {
	var param any
	chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"first","args":{"a":1}}},{"functionCall":{"name":"second","args":{"b":2}}}]}}]}}`)
	events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk, &param)

	starts := findAntigravityInteractionsEventPayloads(events, "step.start")
	if len(starts) != 2 {
		t.Fatalf("step.start count = %d, want 2. Events: %q", len(starts), events)
	}
	id0 := ojson.GetBytesNoCopy(starts[0], "step.id").String()
	callID0 := ojson.GetBytesNoCopy(starts[0], "step.call_id").String()
	id1 := ojson.GetBytesNoCopy(starts[1], "step.id").String()
	callID1 := ojson.GetBytesNoCopy(starts[1], "step.call_id").String()

	if id0 == "" || id0 != callID0 {
		t.Fatalf("first step id/call_id mismatch: id=%q call_id=%q", id0, callID0)
	}
	if id1 == "" || id1 != callID1 {
		t.Fatalf("second step id/call_id mismatch: id=%q call_id=%q", id1, callID1)
	}
	if id0 == id1 {
		t.Fatalf("parallel fallback step IDs collided: %q == %q", id0, id1)
	}
	if !strings.HasSuffix(id0, "_0") {
		t.Fatalf("first fallback step.id = %q, want _0 suffix for ActiveStepIndex=0", id0)
	}
	if !strings.HasSuffix(id1, "_1") {
		t.Fatalf("second fallback step.id = %q, want _1 suffix for ActiveStepIndex=1", id1)
	}
}

func TestConvertAntigravityResponseToInteractionsNonStream_ParallelFunctionCallFallbackIDsUnique(t *testing.T) {
	raw := []byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"first","args":{"a":1}}},{"functionCall":{"name":"second","args":{"b":2}}}]},"finishReason":"STOP"}]}}`)
	out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", nil, nil, raw, nil)

	id0 := ojson.GetBytesNoCopy(out, "steps.0.id").String()
	callID0 := ojson.GetBytesNoCopy(out, "steps.0.call_id").String()
	id1 := ojson.GetBytesNoCopy(out, "steps.1.id").String()
	callID1 := ojson.GetBytesNoCopy(out, "steps.1.call_id").String()

	if id0 == "" || id0 != callID0 {
		t.Fatalf("first non-stream function_call id/call_id mismatch: id=%q call_id=%q. Output: %s", id0, callID0, string(out))
	}
	if id1 == "" || id1 != callID1 {
		t.Fatalf("second non-stream function_call id/call_id mismatch: id=%q call_id=%q. Output: %s", id1, callID1, string(out))
	}
	if id0 == id1 {
		t.Fatalf("non-stream parallel fallback function_call IDs collided: %q == %q. Output: %s", id0, id1, string(out))
	}
	if !strings.HasSuffix(id0, "_0") {
		t.Fatalf("first non-stream fallback id = %q, want _0 suffix. Output: %s", id0, string(out))
	}
	if !strings.HasSuffix(id1, "_1") {
		t.Fatalf("second non-stream fallback id = %q, want _1 suffix. Output: %s", id1, string(out))
	}
}

func TestConvertInteractionsRequestToAntigravitySystemInstructionNormalization(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantExist bool
		wantText  string
	}{
		{
			name:      "null system_instruction is omitted",
			input:     `{"model":"antigravity-test","system_instruction":null,"input":"hi"}`,
			wantExist: false,
		},
		{
			name:      "array of text parts converted to systemInstruction parts",
			input:     `{"model":"antigravity-test","system_instruction":[{"type":"text","text":"be brief"},{"text":"be accurate"},"stay focused"],"input":"hi"}`,
			wantExist: true,
			wantText:  "be brief\n\nbe accurate\n\nstay focused",
		},
		{
			name:      "empty array system_instruction is omitted",
			input:     `{"model":"antigravity-test","system_instruction":[],"input":"hi"}`,
			wantExist: false,
		},
		{
			name:      "array with only non-text items is omitted",
			input:     `{"model":"antigravity-test","system_instruction":[null,{}],"input":"hi"}`,
			wantExist: false,
		},
		{
			name:      "primitive non-string system_instruction is omitted",
			input:     `{"model":"antigravity-test","system_instruction":123,"input":"hi"}`,
			wantExist: false,
		},
		{
			name:      "object system_instruction with text is wrapped",
			input:     `{"model":"antigravity-test","system_instruction":{"text":"be brief"},"input":"hi"}`,
			wantExist: true,
			wantText:  "be brief",
		},
		{
			name:      "object system_instruction with null text is omitted",
			input:     `{"model":"antigravity-test","system_instruction":{"text":null},"input":"hi"}`,
			wantExist: false,
		},
		{
			name:      "object system_instruction with parts is preserved",
			input:     `{"model":"antigravity-test","system_instruction":{"parts":[{"text":"part one"},{"text":"part two"}]},"input":"hi"}`,
			wantExist: true,
			wantText:  "part one\n\npart two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(tt.input), false)
			if errConvert != nil {
				t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
			}
			sys := ojson.GetBytesNoCopy(out, "request.systemInstruction")
			if sys.Exists() != tt.wantExist {
				t.Fatalf("request.systemInstruction exists = %v (raw=%s), want %v. Output: %s", sys.Exists(), sys.Raw, tt.wantExist, string(out))
			}
			if !tt.wantExist {
				return
			}
			if !sys.IsObject() {
				t.Fatalf("request.systemInstruction is not an object: %s. Output: %s", sys.Raw, string(out))
			}
			if gotRole := sys.Get("role").String(); gotRole != "user" {
				t.Fatalf("request.systemInstruction.role = %q, want user. Output: %s", gotRole, string(out))
			}
			if gotText := sys.Get("parts.0.text").String(); gotText != tt.wantText {
				t.Fatalf("request.systemInstruction.parts.0.text = %q, want %q. Output: %s", gotText, tt.wantText, string(out))
			}
		})
	}
}

func TestConvertInteractionsRequestToAntigravityReasoningClearsConflictingThinkingConfig(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantBudget    bool
		wantBudgetVal int64
		wantLevel     bool
		wantLevelVal  string
	}{
		{
			name:          "reasoning auto deletes thinkingLevel before setting thinkingBudget",
			input:         `{"model":"antigravity-test","input":"hi","generation_config":{"thinking_level":"high"},"reasoning":{"effort":"auto"}}`,
			wantBudget:    true,
			wantBudgetVal: -1,
		},
		{
			name:         "reasoning effort deletes thinkingBudget before setting thinkingLevel",
			input:        `{"model":"antigravity-test","input":"hi","generation_config":{"thinking_budget":2048},"reasoning":{"effort":"high"}}`,
			wantLevel:    true,
			wantLevelVal: "high",
		},
		{
			name:         "reasoning thinking_level deletes nested thinkingConfig thinkingBudget",
			input:        `{"model":"antigravity-test","input":"hi","generationConfig":{"thinkingConfig":{"thinkingBudget":1024}},"reasoning":{"thinking_level":"medium"}}`,
			wantLevel:    true,
			wantLevelVal: "medium",
		},
		{
			name:          "reasoning auto deletes nested thinkingConfig thinkingLevel",
			input:         `{"model":"antigravity-test","input":"hi","generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}},"reasoning":{"effort":"auto"}}`,
			wantBudget:    true,
			wantBudgetVal: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errConvert := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(tt.input), false)
			if errConvert != nil {
				t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
			}
			budget := ojson.GetBytesNoCopy(out, "request.generationConfig.thinkingConfig.thinkingBudget")
			level := ojson.GetBytesNoCopy(out, "request.generationConfig.thinkingConfig.thinkingLevel")
			if budget.Exists() != tt.wantBudget {
				t.Fatalf("thinkingBudget exists = %v (raw=%s), want %v. Output: %s", budget.Exists(), budget.Raw, tt.wantBudget, string(out))
			}
			if tt.wantBudget && budget.Int() != tt.wantBudgetVal {
				t.Fatalf("thinkingBudget = %d, want %d. Output: %s", budget.Int(), tt.wantBudgetVal, string(out))
			}
			if level.Exists() != tt.wantLevel {
				t.Fatalf("thinkingLevel exists = %v (raw=%s), want %v. Output: %s", level.Exists(), level.Raw, tt.wantLevel, string(out))
			}
			if tt.wantLevel && level.String() != tt.wantLevelVal {
				t.Fatalf("thinkingLevel = %q, want %q. Output: %s", level.String(), tt.wantLevelVal, string(out))
			}
		})
	}
}

func testAntigravityInteractionsClaudeNativeSignature(t *testing.T) string {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	return base64.StdEncoding.EncodeToString(payload)
}

func TestConvertInteractionsRequestToAntigravity_ClaudeThoughtSignatureInlineAndSanitized(t *testing.T) {
	nativeSig := testAntigravityInteractionsClaudeNativeSignature(t)
	expectedSig, ok := signature.CompatibleAntigravityClaudeThinkingSignature(nativeSig)
	if !ok {
		t.Fatal("test Claude signature should be compatible with Antigravity Claude")
	}

	t.Run("attaches_claude_thought_signature_inline_without_detached_carrier", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-opus-5-5-thinking",
			"input": [
				{"type": "thought", "summary": [{"type": "text", "text": "thinking"}], "signature": "` + expectedSig + `"},
				{"type": "model_output", "content": [{"type": "text", "text": "answer"}]},
				{"type": "user_input", "content": [{"type": "text", "text": "next"}]}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-5-5-thinking", inputJSON, true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 model parts (signed thought + answer, no detached empty carrier), got %d: %s", len(parts), string(out))
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "thinking" || parts[0].Get("thoughtSignature").String() != expectedSig {
			t.Fatalf("expected parts[0] to carry inline thoughtSignature=%q, got: %s", expectedSig, parts[0].Raw)
		}
		if parts[1].Get("thought").Bool() || parts[1].Get("text").String() != "answer" || parts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("expected parts[1] to be plain answer text without signature, got: %s", parts[1].Raw)
		}
	})

	t.Run("normalizes_native_claude_signature_inline", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-opus-4-6-thinking",
			"input": [
				{"type": "thought", "content": [{"type": "text", "text": "internal reasoning"}], "signature": "` + nativeSig + `"},
				{"type": "model_output", "content": [{"type": "text", "text": "visible answer"}]},
				{"type": "user_input", "content": [{"type": "text", "text": "continue"}]}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 model parts, got %d: %s", len(parts), string(out))
		}
		if gotSig := parts[0].Get("thoughtSignature").String(); gotSig != expectedSig {
			t.Fatalf("expected normalized inline thoughtSignature %q, got %q (part=%s)", expectedSig, gotSig, parts[0].Raw)
		}
	})

	t.Run("strips_foreign_gemini_signature_and_drops_incompatible_thought_for_claude", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-opus-5-5-thinking",
			"input": [
				{"type": "thought", "summary": [{"type": "text", "text": "gemini thinking"}], "signature": "EggKBgEMOdbHNA=="},
				{"type": "model_output", "content": [{"type": "text", "text": "answer"}]},
				{"type": "user_input", "content": [{"type": "text", "text": "next"}]}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-5-5-thinking", inputJSON, true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		if bytes.Contains(out, []byte("thoughtSignature")) || bytes.Contains(out, []byte("EggKBgEMOdbHNA==")) {
			t.Fatalf("foreign Gemini thoughtSignature leaked into Claude request: %s", string(out))
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 1 || parts[0].Get("text").String() != "answer" || parts[0].Get("thought").Bool() {
			t.Fatalf("expected only plain answer part after dropping incompatible thought, got: %s", string(out))
		}
	})

	t.Run("signature_only_thought_never_emits_empty_carrier_or_function_call_signature_for_claude", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-sonnet-5-5-thinking",
			"input": [
				{"type": "thought", "signature": "` + expectedSig + `"},
				{"type": "model_output", "content": [{"type": "text", "text": "answer"}]},
				{"type": "thought", "signature": "EggKBgEMOdbHNA=="},
				{"type": "function_call", "name": "lookup", "call_id": "call_1", "arguments": {"q": "x"}, "signature": "EggKBgEMOdbHNA=="},
				{"type": "thought", "signature": "` + expectedSig + `"},
				{"type": "function_result", "name": "lookup", "call_id": "call_1", "result": {"ok": true}}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-sonnet-5-5-thinking", inputJSON, true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		if bytes.Contains(out, []byte("thoughtSignature")) {
			t.Fatalf("unexpected thoughtSignature in Claude request: %s", string(out))
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected exactly 2 model parts (answer + functionCall, no empty carrier), got %d: %s", len(parts), string(out))
		}
		if parts[0].Get("text").String() != "answer" {
			t.Fatalf("expected parts[0] to be answer, got: %s", parts[0].Raw)
		}
		if parts[1].Get("functionCall.name").String() != "lookup" {
			t.Fatalf("expected parts[1] to be functionCall lookup, got: %s", parts[1].Raw)
		}
	})

	t.Run("adjacent_text_thought_and_signature_only_thought_attach_inline_for_claude", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-opus-5-5-thinking",
			"input": [
				{"type": "thought", "content": [{"type": "text", "text": "step reasoning"}]},
				{"type": "thought", "signature": "` + expectedSig + `"},
				{"type": "model_output", "content": [{"type": "text", "text": "done"}]}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-5-5-thinking", inputJSON, true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 model parts (signed thought + done), got %d: %s", len(parts), string(out))
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "step reasoning" || parts[0].Get("thoughtSignature").String() != expectedSig {
			t.Fatalf("expected parts[0] to attach signature inline, got: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != "done" || parts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("expected parts[1] to be plain text done, got: %s", parts[1].Raw)
		}
	})

	t.Run("adjacent_multi_text_thought_and_signature_only_thought_merge_and_attach_inline_for_claude", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-opus-5-5-thinking",
			"input": [
				{"type": "thought", "summary": [{"type": "text", "text": "part 1 "}, {"type": "text", "text": "part 2"}]},
				{"type": "thought", "signature": "` + expectedSig + `"},
				{"type": "model_output", "content": [{"type": "text", "text": "done"}]}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-5-5-thinking", inputJSON, true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 model parts (merged signed thought + done), got %d: %s", len(parts), string(out))
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "part 1 part 2" || parts[0].Get("thoughtSignature").String() != expectedSig {
			t.Fatalf("expected parts[0] to merge multi-part thought text and attach delayed signature inline, got: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != "done" || parts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("expected parts[1] to be plain text done, got: %s", parts[1].Raw)
		}
	})

	t.Run("multi_text_thought_summary_merges_with_inline_signature_for_claude", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "claude-opus-5-5-thinking",
			"input": [
				{"type": "thought", "summary": [{"type": "text", "text": "part 1 "}, {"type": "text", "text": "part 2"}], "signature": "` + expectedSig + `"},
				{"type": "model_output", "content": [{"type": "text", "text": "answer"}]}
			]
		}`)
		out, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-5-5-thinking", inputJSON, true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		parts := ojson.GetBytesNoCopy(out, "request.contents.0.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 model parts (merged signed thought + answer), got %d: %s", len(parts), string(out))
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "part 1 part 2" || parts[0].Get("thoughtSignature").String() != expectedSig {
			t.Fatalf("expected parts[0] to merge thought text with inline signature, got: %s", parts[0].Raw)
		}
	})

	t.Run("claude_response_to_request_round_trip_preserves_inline_thought_signature", func(t *testing.T) {
		upstreamResponse := []byte(`{
			"response": {
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [
							{"text": "thinking", "thought": true, "thoughtSignature": "` + expectedSig + `"},
							{"text": "answer"}
						]
					}
				}]
			}
		}`)
		respOut := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "claude-opus-5-5-thinking", nil, nil, upstreamResponse, nil)
		steps := ojson.GetBytesNoCopy(respOut, "steps").Raw

		turn2Input := `{"model":"claude-opus-5-5-thinking","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}`
		for _, s := range ojson.Get(steps, "@this").Array() {
			turn2Input += "," + s.Raw
		}
		turn2Input += `,{"type":"user_input","content":[{"type":"text","text":"next"}]}]}`

		reqOut, errConvert := ConvertInteractionsRequestToAntigravity("claude-opus-5-5-thinking", []byte(turn2Input), true)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		modelParts := ojson.GetBytesNoCopy(reqOut, "request.contents.1.parts").Array()
		if len(modelParts) != 2 {
			t.Fatalf("expected 2 model parts after round-trip, got %d: %s", len(modelParts), string(reqOut))
		}
		if !modelParts[0].Get("thought").Bool() || modelParts[0].Get("text").String() != "thinking" || modelParts[0].Get("thoughtSignature").String() != expectedSig {
			t.Fatalf("expected round-tripped thought part with inline signature, got: %s", modelParts[0].Raw)
		}
		if modelParts[1].Get("text").String() != "answer" || modelParts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("expected round-tripped answer part without signature, got: %s", modelParts[1].Raw)
		}
	})
}

func TestConvertAntigravityResponseToInteractionsStream_VertexPartialArgs(t *testing.T) {
	t.Run("multi_chunk_partial_args", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"write_file","partialArgs":[{"jsonPath":"$.path","stringValue":"main.go"}],"willContinue":true}}]}}]}}`)
		chunk2 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.content","stringValue":"package main"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)

		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk1, &param)
		if got := countAntigravityInteractionsEvents(events1, "step.start"); got != 1 {
			t.Fatalf("chunk1 step.start count = %d, want 1. Events: %q", got, events1)
		}
		if got := countAntigravityInteractionsEvents(events1, "step.stop"); got != 0 {
			t.Fatalf("chunk1 step.stop count = %d, want 0 while willContinue=true. Events: %q", got, events1)
		}
		startPayload := findAntigravityInteractionsEventPayload(events1, "step.start")
		if got := ojson.GetBytesNoCopy(startPayload, "step.name").String(); got != "write_file" {
			t.Fatalf("step.name = %q, want write_file. Payload: %s", got, string(startPayload))
		}

		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk2, &param)
		if got := countAntigravityInteractionsEvents(events2, "step.start"); got != 0 {
			t.Fatalf("chunk2 step.start count = %d, want 0. Events: %q", got, events2)
		}
		if got := countAntigravityInteractionsEvents(events2, "step.stop"); got != 1 {
			t.Fatalf("chunk2 step.stop count = %d, want 1. Events: %q", got, events2)
		}

		var allEvents [][]byte
		allEvents = append(allEvents, events1...)
		allEvents = append(allEvents, events2...)
		var argsBuilder strings.Builder
		for _, deltaPayload := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(deltaPayload, "delta.type").String() != "arguments_delta" {
				continue
			}
			if idx := ojson.GetBytesNoCopy(deltaPayload, "index").Int(); idx != 0 {
				t.Fatalf("arguments_delta index = %d, want 0. Payload: %s", idx, string(deltaPayload))
			}
			argsBuilder.WriteString(ojson.GetBytesNoCopy(deltaPayload, "delta.arguments").String())
		}
		gotArgs := argsBuilder.String()
		if !ojson.Valid(gotArgs) {
			t.Fatalf("assembled arguments is not valid JSON: %q", gotArgs)
		}
		if ojson.Get(gotArgs, "path").String() != "main.go" || ojson.Get(gotArgs, "content").String() != "package main" {
			t.Fatalf("assembled arguments = %s, want path=main.go and content=package main", gotArgs)
		}
	})

	t.Run("single_chunk_parameterless_will_continue_false", func(t *testing.T) {
		var param any
		chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"ping","willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
		events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk, &param)
		if got := countAntigravityInteractionsEvents(events, "step.start"); got != 1 {
			t.Fatalf("step.start count = %d, want 1. Events: %q", got, events)
		}
		if got := countAntigravityInteractionsEvents(events, "step.stop"); got != 1 {
			t.Fatalf("step.stop count = %d, want 1. Events: %q", got, events)
		}
		delta := findAntigravityInteractionsEventPayload(events, "step.delta")
		if got := ojson.GetBytesNoCopy(delta, "delta.arguments").String(); got != "{}" {
			t.Fatalf("delta.arguments = %q, want {}", got)
		}
	})

	t.Run("args_with_will_continue_preserves_all_fields", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"write_file","args":{"path":"a.txt"},"willContinue":true}}]}}]}}`)
		chunk2 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","args":{"content":"hello"},"willContinue":false}}]}}]}}`)
		chunk3 := []byte(`data: {"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`)

		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk1, &param)
		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk2, &param)
		events3 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk3, &param)
		allEvents := append(append(events1, events2...), events3...)

		if got := countAntigravityInteractionsEvents(allEvents, "step.start"); got != 1 {
			t.Fatalf("step.start count = %d, want 1. Events: %q", got, allEvents)
		}
		if got := countAntigravityInteractionsEvents(allEvents, "step.stop"); got != 1 {
			t.Fatalf("step.stop count = %d, want 1. Events: %q", got, allEvents)
		}
		startPayload := findAntigravityInteractionsEventPayload(allEvents, "step.start")
		if got := ojson.GetBytesNoCopy(startPayload, "step.name").String(); got != "write_file" {
			t.Fatalf("step.name = %q, want write_file", got)
		}
		if got := ojson.GetBytesNoCopy(startPayload, "step.call_id").String(); got != "call_1" {
			t.Fatalf("step.call_id = %q, want call_1", got)
		}
		var argsBuilder strings.Builder
		for _, delta := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(delta, "delta.type").String() == "arguments_delta" {
				argsBuilder.WriteString(ojson.GetBytesNoCopy(delta, "delta.arguments").String())
			}
		}
		if got := argsBuilder.String(); got != `{"path":"a.txt","content":"hello"}` {
			t.Fatalf("assembled arguments = %q, want %q", got, `{"path":"a.txt","content":"hello"}`)
		}
	})

	t.Run("args_will_continue_start_without_continuation_closes_to_empty_object", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"get_value","willContinue":true}}]}}]}}`)
		chunk2 := []byte(`data: {"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)

		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk1, &param)
		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk2, &param)
		allEvents := append(events1, events2...)

		if got := countAntigravityInteractionsEvents(allEvents, "step.start"); got != 1 {
			t.Fatalf("step.start count = %d, want 1. Events: %q", got, allEvents)
		}
		if got := countAntigravityInteractionsEvents(allEvents, "step.stop"); got != 1 {
			t.Fatalf("step.stop count = %d, want 1. Events: %q", got, allEvents)
		}
		var argsBuilder strings.Builder
		for _, delta := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(delta, "delta.type").String() == "arguments_delta" {
				argsBuilder.WriteString(ojson.GetBytesNoCopy(delta, "delta.arguments").String())
			}
		}
		if got := argsBuilder.String(); got != "{}" {
			t.Fatalf("assembled arguments = %q, want {}", got)
		}
	})

	t.Run("continuation_thought_signature_attaches_to_call_A_not_call_B_on_round_trip", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"call_A","partialArgs":[{"jsonPath":"$.a","stringValue":"1"}],"willContinue":true}}]}}]}}`)
		chunk2 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","partialArgs":[{"jsonPath":"$.b","stringValue":"2"}],"willContinue":false},"thoughtSignature":"sig_call_A"}]}}]}}`)
		chunk3 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_2","name":"call_B","args":{"c":"3"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)

		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "gemini-3.8-flash-high", nil, nil, chunk1, &param)
		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "gemini-3.8-flash-high", nil, nil, chunk2, &param)
		events3 := ConvertAntigravityResponseToInteractions(context.Background(), "gemini-3.8-flash-high", nil, nil, chunk3, &param)
		allEvents := append(append(events1, events2...), events3...)

		starts := findAntigravityInteractionsEventPayloads(allEvents, "step.start")
		if len(starts) != 2 {
			t.Fatalf("step.start count = %d, want 2 (call_A and call_B, no detached thought step). Events: %q", len(starts), allEvents)
		}
		type stepAgg struct {
			stepType string
			id       string
			callID   string
			name     string
			sig      string
			args     strings.Builder
		}
		byIndex := make(map[int]*stepAgg)
		var order []int
		for _, rawEv := range allEvents {
			idxLine := bytes.Index(rawEv, []byte("data: "))
			if idxLine < 0 {
				continue
			}
			payload := bytes.TrimSpace(rawEv[idxLine+6:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			root := ojson.ParseBytes(payload)
			switch root.Get("event_type").String() {
			case "step.start":
				stepIdx := int(root.Get("index").Int())
				agg := &stepAgg{
					stepType: root.Get("step.type").String(),
					id:       root.Get("step.id").String(),
					callID:   root.Get("step.call_id").String(),
					name:     root.Get("step.name").String(),
				}
				byIndex[stepIdx] = agg
				order = append(order, stepIdx)
			case "step.delta":
				stepIdx := int(root.Get("index").Int())
				agg := byIndex[stepIdx]
				if agg == nil {
					t.Fatalf("step.delta for unknown index %d: %s", stepIdx, string(payload))
				}
				switch root.Get("delta.type").String() {
				case "arguments_delta":
					agg.args.WriteString(root.Get("delta.arguments").String())
				case "thought_signature":
					agg.sig = root.Get("delta.signature").String()
				}
			}
		}
		if byIndex[0] == nil || byIndex[0].sig != "sig_call_A" {
			t.Fatalf("call_A step signature = %+v, want sig_call_A", byIndex[0])
		}
		if byIndex[1] == nil || byIndex[1].sig != "" {
			t.Fatalf("call_B step signature = %+v, want empty", byIndex[1])
		}

		// Round-trip reconstructed steps back through ConvertInteractionsRequestToAntigravity.
		turn2Doc := ojson.NewDocString(`{"model":"gemini-3.8-flash-high","input":[{"type":"user_input","content":[{"type":"text","text":"go"}]}]}`)
		for _, stepIdx := range order {
			agg := byIndex[stepIdx]
			stepDoc := ojson.NewDocString(`{"type":"function_call","id":"","call_id":"","name":"","arguments":{}}`)
			_ = stepDoc.Set("type", agg.stepType)
			_ = stepDoc.Set("id", agg.id)
			_ = stepDoc.Set("call_id", agg.callID)
			_ = stepDoc.Set("name", agg.name)
			_ = stepDoc.SetRaw("arguments", agg.args.String())
			if agg.sig != "" {
				_ = stepDoc.Set("signature", agg.sig)
			}
			_ = turn2Doc.SetRawBytes("input.-1", stepDoc.Bytes())
		}
		_ = turn2Doc.SetRaw("input.-1", `{"type":"function_result","name":"call_A","call_id":"call_1","result":{"ok":true}}`)
		_ = turn2Doc.SetRaw("input.-1", `{"type":"function_result","name":"call_B","call_id":"call_2","result":{"ok":true}}`)

		reqOut, errConvert := ConvertInteractionsRequestToAntigravity("gemini-3.8-flash-high", turn2Doc.Bytes(), false)
		if errConvert != nil {
			t.Fatalf("ConvertInteractionsRequestToAntigravity error: %v", errConvert)
		}
		modelParts := ojson.GetBytesNoCopy(reqOut, "request.contents.1.parts").Array()
		if len(modelParts) != 2 {
			t.Fatalf("expected 2 model parts after round-trip, got %d: %s", len(modelParts), string(reqOut))
		}
		if modelParts[0].Get("functionCall.name").String() != "call_A" || modelParts[0].Get("thoughtSignature").String() != "sig_call_A" {
			t.Fatalf("expected call_A to carry sig_call_A, got parts[0]=%s", modelParts[0].Raw)
		}
		if modelParts[1].Get("functionCall.name").String() != "call_B" || modelParts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("expected call_B to have no thoughtSignature, got parts[1]=%s", modelParts[1].Raw)
		}
	})

	t.Run("reused_input_buffer_does_not_corrupt_partial_args", func(t *testing.T) {
		var param any
		frame1 := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"write_file","partialArgs":[{"jsonPath":"$.meta.count","numberValue":42},{"jsonPath":"$.path","stringValue":"main.go"}],"willContinue":true}}]}}]}}`
		frame2 := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.content","stringValue":"package main"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`

		buf := make([]byte, max(len(frame1), len(frame2)))
		copy(buf, frame1)
		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, buf[:len(frame1)], &param)
		// Mutate the caller-owned buffer in place before sending the next chunk.
		for i := range buf {
			buf[i] = 'X'
		}
		copy(buf, frame2)
		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, buf[:len(frame2)], &param)
		allEvents := append(events1, events2...)

		var argsBuilder strings.Builder
		for _, delta := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(delta, "delta.type").String() == "arguments_delta" {
				argsBuilder.WriteString(ojson.GetBytesNoCopy(delta, "delta.arguments").String())
			}
		}
		gotArgs := argsBuilder.String()
		if !ojson.Valid(gotArgs) {
			t.Fatalf("assembled arguments is not valid JSON: %q", gotArgs)
		}
		if ojson.Get(gotArgs, "meta.count").Int() != 42 || ojson.Get(gotArgs, "path").String() != "main.go" || ojson.Get(gotArgs, "content").String() != "package main" {
			t.Fatalf("assembled arguments corrupted by buffer reuse: %s", gotArgs)
		}
	})

	t.Run("repeated_continuation_thought_signatures_stay_bounded", func(t *testing.T) {
		var param any
		openChunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"write_file","partialArgs":[{"jsonPath":"$.path","stringValue":"main.go"}],"willContinue":true}}]}}]}}`)
		allEvents := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, openChunk, &param)

		for i := 0; i < 200; i++ {
			chunk := []byte(fmt.Sprintf(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","willContinue":true},"thoughtSignature":"sig_%03d"}]}}]}}`, i))
			allEvents = append(allEvents, ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk, &param)...)
		}
		oversizedSig := strings.Repeat("Z", maxAntigravityToolThoughtSignatureBytes+1)
		oversizedChunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","willContinue":true},"thoughtSignature":"` + oversizedSig + `"}]}}]}}`)
		allEvents = append(allEvents, ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, oversizedChunk, &param)...)

		closeChunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","partialArgs":[{"jsonPath":"$.done","boolValue":true}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)
		allEvents = append(allEvents, ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, closeChunk, &param)...)

		var sigDeltas []string
		for _, delta := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(delta, "delta.type").String() == "thought_signature" {
				if idx := ojson.GetBytesNoCopy(delta, "index").Int(); idx != 0 {
					t.Fatalf("thought_signature delta index = %d, want 0", idx)
				}
				sigDeltas = append(sigDeltas, ojson.GetBytesNoCopy(delta, "delta.signature").String())
			}
		}
		if len(sigDeltas) != 1 || sigDeltas[0] != "sig_199" {
			t.Fatalf("retained thought_signature deltas = %v, want [sig_199]", sigDeltas)
		}
	})

	t.Run("incomplete_stream_flushed_on_finish_and_done", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"write_file","partialArgs":[{"jsonPath":"$.path","stringValue":"main.go"}],"willContinue":true}}]}}]}}`)
		chunk2 := []byte(`data: {"response":{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)

		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk1, &param)
		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk2, &param)
		allEvents := append(events1, events2...)

		if got := countAntigravityInteractionsEvents(allEvents, "step.stop"); got != 1 {
			t.Fatalf("step.stop count = %d, want 1. Events: %q", got, allEvents)
		}
		var argsBuilder strings.Builder
		for _, delta := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(delta, "delta.type").String() == "arguments_delta" {
				argsBuilder.WriteString(ojson.GetBytesNoCopy(delta, "delta.arguments").String())
			}
		}
		gotArgs := argsBuilder.String()
		if !ojson.Valid(gotArgs) || ojson.Get(gotArgs, "path").String() != "main.go" {
			t.Fatalf("flushed arguments = %q, want valid JSON with path=main.go", gotArgs)
		}
		completed := findAntigravityInteractionsEventPayload(allEvents, "interaction.completed")
		if got := ojson.GetBytesNoCopy(completed, "interaction.status").String(); got != "incomplete" {
			t.Fatalf("interaction.status = %q, want incomplete. Payload: %s", got, string(completed))
		}
	})

	t.Run("opening_without_id_continuation_with_id_continues", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"write_file","partialArgs":[{"jsonPath":"$.path","stringValue":"main.go"}],"willContinue":true}}]}}]}}`)
		chunk2 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_late","partialArgs":[{"jsonPath":"$.content","stringValue":"ok"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)

		events1 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk1, &param)
		events2 := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk2, &param)
		allEvents := append(events1, events2...)

		if got := countAntigravityInteractionsEvents(allEvents, "step.start"); got != 1 {
			t.Fatalf("step.start count = %d, want 1. Events: %q", got, allEvents)
		}
		var argsBuilder strings.Builder
		for _, delta := range findAntigravityInteractionsEventPayloads(allEvents, "step.delta") {
			if ojson.GetBytesNoCopy(delta, "delta.type").String() == "arguments_delta" {
				argsBuilder.WriteString(ojson.GetBytesNoCopy(delta, "delta.arguments").String())
			}
		}
		gotArgs := argsBuilder.String()
		if !ojson.Valid(gotArgs) || ojson.Get(gotArgs, "path").String() != "main.go" || ojson.Get(gotArgs, "content").String() != "ok" {
			t.Fatalf("assembled arguments = %q, want valid JSON with path=main.go and content=ok", gotArgs)
		}
	})

	t.Run("orphan_nameless_partial_args_chunk_ignored", func(t *testing.T) {
		var param any
		chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.x","stringValue":"1"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
		events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk, &param)
		if got := countAntigravityInteractionsEvents(events, "step.start"); got != 0 {
			t.Fatalf("step.start count = %d, want 0 for orphan nameless partialArgs chunk. Events: %q", got, events)
		}
	})
}

func TestConvertAntigravityResponseToInteractions_FinishReasonStatusMapping(t *testing.T) {
	tests := []struct {
		finishReason string
		wantStatus   string
	}{
		{finishReason: "STOP", wantStatus: "completed"},
		{finishReason: "", wantStatus: "completed"},
		{finishReason: "MAX_TOKENS", wantStatus: "incomplete"},
		{finishReason: "SAFETY", wantStatus: "failed"},
		{finishReason: "RECITATION", wantStatus: "failed"},
		{finishReason: "BLOCKLIST", wantStatus: "failed"},
		{finishReason: "PROHIBITED_CONTENT", wantStatus: "failed"},
		{finishReason: "SPII", wantStatus: "failed"},
		{finishReason: "IMAGE_SAFETY", wantStatus: "failed"},
		{finishReason: "MALFORMED_FUNCTION_CALL", wantStatus: "failed"},
		{finishReason: "MALFORMED_RESPONSE", wantStatus: "failed"},
		{finishReason: "UNEXPECTED_TOOL_CALL", wantStatus: "failed"},
	}

	for _, tt := range tests {
		t.Run("non_stream/"+tt.finishReason, func(t *testing.T) {
			raw := []byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"` + tt.finishReason + `"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
			out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", nil, nil, raw, nil)
			if got := ojson.GetBytesNoCopy(out, "status").String(); got != tt.wantStatus {
				t.Fatalf("non-stream status for finishReason %q = %q, want %q. Output: %s", tt.finishReason, got, tt.wantStatus, string(out))
			}
		})

		t.Run("stream/"+tt.finishReason, func(t *testing.T) {
			var param any
			chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"` + tt.finishReason + `"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
			events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, chunk, &param)
			if tt.finishReason == "" {
				events = append(events, ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte("data: [DONE]"), &param)...)
			}
			completed := findAntigravityInteractionsEventPayload(events, "interaction.completed")
			if len(completed) == 0 {
				t.Fatalf("interaction.completed not found for finishReason %q. Events: %q", tt.finishReason, events)
			}
			if got := ojson.GetBytesNoCopy(completed, "interaction.status").String(); got != tt.wantStatus {
				t.Fatalf("stream interaction.status for finishReason %q = %q, want %q. Payload: %s", tt.finishReason, got, tt.wantStatus, string(completed))
			}
		})
	}

	t.Run("stream/split_finish_and_usage_frames_preserves_status", func(t *testing.T) {
		var param any
		finishFrame := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"cut"}]},"finishReason":"MAX_TOKENS"}]}}`)
		usageFrame := []byte(`data: {"response":{"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}}`)
		events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, finishFrame, &param)
		events = append(events, ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, usageFrame, &param)...)
		completed := findAntigravityInteractionsEventPayload(events, "interaction.completed")
		if len(completed) == 0 {
			t.Fatalf("interaction.completed not found. Events: %q", events)
		}
		if got := ojson.GetBytesNoCopy(completed, "interaction.status").String(); got != "incomplete" {
			t.Fatalf("split-frame interaction.status = %q, want incomplete. Payload: %s", got, string(completed))
		}
	})
}
