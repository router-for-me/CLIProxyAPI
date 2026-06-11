package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Streaming: Anthropic server_tool_use(web_search) + web_search_tool_result must
// translate into OpenAI web_search_call items (in_progress added, completed done).
func TestConvertClaudeResponse_StreamWebSearch(t *testing.T) {
	lines := []string{
		`data: {"type":"message_start","message":{"id":"msg_ws","usage":{"input_tokens":5}}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"id":"srvtoolu_abc","type":"server_tool_use","name":"web_search","input":{"query":"weather seattle"}}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","content":[{"type":"web_search_result","title":"Seattle Weather","url":"https://example.com/wx","encrypted_content":"sunny 60F","page_age":"October 7, 2025"}]}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_stop"}`,
	}

	var state any
	var added, doneItem, completed string
	for _, ln := range lines {
		evs := ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude", nil, nil, []byte(ln), &state)
		for _, e := range evs {
			s := string(e)
			if strings.Contains(s, `"web_search_call"`) && strings.Contains(s, "response.output_item.added") {
				added = s
			}
			if strings.Contains(s, `"web_search_call"`) && strings.Contains(s, "response.output_item.done") {
				doneItem = s
			}
			if strings.Contains(s, "response.completed") {
				completed = s
			}
		}
	}

	if added == "" {
		t.Fatal("expected an in_progress web_search_call output_item.added event")
	}
	addedPayload := added[strings.Index(added, "{"):]
	if got := gjson.Get(addedPayload, "item.status").String(); got != "in_progress" {
		t.Fatalf("added status = %q, want in_progress: %s", got, addedPayload)
	}
	if got := gjson.Get(addedPayload, "item.action.query").String(); got != "weather seattle" {
		t.Fatalf("added action.query = %q, want weather seattle: %s", got, addedPayload)
	}
	if got := gjson.Get(addedPayload, "item.action.queries.0").String(); got != "weather seattle" {
		t.Fatalf("added action.queries[0] = %q, want weather seattle", got)
	}
	if got := gjson.Get(addedPayload, "item.id").String(); got != "ws_srvtoolu_abc" {
		t.Fatalf("added item.id = %q, want ws_srvtoolu_abc", got)
	}

	if doneItem == "" {
		t.Fatal("expected a completed web_search_call output_item.done event")
	}
	donePayload := doneItem[strings.Index(doneItem, "{"):]
	if got := gjson.Get(donePayload, "item.status").String(); got != "completed" {
		t.Fatalf("done status = %q, want completed: %s", got, donePayload)
	}
	if got := gjson.Get(donePayload, "item.result.0.title").String(); got != "Seattle Weather" {
		t.Fatalf("done result[0].title = %q, want Seattle Weather: %s", got, donePayload)
	}
	if got := gjson.Get(donePayload, "item.result.0.url").String(); got != "https://example.com/wx" {
		t.Fatalf("done result[0].url = %q", got)
	}
	if got := gjson.Get(donePayload, "item.result.0.encrypted_content").String(); got != "sunny 60F" {
		t.Fatalf("done result[0].encrypted_content = %q (must not be dropped)", got)
	}
	if got := gjson.Get(donePayload, "item.result.0.page_age").String(); got != "October 7, 2025" {
		t.Fatalf("done result[0].page_age = %q (must not be dropped)", got)
	}

	// The web_search_call item must also appear in the final response.output.
	if completed == "" {
		t.Fatal("expected response.completed event")
	}
	cp := completed[strings.Index(completed, "{"):]
	found := false
	gjson.Get(cp, "response.output").ForEach(func(_, v gjson.Result) bool {
		if v.Get("type").String() == "web_search_call" && v.Get("status").String() == "completed" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("response.output missing completed web_search_call: %s", cp)
	}
}

// Non-stream: aggregated output must contain a completed web_search_call item.
func TestConvertClaudeResponseNonStream_WebSearch(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_ws2","usage":{"input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"srvtoolu_x","type":"server_tool_use","name":"web_search","input":{"query":"golang sjson"}}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","content":[{"type":"web_search_result","title":"sjson docs","url":"https://github.com/tidwall/sjson","encrypted_content":"set json values fast","page_age":null}]}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_stop"}`,
	}, "\n")

	var state any
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude", nil, nil, []byte(raw), &state)

	var item gjson.Result
	gjson.GetBytes(out, "output").ForEach(func(_, v gjson.Result) bool {
		if v.Get("type").String() == "web_search_call" {
			item = v
		}
		return true
	})
	if !item.Exists() {
		t.Fatalf("non-stream output missing web_search_call: %s", string(out))
	}
	if got := item.Get("status").String(); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got := item.Get("action.query").String(); got != "golang sjson" {
		t.Fatalf("action.query = %q, want golang sjson", got)
	}
	if got := item.Get("result.0.title").String(); got != "sjson docs" {
		t.Fatalf("result[0].title = %q", got)
	}
	if got := item.Get("result.0.encrypted_content").String(); got != "set json values fast" {
		t.Fatalf("result[0].encrypted_content = %q (must not be dropped)", got)
	}
	// page_age was null upstream -> must be omitted, not "null" string.
	if item.Get("result.0.page_age").Exists() {
		t.Fatalf("result[0].page_age should be omitted when upstream null: %s", item.Raw)
	}
}

// Regression: a normal (non-web_search) tool_use must still produce a function_call,
// never a web_search_call.
func TestConvertClaudeResponseNonStream_NormalToolUseUnaffected(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_t"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_1","type":"tool_use","name":"exec_command"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	var state any
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude", nil, nil, []byte(raw), &state)
	if gjson.GetBytes(out, "output.0.type").String() != "function_call" {
		t.Fatalf("normal tool_use must stay function_call: %s", string(out))
	}
}
