package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToGemini_StreamPreservesToolUseID(t *testing.T) {
	ctx := context.Background()
	var param any

	start := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_gateway","name":"lookup"}}`)
	out := ConvertClaudeResponseToGemini(ctx, "gemini-2.5-pro", nil, nil, start, &param)
	if len(out) != 0 {
		t.Fatalf("expected content_block_start to be buffered, got %d chunks", len(out))
	}

	delta := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"status\"}"}}`)
	out = ConvertClaudeResponseToGemini(ctx, "gemini-2.5-pro", nil, nil, delta, &param)
	if len(out) != 0 {
		t.Fatalf("expected input_json_delta to be buffered, got %d chunks", len(out))
	}

	stop := []byte(`data: {"type":"content_block_stop","index":0}`)
	out = ConvertClaudeResponseToGemini(ctx, "gemini-2.5-pro", nil, nil, stop, &param)
	if len(out) != 1 {
		t.Fatalf("expected content_block_stop to emit 1 chunk, got %d", len(out))
	}

	got := gjson.GetBytes(out[0], "candidates.0.content.parts.0.functionCall.id").String()
	if got != "toolu_gateway" {
		t.Fatalf("expected functionCall.id %q, got %q; chunk=%s", "toolu_gateway", got, string(out[0]))
	}
}

func TestConvertClaudeResponseToGeminiNonStreamPreservesToolUseID(t *testing.T) {
	ctx := context.Background()
	raw := []byte(strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_gateway","name":"lookup"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"status\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
	}, "\n"))

	out := ConvertClaudeResponseToGeminiNonStream(ctx, "gemini-2.5-pro", nil, nil, raw, nil)

	got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.id").String()
	if got != "toolu_gateway" {
		t.Fatalf("expected functionCall.id %q, got %q; chunk=%s", "toolu_gateway", got, string(out))
	}
}

func TestConvertClaudeResponseToGemini_StreamThinkingSignature(t *testing.T) {
	const validGeminiSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

	tests := []struct {
		name          string
		signature     string
		wantSignature string
	}{
		{
			name:          "foreign claude signature maps to bypass sentinel",
			signature:     "foreign_claude_sig_123",
			wantSignature: "skip_thought_signature_validator",
		},
		{
			name:          "preserves valid gemini signature",
			signature:     "gemini#" + validGeminiSignature,
			wantSignature: validGeminiSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var param any

			chunks := [][]byte{
				[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-7-sonnet-20250219"}}`),
				[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
				[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking text"}}`),
				[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + tt.signature + `"}}`),
				[]byte(`data: {"type":"content_block_stop","index":0}`),
				[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
				[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"final answer"}}`),
				[]byte(`data: {"type":"content_block_stop","index":1}`),
				[]byte(`data: {"type":"message_stop"}`),
			}

			var emittedParts []gjson.Result
			for _, chunk := range chunks {
				out := ConvertClaudeResponseToGemini(ctx, "gemini-2.5-pro", nil, nil, chunk, &param)
				for _, c := range out {
					parts := gjson.GetBytes(c, "candidates.0.content.parts").Array()
					emittedParts = append(emittedParts, parts...)
				}
			}

			var foundSignature string
			for _, p := range emittedParts {
				if p.Get("thought").Bool() && p.Get("thoughtSignature").Exists() {
					foundSignature = p.Get("thoughtSignature").String()
				}
			}

			if foundSignature != tt.wantSignature {
				t.Fatalf("expected thoughtSignature %q, got %q", tt.wantSignature, foundSignature)
			}
		})
	}
}

func TestConvertClaudeResponseToGeminiNonStream_ThinkingSignature(t *testing.T) {
	const validGeminiSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

	tests := []struct {
		name          string
		signature     string
		wantSignature string
	}{
		{
			name:          "foreign claude signature maps to bypass sentinel",
			signature:     "foreign_claude_sig_123",
			wantSignature: "skip_thought_signature_validator",
		},
		{
			name:          "preserves valid gemini signature",
			signature:     "gemini#" + validGeminiSignature,
			wantSignature: validGeminiSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			raw := []byte(strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-7-sonnet-20250219"}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking text"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + tt.signature + `"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"final answer"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_stop"}`,
			}, "\n"))

			out := ConvertClaudeResponseToGeminiNonStream(ctx, "gemini-2.5-pro", nil, nil, raw, nil)

			thoughtPart := gjson.GetBytes(out, "candidates.0.content.parts.0")
			if !thoughtPart.Get("thought").Bool() || thoughtPart.Get("text").String() != "thinking text" {
				t.Fatalf("expected thought part with text 'thinking text', got %s", thoughtPart.Raw)
			}
			if got := thoughtPart.Get("thoughtSignature").String(); got != tt.wantSignature {
				t.Fatalf("expected thoughtSignature %q, got %q", tt.wantSignature, got)
			}

			textPart := gjson.GetBytes(out, "candidates.0.content.parts.1")
			if textPart.Get("text").String() != "final answer" {
				t.Fatalf("expected text part 'final answer', got %s", textPart.Raw)
			}
		})
	}
}
