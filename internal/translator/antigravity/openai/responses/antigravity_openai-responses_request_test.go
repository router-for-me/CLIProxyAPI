package responses

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConvertOpenAIResponsesRequestToAntigravity_ClaudeReasoningKeepsClaudeSignature(t *testing.T) {
	nativeSig := testAntigravityResponsesClaudeSignature(t)
	antigravitySig, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(nativeSig)
	if !ok {
		t.Fatal("test Claude signature should be compatible with Antigravity Claude")
	}

	tests := []struct {
		name      string
		encrypted string
	}{
		{
			name:      "Claude native E signature",
			encrypted: nativeSig,
		},
		{
			name:      "Antigravity double-layer R signature",
			encrypted: antigravitySig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{
				"model": "claude-opus-4-6-thinking",
				"input": [
					{
						"id": "rs_prev",
						"type": "reasoning",
						"encrypted_content": "` + tt.encrypted + `",
						"summary": [{"type": "summary_text", "text": "internal reasoning"}]
					},
					{
						"role": "assistant",
						"content": [{"type": "output_text", "text": "visible answer"}]
					},
					{
						"role": "user",
						"content": [{"type": "input_text", "text": "continue"}]
					}
				]
			}`)

			out := ConvertOpenAIResponsesRequestToAntigravity("claude-opus-4-6-thinking", raw, false)
			part := gjson.GetBytes(out, "request.contents.0.parts.0")
			if !part.Get("thought").Bool() {
				t.Fatalf("first part should remain a thought block. Output: %s", out)
			}
			if got := part.Get("thoughtSignature").String(); got != antigravitySig {
				t.Fatalf("thoughtSignature prefix/len = %q/%d, want %q/%d. Output: %s",
					firstByte(got), len(got), firstByte(antigravitySig), len(antigravitySig), out)
			}
			if got := part.Get("text").String(); got != "internal reasoning" {
				t.Fatalf("thought text = %q, want internal reasoning. Output: %s", got, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_ClaudeReasoningDropsIncompatibleSignature(t *testing.T) {
	raw := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"input": [
			{
				"id": "rs_prev",
				"type": "reasoning",
				"encrypted_content": "` + testAntigravityResponsesGPTSignature() + `",
				"summary": [{"type": "summary_text", "text": "must not reach Claude"}]
			},
			{
				"role": "assistant",
				"content": [{"type": "output_text", "text": "visible answer"}]
			},
			{
				"role": "user",
				"content": [{"type": "input_text", "text": "continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity("claude-opus-4-6-thinking", raw, false)
	if strings.Contains(string(out), sigcompat.GeminiSkipThoughtSignatureValidator) {
		t.Fatalf("Claude target must not receive Gemini bypass signature. Output: %s", out)
	}
	if gjson.GetBytes(out, `request.contents.#.parts.#(thought=true)#`).Int() != 0 {
		t.Fatalf("incompatible reasoning block should be dropped. Output: %s", out)
	}
	if strings.Contains(string(out), "must not reach Claude") {
		t.Fatalf("incompatible reasoning text should be dropped. Output: %s", out)
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "visible answer" {
		t.Fatalf("visible assistant text = %q, want visible answer. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_ClaudeReasoningDropsEmptyThinkingText(t *testing.T) {
	rawSignature := testAntigravityResponsesClaudeSignature(t)
	raw := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"input": [
			{
				"id": "rs_prev",
				"type": "reasoning",
				"encrypted_content": "` + rawSignature + `",
				"summary": []
			},
			{
				"role": "assistant",
				"content": [{"type": "output_text", "text": "visible answer"}]
			},
			{
				"role": "user",
				"content": [{"type": "input_text", "text": "continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity("claude-opus-4-6-thinking", raw, false)
	if gjson.GetBytes(out, `request.contents.#.parts.#(thought=true)#`).Int() != 0 {
		t.Fatalf("empty-text reasoning block should be dropped for Antigravity Claude. Output: %s", out)
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "visible answer" {
		t.Fatalf("visible assistant text = %q, want visible answer. Output: %s", got, out)
	}
}

func testAntigravityResponsesClaudeSignature(t *testing.T) string {
	t.Helper()
	return testAntigravityResponsesClaudeSignatureForModel(t, "claude-sonnet-4-6")
}

func testAntigravityResponsesClaudeSignatureForModel(t *testing.T, model string) string {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, model)

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

func testAntigravityResponsesGPTSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	payload[8] = 1
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(payload)
}

func firstByte(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}

func TestConvertOpenAIResponsesRequestToAntigravity_EmptyClaudeReasoningDoesNotShiftLaterSignature(t *testing.T) {
	rawSig1 := testAntigravityResponsesClaudeSignatureForModel(t, "claude-sonnet-4-6")
	rawSig2 := testAntigravityResponsesClaudeSignatureForModel(t, "claude-opus-4-6")
	expectedSig2, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(rawSig2)
	if !ok {
		t.Fatal("second Claude signature should be compatible")
	}
	raw := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"input":[
			{"type":"reasoning","encrypted_content":"` + rawSig1 + `","summary":[]},
			{"role":"user","content":[{"type":"input_text","text":"boundary"}]},
			{"type":"reasoning","encrypted_content":"` + rawSig2 + `","summary":[{"type":"summary_text","text":"second reasoning"}]},
			{"role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToAntigravity("claude-opus-4-6-thinking", raw, false)
	var thoughts []gjson.Result
	for _, content := range gjson.GetBytes(out, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("thought").Bool() {
				thoughts = append(thoughts, part)
			}
		}
	}
	if len(thoughts) != 1 {
		t.Fatalf("thought count = %d, want only the non-empty reasoning item. Output: %s", len(thoughts), out)
	}
	if got := thoughts[0].Get("text").String(); got != "second reasoning" {
		t.Fatalf("thought text = %q, want second reasoning. Output: %s", got, out)
	}
	if got := thoughts[0].Get("thoughtSignature").String(); got != expectedSig2 {
		t.Fatalf("later thought received the wrong signature prefix/len = %q/%d, want %q/%d. Output: %s", firstByte(got), len(got), firstByte(expectedSig2), len(expectedSig2), out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_EmptyClaudeReasoningBeforeFunctionDoesNotShiftLaterSignature(t *testing.T) {
	rawSig1 := testAntigravityResponsesClaudeSignatureForModel(t, "claude-sonnet-4-6")
	rawSig2 := testAntigravityResponsesClaudeSignatureForModel(t, "claude-opus-4-6")
	expectedSig2, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(rawSig2)
	if !ok {
		t.Fatal("second Claude signature should be compatible")
	}
	raw := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"input":[
			{"type":"reasoning","encrypted_content":"` + rawSig1 + `","summary":[]},
			{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}"},
			{"type":"function_call_output","call_id":"call-1","output":"ok"},
			{"type":"reasoning","encrypted_content":"` + rawSig2 + `","summary":[{"type":"summary_text","text":"second reasoning"}]},
			{"role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToAntigravity("claude-opus-4-6-thinking", raw, false)
	var thoughts []gjson.Result
	for _, content := range gjson.GetBytes(out, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("thought").Bool() {
				thoughts = append(thoughts, part)
			}
		}
	}
	if len(thoughts) != 1 || thoughts[0].Get("text").String() != "second reasoning" {
		t.Fatalf("later reasoning placement malformed. Output: %s", out)
	}
	if got := thoughts[0].Get("thoughtSignature").String(); got != expectedSig2 {
		t.Fatalf("later thought received the wrong signature prefix/len = %q/%d, want %q/%d. Output: %s", firstByte(got), len(got), firstByte(expectedSig2), len(expectedSig2), out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_GeminiReasoningUsesNativeThoughtSignaturePlacement(t *testing.T) {
	sig := "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"
	raw := []byte(`{"model":"gemini-3.5-flash","input":[{"type":"reasoning","encrypted_content":"gemini#` + sig + `","summary":[{"type":"summary_text","text":"reasoning summary"}]}]}`)
	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash-agent", raw, false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("parts length = %d, want 1. Output: %s", len(parts), out)
	}
	if got := parts[0].Get("thought").Bool(); !got {
		t.Fatalf("parts[0] should be thought. Output: %s", out)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != sig {
		t.Fatalf("parts[0].thoughtSignature = %q, want preserved Gemini signature. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_PreservesToolResultImage(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "请帮我读取分析这张图片"}]},
			{"type": "function_call", "id": "fc_read", "call_id": "call_read_1", "name": "read", "arguments": "{\"path\":\"/path/to/image.png\"}"},
			{
				"type": "function_call_output",
				"call_id": "call_read_1",
				"output": [
					{"type": "input_text", "text": "Read image file [image/png]"},
					{"type": "input_image", "detail": "auto", "image_url": "data:image/png;base64,QUJD"}
				]
			}
		]
	}`
	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d. Output: %s", len(contents), out)
	}
	funcContent := contents[2]
	if got := funcContent.Get("role").String(); got != "user" {
		t.Fatalf("role = %q, want user. Output: %s", got, out)
	}
	funcResp := funcContent.Get("parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatalf("functionResponse should exist. Output: %s", out)
	}
	if got := funcResp.Get("id").String(); got != "call_read_1" {
		t.Fatalf("id = %q, want call_read_1", got)
	}
	if got := funcResp.Get("name").String(); got != "read" {
		t.Fatalf("name = %q, want read", got)
	}
	inlineData := funcResp.Get("parts.0.inlineData")
	if !inlineData.Exists() {
		t.Fatalf("expected functionResponse.parts.0.inlineData to exist, got: %s", out)
	}
	if got := inlineData.Get("mimeType").String(); got != "image/png" {
		t.Errorf("expected mimeType image/png, got %q", got)
	}
	if got := inlineData.Get("data").String(); got != "QUJD" {
		t.Errorf("expected data QUJD, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_AttachesParallelToolImagesToNearestResponse(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "read both"}]},
			{"type": "function_call", "id": "fc_a", "call_id": "call_a", "name": "read", "arguments": "{\"path\":\"/tmp/a.png\"}"},
			{"type": "function_call", "id": "fc_b", "call_id": "call_b", "name": "read", "arguments": "{\"path\":\"/tmp/b.png\"}"},
			{
				"type": "function_call_output",
				"call_id": "call_a",
				"output": [
					{"type": "input_text", "text": "file A"},
					{"type": "input_image", "image_url": "data:image/png;base64,AAA"}
				]
			},
			{
				"type": "function_call_output",
				"call_id": "call_b",
				"output": [
					{"type": "input_text", "text": "file B"},
					{"type": "input_image", "image_url": "data:image/jpeg;base64,BBB"}
				]
			}
		]
	}`
	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(out, "request.contents.2.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("function parts = %d, want 2. Output: %s", len(parts), out)
	}
	got := map[string]string{}
	for _, part := range parts {
		fr := part.Get("functionResponse")
		got[fr.Get("id").String()] = fr.Get("parts.0.inlineData.data").String()
	}
	if got["call_a"] != "AAA" {
		t.Fatalf("call_a image = %q, want AAA. Output: %s", got["call_a"], out)
	}
	if got["call_b"] != "BBB" {
		t.Fatalf("call_b image = %q, want BBB. Output: %s", got["call_b"], out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_PreservesAdditionalToolsAndToolConfig(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "namespace",
						"name": "functions",
						"tools": [
							{"type": "custom", "name": "exec", "description": "Execute a command"},
							{"type": "function", "name": "continuity_probe", "description": "Probe", "parameters": {"type": "object", "properties": {"value": {"type": "string"}}, "required": ["value"]}}
						]
					}
				]
			},
			{"role": "user", "content": [{"type": "input_text", "text": "test"}]}
		],
		"tool_choice": {
			"type": "function",
			"name": "continuity_probe",
			"namespace": "functions"
		}
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid JSON output: %s", out)
	}

	decls := gjson.GetBytes(out, "request.tools.0.functionDeclarations").Array()
	if len(decls) != 2 {
		t.Fatalf("expected 2 functionDeclarations in request.tools, got %d; raw: %s", len(decls), out)
	}

	mode := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String()
	if mode != "ANY" {
		t.Fatalf("mode = %q, want ANY", mode)
	}
	allowed := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String()
	if allowed != "functions__continuity_probe" {
		t.Fatalf("allowedFunctionNames.0 = %q, want functions__continuity_probe", allowed)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_MidSessionDeveloperMessageDoesNotMutateSystemInstruction(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"instructions": "Be a helpful assistant",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Turn 1 user"}
				]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "Turn 1 assistant"}
				]
			},
			{
				"type": "message",
				"role": "developer",
				"content": "<image_resize_notice>Image 1 was resized to 800x600</image_resize_notice>"
			},
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Turn 2 user"}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid JSON output: %s", out)
	}

	// In Antigravity envelope, systemInstruction is at request.systemInstruction
	sysParts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
	if len(sysParts) != 1 {
		t.Fatalf("request.systemInstruction parts count = %d, want 1; output=%s", len(sysParts), out)
	}
	if got := sysParts[0].Get("text").String(); got != "Be a helpful assistant" {
		t.Fatalf("systemInstruction part = %q, want %q; output=%s", got, "Be a helpful assistant", out)
	}

	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("request.contents count = %d, want 3; output=%s", len(contents), out)
	}
	if contents[2].Get("role").String() != "user" {
		t.Fatalf("turn 2 role = %q, want user; output=%s", contents[2].Get("role").String(), out)
	}
	turn2Parts := contents[2].Get("parts").Array()
	if len(turn2Parts) != 2 {
		t.Fatalf("turn 2 parts count = %d, want 2; output=%s", len(turn2Parts), out)
	}
	expectedDevText := "<system-reminder>\n<image_resize_notice>Image 1 was resized to 800x600</image_resize_notice>\n</system-reminder>"
	if got := turn2Parts[0].Get("text").String(); got != expectedDevText {
		t.Fatalf("turn 2 part 0 = %q, want %q; output=%s", got, expectedDevText, out)
	}
	if got := turn2Parts[1].Get("text").String(); got != "Turn 2 user" {
		t.Fatalf("turn 2 part 1 = %q, want Turn 2 user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_MidSessionSystemReminderEnvelope(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"instructions": "Be a helpful assistant",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Turn 1 user"}
				]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "Turn 1 assistant"}
				]
			},
			{
				"type": "message",
				"role": "system",
				"content": "Please decide which tool to call next."
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	result := gjson.ParseBytes(out)

	contents := result.Get("request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents count = %d, want 3; output=%s", len(contents), out)
	}
	expectedReminder := "<system-reminder>\nPlease decide which tool to call next.\n</system-reminder>"
	if got := contents[2].Get("parts.0.text").String(); got != expectedReminder {
		t.Fatalf("mid-session system reminder mismatch:\ngot:  %q\nwant: %q", got, expectedReminder)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_InterveningDeveloperMessagePreservesToolPairing(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"instructions": "Be a helpful assistant",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Run tool"}
				]
			},
			{
				"type": "function_call",
				"call_id": "call-1",
				"name": "run_command",
				"arguments": "{\"command\":\"echo test\"}"
			},
			{
				"type": "message",
				"role": "developer",
				"content": "<permissions instructions>\nApproved: echo\n</permissions instructions>"
			},
			{
				"type": "function_call_output",
				"call_id": "call-1",
				"output": "test"
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid JSON output: %s", out)
	}

	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity request: %v; output=%s", errPair, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_ReasoningSummaries(t *testing.T) {
	tests := []struct {
		name       string
		inputJSON  string
		wantExists bool
		wantVal    bool
	}{
		{
			name:       "effort alone enables includeThoughts",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"high"},"input":"hello"}`,
			wantExists: true,
			wantVal:    true,
		},
		{
			name:       "effort none does not enable includeThoughts",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"none"},"input":"hello"}`,
			wantExists: false,
		},
		{
			name:       "effort empty does not enable includeThoughts",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"  "},"input":"hello"}`,
			wantExists: false,
		},
		{
			name:       "missing effort does not enable includeThoughts",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{},"input":"hello"}`,
			wantExists: false,
		},
		{
			name:       "non-string effort does not enable includeThoughts",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":10},"input":"hello"}`,
			wantExists: false,
		},
		{
			name:       "explicit summary preserved without overriding",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"high","summary":"auto"},"input":"hello"}`,
			wantExists: true,
			wantVal:    true,
		},
		{
			name:       "explicit null summary preserved without enabling",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"high","summary":null},"input":"hello"}`,
			wantExists: true,
			wantVal:    false,
		},
		{
			name:       "explicit none summary disables includeThoughts",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"high","summary":"none"},"input":"hello"}`,
			wantExists: true,
			wantVal:    false,
		},
		{
			name:       "explicit generate_summary preserved",
			inputJSON:  `{"model":"gemini-3-flash","reasoning":{"effort":"high","generate_summary":"detailed"},"input":"hello"}`,
			wantExists: true,
			wantVal:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(tc.inputJSON), false)
			res := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts")
			if res.Exists() != tc.wantExists {
				t.Fatalf("includeThoughts exists = %v, want %v; out=%s", res.Exists(), tc.wantExists, out)
			}
			if tc.wantExists && res.Bool() != tc.wantVal {
				t.Fatalf("includeThoughts = %v, want %v; out=%s", res.Bool(), tc.wantVal, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_FunctionCallOutputAlternateIDs(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.7-flash-high",
		"input": [
			{"role":"user","content":"run command"},
			{"type":"function_call","call_id":"call_bash_1","name":"Bash","arguments":"{\"command\":\"ls\"}"},
			{"type":"function_call_output","id":"call_bash_1","output":"main.go"}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.7-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity request: %v; output=%s", errPair, out)
	}

	responseID := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String()
	if responseID != "call_bash_1" {
		t.Fatalf("functionResponse.id = %q, want call_bash_1", responseID)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_InterruptedFunctionCallPreservesPairing(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"List the files."}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Stop, do something else instead."}]},
			{"type":"function_call","call_id":"c2","name":"shell","arguments":"{\"command\":[\"pwd\"]}"},
			{"type":"function_call_output","call_id":"c2","output":"/tmp\n"}
		],
		"tools": [{"type":"function","name":"shell","description":"Runs a shell command.","strict":false,
			"parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}},
			"required":["command"],"additionalProperties":false}}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"store": false,
		"stream": false
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.8-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity request: %v; output=%s", errPair, out)
	}

	c1Resp := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
	if c1Resp.Get("id").String() != "c1" || c1Resp.Get("name").String() != "shell" || c1Resp.Get("response.result").String() != "call interrupted, no output" {
		t.Fatalf("unexpected synthesized response for c1: %s", c1Resp.Raw)
	}
	c2Resp := gjson.GetBytes(out, "request.contents.5.parts.0.functionResponse")
	if c2Resp.Get("id").String() != "c2" || c2Resp.Get("name").String() != "shell" || c2Resp.Get("response.result").String() != "/tmp\n" {
		t.Fatalf("unexpected real response for c2: %s", c2Resp.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_ParallelInterruptedFunctionCallPreservesPairingAndOrder(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"List and print."}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},
			{"type":"function_call","call_id":"c2","name":"shell","arguments":"{\"command\":[\"pwd\"]}"},
			{"type":"function_call_output","call_id":"c2","output":"/tmp\n"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Stop, do something else instead."}]},
			{"type":"function_call","call_id":"c3","name":"shell","arguments":"{\"command\":[\"whoami\"]}"},
			{"type":"function_call_output","call_id":"c3","output":"root\n"}
		],
		"tools": [{"type":"function","name":"shell","description":"Runs a shell command.","strict":false,
			"parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}},
			"required":["command"],"additionalProperties":false}}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"store": false,
		"stream": false
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.8-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity request: %v; output=%s", errPair, out)
	}

	c1Resp := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
	if c1Resp.Get("id").String() != "c1" || c1Resp.Get("name").String() != "shell" || c1Resp.Get("response.result").String() != "call interrupted, no output" {
		t.Fatalf("unexpected synthesized response for c1: %s", c1Resp.Raw)
	}
	c2Resp := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse")
	if c2Resp.Get("id").String() != "c2" || c2Resp.Get("name").String() != "shell" || c2Resp.Get("response.result").String() != "/tmp\n" {
		t.Fatalf("unexpected real response for c2: %s", c2Resp.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_TrailingPartialParallelCallsPreservesPairingAndOrder(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"List and print."}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},
			{"type":"function_call","call_id":"c2","name":"shell","arguments":"{\"command\":[\"pwd\"]}"},
			{"type":"function_call_output","call_id":"c2","output":"/tmp\n"}
		],
		"tools": [{"type":"function","name":"shell","description":"Runs a shell command.","strict":false,
			"parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}},
			"required":["command"],"additionalProperties":false}}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"store": false,
		"stream": false
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.8-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity trailing partial parallel request: %v; output=%s", errPair, out)
	}

	c1Resp := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
	if c1Resp.Get("id").String() != "c1" || c1Resp.Get("name").String() != "shell" || c1Resp.Get("response.result").String() != "call interrupted, no output" {
		t.Fatalf("unexpected synthesized response for c1: %s", c1Resp.Raw)
	}
	c2Resp := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse")
	if c2Resp.Get("id").String() != "c2" || c2Resp.Get("name").String() != "shell" || c2Resp.Get("response.result").String() != "/tmp\n" {
		t.Fatalf("unexpected real response for c2: %s", c2Resp.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_InterruptedMessageBeforeRealOutputPreservesPairingAndOrder(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.8-flash-high",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"List and print."}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},
			{"type":"function_call","call_id":"c2","name":"shell","arguments":"{\"command\":[\"pwd\"]}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Stop, do something else instead."}]},
			{"type":"function_call_output","call_id":"c2","output":"/tmp\n"}
		],
		"tools": [{"type":"function","name":"shell","description":"Runs a shell command.","strict":false,
			"parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}},
			"required":["command"],"additionalProperties":false}}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"store": false,
		"stream": false
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.8-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity interrupted message before real output request: %v; output=%s", errPair, out)
	}

	// The user message precedes the completed tool response turn
	stopText := gjson.GetBytes(out, "request.contents.2.parts.0.text").String()
	if stopText != "Stop, do something else instead." {
		t.Fatalf("unexpected text in request.contents[2]: %q", stopText)
	}
	c1Resp := gjson.GetBytes(out, "request.contents.3.parts.0.functionResponse")
	if c1Resp.Get("id").String() != "c1" || c1Resp.Get("name").String() != "shell" || c1Resp.Get("response.result").String() != "call interrupted, no output" {
		t.Fatalf("unexpected synthesized response for c1: %s", c1Resp.Raw)
	}
	c2Resp := gjson.GetBytes(out, "request.contents.3.parts.1.functionResponse")
	if c2Resp.Get("id").String() != "c2" || c2Resp.Get("name").String() != "shell" || c2Resp.Get("response.result").String() != "/tmp\n" {
		t.Fatalf("unexpected real response for c2: %s", c2Resp.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_FunctionCallOutputWithFCOItemID(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.7-flash-high",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run command"}]},
			{"type":"function_call","call_id":"call_1788961125480214178_817","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","id":"fco_01a08664-2d16-7a91-8ab2-2eccd49e4c3e","output":"/tmp"}
		],
		"tools": [{"type":"function","name":"Bash","description":"Runs Bash command.","strict":false,
			"parameters":{"type":"object","properties":{"command":{"type":"string"}},
			"required":["command"],"additionalProperties":false}}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"store": false,
		"stream": false
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.7-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity fco item ID request: %v; output=%s", errPair, out)
	}

	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d; output=%s", len(contents), string(out))
	}

	responses := contents[2].Get("parts").Array()
	if len(responses) != 1 {
		t.Fatalf("expected 1 response part, got %d; output=%s", len(responses), string(out))
	}

	if gotID := responses[0].Get("functionResponse.id").String(); gotID != "call_1788961125480214178_817" {
		t.Fatalf("response id = %q, want call_1788961125480214178_817", gotID)
	}
	if gotName := responses[0].Get("functionResponse.name").String(); gotName != "Bash" {
		t.Fatalf("response name = %q, want Bash", gotName)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_OrphanFunctionCallOutputBecomesUserText(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.7-flash-high",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","id":"fco_01a09fca-8d33-73a1-97fd-4d83ecc02f9d","name":"send_message_to_thread","output":"<codex_delegation>\n  <source_thread_id>01a022d7-d4d0-72b2-8571-4590484ccaee</source_thread_id>\n  <input>Execute sub-task</input>\n</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","id":"fco_01a09fca-a5f0-7b40-9943-21fbc923c537","output":"/Users/developer"}
		],
		"tools": [{"type":"function","name":"Bash","description":"Runs Bash command.","strict":false,
			"parameters":{"type":"object","properties":{"command":{"type":"string"}},
			"required":["command"],"additionalProperties":false}}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"store": false,
		"stream": false
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.7-flash-high", []byte(inputJSON), false)
	rawRequest := gjson.GetBytes(out, "request").Raw
	if errPair := sigcompat.ValidateGeminiFunctionCallPairing([]byte(rawRequest)); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity orphan output request: %v; output=%s", errPair, out)
	}

	delegationFound := false
	bashCallID := ""
	bashResponseID := ""
	for _, content := range gjson.GetBytes(out, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if fr := part.Get("functionResponse"); fr.Exists() {
				if fr.Get("id").String() == "" {
					t.Fatalf("orphan output emitted as functionResponse with empty id: %s", string(out))
				}
				if fr.Get("name").String() == "Bash" {
					bashResponseID = fr.Get("id").String()
				}
			}
			if part.Get("functionCall.name").String() == "Bash" {
				bashCallID = part.Get("functionCall.id").String()
			}
			if content.Get("role").String() == "user" && strings.Contains(part.Get("text").String(), "<codex_delegation>") {
				delegationFound = true
			}
		}
	}
	if !delegationFound {
		t.Fatalf("expected orphan send_message_to_thread output as user text; output=%s", string(out))
	}
	if bashCallID != "call_1789387253098037589_85" {
		t.Fatalf("bash functionCall.id = %q; output=%s", bashCallID, string(out))
	}
	if bashResponseID != "call_1789387253098037589_85" {
		t.Fatalf("bash functionResponse.id = %q; output=%s", bashResponseID, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_WebSearch(t *testing.T) {
	capableModel := "ag-websearch-test-model"
	incapableModel := "ag-websearch-incapable-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("ag-search-test-client", "antigravity", []*registry.ModelInfo{
		{ID: capableModel, SupportsWebSearch: true},
		{ID: incapableModel, SupportsWebSearch: false},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("ag-search-test-client")
	})

	input := []byte(`{
		"model": "` + capableModel + `",
		"input": "What is the newest Go release?",
		"tools": [{
			"type": "web_search",
			"filters": {
				"allowed_domains": ["go.dev"]
			}
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity(capableModel, input, false)
	parsed := gjson.ParseBytes(out)

	if parsed.Get("requestType").String() != "web_search" {
		t.Fatalf("expected requestType web_search, got %q. Output: %s", parsed.Get("requestType").String(), out)
	}
	if query := parsed.Get("request.contents.0.parts.0.text").String(); query != "What is the newest Go release?" {
		t.Fatalf("expected query 'What is the newest Go release?', got %q", query)
	}
	if maxResult := parsed.Get("request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int(); maxResult != 5 {
		t.Fatalf("expected maxResultCount 5, got %d", maxResult)
	}
	domains := parsed.Get("request.tools.0.googleSearch.includedDomains").Array()
	if len(domains) != 1 || domains[0].String() != "go.dev" {
		t.Fatalf("expected includedDomains ['go.dev'], got %s", parsed.Get("request.tools.0.googleSearch.includedDomains").Raw)
	}

	// Incapable model should not build web_search envelope
	incapableInput := []byte(`{
		"model": "` + incapableModel + `",
		"input": "What is the newest Go release?",
		"tools": [{"type": "web_search"}]
	}`)
	incapableOut := ConvertOpenAIResponsesRequestToAntigravity(incapableModel, incapableInput, false)
	if gjson.GetBytes(incapableOut, "requestType").String() == "web_search" {
		t.Fatalf("incapable model should not build web_search requestType envelope, got: %s", incapableOut)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_MixedToolsSuppressesGoogleSearch(t *testing.T) {
	modelID := "ag-mixed-tools-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("client-ag-mixed", "antigravity", []*registry.ModelInfo{
		{ID: modelID, SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("client-ag-mixed")
	})

	input := []byte(`{
		"model": "` + modelID + `",
		"input": "Search weather and lookup local data",
		"tools": [
			{"type": "web_search"},
			{"type": "function", "name": "lookup_data", "description": "Lookup data", "parameters": {"type": "object", "properties": {"k": {"type": "string"}}}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	parsed := gjson.ParseBytes(out)

	// 1. Must not build independent web_search requestType envelope
	if parsed.Get("requestType").String() == "web_search" {
		t.Fatalf("mixed tools must not build independent web_search requestType, got: %s", out)
	}

	// 2. Must not contain native googleSearch block in request.tools
	for _, tool := range parsed.Get("request.tools").Array() {
		if tool.Get("googleSearch").Exists() {
			t.Fatalf("mixed tools must not inject native googleSearch into chat request: %s", out)
		}
	}

	// 3. Must preserve functionDeclarations for lookup_data
	fnFound := false
	for _, tool := range parsed.Get("request.tools").Array() {
		for _, fn := range tool.Get("functionDeclarations").Array() {
			if fn.Get("name").String() == "lookup_data" {
				fnFound = true
				break
			}
		}
	}
	if !fnFound {
		t.Fatalf("custom function lookup_data should be preserved in request.tools: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_CrossProviderCapabilityIsolation(t *testing.T) {
	modelID := "gemini-cross-prov-search-iso"

	reg := registry.GetGlobalRegistry()
	// AI Studio client supports search on this model
	reg.RegisterClient("client-aistudio-search", "aistudio", []*registry.ModelInfo{
		{ID: modelID, SupportsWebSearch: true},
	})
	// Antigravity client does NOT support search on this model
	reg.RegisterClient("client-antigravity-nosearch", "antigravity", []*registry.ModelInfo{
		{ID: modelID, SupportsWebSearch: false},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("client-aistudio-search")
		reg.UnregisterClient("client-antigravity-nosearch")
	})

	input := []byte(`{
		"model": "` + modelID + `",
		"input": "Search web",
		"tools": [{"type": "web_search"}]
	}`)

	// Antigravity dedicated request builder must not build web_search envelope
	// by borrowing AI Studio's capability
	if shouldBuildAntigravityResponsesWebSearchRequest(modelID, input, nil) {
		t.Fatalf("shouldBuildAntigravityResponsesWebSearchRequest should be false for Antigravity route when Antigravity model lacks search capability")
	}

	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	if gjson.GetBytes(out, "requestType").String() == "web_search" {
		t.Fatalf("ConvertOpenAIResponsesRequestToAntigravity should not build web_search requestType envelope when Antigravity route lacks capability: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_DoesNotBorrowNativeCapabilityFromGemini(t *testing.T) {
	const modelID = "gemini-provider-native-capability-only"
	webSearch := true
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("gemini-native-search-only", "gemini", []*registry.ModelInfo{{
		ID:                 modelID,
		NativeCapabilities: &registry.NativeCapabilities{WebSearch: &webSearch},
	}})
	t.Cleanup(func() { reg.UnregisterClient("gemini-native-search-only") })

	input := []byte(`{
		"model": "` + modelID + `",
		"input": "Search web",
		"tools": [{"type": "web_search"}]
	}`)
	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	if gjson.GetBytes(out, "requestType").String() == "web_search" {
		t.Fatalf("borrowed Gemini capability for Antigravity route: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_LocalWebSearchCapability(t *testing.T) {
	trueVal, falseVal := true, false
	for _, tc := range []struct {
		name       string
		capability *bool
		probe      bool
		wantSearch bool
	}{
		{name: "unknown without probe"},
		{name: "unknown with probe", probe: true, wantSearch: true},
		{name: "false without probe", capability: &falseVal},
		{name: "false vetoes probe", capability: &falseVal, probe: true},
		{name: "true still requires probe", capability: &trueVal},
		{name: "true with probe", capability: &trueVal, probe: true, wantSearch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const modelID = "gemini-responses-local-search"
			const clientID = "ag-responses-local-search"
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{{
				ID:                 modelID,
				NativeCapabilities: &registry.NativeCapabilities{WebSearch: tc.capability},
			}})
			t.Cleanup(func() { reg.UnregisterClient(clientID) })
			if tc.probe && !reg.ApplyClientModelCapabilities(clientID, reg.ClientRegistrationEpoch(clientID), func(_ string, info *registry.ModelInfo) {
				info.SupportsWebSearch = true
			}) {
				t.Fatal("capability probe update was not applied")
			}

			input := []byte(`{"model":"` + modelID + `","input":"Search weather","tools":[{"type":"web_search"}]}`)
			unknownInfo := &registry.ModelInfo{ID: modelID, NativeCapabilities: &registry.NativeCapabilities{}}
			for name, out := range map[string][]byte{
				"legacy": ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false),
				"unknown envelope with suffix": ConvertOpenAIResponsesRequestEnvelopeToAntigravity(context.Background(), sdktranslator.RequestEnvelope{
					Model: modelID + "(high)", Body: input, Stream: true, ModelInfo: unknownInfo,
				}).Body,
			} {
				if got := gjson.GetBytes(out, "requestType").String() == "web_search"; got != tc.wantSearch {
					t.Fatalf("%s: web_search = %v, want %v; output=%s", name, got, tc.wantSearch, out)
				}
				if got := gjson.GetBytes(out, "request.tools.0.googleSearch").Exists(); got != tc.wantSearch {
					t.Fatalf("%s: googleSearch present = %v, want %v; output=%s", name, got, tc.wantSearch, out)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_IgnoresOtherProviderSearchVeto(t *testing.T) {
	const modelID = "gemini-responses-provider-search-veto"
	webSearch := false
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("ag-responses-provider-search-veto", "antigravity", []*registry.ModelInfo{{
		ID: modelID, SupportsWebSearch: true,
	}})
	reg.RegisterClient("gemini-responses-provider-search-veto", "gemini", []*registry.ModelInfo{{
		ID: modelID, NativeCapabilities: &registry.NativeCapabilities{WebSearch: &webSearch},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient("ag-responses-provider-search-veto")
		reg.UnregisterClient("gemini-responses-provider-search-veto")
	})

	input := []byte(`{"model":"` + modelID + `","input":"Search weather","tools":[{"type":"web_search"}]}`)
	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	if gjson.GetBytes(out, "requestType").String() != "web_search" {
		t.Fatalf("another provider disabled Antigravity search: %s", out)
	}
	if !gjson.GetBytes(out, "request.tools.0.googleSearch").Exists() {
		t.Fatalf("missing Antigravity googleSearch tool: %s", out)
	}
}

func registerAntigravityResponsesWebSearchModel(t *testing.T, clientID, modelID string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{ID: modelID, SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})
}

func TestConvertOpenAIResponsesRequestToAntigravity_WebSearchPreservesMultiTurnContents(t *testing.T) {
	modelID := "ag-websearch-multiturn-model"
	registerAntigravityResponsesWebSearchModel(t, "ag-search-multiturn-client", modelID)

	input := []byte(`{
		"model": "` + modelID + `",
		"reasoning": {"effort": "high"},
		"input": [
			{
				"role": "user",
				"content": [{"type": "input_text", "text": "We are discussing Paris."}]
			},
			{
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Paris is the capital of France."}]
			},
			{
				"role": "user",
				"content": [{"type": "input_text", "text": "How is the weather there tomorrow?"}]
			}
		],
		"tools": [{"type": "web_search"}]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	parsed := gjson.ParseBytes(out)
	if parsed.Get("requestType").String() != "web_search" {
		t.Fatalf("expected requestType web_search, got %q. Output: %s", parsed.Get("requestType").String(), out)
	}

	contents := parsed.Get("request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("request.contents count = %d, want 3. Output: %s", len(contents), out)
	}
	if contents[0].Get("role").String() != "user" || contents[0].Get("parts.0.text").String() != "We are discussing Paris." {
		t.Fatalf("first turn not preserved. Output: %s", out)
	}
	if contents[1].Get("role").String() != "model" || contents[1].Get("parts.0.text").String() != "Paris is the capital of France." {
		t.Fatalf("assistant turn not preserved as model. Output: %s", out)
	}
	if contents[2].Get("role").String() != "user" || contents[2].Get("parts.0.text").String() != "How is the weather there tomorrow?" {
		t.Fatalf("current user turn not preserved. Output: %s", out)
	}

	if parsed.Get("request.generationConfig.thinkingConfig.thinkingLevel").String() != "high" {
		t.Fatalf("thinkingLevel not preserved. Output: %s", out)
	}
	if !parsed.Get("request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be enabled from reasoning.effort. Output: %s", out)
	}
	if parsed.Get("request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int() != 5 {
		t.Fatalf("expected maxResultCount 5, got %d. Output: %s", parsed.Get("request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int(), out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_WebSearchPreservesInstructions(t *testing.T) {
	modelID := "ag-websearch-instructions-model"
	registerAntigravityResponsesWebSearchModel(t, "ag-search-instructions-client", modelID)

	const userInstructions = "Answer as a travel concierge. Use metric units."
	input := []byte(`{
		"model": "` + modelID + `",
		"instructions": "` + userInstructions + `",
		"input": "How is the weather in Paris tomorrow?",
		"tools": [{"type": "web_search"}]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	parsed := gjson.ParseBytes(out)
	if parsed.Get("requestType").String() != "web_search" {
		t.Fatalf("expected requestType web_search, got %q. Output: %s", parsed.Get("requestType").String(), out)
	}

	foundUserInstructions := false
	for _, part := range parsed.Get("request.systemInstruction.parts").Array() {
		if part.Get("text").String() == userInstructions {
			foundUserInstructions = true
			break
		}
	}
	if !foundUserInstructions {
		t.Fatalf("user instructions missing from request.systemInstruction. Output: %s", out)
	}
	if parsed.Get("request.contents.0.parts.0.text").String() != "How is the weather in Paris tomorrow?" {
		t.Fatalf("user query not preserved. Output: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_WebSearchToolChoiceAutoPreservesContext(t *testing.T) {
	modelID := "ag-websearch-toolchoice-auto-model"
	registerAntigravityResponsesWebSearchModel(t, "ag-search-toolchoice-auto-client", modelID)

	input := []byte(`{
		"model": "` + modelID + `",
		"instructions": "Keep answers concise.",
		"tool_choice": "auto",
		"input": [
			{
				"role": "user",
				"content": [{"type": "input_text", "text": "We are discussing Paris."}]
			},
			{
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Understood."}]
			},
			{
				"role": "user",
				"content": [{"type": "input_text", "text": "How is the weather there tomorrow?"}]
			}
		],
		"tools": [{"type": "web_search"}]
	}`)

	out := ConvertOpenAIResponsesRequestToAntigravity(modelID, input, false)
	parsed := gjson.ParseBytes(out)
	if parsed.Get("requestType").String() != "web_search" {
		t.Fatalf("expected requestType web_search with tool_choice auto, got %q. Output: %s", parsed.Get("requestType").String(), out)
	}

	contents := parsed.Get("request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("request.contents count = %d, want 3. Output: %s", len(contents), out)
	}
	if contents[0].Get("parts.0.text").String() != "We are discussing Paris." {
		t.Fatalf("conversation context dropped with tool_choice auto. Output: %s", out)
	}
	if contents[2].Get("parts.0.text").String() != "How is the weather there tomorrow?" {
		t.Fatalf("current query dropped with tool_choice auto. Output: %s", out)
	}

	foundUserInstructions := false
	for _, part := range parsed.Get("request.systemInstruction.parts").Array() {
		if part.Get("text").String() == "Keep answers concise." {
			foundUserInstructions = true
			break
		}
	}
	if !foundUserInstructions {
		t.Fatalf("user instructions missing with tool_choice auto. Output: %s", out)
	}
	if parsed.Get("request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int() != 5 {
		t.Fatalf("expected maxResultCount 5, got %d. Output: %s", parsed.Get("request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount").Int(), out)
	}
}

func TestConvertOpenAIResponsesRequestEnvelopeToAntigravity(t *testing.T) {
	const modelID = "gemini-3.8-flash-high"
	trueVal, falseVal := true, false
	for _, tc := range []struct {
		name            string
		enabled         bool
		localCapability *bool
	}{
		{name: "request true without local model", enabled: true},
		{name: "request false without local model"},
		{name: "request true overrides local false", enabled: true, localCapability: &falseVal},
		{name: "request false overrides local true", localCapability: &trueVal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.localCapability != nil {
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient("ag-responses-envelope-local", "antigravity", []*registry.ModelInfo{{
					ID:                 modelID,
					SupportsWebSearch:  true,
					NativeCapabilities: &registry.NativeCapabilities{WebSearch: tc.localCapability},
				}})
				t.Cleanup(func() { reg.UnregisterClient("ag-responses-envelope-local") })
			}
			input := []byte(`{"model":"` + modelID + `","input":"Search weather","tools":[{"type":"web_search"}]}`)
			info := &registry.ModelInfo{
				ID:                 modelID,
				NativeCapabilities: &registry.NativeCapabilities{WebSearch: &tc.enabled},
			}
			out := ConvertOpenAIResponsesRequestEnvelopeToAntigravity(context.Background(), sdktranslator.RequestEnvelope{
				Model: modelID, Body: input, ModelInfo: info,
			})
			if got := gjson.GetBytes(out.Body, "requestType").String() == "web_search"; got != tc.enabled {
				t.Fatalf("web_search = %v, want %v; output=%s", got, tc.enabled, out.Body)
			}
			if got := gjson.GetBytes(out.Body, "request.tools.0.googleSearch").Exists(); got != tc.enabled {
				t.Fatalf("googleSearch present = %v, want %v; output=%s", got, tc.enabled, out.Body)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_AudioAndVideoInput(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Analyze audio and video"},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}},
					{"type": "audio", "audio": {"data": "SUQzBA==", "format": "mp3"}},
					{"type": "audio", "audio": {"data": "UklGRg==", "format": "wav"}},
					{"type": "input_video", "video_url": "data:video/mp4;base64,AAAAIGZ0eXA="},
					{"type": "video", "video": {"data": "GkXfo59ChoEBQveBAULygQ8=", "format": "webm"}}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) == 0 {
		t.Fatalf("expected at least 1 content, got 0. Output: %s", out)
	}

	parts := contents[0].Get("parts").Array()
	if len(parts) < 6 {
		t.Fatalf("expected at least 6 parts, got %d. Output: %s", len(parts), out)
	}

	var foundAudio, foundAudioAliasMp3, foundWavAudio, foundVideo, foundWebm bool
	var audioCount int
	for _, part := range parts {
		inline := part.Get("inline_data")
		if !inline.Exists() {
			inline = part.Get("inlineData")
		}
		if !inline.Exists() {
			continue
		}
		mime := inline.Get("mime_type").String()
		if mime == "" {
			mime = inline.Get("mimeType").String()
		}
		data := inline.Get("data").String()
		if mime == "audio/mpeg" && data == "SUQzBA==" {
			audioCount++
			if audioCount == 1 {
				foundAudio = true
			} else {
				foundAudioAliasMp3 = true
			}
		}
		if mime == "audio/wav" && data == "UklGRg==" {
			foundWavAudio = true
		}
		if mime == "video/mp4" && data == "AAAAIGZ0eXA=" {
			foundVideo = true
		}
		if mime == "video/webm" && data == "GkXfo59ChoEBQveBAULygQ8=" {
			foundWebm = true
		}
	}

	if !foundAudio {
		t.Fatalf("expected audio part with mime audio/mpeg, not found. Output: %s", out)
	}
	if !foundAudioAliasMp3 {
		t.Fatalf("expected audio alias part with mime audio/mpeg, not found. Output: %s", out)
	}
	if !foundWavAudio {
		t.Fatalf("expected audio alias part with mime audio/wav, not found. Output: %s", out)
	}
	if !foundVideo {
		t.Fatalf("expected video part with mime video/mp4, not found. Output: %s", out)
	}
	if !foundWebm {
		t.Fatalf("expected video part with mime video/webm, not found. Output: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_InvalidDataURLsRejected(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_audio", "data": " DATA:audio/wav;base64,!!!"},
					{"type": "input_image", "source": {"type": "base64", "data": " DATA:image/png;base64,!!!"}},
					{"type": "input_video", "video_url": "data:text/plain,hello"}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	for _, content := range contents {
		for _, part := range content.Get("parts").Array() {
			if part.Get("inline_data").Exists() || part.Get("inlineData").Exists() {
				t.Fatalf("expected no inlineData/inline_data for invalid data URLs, got: %s", part.Raw)
			}
		}
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_FallbackMIME(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_image", "image_url": "data:;base64,AAAA", "filename": "photo.jpeg"},
					{"type": "input_file", "file_data": "data:;base64,BBBB", "format": "pdf"},
					{"type": "input_image", "image_url": "data:;base64,CCCC", "filename": "photo"},
					{"type": "input_image", "image_url": "data:;base64,DDDD", "filename": "photo.unknownext"},
					{"type": "input_image", "image_url": "data:binary/octet-stream;base64,EEEE", "filename": "photo.jpeg"},
					{"type": "input_file", "file_data": "QUJD", "filename": "report.pdf", "mime_type": "binary/octet-stream"}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) < 6 {
		t.Fatalf("expected at least 6 parts, got %d. Output: %s", len(parts), out)
	}

	getMime := func(part gjson.Result) string {
		if inline := part.Get("inline_data"); inline.Exists() {
			if m := inline.Get("mime_type").String(); m != "" {
				return m
			}
			return inline.Get("mimeType").String()
		}
		if inline := part.Get("inlineData"); inline.Exists() {
			if m := inline.Get("mimeType").String(); m != "" {
				return m
			}
			return inline.Get("mime_type").String()
		}
		return ""
	}

	if got := getMime(parts[0]); got != "image/jpeg" {
		t.Errorf("parts[0] mime = %q, want image/jpeg. Output: %s", got, out)
	}
	if got := getMime(parts[1]); got != "application/pdf" {
		t.Errorf("parts[1] mime = %q, want application/pdf. Output: %s", got, out)
	}
	if got := getMime(parts[2]); got != "image/png" {
		t.Errorf("parts[2] (photo no ext) mime = %q, want image/png. Output: %s", got, out)
	}
	if got := getMime(parts[3]); got != "image/png" {
		t.Errorf("parts[3] (photo.unknownext) mime = %q, want image/png. Output: %s", got, out)
	}
	if got := getMime(parts[4]); got != "image/jpeg" {
		t.Errorf("parts[4] (binary/octet-stream fallback) mime = %q, want image/jpeg. Output: %s", got, out)
	}
	if got := getMime(parts[5]); got != "application/pdf" {
		t.Errorf("parts[5] (file binary/octet-stream fallback) mime = %q, want application/pdf. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_RemoteAudioAndVideo(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_audio", "audio_url": "https://example.com/audio.wav"},
					{"type": "input_video", "video_url": "https://example.com/video.mp4"},
					{"type": "input_image", "image_url": "https://example.com/photo.jpeg"},
					{"type": "input_video", "video_url": "https://example.com/clip.webm", "format": "binary/octet-stream"},
					{"type": "input_audio", "audio_url": "https://example.com/recording.mp3", "mime_type": "application/octet-stream"}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) == 0 {
		t.Fatalf("expected at least 1 content, got 0. Output: %s", out)
	}

	parts := contents[0].Get("parts").Array()
	if len(parts) < 5 {
		t.Fatalf("expected at least 5 parts, got %d. Output: %s", len(parts), out)
	}

	var foundAudio, foundVideo, foundImage, foundWebm, foundMp3 bool
	for _, part := range parts {
		fileData := part.Get("file_data")
		if !fileData.Exists() {
			fileData = part.Get("fileData")
		}
		if !fileData.Exists() {
			continue
		}
		mime := fileData.Get("mime_type").String()
		if mime == "" {
			mime = fileData.Get("mimeType").String()
		}
		uri := fileData.Get("file_uri").String()
		if uri == "" {
			uri = fileData.Get("fileUri").String()
		}
		if mime == "audio/wav" && uri == "https://example.com/audio.wav" {
			foundAudio = true
		}
		if mime == "video/mp4" && uri == "https://example.com/video.mp4" {
			foundVideo = true
		}
		if mime == "image/jpeg" && uri == "https://example.com/photo.jpeg" {
			foundImage = true
		}
		if mime == "video/webm" && uri == "https://example.com/clip.webm" {
			foundWebm = true
		}
		if mime == "audio/mpeg" && uri == "https://example.com/recording.mp3" {
			foundMp3 = true
		}
	}

	if !foundAudio {
		t.Fatalf("expected remote audio part with mime audio/wav, not found. Output: %s", out)
	}
	if !foundVideo {
		t.Fatalf("expected remote video part with mime video/mp4, not found. Output: %s", out)
	}
	if !foundImage {
		t.Fatalf("expected remote image part with mime image/jpeg, not found. Output: %s", out)
	}
	if !foundWebm {
		t.Fatalf("expected remote video part with mime video/webm, not found. Output: %s", out)
	}
	if !foundMp3 {
		t.Fatalf("expected remote audio part with mime audio/mpeg, not found. Output: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_InlineExplicitFormatNotOverriddenByFilename(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_audio", "data": "UklGRg==", "format": "wav", "filename": "download.bin"},
					{"type": "input_video", "data": "AAAAIGZ0eXA=", "format": "mp4", "filename": "download.bin"}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %d. Output: %s", len(parts), out)
	}

	getMime := func(part gjson.Result) string {
		if inline := part.Get("inline_data"); inline.Exists() {
			if m := inline.Get("mime_type").String(); m != "" {
				return m
			}
			return inline.Get("mimeType").String()
		}
		if inline := part.Get("inlineData"); inline.Exists() {
			if m := inline.Get("mimeType").String(); m != "" {
				return m
			}
			return inline.Get("mime_type").String()
		}
		return ""
	}

	if got := getMime(parts[0]); got != "audio/wav" {
		t.Errorf("audio inline mime = %q, want audio/wav. Output: %s", got, out)
	}
	if got := getMime(parts[1]); got != "video/mp4" {
		t.Errorf("video inline mime = %q, want video/mp4. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_RemoteNestedFormatNotOverriddenByFilename(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_video", "video_url": "https://example.com/download.bin", "input_video": {"format": "mp4"}},
					{"type": "input_audio", "audio_url": "https://example.com/download.bin", "input_audio": {"mime_type": "audio/wav"}},
					{"type": "input_file", "file_url": "https://example.com/download.bin", "file": {"format": "pdf"}},
					{"type": "input_file", "file_url": "https://example.com/download.bin", "file": {"format": "jpeg"}},
					{"type": "input_image", "image_url": "https://example.com/download.bin", "image": {"format": "jpeg"}}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) < 5 {
		t.Fatalf("expected at least 5 parts, got %d. Output: %s", len(parts), out)
	}

	getFileMime := func(part gjson.Result) string {
		if fileData := part.Get("file_data"); fileData.Exists() {
			if m := fileData.Get("mime_type").String(); m != "" {
				return m
			}
			return fileData.Get("mimeType").String()
		}
		if fileData := part.Get("fileData"); fileData.Exists() {
			if m := fileData.Get("mimeType").String(); m != "" {
				return m
			}
			return fileData.Get("mime_type").String()
		}
		return ""
	}

	if got := getFileMime(parts[0]); got != "video/mp4" {
		t.Errorf("video file_data mime = %q, want video/mp4. Output: %s", got, out)
	}
	if got := getFileMime(parts[1]); got != "audio/wav" {
		t.Errorf("audio file_data mime = %q, want audio/wav. Output: %s", got, out)
	}
	if got := getFileMime(parts[2]); got != "application/pdf" {
		t.Errorf("file file_data mime = %q, want application/pdf. Output: %s", got, out)
	}
	if got := getFileMime(parts[3]); got != "image/jpeg" {
		t.Errorf("jpeg file file_data mime = %q, want image/jpeg. Output: %s", got, out)
	}
	if got := getFileMime(parts[4]); got != "image/jpeg" {
		t.Errorf("image.format jpeg file_data mime = %q, want image/jpeg. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_GenericMIMEWithNestedFormatNotOverriddenByFilename(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_video", "video_url": "https://example.com/download.bin", "mime_type": "application/octet-stream", "input_video": {"format": "mp4"}},
					{"type": "input_audio", "audio_url": "https://example.com/download.bin", "mime_type": "binary/octet-stream", "input_audio": {"format": "wav"}},
					{"type": "input_file", "file_url": "https://example.com/download.bin", "mime_type": "application/octet-stream", "file": {"format": "pdf"}}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 parts, got %d. Output: %s", len(parts), out)
	}

	getFileMime := func(part gjson.Result) string {
		if fileData := part.Get("file_data"); fileData.Exists() {
			if m := fileData.Get("mime_type").String(); m != "" {
				return m
			}
			return fileData.Get("mimeType").String()
		}
		if fileData := part.Get("fileData"); fileData.Exists() {
			if m := fileData.Get("mimeType").String(); m != "" {
				return m
			}
			return fileData.Get("mime_type").String()
		}
		return ""
	}

	if got := getFileMime(parts[0]); got != "video/mp4" {
		t.Errorf("video file_data mime = %q, want video/mp4. Output: %s", got, out)
	}
	if got := getFileMime(parts[1]); got != "audio/wav" {
		t.Errorf("audio file_data mime = %q, want audio/wav. Output: %s", got, out)
	}
	if got := getFileMime(parts[2]); got != "application/pdf" {
		t.Errorf("file file_data mime = %q, want application/pdf. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_UnknownExtensionFallsBackToDefaults(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_audio", "audio_url": "https://example.com/download.bin", "mime_type": "application/octet-stream"},
					{"type": "input_video", "video_url": "https://example.com/download.bin", "mime_type": "application/octet-stream"}
				]
			}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %d. Output: %s", len(parts), out)
	}

	getFileMime := func(part gjson.Result) string {
		if fileData := part.Get("file_data"); fileData.Exists() {
			if m := fileData.Get("mime_type").String(); m != "" {
				return m
			}
			return fileData.Get("mimeType").String()
		}
		if fileData := part.Get("fileData"); fileData.Exists() {
			if m := fileData.Get("mimeType").String(); m != "" {
				return m
			}
			return fileData.Get("mime_type").String()
		}
		return ""
	}

	if got := getFileMime(parts[0]); got != "audio/wav" {
		t.Errorf("audio file_data mime = %q, want audio/wav. Output: %s", got, out)
	}
	if got := getFileMime(parts[1]); got != "video/mp4" {
		t.Errorf("video file_data mime = %q, want video/mp4. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_TopLevelMedia(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"input": [
			{"type": "input_text", "text": "Describe video and audio"},
			{"type": "input_video", "video_url": "data:video/mp4;base64,AAAAIGZ0eXA="},
			{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
		]
	}`

	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) == 0 {
		t.Fatalf("expected at least 1 content, got 0. Output: %s", out)
	}

	parts := contents[0].Get("parts").Array()
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 parts, got %d. Output: %s", len(parts), out)
	}

	var foundText, foundVideo, foundAudio bool
	for _, part := range parts {
		if part.Get("text").String() == "Describe video and audio" {
			foundText = true
		}
		inline := part.Get("inline_data")
		if !inline.Exists() {
			inline = part.Get("inlineData")
		}
		if !inline.Exists() {
			continue
		}
		mime := inline.Get("mime_type").String()
		if mime == "" {
			mime = inline.Get("mimeType").String()
		}
		data := inline.Get("data").String()
		if mime == "video/mp4" && data == "AAAAIGZ0eXA=" {
			foundVideo = true
		}
		if mime == "audio/mpeg" && data == "SUQzBA==" {
			foundAudio = true
		}
	}

	if !foundText {
		t.Errorf("expected text part in output: %s", out)
	}
	if !foundVideo {
		t.Errorf("expected video inline part in output: %s", out)
	}
	if !foundAudio {
		t.Errorf("expected audio inline part in output: %s", out)
	}
}
