package responses

import (
	"encoding/base64"
	"strings"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
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
