package openai

import (
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestRepairResponsesWebsocketToolCallsDropsOrphanToolSearchOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"tool_search_output","call_id":"call-1","status":"completed","execution":"client","tools":[]},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedToolSearchCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"tool_search_call","call_id":"call-1","status":"completed","execution":"client","arguments":{"query":"tools"}}`))

	raw := []byte(`{"input":[{"type":"tool_search_output","call_id":"call-1","status":"completed","execution":"client","tools":[]},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "tool_search_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted tool_search_call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "tool_search_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected tool_search_output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsServerToolSearchOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"tool_search_output","call_id":"call-1","status":"completed","execution":"server","tools":[]},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "tool_search_output" || input[0].Get("execution").String() != "server" {
		t.Fatalf("server tool_search_output should be preserved: %s", input[0].Raw)
	}
}

func TestRecordPendingToolCallIDsFromPayloadDropsSatisfiedToolSearchCall(t *testing.T) {
	pending := map[string]struct{}{}
	payload := []byte(`{"type":"response.completed","response":{"output":[{"type":"tool_search_call","call_id":"call-1","id":"tsc-1"},{"type":"tool_search_output","call_id":"call-1","id":"tso-1","status":"completed","execution":"client","tools":[]}]}}`)

	recordPendingToolCallIDsFromPayload(pending, payload)

	if len(pending) != 0 {
		t.Fatalf("pending tool call ids = %v, want empty", sortedStringSet(pending))
	}
}
