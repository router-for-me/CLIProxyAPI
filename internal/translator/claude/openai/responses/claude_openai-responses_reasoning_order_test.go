package responses

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertOpenAIResponsesRequestToClaude_KeepsLatestConsecutiveReasoning(t *testing.T) {
	firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
	secondRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")
	thirdRaw, thirdSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-third")

	raw := responsesRequestFromItems(
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prefix"}]}`,
		responsesReasoningItem(firstRaw, "first reasoning"),
		responsesReasoningItem(secondRaw, "second reasoning"),
		responsesReasoningItem(thirdRaw, "third reasoning"),
		responsesFunctionCallItem("call_latest", "latest_tool"),
		responsesFunctionCallOutputItem("call_latest", "done"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"text", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("text").String(); got != "prefix" {
		t.Fatalf("prefix text = %q, want prefix. Output: %s", got, out)
	}
	if got := content[1].Get("thinking").String(); got != "third reasoning" {
		t.Fatalf("thinking text = %q, want latest consecutive reasoning. Output: %s", got, out)
	}
	if got := content[1].Get("signature").String(); got != thirdSignature {
		t.Fatalf("thinking signature = %q, want latest signature %q. Output: %s", got, thirdSignature, out)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolCallsSeparateReasoningBlocks(t *testing.T) {
	firstRaw, firstSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
	secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")

	raw := responsesRequestFromItems(
		responsesReasoningItem(firstRaw, "first reasoning"),
		responsesFunctionCallItem("call_first", "first_tool"),
		responsesReasoningItem(secondRaw, "second reasoning"),
		responsesFunctionCallItem("call_second", "second_tool"),
		responsesFunctionCallOutputItem("call_first", "first result"),
		responsesFunctionCallOutputItem("call_second", "second result"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "tool_use", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("signature").String(); got != firstSignature {
		t.Fatalf("first thinking signature = %q, want %q", got, firstSignature)
	}
	if got := content[2].Get("signature").String(); got != secondSignature {
		t.Fatalf("second thinking signature = %q, want %q", got, secondSignature)
	}
	if got := content[1].Get("id").String(); got != "call_first" {
		t.Fatalf("first tool_use id = %q, want call_first", got)
	}
	if got := content[3].Get("id").String(); got != "call_second" {
		t.Fatalf("second tool_use id = %q, want call_second", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_NonThinkingBlocksSeparateReasoning(t *testing.T) {
	firstRaw, firstSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
	secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")
	const redactedData = "opaque-redacted-data"

	raw := responsesRequestFromItems(
		responsesReasoningItem(firstRaw, "first reasoning"),
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible separator"}]}`,
		responsesReasoningItem(secondRaw, "second reasoning"),
		`{"type":"reasoning","encrypted_content":"`+ClaudeResponsesRedactedThinkingPrefix+redactedData+`","summary":[]}`,
		responsesReasoningItem(firstRaw, "third reasoning"),
		responsesFunctionCallItem("call_separator", "separator_tool"),
		responsesFunctionCallOutputItem("call_separator", "done"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "text", "thinking", "redacted_thinking", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("signature").String(); got != firstSignature {
		t.Fatalf("first thinking signature = %q, want %q", got, firstSignature)
	}
	if got := content[2].Get("signature").String(); got != secondSignature {
		t.Fatalf("second thinking signature = %q, want %q", got, secondSignature)
	}
	if got := content[3].Get("data").String(); got != redactedData {
		t.Fatalf("redacted data = %q, want %q", got, redactedData)
	}
	if got := content[4].Get("signature").String(); got != firstSignature {
		t.Fatalf("third thinking signature = %q, want %q", got, firstSignature)
	}
	if got := content[5].Get("id").String(); got != "call_separator" {
		t.Fatalf("tool_use id = %q, want call_separator", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_KeepsSameResponseThinkingBlocks(t *testing.T) {
	reasoningRaw, reasoningSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-reasoning")
	updateRaw, updateSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-update")

	// One Claude response returned a reasoning block, a progress-update block,
	// and a tool call; the response translator named them rs_<id>_<index>.
	raw := responsesRequestFromItems(
		responsesReasoningItemWithID("rs_msg_01Same_0", reasoningRaw, "primary reasoning"),
		responsesReasoningItemWithID("rs_msg_01Same_1", updateRaw, "progress update"),
		responsesFunctionCallItem("call_same", "same_tool"),
		responsesFunctionCallOutputItem("call_same", "done"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("thinking").String(); got != "primary reasoning" {
		t.Fatalf("first thinking text = %q, want primary reasoning", got)
	}
	if got := content[0].Get("signature").String(); got != reasoningSignature {
		t.Fatalf("first thinking signature = %q, want reasoning signature %q", got, reasoningSignature)
	}
	if got := content[1].Get("thinking").String(); got != "progress update" {
		t.Fatalf("second thinking text = %q, want progress update", got)
	}
	if got := content[1].Get("signature").String(); got != updateSignature {
		t.Fatalf("second thinking signature = %q, want progress-update signature %q", got, updateSignature)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_CollapsesThinkingFromDifferentResponses(t *testing.T) {
	firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-first")
	updateRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-update")
	laterRaw, laterSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-later")

	// A later response's thinking must not stay adjacent to an earlier
	// response's, so the earlier trailing thinking run is replaced as a whole.
	raw := responsesRequestFromItems(
		responsesReasoningItemWithID("rs_msg_01First_0", firstRaw, "first reasoning"),
		responsesReasoningItemWithID("rs_msg_01First_1", updateRaw, "first update"),
		responsesReasoningItemWithID("rs_msg_01Later_0", laterRaw, "later reasoning"),
		responsesFunctionCallItem("call_later", "later_tool"),
		responsesFunctionCallOutputItem("call_later", "done"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("signature").String(); got != laterSignature {
		t.Fatalf("thinking signature = %q, want latest response signature %q. Output: %s", got, laterSignature, out)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_CollapsesDuplicateOrInvalidSameResponseThinking(t *testing.T) {
	firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-first")
	secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-second")

	cases := []struct {
		name     string
		firstID  string
		secondID string
	}{
		{name: "duplicate block index", firstID: "rs_msg_01Dup_0", secondID: "rs_msg_01Dup_0"},
		{name: "descending block index", firstID: "rs_msg_01Order_2", secondID: "rs_msg_01Order_1"},
		{name: "negative block index", firstID: "rs_msg_01Neg_-1", secondID: "rs_msg_01Neg_0"},
		{name: "empty response id", firstID: "rs__0", secondID: "rs__1"},
		{name: "signed positive suffix", firstID: "rs_msg_01Plus_+0", secondID: "rs_msg_01Plus_+1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := responsesRequestFromItems(
				responsesReasoningItemWithID(tc.firstID, firstRaw, "first"),
				responsesReasoningItemWithID(tc.secondID, secondRaw, "second"),
				responsesFunctionCallItem("call_edge", "edge_tool"),
				responsesFunctionCallOutputItem("call_edge", "done"),
			)

			out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
			content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
			wantTypes := []string{"thinking", "tool_use"}
			if len(content) != len(wantTypes) {
				t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
			}
			for index, wantType := range wantTypes {
				if got := content[index].Get("type").String(); got != wantType {
					t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
				}
			}
			if got := content[0].Get("signature").String(); got != secondSignature {
				t.Fatalf("thinking signature = %q, want %q. Output: %s", got, secondSignature, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_RoundTripsSameResponseAdjacentThinkingBlocks(t *testing.T) {
	reasoningRaw, reasoningSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-reasoning")
	updateRaw, updateSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-update")

	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_01RoundTrip","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Inspect the repository."}}`),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":%q}}`, reasoningRaw)),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"Running grep now."}}`),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":%q}}`, updateRaw)),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01RT","name":"exec_command","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	assertRoundTrip := func(t *testing.T, outputArray ojson.Result) {
		t.Helper()
		var items []string
		outputArray.ForEach(func(_, item ojson.Result) bool {
			items = append(items, item.Raw)
			return true
		})
		items = append(items, responsesFunctionCallOutputItem("toolu_01RT", "ok"))

		replayReq := responsesRequestFromItems(items...)
		claudeReq, err := ConvertOpenAIResponsesRequestToClaude("claude-opus-5-5", replayReq, false)
		if err != nil {
			t.Fatalf("ConvertOpenAIResponsesRequestToClaude error = %v", err)
		}
		content := ojson.GetBytesNoCopy(claudeReq, "messages.0.content").Array()
		wantTypes := []string{"thinking", "thinking", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), claudeReq)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, claudeReq)
			}
		}
		if got := content[0].Get("thinking").String(); got != "Inspect the repository." {
			t.Fatalf("first thinking text = %q, want Inspect the repository.", got)
		}
		if got := content[0].Get("signature").String(); got != reasoningSignature {
			t.Fatalf("first thinking signature = %q, want %q", got, reasoningSignature)
		}
		if got := content[1].Get("thinking").String(); got != "Running grep now." {
			t.Fatalf("second thinking text = %q, want Running grep now.", got)
		}
		if got := content[1].Get("signature").String(); got != updateSignature {
			t.Fatalf("second thinking signature = %q, want %q", got, updateSignature)
		}
	}

	t.Run("stream", func(t *testing.T) {
		outputs := translateClaudeResponsesStreamThroughRegistry(chunks)
		var completed ojson.Result
		for _, output := range outputs {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				completed = data
			}
		}
		if !completed.Exists() {
			t.Fatalf("missing response.completed event")
		}
		assertRoundTrip(t, completed.Get("response.output"))
	})

	t.Run("non-stream", func(t *testing.T) {
		rawResp := ConvertClaudeResponseToOpenAIResponsesNonStream(
			context.Background(),
			"claude-opus-5-5",
			nil,
			nil,
			bytes.Join(chunks, []byte("\n")),
			nil,
		)
		assertRoundTrip(t, ojson.GetBytesNoCopy(rawResp, "output"))
	})
}

func TestParseClaudeReasoningItemID(t *testing.T) {
	cases := []struct {
		itemID     string
		wantRespID string
		wantIndex  int
		wantOK     bool
	}{
		{itemID: "rs_msg_011ChF1ocEToPEeFhKVmgRWx_0", wantRespID: "msg_011ChF1ocEToPEeFhKVmgRWx", wantIndex: 0, wantOK: true},
		{itemID: "rs_resp_1_12", wantRespID: "resp_1", wantIndex: 12, wantOK: true},
		{itemID: "rs_msg", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "rs__1", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "rs_   _1", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "rs_msg_", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "rs_msg_-1", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "rs_msg_+1", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "rs_msg_x", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "msg_1_0", wantRespID: "", wantIndex: -1, wantOK: false},
		{itemID: "", wantRespID: "", wantIndex: -1, wantOK: false},
	}
	for _, tc := range cases {
		gotRespID, gotIndex, gotOK := parseClaudeReasoningItemID(tc.itemID)
		if gotRespID != tc.wantRespID || gotIndex != tc.wantIndex || gotOK != tc.wantOK {
			t.Errorf("parseClaudeReasoningItemID(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.itemID, gotRespID, gotIndex, gotOK, tc.wantRespID, tc.wantIndex, tc.wantOK)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_CollapsesReplayedSameResponseThinkingPair(t *testing.T) {
	firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-first")
	firstUpdateRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-first-update")
	secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-second")
	secondUpdateRaw, secondUpdateSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-second-update")

	raw := responsesRequestFromItems(
		responsesReasoningItemWithID("rs_msg_01Same_0", firstRaw, "first reasoning"),
		responsesReasoningItemWithID("rs_msg_01Same_1", firstUpdateRaw, "first update"),
		responsesReasoningItemWithID("rs_msg_01Same_0", secondRaw, "replayed reasoning"),
		responsesReasoningItemWithID("rs_msg_01Same_1", secondUpdateRaw, "replayed update"),
		responsesFunctionCallItem("call_replay", "replay_tool"),
		responsesFunctionCallOutputItem("call_replay", "done"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("signature").String(); got != secondSignature {
		t.Fatalf("first thinking signature = %q, want %q", got, secondSignature)
	}
	if got := content[1].Get("signature").String(); got != secondUpdateSignature {
		t.Fatalf("second thinking signature = %q, want %q", got, secondUpdateSignature)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DuplicateTrailingSameResponseThinkingPreservesPrefix(t *testing.T) {
	reasoningRaw, reasoningSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-reasoning")
	firstUpdateRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-update-first")
	secondUpdateRaw, secondUpdateSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-5-update-second")

	raw := responsesRequestFromItems(
		responsesReasoningItemWithID("rs_msg_01Same_0", reasoningRaw, "primary reasoning"),
		responsesReasoningItemWithID("rs_msg_01Same_1", firstUpdateRaw, "first progress update"),
		responsesReasoningItemWithID("rs_msg_01Same_1", secondUpdateRaw, "latest progress update"),
		responsesFunctionCallItem("call_dup_tail", "dup_tail_tool"),
		responsesFunctionCallOutputItem("call_dup_tail", "done"),
	)

	out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := ojson.GetBytesNoCopy(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("thinking").String(); got != "primary reasoning" {
		t.Fatalf("first thinking text = %q, want primary reasoning", got)
	}
	if got := content[0].Get("signature").String(); got != reasoningSignature {
		t.Fatalf("first thinking signature = %q, want %q", got, reasoningSignature)
	}
	if got := content[1].Get("thinking").String(); got != "latest progress update" {
		t.Fatalf("second thinking text = %q, want latest progress update", got)
	}
	if got := content[1].Get("signature").String(); got != secondUpdateSignature {
		t.Fatalf("second thinking signature = %q, want %q", got, secondUpdateSignature)
	}
}

func responsesReasoningItemWithID(id, signature, text string) string {
	return fmt.Sprintf(`{"type":"reasoning","id":%q,"encrypted_content":%q,"summary":[{"type":"summary_text","text":%q}]}`, id, signature, text)
}

func responsesReasoningItem(signature, text string) string {
	return fmt.Sprintf(`{"type":"reasoning","encrypted_content":%q,"summary":[{"type":"summary_text","text":%q}]}`, signature, text)
}

func responsesFunctionCallItem(callID, name string) string {
	return fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":"{}"}`, callID, name)
}

func responsesFunctionCallOutputItem(callID, output string) string {
	return fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":%q}`, callID, output)
}

func responsesWebSearchCallItem(id, query string) string {
	return fmt.Sprintf(`{"type":"web_search_call","id":%q,"status":"completed","action":{"type":"search","query":%q}}`, id, query)
}

func TestConvertOpenAIResponsesRequestToClaude_WebSearchSeparatesToolUseWithThinking(t *testing.T) {
	rawSig, signature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-test")

	t.Run("function_call then web_search_call", func(t *testing.T) {
		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			responsesReasoningItem(rawSig, "I should search and then run a command."),
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Searching, then running."}]}`,
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := ojson.GetBytesNoCopy(out, "messages.1.content").Array()
		wantTypes := []string{"thinking", "text", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
		if got := content[0].Get("signature").String(); got != signature {
			t.Fatalf("first thinking signature = %q, want %q", got, signature)
		}
		if got := content[4].Get("signature").String(); got != signature {
			t.Fatalf("second thinking signature = %q, want %q", got, signature)
		}
		if got := content[4].Get("thinking").String(); got != "I should search and then run a command." {
			t.Fatalf("second thinking text = %q, want original thinking text", got)
		}
		if got := content[5].Get("id").String(); got != "call_00_abc" {
			t.Fatalf("tool_use id = %q, want call_00_abc", got)
		}
	})

	t.Run("web_search_call then function_call", func(t *testing.T) {
		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			responsesReasoningItem(rawSig, "I should search and then run a command."),
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Searching, then running."}]}`,
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := ojson.GetBytesNoCopy(out, "messages.1.content").Array()
		wantTypes := []string{"thinking", "text", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
		if got := content[4].Get("signature").String(); got != signature {
			t.Fatalf("second thinking signature = %q, want %q", got, signature)
		}
		if got := content[5].Get("id").String(); got != "call_00_abc" {
			t.Fatalf("tool_use id = %q, want call_00_abc", got)
		}
	})

	t.Run("no thinking item present", func(t *testing.T) {
		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Searching, then running."}]}`,
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := ojson.GetBytesNoCopy(out, "messages.1.content").Array()
		wantTypes := []string{"text", "server_tool_use", "web_search_tool_result", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
	})

	t.Run("selects latest thinking block when multiple exist", func(t *testing.T) {
		firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
		secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")

		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			responsesReasoningItem(firstRaw, "first reasoning"),
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"thought once"}]}`,
			responsesReasoningItem(secondRaw, "second reasoning"),
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out, _ := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := ojson.GetBytesNoCopy(out, "messages.1.content").Array()
		wantTypes := []string{"thinking", "text", "thinking", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
		if got := content[5].Get("signature").String(); got != secondSignature {
			t.Fatalf("separated thinking signature = %q, want latest %q", got, secondSignature)
		}
		if got := content[5].Get("thinking").String(); got != "second reasoning" {
			t.Fatalf("separated thinking text = %q, want latest 'second reasoning'", got)
		}
	})
}
