package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

func TestIssue7279GeminiToolResultsUseClaudeUserMessages(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		wantIDs  []string
	}{
		{
			name: "model role with parallel responses",
			contents: `[
				{"role":"user","parts":[{"text":"read files"}]},
				{"role":"model","parts":[{"functionCall":{"name":"read","id":"call-a","args":{}}},{"functionCall":{"name":"read","id":"call-b","args":{}}}]},
				{"role":"model","parts":[{"functionResponse":{"name":"read","id":"call-a","response":{"result":"one"}}},{"functionResponse":{"name":"read","id":"call-b","response":{"result":"two"}}}]}
			]`,
			wantIDs: []string{"call-a", "call-b"},
		},
		{
			name: "assistant role with snake case response",
			contents: `[
				{"role":"user","parts":[{"text":"read file"}]},
				{"role":"model","parts":[{"function_call":{"name":"read","id":"call-a","args":{}}}]},
				{"role":"assistant","parts":[{"function_response":{"name":"read","id":"call-a","response":{"result":"one"}}}]}
			]`,
			wantIDs: []string{"call-a"},
		},
		{
			name: "call and response in one model turn",
			contents: `[
				{"role":"user","parts":[{"text":"read file"}]},
				{"role":"model","parts":[{"functionCall":{"name":"read","id":"call-a","args":{}}},{"functionResponse":{"name":"read","id":"call-a","response":{"result":"one"}}}]}
			]`,
			wantIDs: []string{"call-a"},
		},
		{
			name: "ordinary user tool result",
			contents: `[
				{"role":"user","parts":[{"text":"read file"}]},
				{"role":"model","parts":[{"functionCall":{"name":"read","id":"call-a","args":{}}}]},
				{"role":"user","parts":[{"functionResponse":{"name":"read","id":"call-a","response":{"result":"one"}}}]}
			]`,
			wantIDs: []string{"call-a"},
		},
		{
			name: "model result without explicit tool id",
			contents: `[
				{"role":"user","parts":[{"text":"read file"}]},
				{"role":"model","parts":[{"functionCall":{"name":"read","args":{}}}]},
				{"role":"model","parts":[{"functionResponse":{"name":"read","response":{"result":"one"}}}]}
			]`,
			wantIDs: []string{"toolu_gemini_0000000000000001"},
		},
		{
			name: "missing result role",
			contents: `[
				{"role":"user","parts":[{"text":"read file"}]},
				{"role":"model","parts":[{"functionCall":{"name":"read","id":"call-a","args":{}}}]},
				{"parts":[{"functionResponse":{"name":"read","id":"call-a","response":{"result":"one"}}}]}
			]`,
			wantIDs: []string{"call-a"},
		},
	}

	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "/nonstream", true: "/stream"}[stream], func(t *testing.T) {
				out, errConvert := ConvertGeminiRequestToClaude("claude-sonnet-4", []byte(`{"contents":`+tt.contents+`}`), stream)
				if errConvert != nil {
					t.Fatalf("conversion failed: %v", errConvert)
				}
				messages := ojson.GetBytesNoCopy(out, "messages").Array()
				if len(messages) != 3 {
					t.Fatalf("messages = %s; want user, assistant tool calls, user tool results", ojson.GetBytesNoCopy(out, "messages").Raw)
				}
				if got := messages[1].Get("role").String(); got != "assistant" {
					t.Fatalf("tool call role = %q, want assistant; body=%s", got, out)
				}
				if got := messages[2].Get("role").String(); got != "user" {
					t.Fatalf("tool result role = %q, want user; body=%s", got, out)
				}
				calls := messages[1].Get("content").Array()
				results := messages[2].Get("content").Array()
				if len(calls) != len(tt.wantIDs) || len(results) != len(tt.wantIDs) {
					t.Fatalf("calls=%s results=%s, want %d pairs", messages[1].Get("content").Raw, messages[2].Get("content").Raw, len(tt.wantIDs))
				}
				for i, wantID := range tt.wantIDs {
					if calls[i].Get("type").String() != "tool_use" || calls[i].Get("id").String() != wantID {
						t.Fatalf("call[%d] = %s, want tool_use id %q", i, calls[i].Raw, wantID)
					}
					if results[i].Get("type").String() != "tool_result" || results[i].Get("tool_use_id").String() != wantID {
						t.Fatalf("result[%d] = %s, want tool_result id %q", i, results[i].Raw, wantID)
					}
				}
			})
		}
	}
}

func TestIssue7279ToolResultsPrecedeUserText(t *testing.T) {
	tests := map[string]string{
		"text and result in one turn": `[
			{"role":"user","parts":[{"text":"read file"}]},
			{"role":"model","parts":[{"functionCall":{"name":"read","id":"call-a","args":{}}}]},
			{"role":"model","parts":[{"text":"continue"},{"functionResponse":{"name":"read","id":"call-a","response":{"result":"one"}}}]}
		]`,
		"text before a separate result turn": `[
			{"role":"user","parts":[{"text":"read file"}]},
			{"role":"model","parts":[{"functionCall":{"name":"read","id":"call-a","args":{}}}]},
			{"role":"user","parts":[{"text":"continue"}]},
			{"role":"model","parts":[{"functionResponse":{"name":"read","id":"call-a","response":{"result":"one"}}}]}
		]`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			out, errConvert := ConvertGeminiRequestToClaude("claude-sonnet-4", []byte(`{"contents":`+contents+`}`), true)
			if errConvert != nil {
				t.Fatalf("conversion failed: %v", errConvert)
			}
			message := ojson.GetBytesNoCopy(out, "messages.2")
			if message.Get("role").String() != "user" || message.Get("content.0.type").String() != "tool_result" || message.Get("content.0.tool_use_id").String() != "call-a" || message.Get("content.1.text").String() != "continue" {
				t.Fatalf("user content = %s; want tool result before text", message.Raw)
			}
		})
	}
}

func TestIssue7279ClaudeThinkingSignaturesAreValidGeminiBytes(t *testing.T) {
	const validGeminiSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"
	tests := []struct {
		name      string
		signature string
		want      string
	}{
		{name: "foreign Claude signature", signature: "foreign_claude_sig_123"},
		{name: "native Gemini signature", signature: "gemini#" + validGeminiSignature, want: validGeminiSignature},
		{name: "unpadded native Gemini signature", signature: "gemini#EggKBgEMOdbHNA", want: "EggKBgEMOdbHNA=="},
	}
	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "/nonstream", true: "/stream"}[stream], func(t *testing.T) {
				events := []string{
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","signature":"` + tt.signature + `"}}`,
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`,
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + tt.signature + `"}}`,
				}
				var outputs [][]byte
				if stream {
					var param any
					for _, event := range events {
						outputs = append(outputs, ConvertClaudeResponseToGemini(context.Background(), "claude-sonnet-4", nil, nil, []byte(event), &param)...)
					}
				} else {
					outputs = append(outputs, ConvertClaudeResponseToGeminiNonStream(context.Background(), "claude-sonnet-4", nil, nil, []byte(strings.Join(events, "\n")), nil))
				}
				var thoughtText strings.Builder
				var foundSignature string
				for _, out := range outputs {
					for _, part := range ojson.GetBytesNoCopy(out, "candidates.0.content.parts").Array() {
						if part.Get("thought").Bool() {
							thoughtText.WriteString(part.Get("text").String())
						}
						if signature := part.Get("thoughtSignature"); signature.Exists() {
							foundSignature = signature.String()
							if _, errDecode := base64.StdEncoding.DecodeString(foundSignature); errDecode != nil {
								t.Fatalf("thoughtSignature %q is not valid base64: %v; response=%s", foundSignature, errDecode, out)
							}
						}
					}
				}
				if thoughtText.String() != "reasoning" {
					t.Fatalf("thought text = %q, want reasoning", thoughtText.String())
				}
				if tt.want != "" && foundSignature != tt.want {
					t.Fatalf("native thought signature = %q, want %q", foundSignature, tt.want)
				}
			})
		}
	}
}
