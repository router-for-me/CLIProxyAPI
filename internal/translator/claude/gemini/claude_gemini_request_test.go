package gemini

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertGeminiRequestToClaude_ThinkingSummaryVisibility(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wanted string
	}{
		{name: "include thoughts", input: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, wanted: "summarized"},
		{name: "exclude thoughts", input: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":false}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, wanted: "omitted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, _ := ConvertGeminiRequestToClaude("claude-opus-5-5", []byte(test.input), false)
			if got := ojson.GetBytesNoCopy(out, "thinking.display").String(); got != test.wanted {
				t.Fatalf("thinking.display = %q, want %q; body=%s", got, test.wanted, out)
			}
		})
	}
}

func TestConvertGeminiRequestToClaude_PreservesCustomToolIDs(t *testing.T) {
	tests := []struct {
		name          string
		callField     string
		responseField string
		want          string
	}{
		{
			name:          "id",
			callField:     `"id":"call_gateway_id"`,
			responseField: `"id":"call_gateway_id"`,
			want:          "call_gateway_id",
		},
		{
			name:          "call_id",
			callField:     `"call_id":"call_gateway_call_id"`,
			responseField: `"call_id":"call_gateway_call_id"`,
			want:          "call_gateway_call_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"contents": [
					{
						"role": "model",
						"parts": [
							{"functionCall": {"name": "lookup", %s, "args": {"query": "status"}}}
						]
					},
					{
						"role": "user",
						"parts": [
							{"functionResponse": {"name": "lookup", %s, "response": {"result": "ok"}}}
						]
					}
				]
			}`, tt.callField, tt.responseField))

			out, _ := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)

			gotCallID := ojson.GetBytesNoCopy(out, "messages.0.content.0.id").String()
			if gotCallID != tt.want {
				t.Fatalf("expected tool_use id %q, got %q; output=%s", tt.want, gotCallID, string(out))
			}

			gotResultID := ojson.GetBytesNoCopy(out, "messages.1.content.0.tool_use_id").String()
			if gotResultID != tt.want {
				t.Fatalf("expected tool_result tool_use_id %q, got %q; output=%s", tt.want, gotResultID, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToClaude_GroupsConsecutiveRoleTurns(t *testing.T) {
	raw := []byte(`{
		"contents":[
			{"role":"model","parts":[{"text":"answer"}]},
			{"role":"model","parts":[{"functionCall":{"name":"first","id":"call_1","args":{}}}]},
			{"role":"model","parts":[{"functionCall":{"name":"second","id":"call_2","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"first","id":"call_1","response":{"result":"one"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"second","id":"call_2","response":{"result":"two"}}}]}
		]
	}`)

	out, _ := ConvertGeminiRequestToClaude("claude-test", raw, false)
	messages := ojson.GetBytesNoCopy(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", len(messages), string(out))
	}
	assistantContent := messages[0].Get("content").Array()
	wantAssistantTypes := []string{"text", "tool_use", "tool_use"}
	if len(assistantContent) != len(wantAssistantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(assistantContent), len(wantAssistantTypes), string(out))
	}
	for i, wantType := range wantAssistantTypes {
		if got := assistantContent[i].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q", i, got, wantType)
		}
	}
	userContent := messages[1].Get("content").Array()
	if len(userContent) != 2 {
		t.Fatalf("user content count = %d, want 2. Output: %s", len(userContent), string(out))
	}
	for i, wantID := range []string{"call_1", "call_2"} {
		if got := userContent[i].Get("type").String(); got != "tool_result" {
			t.Fatalf("user content[%d].type = %q, want tool_result", i, got)
		}
		if got := userContent[i].Get("tool_use_id").String(); got != wantID {
			t.Fatalf("user content[%d].tool_use_id = %q, want %q", i, got, wantID)
		}
	}
}

func TestConvertGeminiRequestToClaude_KeepsSystemInstructionUserSeparate(t *testing.T) {
	raw := []byte(`{
		"system_instruction":{"parts":[{"text":"system rule"}]},
		"contents":[{"role":"user","parts":[{"text":"question"}]}]
	}`)
	out, _ := ConvertGeminiRequestToClaude("claude-test", raw, false)
	messages := ojson.GetBytesNoCopy(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", len(messages), string(out))
	}
	if got := messages[0].Get("content.0.text").String(); got != "system rule" {
		t.Fatalf("system user text = %q, want system rule", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "question" {
		t.Fatalf("ordinary user text = %q, want question", got)
	}
}

func TestConvertGeminiRequestToClaude_SupportsCamelCaseSystemInstruction(t *testing.T) {
	raw := []byte(`{
		"systemInstruction":{"parts":[{"text":"system rule in camelCase"}]},
		"contents":[{"role":"user","parts":[{"text":"question"}]}]
	}`)
	out, _ := ConvertGeminiRequestToClaude("claude-test", raw, false)
	messages := ojson.GetBytesNoCopy(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", len(messages), string(out))
	}
	if got := messages[0].Get("content.0.text").String(); got != "system rule in camelCase" {
		t.Fatalf("system user text = %q, want system rule in camelCase", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "question" {
		t.Fatalf("ordinary user text = %q, want question", got)
	}
}

func TestConvertGeminiRequestToClaude_DropsTemperature(t *testing.T) {
	raw := []byte(`{
		"generationConfig": {
			"temperature": 0.2,
			"topP": 0.8
		},
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "hi"}]
			}
		]
	}`)

	out, _ := ConvertGeminiRequestToClaude("claude-sonnet-5", raw, false)

	if ojson.GetBytesNoCopy(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
	if got := ojson.GetBytesNoCopy(out, "top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got)
	}
}

func TestConvertGeminiRequestToClaude_AcceptsCamelInlineData(t *testing.T) {
	out, _ := ConvertGeminiRequestToClaude("claude-sonnet-4", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.0.type").String(); got != "image" {
		t.Fatalf("content type = %q, want image. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.0.source.media_type").String(); got != "image/png" {
		t.Fatalf("media_type = %q, want image/png. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToClaude_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out, _ := ConvertGeminiRequestToClaude("claude-sonnet-4", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	// A user attachment Claude cannot read is dropped rather than replaced by placeholder text.
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.#").Int(); got != 1 {
		t.Fatalf("user content has %d blocks, want only the document. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.0.type").String(); got != "document" {
		t.Fatalf("document content type = %q, want document. Output: %s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "messages.0.content.#(type==\"image\")").Exists() {
		t.Fatalf("non-image inlineData must not be converted to image. Output: %s", string(out))
	}
	if strings.Contains(string(out), "Media content") {
		t.Fatalf("user attachment was replaced by placeholder text. Output: %s", string(out))
	}
}

func TestConvertGeminiRequestToClaude_DropsHiddenThoughtParts(t *testing.T) {
	t.Run("thought-only turn", func(t *testing.T) {
		out, _ := ConvertGeminiRequestToClaude("claude-test", []byte(`{
			"contents":[
				{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"}]},
				{"role":"user","parts":[{"text":"continue"}]}
			]
		}`), false)

		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 1 || messages[0].Get("role").String() != "user" || messages[0].Get("content.0.text").String() != "continue" {
			t.Fatalf("hidden thought turn was not dropped. Output: %s", string(out))
		}
	})

	t.Run("mixed turn", func(t *testing.T) {
		out, _ := ConvertGeminiRequestToClaude("claude-test", []byte(`{
			"contents":[{"role":"model","parts":[
				{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"},
				{"text":"visible answer"}
			]}]
		}`), false)

		content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
		if len(content) != 1 || content[0].Get("type").String() != "text" || content[0].Get("text").String() != "visible answer" {
			t.Fatalf("hidden thought was not dropped independently of visible text. Output: %s", string(out))
		}
	})
}

func TestConvertGeminiRequestToClaude_DeterministicToolIDs(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "first_tool", "args": {"q": "one"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "first_tool", "response": {"result": "ok1"}}}
				]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "second_tool", "args": {"q": "two"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "second_tool", "response": {"result": "ok2"}}}
				]
			}
		]
	}`)

	out1, _ := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)
	out2, _ := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)

	if string(out1) != string(out2) {
		t.Fatalf("expected deterministic output across multiple conversions, got different outputs:\nout1=%s\nout2=%s", string(out1), string(out2))
	}

	wantID1 := "toolu_gemini_0000000000000001"
	wantID2 := "toolu_gemini_0000000000000002"

	gotCall1 := ojson.GetBytesNoCopy(out1, "messages.0.content.0.id").String()
	gotResp1 := ojson.GetBytesNoCopy(out1, "messages.1.content.0.tool_use_id").String()
	gotCall2 := ojson.GetBytesNoCopy(out1, "messages.2.content.0.id").String()
	gotResp2 := ojson.GetBytesNoCopy(out1, "messages.3.content.0.tool_use_id").String()

	if gotCall1 != wantID1 || gotResp1 != wantID1 {
		t.Fatalf("expected first tool pair to have id %q, got call=%q, resp=%q", wantID1, gotCall1, gotResp1)
	}
	if gotCall2 != wantID2 || gotResp2 != wantID2 {
		t.Fatalf("expected second tool pair to have id %q, got call=%q, resp=%q", wantID2, gotCall2, gotResp2)
	}
}

func TestConvertGeminiRequestToClaude_PreservesCallerSuppliedMetadataUserID(t *testing.T) {
	testCases := []struct {
		name     string
		rawJSON  string
		expected string
	}{
		{
			name:     "plain string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"custom-gemini-user-123"},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			expected: "custom-gemini-user-123",
		},
		{
			name:     "special characters and json string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"foo\"bar\nbaz\\qux"},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			expected: "foo\"bar\nbaz\\qux",
		},
		{
			name:     "claude code json format",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			expected: `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","session_id":"11111111-2222-4333-8444-555555555555"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := ConvertGeminiRequestToClaude("claude-test", []byte(tc.rawJSON), false)
			if !ojson.ValidBytes(out) {
				t.Fatalf("output is invalid json: %s", string(out))
			}
			got := ojson.GetBytesNoCopy(out, "metadata.user_id").String()
			if got != tc.expected {
				t.Fatalf("metadata.user_id = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestConvertGeminiRequestToClaude_DifferentSessionsProduceDifferentUserIDs(t *testing.T) {
	a := []byte(`{"model":"claude-test","prompt_cache_key":"gemini-session-a","contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	b := []byte(`{"model":"claude-test","prompt_cache_key":"gemini-session-b","contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	outA, _ := ConvertGeminiRequestToClaude("claude-test", a, false)
	outB, _ := ConvertGeminiRequestToClaude("claude-test", b, false)
	idA := ojson.GetBytesNoCopy(outA, "metadata.user_id").String()
	idB := ojson.GetBytesNoCopy(outB, "metadata.user_id").String()
	if idA == idB {
		t.Fatalf("different prompt_cache_key produced identical metadata.user_id: %q", idA)
	}
}

func TestConvertGeminiRequestToClaude_DefaultRoleDifferentContentProducesDifferentUserIDs(t *testing.T) {
	a := []byte(`{"contents":[{"parts":[{"text":"first prompt"}]}]}`)
	b := []byte(`{"contents":[{"parts":[{"text":"second prompt"}]}]}`)
	outA, _ := ConvertGeminiRequestToClaude("claude-test", a, false)
	outB, _ := ConvertGeminiRequestToClaude("claude-test", b, false)
	idA := ojson.GetBytesNoCopy(outA, "metadata.user_id").String()
	idB := ojson.GetBytesNoCopy(outB, "metadata.user_id").String()
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id without role, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different prompt texts without role produced identical metadata.user_id: %q", idA)
	}
	if got := ojson.GetBytesNoCopy(outA, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want user; out=%s", got, string(outA))
	}
}

func TestConvertGeminiRequestToClaude_SanitizesToolNamesAndProvidesFallbackSchema(t *testing.T) {
	inputJSON := `{
		"contents": [
			{
				"role": "model",
				"parts": [
					{
						"functionCall": {
							"name": "mcp.server:get_data",
							"args": {}
						}
					}
				]
			},
			{
				"role": "user",
				"parts": [
					{
						"functionResponse": {
							"name": "mcp.server:get_data",
							"response": {"result": "ok"}
						}
					}
				]
			}
		],
		"tools": [
			{
				"functionDeclarations": [
					{
						"name": "mcp.server:get_data",
						"description": "parameterless mcp tool"
					}
				]
			}
		],
		"toolConfig": {
			"functionCallingConfig": {
				"mode": "ANY",
				"allowedFunctionNames": ["mcp.server:get_data"]
			}
		}
	}`

	result, _ := ConvertGeminiRequestToClaude("claude-test", []byte(inputJSON), false)

	// 1. Tool declaration name sanitized
	toolName := ojson.GetBytesNoCopy(result, "tools.0.name").String()
	if toolName != "mcp_server_get_data" {
		t.Fatalf("tools.0.name = %q, want mcp_server_get_data. Output: %s", toolName, result)
	}

	// 2. Fallback input_schema
	schema := ojson.GetBytesNoCopy(result, "tools.0.input_schema")
	if !schema.Exists() || schema.Get("type").String() != "object" {
		t.Fatalf("tools.0.input_schema = %s, want object schema. Output: %s", schema, result)
	}

	// 3. Historical tool_use in model/assistant turn sanitized
	toolUseName := ojson.GetBytesNoCopy(result, "messages.0.content.0.name").String()
	if toolUseName != "mcp_server_get_data" {
		t.Fatalf("messages.0.content.0.name = %q, want mcp_server_get_data. Output: %s", toolUseName, result)
	}

	// 4. Tool choice name sanitized
	toolChoiceName := ojson.GetBytesNoCopy(result, "tool_choice.name").String()
	if toolChoiceName != "mcp_server_get_data" {
		t.Fatalf("tool_choice.name = %q, want mcp_server_get_data. Output: %s", toolChoiceName, result)
	}
}

func TestConvertGeminiRequestToClaude_FunctionResponsePartsMedia(t *testing.T) {
	t.Run("inlineData image and PDF alongside response result", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "user", "parts": [{"text": "Read red.png and doc.pdf"}]},
				{"role": "model", "parts": [{"functionCall": {"name": "read_files", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_files",
						"response": {"result": "Loaded files"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}},
							{"inlineData": {"mimeType": "application/pdf", "data": "JVBERi0xLjQK"}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToClaude("claude-sonnet-4-6", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToClaude() error = %v", err)
		}
		toolResult := ojson.GetBytesNoCopy(out, "messages.2.content.0")
		if got := toolResult.Get("type").String(); got != "tool_result" {
			t.Fatalf("messages.2.content.0.type = %q, want tool_result; out=%s", got, out)
		}
		content := toolResult.Get("content")
		if !content.IsArray() {
			t.Fatalf("expected tool_result.content to be array, got %s; out=%s", content.Raw, out)
		}
		blocks := content.Array()
		if len(blocks) != 3 {
			t.Fatalf("expected 3 blocks (text + image + document), got %d: %s", len(blocks), content.Raw)
		}
		if blocks[0].Get("type").String() != "text" || blocks[0].Get("text").String() != "Loaded files" {
			t.Fatalf("blocks[0] = %s, want text 'Loaded files'", blocks[0].Raw)
		}
		if blocks[1].Get("type").String() != "image" || blocks[1].Get("source.type").String() != "base64" || blocks[1].Get("source.media_type").String() != "image/png" || blocks[1].Get("source.data").String() != "iVBORw0KGgo=" {
			t.Fatalf("blocks[1] = %s, want base64 image/png block", blocks[1].Raw)
		}
		if blocks[2].Get("type").String() != "document" || blocks[2].Get("source.type").String() != "base64" || blocks[2].Get("source.media_type").String() != "application/pdf" || blocks[2].Get("source.data").String() != "JVBERi0xLjQK" {
			t.Fatalf("blocks[2] = %s, want base64 application/pdf document block", blocks[2].Raw)
		}
	})

	t.Run("fileData image URL with empty response", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "fetch_image", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "fetch_image",
						"response": {},
						"parts": [
							{"fileData": {"mimeType": "image/jpeg", "fileUri": "https://example.com/a.jpg"}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToClaude("claude-sonnet-4-6", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToClaude() error = %v", err)
		}
		blocks := ojson.GetBytesNoCopy(out, "messages.1.content.0.content").Array()
		if len(blocks) != 1 {
			t.Fatalf("expected 1 image block (no empty text block), got %d; out=%s", len(blocks), out)
		}
		if blocks[0].Get("type").String() != "image" || blocks[0].Get("source.type").String() != "url" || blocks[0].Get("source.url").String() != "https://example.com/a.jpg" {
			t.Fatalf("blocks[0] = %s, want url image block", blocks[0].Raw)
		}
	})

	t.Run("unsupported media-only functionResponse beside sendable text still refuses request", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "read_audio", "id": "call_1", "args": {}}}]},
				{"role": "user", "parts": [
					{"functionResponse": {
						"name": "read_audio",
						"response": {},
						"parts": [
							{"inline_data": {"mime_type": "audio/wav", "data": "UklGRg=="}}
						]
					}},
					{"text": "continue"}
				]}
			]
		}`)

		out, err := ConvertGeminiRequestToClaude("claude-sonnet-4-6", inputJSON, false)
		if err == nil {
			t.Fatalf("expected error for unsupported audio-only functionResponse, got nil; out=%s", out)
		}
		if err.Error() != "unsupported content part: inlineData" {
			t.Fatalf("err = %q, want %q", err.Error(), "unsupported content part: inlineData")
		}
	})

	t.Run("snake_case function_response with mixed text and extra response fields", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [{"function_call": {"name": "read_files", "id": "call_2", "args": {}}}]},
				{"role": "user", "parts": [{
					"function_response": {
						"name": "read_files",
						"id": "call_2",
						"response": {"result": "partial", "error": "second missing"},
						"parts": [
							{"text": "caption"},
							{"inline_data": {"mime_type": "image/png", "data": "iVBORw0KGgo="}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToClaude("claude-sonnet-4-6", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToClaude() error = %v", err)
		}
		blocks := ojson.GetBytesNoCopy(out, "messages.1.content.0.content").Array()
		if len(blocks) != 3 {
			t.Fatalf("expected 3 blocks, got %d; out=%s", len(blocks), out)
		}
		if blocks[0].Get("type").String() != "text" || !strings.Contains(blocks[0].Get("text").String(), "second missing") {
			t.Fatalf("blocks[0] = %s, want full response JSON preserving error field", blocks[0].Raw)
		}
		if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "caption" {
			t.Fatalf("blocks[1] = %s, want nested text 'caption'", blocks[1].Raw)
		}
		if blocks[2].Get("type").String() != "image" || blocks[2].Get("source.media_type").String() != "image/png" {
			t.Fatalf("blocks[2] = %s, want image/png block", blocks[2].Raw)
		}
	})

	t.Run("whitespace-formatted empty response and whitespace text still refuse unsupported audio", func(t *testing.T) {
		for _, resp := range []string{`{ }`, `{"result": "   "}`} {
			inputJSON := []byte(`{
				"contents": [
					{"role": "model", "parts": [{"functionCall": {"name": "read_audio", "id": "call_1", "args": {}}}]},
					{"role": "user", "parts": [
						{"functionResponse": {
							"name": "read_audio",
							"response": ` + resp + `,
							"parts": [
								{"text": "   "},
								{"inlineData": {"mimeType": "audio/wav", "data": "UklGRg=="}}
							]
						}},
						{"text": "continue"}
					]}
				]
			}`)

			out, err := ConvertGeminiRequestToClaude("claude-sonnet-4-6", inputJSON, false)
			if err == nil || err.Error() != "unsupported content part: inlineData" {
				t.Fatalf("resp=%s: err = %v, want unsupported content part: inlineData; out=%s", resp, err, out)
			}
		}
	})

	t.Run("fallback preserves sibling response fields when no nested blocks exist", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "read_file", "id": "call_3", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_file",
						"response": {"result": "", "error": "failed"}
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToClaude("claude-sonnet-4-6", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToClaude() error = %v", err)
		}
		got := ojson.GetBytesNoCopy(out, "messages.1.content.0.content").String()
		if !strings.Contains(got, `"error"`) || !strings.Contains(got, `"failed"`) {
			t.Fatalf("tool_result.content = %q, want full response JSON preserving error field", got)
		}
	})
}
