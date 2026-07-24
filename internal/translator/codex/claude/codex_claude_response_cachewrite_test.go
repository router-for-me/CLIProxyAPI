package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// The Codex Responses API reports cache-write tokens under
// usage.input_tokens_details.cache_write_tokens. Anthropic's response schema
// carries the same figure as usage.cache_creation_input_tokens. The sibling
// Codex->OpenAI translator already maps it; these tests pin the Codex->Claude
// path so it is no longer dropped (upstream issue #4262). Without the mapping,
// clients reading the Anthropic usage block under-report cost and context.

func TestConvertCodexResponseToClaude_StreamPreservesCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-sol"}}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	var delta gjson.Result
	found := false
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			d := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if d.Get("type").String() == "message_delta" {
				delta = d
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no message_delta emitted; outputs=%v", outputs)
	}

	if got := delta.Get("usage.cache_creation_input_tokens"); !got.Exists() {
		t.Fatalf("cache_creation_input_tokens missing; delta=%s", delta.Raw)
	} else if got.Int() != 40 {
		t.Fatalf("cache_creation_input_tokens = %d, want 40; delta=%s", got.Int(), delta.Raw)
	}
	// input_tokens still has cached reads subtracted (100 - 30), unchanged behavior.
	if got := delta.Get("usage.input_tokens").Int(); got != 70 {
		t.Fatalf("input_tokens = %d, want 70; delta=%s", got, delta.Raw)
	}
	if got := delta.Get("usage.cache_read_input_tokens").Int(); got != 30 {
		t.Fatalf("cache_read_input_tokens = %d, want 30; delta=%s", got, delta.Raw)
	}
}

func TestConvertCodexResponseToClaude_StreamOmitsCacheWriteWhenZero(t *testing.T) {
	ctx := context.Background()
	var param any
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_2","model":"gpt-5.6-sol"}}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_2","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}}`),
	}
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", []byte(`{"messages":[]}`), nil, chunk, &param)...)
	}
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			d := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if d.Get("type").String() == "message_delta" &&
				d.Get("usage.cache_creation_input_tokens").Exists() {
				t.Fatalf("cache_creation_input_tokens should be omitted when zero; delta=%s", d.Raw)
			}
		}
	}
}

func TestConvertCodexResponseToClaudeNonStream_PreservesCacheWriteTokens(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_3","model":"gpt-5.6-sol","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := ConvertCodexResponseToClaudeNonStream(context.Background(), "", []byte(`{"messages":[]}`), nil, raw, nil)

	got := gjson.GetBytes(out, "usage.cache_creation_input_tokens")
	if !got.Exists() {
		t.Fatalf("cache_creation_input_tokens missing; out=%s", string(out))
	}
	if got.Int() != 40 {
		t.Fatalf("cache_creation_input_tokens = %d, want 40; out=%s", got.Int(), string(out))
	}
	if v := gjson.GetBytes(out, "usage.input_tokens").Int(); v != 70 {
		t.Fatalf("input_tokens = %d, want 70; out=%s", v, string(out))
	}
}
