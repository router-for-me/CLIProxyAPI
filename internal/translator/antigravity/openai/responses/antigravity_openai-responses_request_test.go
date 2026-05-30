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
