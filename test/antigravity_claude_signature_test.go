package test

import (
	"context"
	"strings"
	"testing"

	agclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/antigravity/claude"
	"github.com/tidwall/gjson"
)

func TestAntigravityClaudeRequest_DropsUnsignedThinkingBlocks(t *testing.T) {
	model := "gemini-claude-sonnet-4-5-thinking"
	input := []byte(`{
  "model":"` + model + `",
  "messages":[
    {"role":"assistant","content":[{"type":"thinking","thinking":"secret without signature"}]},
    {"role":"user","content":[{"type":"text","text":"hi"}]}
  ]
}`)

	out := agclaude.ConvertClaudeRequestToAntigravity(model, input, false)
	contents := gjson.GetBytes(out, "request.contents")
	if !contents.Exists() || !contents.IsArray() {
		t.Fatalf("expected request.contents array, got: %s", string(out))
	}
	if got := len(contents.Array()); got != 1 {
		t.Fatalf("expected 1 content message after dropping unsigned thinking-only assistant message, got %d: %s", got, contents.Raw)
	}
	if role := contents.Array()[0].Get("role").String(); role != "user" {
		t.Fatalf("expected remaining message role=user, got %q", role)
	}
}

func TestAntigravityClaudeStreamResponse_EmitsSignatureDeltaForStandaloneSignaturePart(t *testing.T) {
	raw := []byte(`{
  "response":{
    "responseId":"resp_1",
    "modelVersion":"claude-sonnet-4-5-thinking",
    "candidates":[{
      "content":{"parts":[
        {"text":"THOUGHT","thought":true},
        {"thought":true,"thoughtSignature":"sig123"},
        {"text":"ANSWER","thought":false}
      ]},
      "finishReason":"STOP"
    }],
    "usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":1,"totalTokenCount":3}
  }
}`)

	var param any
	chunks := agclaude.ConvertAntigravityResponseToClaude(context.Background(), "", nil, nil, raw, &param)
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, `"type":"signature_delta"`) {
		t.Fatalf("expected signature_delta in stream output, got: %s", joined)
	}
	if !strings.Contains(joined, `"signature":"sig123"`) {
		t.Fatalf("expected signature sig123 in stream output, got: %s", joined)
	}
	// Signature delta must be attached to the thinking content block (index 0 in this minimal stream).
	if !strings.Contains(joined, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`) {
		t.Fatalf("expected signature_delta to target thinking block index 0, got: %s", joined)
	}
}

func TestAntigravityClaudeNonStreamResponse_IncludesThinkingSignature(t *testing.T) {
	raw := []byte(`{
  "response":{
    "responseId":"resp_1",
    "modelVersion":"claude-sonnet-4-5-thinking",
    "candidates":[{
      "content":{"parts":[
        {"text":"THOUGHT","thought":true},
        {"thought":true,"thoughtSignature":"sig123"},
        {"text":"ANSWER","thought":false}
      ]},
      "finishReason":"STOP"
    }],
    "usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":1,"totalTokenCount":3}
  }
}`)

	out := agclaude.ConvertAntigravityResponseToClaudeNonStream(context.Background(), "", nil, nil, raw, nil)
	if !gjson.Valid(out) {
		t.Fatalf("expected valid JSON output, got: %s", out)
	}
	content := gjson.Get(out, "content")
	if !content.Exists() || !content.IsArray() {
		t.Fatalf("expected content array in output, got: %s", out)
	}

	found := false
	for _, block := range content.Array() {
		if block.Get("type").String() != "thinking" {
			continue
		}
		found = true
		if got := block.Get("signature").String(); got != "sig123" {
			t.Fatalf("expected thinking.signature=sig123, got %q (block=%s)", got, block.Raw)
		}
		if got := block.Get("thinking").String(); got != "THOUGHT" {
			t.Fatalf("expected thinking.thinking=THOUGHT, got %q (block=%s)", got, block.Raw)
		}
	}
	if !found {
		t.Fatalf("expected a thinking block in output, got: %s", out)
	}
}
