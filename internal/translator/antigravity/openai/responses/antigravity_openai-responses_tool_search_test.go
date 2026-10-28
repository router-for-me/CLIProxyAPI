package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToAntigravity_ExposesClientToolSearch(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.8-flash-high",
		"tools": [
			{"type": "tool_search", "execution": "client", "description": "Search deferred tools.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}
		],
		"input": [
			{"type": "message", "role": "user", "content": "find tools"},
			{"type": "tool_search_call", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "fixture"}},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_1", "status": "completed", "tools": [
				{"type": "namespace", "name": "fixture_tools", "tools": [{"type": "function", "name": "ping", "defer_loading": true, "parameters": {"type": "object", "properties": {}}}]}
			]}
		]
	}`)
	out, err := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.8-flash-high", raw, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root := gjson.ParseBytes(out)

	names := make([]string, 0)
	root.Get("request.tools.0.functionDeclarations").ForEach(func(_, declaration gjson.Result) bool {
		names = append(names, declaration.Get("name").String())
		return true
	})
	if len(names) != 2 || names[0] != "tool_search" || names[1] != "fixture_tools__ping" {
		t.Fatalf("functionDeclarations = %v, want [tool_search fixture_tools__ping]; raw: %s", names, out)
	}
	contents := root.Get("request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d: %s", len(contents), root.Get("request.contents").Raw)
	}
	if got := contents[1].Get("parts.0.functionCall.name").String(); got != "tool_search" {
		t.Fatalf("replayed search call name = %q, want tool_search", got)
	}
	if got := contents[2].Get("parts.0.functionResponse.name").String(); got != "tool_search" {
		t.Fatalf("replayed search output name = %q, want tool_search", got)
	}
}

func TestConvertAntigravityResponseToOpenAIResponses_ToolSearchCall(t *testing.T) {
	originalRequest := []byte(`{"model":"gemini-3.8-flash-high","tools":[{"type":"tool_search","execution":"client"}],"input":"find tools"}`)
	chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"fixture"}}}]},"finishReason":"STOP"}],"responseId":"resp_ag_tool_search"}}`)

	var param any
	var added, done, completed gjson.Result
	for _, frame := range [][]byte{chunk, []byte("data: [DONE]")} {
		for _, output := range ConvertAntigravityResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", originalRequest, nil, frame, &param) {
			payload := string(output)
			if idx := strings.Index(payload, "data:"); idx >= 0 {
				data := gjson.Parse(payload[idx+len("data:"):])
				switch data.Get("type").String() {
				case "response.output_item.added":
					added = data
				case "response.output_item.done":
					done = data
				case "response.completed":
					completed = data
				}
			}
		}
	}
	if !added.Exists() || !done.Exists() || !completed.Exists() {
		t.Fatalf("missing tool_search_call lifecycle events: added=%v done=%v completed=%v", added.Exists(), done.Exists(), completed.Exists())
	}
	if got := done.Get("item.type").String(); got != "tool_search_call" {
		t.Fatalf("item type = %q, want tool_search_call; raw: %s", got, done.Raw)
	}
	if got := done.Get("item.arguments.query").String(); got != "fixture" {
		t.Fatalf("item arguments = %s", done.Get("item.arguments").Raw)
	}
	if got := done.Get("item.execution").String(); got != "client" {
		t.Fatalf("item execution = %q", got)
	}
	callID := done.Get("item.call_id").String()
	if callID == "" || added.Get("item.call_id").String() != callID || completed.Get("response.output.0.call_id").String() != callID {
		t.Fatalf("call_id must be non-empty and stable across events")
	}
	wantItemID := "tsc_" + callID
	for _, test := range []struct {
		label string
		item  gjson.Result
	}{
		{label: "added", item: added.Get("item")},
		{label: "done", item: done.Get("item")},
		{label: "completed", item: completed.Get("response.output.0")},
	} {
		if got := test.item.Get("id").String(); got != wantItemID {
			t.Fatalf("%s id = %q, want %q; raw: %s", test.label, got, wantItemID, test.item.Raw)
		}
	}

	nonStreamRaw := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"fixture"}}}]},"finishReason":"STOP"}],"responseId":"resp_ag_tool_search_nonstream"}}`)
	nonStreamOut := ConvertAntigravityResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.8-flash-high", originalRequest, nil, nonStreamRaw, nil)
	nonStreamItem := gjson.GetBytes(nonStreamOut, "output.0")
	if got := nonStreamItem.Get("type").String(); got != "tool_search_call" {
		t.Fatalf("non-stream item type = %q, want tool_search_call; raw: %s", got, nonStreamOut)
	}
	if nonStreamCallID := nonStreamItem.Get("call_id").String(); nonStreamCallID == "" {
		t.Fatalf("non-stream call_id must be non-empty: %s", nonStreamItem.Raw)
	} else if got := nonStreamItem.Get("id").String(); got != "tsc_"+nonStreamCallID {
		t.Fatalf("non-stream id = %q, want %q; raw: %s", got, "tsc_"+nonStreamCallID, nonStreamItem.Raw)
	}
}
