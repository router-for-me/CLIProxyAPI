package common

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeMessagesJSONToSSEPassthrough(t *testing.T) {
	for _, raw := range []string{
		"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"sse-model\"}}\r\n\r\n: keepalive\r\n",
		"data: {\"type\":\"message_stop\"}\n\n",
		`{"type":"error","error":{"message":"bad request"}}`,
		`{"type":"message","content":`,
		`{"type":"message","content":"not an array"}`,
		"",
	} {
		out, model := ClaudeMessagesJSONToSSE([]byte(raw))
		if !bytes.Equal(out, []byte(raw)) || model != "" {
			t.Errorf("passthrough changed payload: %q -> %q, model=%q", raw, out, model)
		}
	}
}

func TestClaudeMessagesJSONToSSEEvents(t *testing.T) {
	raw := []byte(`{
		"id":"msg_native","type":"message","role":"assistant","model":"claude-native",
		"content":[
			{"type":"text","text":"line 1\n\"quoted\" \\ 雪 <tag>"},
			{"type":"tool_use","id":"tool_nested","name":"lookup","input":{"nested":{"text":"line\n\"quoted\"","items":[true,null,2]},"empty":{}}},
			{"type":"tool_use","id":"tool_empty","name":"clock","input":{}},
			{"type":"thinking","thinking":"plan\nnext","signature":"sig\"native"},
			{"type":"redacted_thinking","data":"opaque"}
		],
		"stop_reason":"stop_sequence","stop_sequence":"END\n",
		"usage":{"input_tokens":13,"output_tokens":5,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}
	}`)
	out, model := ClaudeMessagesJSONToSSE(raw)
	if model != "claude-native" {
		t.Errorf("model = %q, want claude-native", model)
	}
	var events []gjson.Result
	for _, line := range bytes.Split(out, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if !gjson.ValidBytes(payload) {
			t.Fatalf("invalid SSE JSON: %s", payload)
		}
		events = append(events, gjson.ParseBytes(payload))
	}
	wantTypes := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_stop",
		"message_delta", "message_stop",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d; payload=%s", len(events), len(wantTypes), out)
	}
	for i, want := range wantTypes {
		if got := events[i].Get("type").String(); got != want {
			t.Errorf("event %d type = %q, want %q", i, got, want)
		}
	}
	root := gjson.ParseBytes(raw)
	for _, path := range []string{"id", "model", "role", "usage"} {
		if got, want := events[0].Get("message."+path).Value(), root.Get(path).Value(); !reflect.DeepEqual(got, want) {
			t.Errorf("message_start %s = %#v, want %#v", path, got, want)
		}
	}
	if got := events[0].Get("message.content"); !got.IsArray() || len(got.Array()) != 0 {
		t.Errorf("message_start content must be empty, got %s", got.Raw)
	}
	for _, block := range []struct{ start, stop, index int }{{1, 3, 0}, {4, 6, 1}, {7, 9, 2}, {10, 13, 3}, {14, 15, 4}} {
		for i := block.start; i <= block.stop; i++ {
			if got := events[i].Get("index"); !got.Exists() || got.Int() != int64(block.index) {
				t.Errorf("event %d index = %s, want %d", i, got.Raw, block.index)
			}
		}
	}
	for _, check := range []struct {
		event int
		path  string
		want  string
	}{
		{2, "delta.type", "text_delta"}, {2, "delta.text", root.Get("content.0.text").String()},
		{4, "content_block.id", "tool_nested"}, {4, "content_block.name", "lookup"},
		{5, "delta.type", "input_json_delta"}, {8, "delta.type", "input_json_delta"},
		{11, "delta.type", "thinking_delta"}, {11, "delta.thinking", "plan\nnext"},
		{12, "delta.type", "signature_delta"}, {12, "delta.signature", "sig\"native"},
		{14, "content_block.type", "redacted_thinking"}, {14, "content_block.data", "opaque"},
		{16, "delta.stop_reason", "stop_sequence"}, {16, "delta.stop_sequence", "END\n"},
	} {
		if got := events[check.event].Get(check.path).String(); got != check.want {
			t.Errorf("event %d %s = %q, want %q", check.event, check.path, got, check.want)
		}
	}
	for _, check := range []struct{ event, block int }{{5, 1}, {8, 2}} {
		var got any
		if errUnmarshal := json.Unmarshal([]byte(events[check.event].Get("delta.partial_json").String()), &got); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		want := root.Get("content").Array()[check.block].Get("input").Value()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("tool input = %#v, want %#v", got, want)
		}
	}
	if got, want := events[16].Get("usage").Value(), root.Get("usage").Value(); !reflect.DeepEqual(got, want) {
		t.Errorf("final usage = %#v, want %#v", got, want)
	}
}

func TestClaudeMessagesJSONToSSECitations(t *testing.T) {
	raw := []byte(`{"type":"message","content":[{"type":"text","text":"Answer.","citations":[{"type":"web_search_result_location","url":"https://example.com","title":"Source","cited_text":"Answer.","encrypted_index":"IDX"}]}]}`)
	out, _ := ClaudeMessagesJSONToSSE(raw)
	count := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		event := gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
		if event.Get("delta.type").String() == "citations_delta" {
			count++
			if event.Get("index").Int() != 0 || event.Get("delta.citation.encrypted_index").String() != "IDX" {
				t.Errorf("citation not preserved: %s", line)
			}
		}
	}
	if count != 1 {
		t.Fatalf("citation events = %d, want 1", count)
	}
}
