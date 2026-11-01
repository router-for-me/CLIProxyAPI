package openai

import (
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// TestMergeResponsesWebsocketInputDropsReplayedContentTokenCount guards issue #7146:
// items replayed from cached upstream output must not carry the output-only
// internal_chat_message_metadata_passthrough.content_token_count field into the
// next upstream request, while client-sent items stay untouched.
func TestMergeResponsesWebsocketInputDropsReplayedContentTokenCount(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant","phase":"commentary","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1","create_time":1793474327.5,"content_token_count":64},"content":[{"type":"output_text","text":"hi"}]},
		{"type":"message","id":"assistant-2","role":"assistant","internal_chat_message_metadata_passthrough":{ "content_token_count":3 },"content":[{"type":"output_text","text":"only-count"}]},
		{"type":"message","id":"assistant-3","role":"assistant","internal_chat_message_metadata_passthrough":{"turn_id":"turn-3"},"content":[{"type":"output_text","text":"no-count"}]},
		{"type":"message","id":"assistant-4","role":"assistant","\u0069nternal_chat_message_metadata_passthrough":{"content\u005ftoken_count":7,"turn_id":"turn-escaped"},"content":[{"type":"output_text","text":"escaped"}]},
		{"type":"message","id":"assistant-5","role":"assistant","internal_chat_message_metadata_passthrough":{"content_token_count":8,"turn_id":"turn-first","content_token_count":9},"content":[{"type":"output_text","text":"dup child"}]},
		{"type":"message","id":"assistant-6","role":"assistant","internal_chat_message_metadata_passthrough":{"content_token_count":10},"internal_chat_message_metadata_passthrough":{"content_token_count":11,"turn_id":"turn-second"},"content":[{"type":"output_text","text":"dup parent"}]},
		{"type":"message","id":"assistant-7","role":"assistant","internal_chat_message_metadata_passthrough":null,"content":[{"type":"output_text","text":"null passthrough"}]},
		{"type":"message","id":"assistant-8","role":"assistant","internal_chat_message_metadata_passthrough":{"content_token_count":12},"tag":"first","tag":"second","content":[{"type":"output_text","text":"dup non-passthrough"}]},
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1","content_token_count":5}}
	]`)
	// The client-sent append item carries the field as well: client state is never rewritten.
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1","internal_chat_message_metadata_passthrough":{"content_token_count":99}}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 11 {
		t.Fatalf("merged input len = %d, want 11: %s", len(input), normalized)
	}
	if input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected previous-request item: %s", input[0].Raw)
	}

	first := input[1]
	if first.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
		t.Fatalf("replayed assistant message must not carry content_token_count: %s", first.Raw)
	}
	if first.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-1" ||
		!first.Get("internal_chat_message_metadata_passthrough.create_time").Exists() {
		t.Fatalf("replayed assistant message must keep other passthrough keys: %s", first.Raw)
	}

	second := input[2]
	if second.Get("internal_chat_message_metadata_passthrough").Exists() {
		t.Fatalf("passthrough object emptied by removal must be dropped: %s", second.Raw)
	}

	third := input[3]
	if third.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-3" {
		t.Fatalf("passthrough without content_token_count must stay untouched: %s", third.Raw)
	}

	escaped := input[4]
	if escaped.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
		t.Fatalf("escaped passthrough key must not bypass removal: %s", escaped.Raw)
	}
	if escaped.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-escaped" {
		t.Fatalf("escaped passthrough must keep other keys: %s", escaped.Raw)
	}

	duplicateChild := input[5]
	if duplicateChild.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
		t.Fatalf("duplicate content_token_count members must all be removed: %s", duplicateChild.Raw)
	}
	if duplicateChild.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-first" {
		t.Fatalf("duplicate-child passthrough must keep other keys: %s", duplicateChild.Raw)
	}

	duplicateParent := input[6]
	if duplicateParent.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
		t.Fatalf("duplicate passthrough members must not hide content_token_count: %s", duplicateParent.Raw)
	}
	if duplicateParent.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-second" {
		t.Fatalf("duplicate-parent passthrough must keep other keys: %s", duplicateParent.Raw)
	}

	nullPassthrough := input[7]
	if !nullPassthrough.Get("internal_chat_message_metadata_passthrough").Exists() ||
		nullPassthrough.Get("internal_chat_message_metadata_passthrough").Type != gjson.Null {
		t.Fatalf("null passthrough must stay untouched: %s", nullPassthrough.Raw)
	}

	duplicateOther := input[8]
	if duplicateOther.Get("internal_chat_message_metadata_passthrough").Exists() {
		t.Fatalf("emptied passthrough must be dropped beside duplicate members: %s", duplicateOther.Raw)
	}
	duplicateTags := make([]string, 0, 2)
	duplicateOther.ForEach(func(key, value gjson.Result) bool {
		if key.String() == "tag" {
			duplicateTags = append(duplicateTags, value.String())
		}
		return true
	})
	if len(duplicateTags) != 2 || duplicateTags[0] != "first" || duplicateTags[1] != "second" {
		t.Fatalf("duplicate non-passthrough members must be preserved in order: %s", duplicateOther.Raw)
	}

	fourth := input[9]
	if fourth.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
		t.Fatalf("replayed tool call must not carry content_token_count: %s", fourth.Raw)
	}
	if fourth.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-1" ||
		fourth.Get("name").String() != "lookup" {
		t.Fatalf("replayed tool call must keep other fields: %s", fourth.Raw)
	}

	last := input[10]
	if !last.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
		t.Fatalf("client-sent input must not be rewritten: %s", last.Raw)
	}
}

// TestResponsesWebsocketToolCacheSanitizesTransactionalLifecycle locks the
// recordResponse -> commit -> default cache -> repair path of issue #7146,
// including a large argument payload and item-level payload events.
func TestResponsesWebsocketToolCacheSanitizesTransactionalLifecycle(t *testing.T) {
	sessionKey := "session-7146-transaction"
	defer defaultWebsocketToolOutputCache.deleteSession(sessionKey)
	defer defaultWebsocketToolCallCache.deleteSession(sessionKey)

	largeArgument := strings.Repeat("x", 64<<10)
	turn := newResponsesWebsocketToolCacheTurn(sessionKey)
	turn.recordResponse([]byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"` + largeArgument + `","\u0069nternal_chat_message_metadata_passthrough":{"turn_id":"tx","content_token_count":9}}}`))
	turn.recordResponse([]byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","id":"fc-2","call_id":"call-2","name":"grep","arguments":"{}","internal_chat_message_metadata_passthrough":{"content_token_count":2}}]}}`))
	turn.commit()

	repaired := repairResponsesWebsocketToolCallsWithoutRecording(sessionKey, []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"function_call_output","call_id":"call-2","output":"ok"}]}`))
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 4 {
		t.Fatalf("repaired input len = %d, want 4: %s", len(input), repaired)
	}
	if injected := input[0]; injected.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() || injected.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "tx" || len(injected.Get("arguments").String()) != len(largeArgument) {
		t.Fatalf("transactional upstream call was not sanitized intact: %s", injected.Raw)
	}
	if injected := input[2]; injected.Get("internal_chat_message_metadata_passthrough").Exists() {
		t.Fatalf("emptied transactional passthrough must be dropped: %s", injected.Raw)
	}
}

// TestResponsesWebsocketToolCachePreservesClientItems keeps the client-data
// boundary of issue #7146: only tool calls recorded from upstream response
// payloads are sanitized, while client tool results round-trip unchanged
// through the repair cache.
func TestResponsesWebsocketToolCachePreservesClientItems(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-7146-client"

	warmPayload := []byte(`{"input":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","id":"fco-1","output":"ok","internal_chat_message_metadata_passthrough":{"content_token_count":42,"turn_id":"client"}}]}`)
	repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, warmPayload)

	raw := []byte(`{"input":[{"type":"function_call","id":"fc-next","call_id":"call-1","name":"lookup","arguments":"{}"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[1].Get("internal_chat_message_metadata_passthrough.content_token_count").Int() != 42 ||
		input[1].Get("internal_chat_message_metadata_passthrough.turn_id").String() != "client" {
		t.Fatalf("client tool output must not be sanitized through the cache: %s", input[1].Raw)
	}
}

// TestResponsesWebsocketToolCachePreservesTransactionalClientItems verifies
// the same client boundary through prepareResponsesWebsocketFallbackTurn and
// commit into the default caches.
func TestResponsesWebsocketToolCachePreservesTransactionalClientItems(t *testing.T) {
	const sessionKey = "session-7146-transaction-client"
	defer defaultWebsocketToolOutputCache.deleteSession(sessionKey)
	defer defaultWebsocketToolCallCache.deleteSession(sessionKey)

	_, turn := prepareResponsesWebsocketFallbackTurn(sessionKey, []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","id":"fco-1","output":"ok","internal_chat_message_metadata_passthrough":{"content_token_count":42,"turn_id":"client"}}]}`))
	turn.commit()

	repaired := repairResponsesWebsocketToolCallsWithoutRecording(sessionKey, []byte(`{"input":[{"type":"function_call","id":"fc-next","call_id":"call-1","name":"lookup","arguments":"{}"}]}`))
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 || input[1].Get("internal_chat_message_metadata_passthrough.content_token_count").Int() != 42 || input[1].Get("internal_chat_message_metadata_passthrough.turn_id").String() != "client" {
		t.Fatalf("transactional client output must not be sanitized: %s", repaired)
	}
}

// TestResponsesWebsocketReplaySanitizedItemLeavesInvalidJSONUnchanged ensures
// malformed upstream items are never truncated by the cleanup.
func TestResponsesWebsocketReplaySanitizedItemLeavesInvalidJSONUnchanged(t *testing.T) {
	invalidInputs := []string{
		`{"internal_chat_message_metadata_passthrough":{"content_token_count":1},"content":`,
		`{"internal_chat_message_metadata_passthrough":{"content_token_count":1},"content":"unterminated`,
		`{"internal_chat_message_metadata_passthrough":{"content_token_count":1}} trailing`,
		`{"internal_chat_message_metadata_passthrough":{"content_token_count":1},"content":}`,
	}
	for _, invalid := range invalidInputs {
		cleaned := responsesWebsocketReplaySanitizedItem(invalid)
		if cleaned != invalid {
			t.Fatalf("invalid item mutated: in=%q out=%q", invalid, cleaned)
		}
		if cleanedBytes := responsesWebsocketReplaySanitizedBytes([]byte(invalid)); string(cleanedBytes) != invalid {
			t.Fatalf("invalid bytes mutated: in=%q out=%q", invalid, cleanedBytes)
		}
	}
}

// TestResponsesWebsocketToolCacheStripsContentTokenCount covers the second replay
// carrier of issue #7146: tool calls recorded from upstream payloads into the
// repair cache must be sanitized before being re-inserted into a later request.
func TestResponsesWebsocketToolCacheStripsContentTokenCount(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-7146"

	recordResponsesWebsocketToolCallsFromPayloadWithCache(callCache, sessionKey, []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1","content_token_count":9}},{"type":"function_call","id":"fc-2","call_id":"call-2","name":"grep","arguments":"{}","internal_chat_message_metadata_passthrough":{"content_token_count":2}}]}}`))

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"function_call_output","call_id":"call-2","output":"ok"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 4 {
		t.Fatalf("repaired input len = %d, want 4: %s", len(input), repaired)
	}
	for index, item := range input {
		if item.Get("internal_chat_message_metadata_passthrough.content_token_count").Exists() {
			t.Fatalf("repaired input[%d] must not carry content_token_count: %s", index, repaired)
		}
	}
	if injected := input[0]; injected.Get("type").String() != "function_call" ||
		injected.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn-1" ||
		injected.Get("name").String() != "lookup" {
		t.Fatalf("inserted cached call must keep other fields: %s", injected.Raw)
	}
	if injected := input[2]; injected.Get("type").String() != "function_call" ||
		injected.Get("internal_chat_message_metadata_passthrough").Exists() {
		t.Fatalf("passthrough object emptied by removal must be dropped from inserted call: %s", injected.Raw)
	}
}
