package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeResponsesAdjacentCitedTextBlocksShareMessage(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_citations","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"The store reopened in 2024"}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com/store","title":"Store"}}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":", and "}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"Olga was named for Ohlert."}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com/olga","title":"Olga"}}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	wantText := "The store reopened in 2024, and Olga was named for Ohlert."
	checkOutput := func(t *testing.T, items []gjson.Result) {
		t.Helper()
		if len(items) != 2 {
			t.Fatalf("output items = %d, want 2 (web search and one message)", len(items))
		}
		if got := items[0].Get("type").String(); got != "web_search_call" {
			t.Fatalf("first output item type = %q, want web_search_call", got)
		}
		message := items[1]
		if got := message.Get("type").String(); got != "message" {
			t.Fatalf("second output item type = %q, want message", got)
		}
		if got := message.Get("content.#").Int(); got != 1 {
			t.Fatalf("message content parts = %d, want 1", got)
		}
		if got := message.Get("content.0.text").String(); got != wantText {
			t.Fatalf("message text = %q, want %q", got, wantText)
		}
		annotations := message.Get("content.0.annotations").Array()
		if len(annotations) != 2 || annotations[0].Get("url").String() != "https://example.com/store" || annotations[1].Get("url").String() != "https://example.com/olga" {
			t.Fatalf("annotations = %s, want both citations", message.Get("content.0.annotations").Raw)
		}
	}

	t.Run("stream", func(t *testing.T) {
		counts := make(map[string]int)
		var completed gjson.Result
		for _, output := range translateClaudeResponsesStreamThroughRegistry(chunks) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			counts[event]++
			if event == "response.completed" {
				completed = data
			}
		}
		checkOutput(t, completed.Get("response.output").Array())
		for _, event := range []string{"response.content_part.added", "response.output_text.done", "response.content_part.done"} {
			if counts[event] != 1 {
				t.Errorf("%s events = %d, want 1", event, counts[event])
			}
		}
		if counts["response.output_item.done"] != 2 {
			t.Errorf("response.output_item.done events = %d, want 2", counts["response.output_item.done"])
		}
	})

	t.Run("nonstream", func(t *testing.T) {
		var lines []string
		for _, chunk := range chunks {
			lines = append(lines, string(chunk))
		}
		out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, []byte(strings.Join(lines, "\n")), nil)
		checkOutput(t, gjson.GetBytes(out, "output").Array())
	})
}
