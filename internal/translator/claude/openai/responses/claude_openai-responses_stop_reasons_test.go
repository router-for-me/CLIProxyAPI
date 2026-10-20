package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// pausedSearchChunks renders one Claude response: signed thinking, an optional
// finished search, text, and a search left without a result by pause_turn.
func pausedSearchChunks(t *testing.T, messageID, signature string, finishedID, pausedID, pausedQuery string) [][]byte {
	t.Helper()
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"` + messageID + `","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"searching"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
	}
	index := 1
	if finishedID != "" {
		chunks = append(chunks,
			[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"web_search","input":{}}}`, index, finishedID)),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"done query\"}"}}`, index)),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, index)),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":%q,"content":[{"type":"web_search_result","url":"https://example.com/done","title":"Done","encrypted_content":"ENC_DONE"}]}}`, index+1, finishedID)),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, index+1)),
		)
		index += 2
	}
	chunks = append(chunks,
		[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index)),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":"Partial answer."}}`, index)),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, index)),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"web_search","input":{}}}`, index+1, pausedID)),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"%s\"}"}}`, index+1, pausedQuery)),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, index+1)),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":5}}`),
		[]byte(`data: {"type":"message_stop"}`),
	)
	return chunks
}

// continuationChunks renders the response that finishes a paused search.
func continuationChunks(messageID, pausedID string, stopReason string) [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"` + messageID + `","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":%q,"content":[{"type":"web_search_result","url":"https://example.com/p","title":"P","encrypted_content":"ENC_PAUSED"}]}}`, pausedID)),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Final answer."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stopReason)),
		[]byte(`data: {"type":"message_stop"}`),
	}
}

// streamTerminal returns the terminal event name and its response object.
func streamTerminal(t *testing.T, chunks [][]byte) (string, gjson.Result) {
	t.Helper()
	var name string
	var response gjson.Result
	for _, output := range translateClaudeResponsesStreamThroughRegistry(chunks) {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.completed", "response.incomplete", "response.failed":
			name, response = event, data.Get("response")
		}
	}
	if name == "" {
		t.Fatal("no terminal event")
	}
	return name, response
}

// codexHistoryRequest builds the next Responses request exactly as a client
// would: the user prompt followed by the output items it received, without
// re-adding anything the translator dropped.
func codexHistoryRequest(t *testing.T, responses ...gjson.Result) []byte {
	t.Helper()
	request := []byte(`{"model":"claude-test","tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"research"}]}]}`)
	for _, response := range responses {
		for _, item := range response.Get("output").Array() {
			// Codex does not keep web_search_call results; the proxy cache restores
			// them later, so the history carries none.
			raw := []byte(item.Raw)
			if item.Get("type").String() == "web_search_call" {
				raw, _ = sjson.DeleteBytes(raw, "results")
			}
			request, _ = sjson.SetRawBytes(request, "input.-1", raw)
		}
	}
	return request
}

func claudeAssistantBlocks(t *testing.T, claudeRequest []byte) []gjson.Result {
	t.Helper()
	var blocks []gjson.Result
	for _, message := range gjson.GetBytes(claudeRequest, "messages").Array() {
		if message.Get("role").String() == "assistant" {
			blocks = append(blocks, message.Get("content").Array()...)
		}
	}
	return blocks
}

func blockTypes(blocks []gjson.Result) string {
	types := make([]string, 0, len(blocks))
	for _, block := range blocks {
		types = append(types, block.Get("type").String())
	}
	return strings.Join(types, ",")
}

func TestPauseTurnRoundTripSendsPausedSearchBackWithoutResult(t *testing.T) {
	signature := mustTestSignature(t)
	event, response := streamTerminal(t, pausedSearchChunks(t, "msg_p1", signature, "srvtoolu_done", "srvtoolu_paused", "paused query"))
	if event != "response.completed" || response.Get("end_turn").Raw != "false" {
		t.Fatalf("terminal = %s end_turn=%s", event, response.Get("end_turn").Raw)
	}
	var pausedItem gjson.Result
	for _, item := range response.Get("output").Array() {
		if item.Get("id").String() == responsesWebSearchCallID("srvtoolu_paused") {
			pausedItem = item
		}
	}
	if pausedItem.Get("status").String() != "in_progress" || pausedItem.Get("results").Exists() {
		t.Fatalf("paused item = %s", pausedItem.Raw)
	}

	claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", codexHistoryRequest(t, response), true)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	messages := gjson.GetBytes(claudeRequest, "messages").Array()
	last := messages[len(messages)-1]
	if last.Get("role").String() != "assistant" {
		t.Fatalf("last message role = %q: %s", last.Get("role").String(), claudeRequest)
	}
	blocks := claudeAssistantBlocks(t, claudeRequest)
	if got, want := blockTypes(blocks), "thinking,server_tool_use,web_search_tool_result,text,server_tool_use"; got != want {
		t.Fatalf("assistant blocks = %s, want %s: %s", got, want, claudeRequest)
	}
	if got := blocks[0].Get("signature").String(); got == "" {
		t.Fatalf("thinking signature lost: %s", blocks[0].Raw)
	}
	if blocks[1].Get("id").String() != "srvtoolu_done" || blocks[2].Get("tool_use_id").String() != "srvtoolu_done" {
		t.Fatalf("finished search pair changed: %s %s", blocks[1].Raw, blocks[2].Raw)
	}
	paused := blocks[4]
	if paused.Get("id").String() != "srvtoolu_paused" || paused.Get("input.query").String() != "paused query" {
		t.Fatalf("paused search = %s", paused.Raw)
	}
}

func TestPauseTurnContinuationEmitsSinglePairUnderPausedID(t *testing.T) {
	signature := mustTestSignature(t)
	_, paused := streamTerminal(t, pausedSearchChunks(t, "msg_p1", signature, "", "srvtoolu_paused", "paused query"))
	event, finished := streamTerminal(t, continuationChunks("msg_p2", "srvtoolu_paused", "end_turn"))
	if event != "response.completed" || finished.Get("end_turn").Exists() {
		t.Fatalf("continuation terminal = %s end_turn=%s", event, finished.Get("end_turn").Raw)
	}

	claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", codexHistoryRequest(t, paused, finished), true)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	blocks := claudeAssistantBlocks(t, claudeRequest)
	uses := 0
	for i, block := range blocks {
		if block.Get("type").String() == "server_tool_use" {
			uses++
			if block.Get("id").String() != "srvtoolu_paused" || block.Get("input.query").String() != "paused query" {
				t.Fatalf("server_tool_use = %s", block.Raw)
			}
			if i+1 >= len(blocks) || blocks[i+1].Get("type").String() != "web_search_tool_result" || blocks[i+1].Get("tool_use_id").String() != "srvtoolu_paused" {
				t.Fatalf("paused search lost its result pairing: %s", claudeRequest)
			}
		}
	}
	if uses != 1 {
		t.Fatalf("server_tool_use count = %d, want 1: %s", uses, claudeRequest)
	}
}

func TestPauseTurnTwiceKeepsOnlyLatestSearchUnanswered(t *testing.T) {
	signature := mustTestSignature(t)
	_, first := streamTerminal(t, pausedSearchChunks(t, "msg_p1", signature, "", "srvtoolu_a", "query a"))
	// The continuation answers search A and pauses again on search B.
	second := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_p2","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_a","content":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"ENC_A"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_b","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"query b\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":5}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	event, secondResponse := streamTerminal(t, second)
	if event != "response.completed" || secondResponse.Get("end_turn").Raw != "false" {
		t.Fatalf("second pause terminal = %s end_turn=%s", event, secondResponse.Get("end_turn").Raw)
	}
	claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", codexHistoryRequest(t, first, secondResponse), true)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	blocks := claudeAssistantBlocks(t, claudeRequest)
	got := blockTypes(blocks)
	if want := "thinking,text,server_tool_use,web_search_tool_result,server_tool_use"; got != want {
		t.Fatalf("assistant blocks = %s, want %s: %s", got, want, claudeRequest)
	}
	if blocks[2].Get("id").String() != "srvtoolu_a" || blocks[4].Get("id").String() != "srvtoolu_b" || blocks[4].Get("input.query").String() != "query b" {
		t.Fatalf("searches = %s / %s", blocks[2].Raw, blocks[4].Raw)
	}
}

func TestPausedSearchFollowedByUserTurnKeepsLegacyPair(t *testing.T) {
	signature := mustTestSignature(t)
	_, paused := streamTerminal(t, pausedSearchChunks(t, "msg_p1", signature, "", "srvtoolu_paused", "paused query"))
	request := codexHistoryRequest(t, paused)
	request, _ = sjson.SetRawBytes(request, "input.-1", []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"never mind"}]}`))
	claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", request, true)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	blocks := claudeAssistantBlocks(t, claudeRequest)
	if got, want := blockTypes(blocks), "thinking,text,server_tool_use,web_search_tool_result"; got != want {
		t.Fatalf("assistant blocks = %s, want %s: %s", got, want, claudeRequest)
	}
}

func TestStopReasonErrorsKeepOutputEmittedBeforeTerminal(t *testing.T) {
	runStopReasonErrorsKeepOutput(t)
}

// Repeated web_search_call ids outside a pause/continuation pair keep the
// previous replay: one server_tool_use + result per item, in order.
func TestRepeatedWebSearchIDsAcrossTurnsAreNotMerged(t *testing.T) {
	for _, boundary := range []string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}`,
		`{"type":"function_call_output","call_id":"call_1","output":"ok"}`,
	} {
		request := []byte(`{"model":"claude-test","tools":[{"type":"web_search"},{"type":"function","name":"f","parameters":{"type":"object","properties":{}}}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`)
		first := `{"id":"` + responsesWebSearchCallID("srvtoolu_x") + `","type":"web_search_call","status":"completed","action":{"type":"search","query":"query a"}}`
		second := `{"id":"` + responsesWebSearchCallID("srvtoolu_x") + `","type":"web_search_call","status":"completed","action":{"type":"search","query":"query b"}}`
		items := []string{first}
		if strings.Contains(boundary, "function_call_output") {
			items = append(items, `{"type":"function_call","call_id":"call_1","name":"f","arguments":"{}"}`)
		}
		items = append(items, boundary, second)
		for _, item := range items {
			request, _ = sjson.SetRawBytes(request, "input.-1", []byte(item))
		}
		claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", request, true)
		if errTranslate != nil {
			t.Fatal(errTranslate)
		}
		var queries []string
		for _, block := range claudeAssistantBlocks(t, claudeRequest) {
			if block.Get("type").String() == "server_tool_use" {
				queries = append(queries, block.Get("input.query").String())
			}
		}
		if got := strings.Join(queries, ","); got != "query a,query b" {
			t.Fatalf("boundary %s: server_tool_use queries = %s: %s", boundary, got, claudeRequest)
		}
	}
}

// A paused search followed by a tool output is not a continuation pair: the
// later item with the same id is not merged into it.
func TestPausedSearchContinuationRequiresSameAssistantRun(t *testing.T) {
	paused := `{"id":"` + responsesWebSearchCallID("srvtoolu_p") + `","type":"web_search_call","status":"in_progress","action":{"type":"search","query":"paused"}}`
	later := `{"id":"` + responsesWebSearchCallID("srvtoolu_p") + `","type":"web_search_call","status":"completed","action":{"type":"search","query":""}}`
	request := []byte(`{"model":"claude-test","tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`)
	for _, item := range []string{paused, `{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`, later} {
		request, _ = sjson.SetRawBytes(request, "input.-1", []byte(item))
	}
	claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", request, true)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	uses := 0
	for _, block := range claudeAssistantBlocks(t, claudeRequest) {
		if block.Get("type").String() == "server_tool_use" {
			uses++
		}
	}
	if uses != 2 {
		t.Fatalf("server_tool_use count = %d, want 2 (no merge across a user turn): %s", uses, claudeRequest)
	}
}

func runStopReasonErrorsKeepOutput(t *testing.T) {
	for _, tc := range []struct {
		reason string
		event  string
	}{
		{"refusal", "response.incomplete"},
		{"model_context_window_exceeded", "response.failed"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			chunks := [][]byte{
				[]byte(`data: {"type":"message_start","message":{"id":"msg_e","usage":{"input_tokens":7,"output_tokens":0,"cache_read_input_tokens":3}}}`),
				[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Some text."}}`),
				[]byte(`data: {"type":"content_block_stop","index":0}`),
				[]byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":4}}`, tc.reason)),
				[]byte(`data: {"type":"message_stop"}`),
			}
			var order []string
			var terminal gjson.Result
			for _, output := range translateClaudeResponsesStreamThroughRegistry(chunks) {
				event, data := parseClaudeResponsesSSEEvent(t, output)
				if event == "response.output_item.done" && data.Get("item.type").String() == "message" {
					order = append(order, "message_done:"+data.Get("item.content.0.text").String())
				}
				if event == tc.event {
					order = append(order, event)
					terminal = data.Get("response")
				}
				if event == "response.completed" {
					t.Fatalf("unexpected response.completed for %s", tc.reason)
				}
			}
			if got := strings.Join(order, "|"); got != "message_done:Some text.|"+tc.event {
				t.Fatalf("event order = %s", got)
			}
			if terminal.Get("usage.input_tokens").Int() != 10 || terminal.Get("usage.input_tokens_details.cached_tokens").Int() != 3 || terminal.Get("usage.output_tokens").Int() != 4 {
				t.Fatalf("usage = %s", terminal.Get("usage").Raw)
			}
		})
	}
}

func TestResponsesParallelToolCallsFalseMapsToDisableParallelToolUse(t *testing.T) {
	tools := `[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]`
	for _, tc := range []struct {
		name    string
		request string
		want    string
	}{
		{"no tool_choice", `{"model":"claude-test","parallel_tool_calls":false,"tools":` + tools + `,"input":"hi"}`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"auto", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":"auto","tools":` + tools + `,"input":"hi"}`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"required", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":"required","tools":` + tools + `,"input":"hi"}`, `{"type":"any","disable_parallel_tool_use":true}`},
		{"forced tool", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"function","name":"shell"},"tools":` + tools + `,"input":"hi"}`, `{"name":"shell","type":"tool","disable_parallel_tool_use":true}`},
		{"none", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":"none","tools":` + tools + `,"input":"hi"}`, ``},
		{"no tools", `{"model":"claude-test","parallel_tool_calls":false,"input":"hi"}`, ``},
		{"forced tool filtered out", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"function","name":"missing"},"tools":` + tools + `,"input":"hi"}`, ``},
		{"builtin web_search present", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"web_search"},"tools":[{"type":"web_search"}],"input":"hi"}`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"builtin web_search filtered out", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"web_search"},"tools":` + tools + `,"input":"hi"}`, ``},
		{"true", `{"model":"claude-test","parallel_tool_calls":true,"tools":` + tools + `,"input":"hi"}`, ``},
		{"absent", `{"model":"claude-test","tools":` + tools + `,"input":"hi"}`, ``},
		{"true with auto", `{"model":"claude-test","parallel_tool_calls":true,"tool_choice":"auto","tools":` + tools + `,"input":"hi"}`, `{"type":"auto"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(tc.request), true)
			if errTranslate != nil {
				t.Fatal(errTranslate)
			}
			if got := gjson.GetBytes(out, "tool_choice").Raw; got != tc.want {
				t.Fatalf("tool_choice = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestContextWindowExceededNonStreamIsFailedWithError(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_c","usage":{"input_tokens":5,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":2}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	output := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chunks, []byte("\n")), nil))
	if output.Get("status").String() != "failed" || output.Get("error.code").String() != "context_length_exceeded" {
		t.Fatalf("response = %s", output.Raw)
	}
	if output.Get("usage.output_tokens").Int() != 2 || output.Get("usage.input_tokens").Int() != 5 {
		t.Fatalf("usage = %s", output.Get("usage").Raw)
	}
}

func TestPrefillRejectingModelStillDropsCompletedThinkingPlusTextTrailingAssistant(t *testing.T) {
	signature := mustTestSignature(t)
	raw := []byte(fmt.Sprintf(`{
		"model": "claude-sonnet-4-6",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
			{"type": "reasoning", "encrypted_content": %q, "summary": [{"type": "summary_text", "text": "thought"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "prefill text"}]}
		]
	}`, signature))
	out, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-6", raw, false)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 || messages[0].Get("role").String() != "user" {
		t.Fatalf("expected trailing thinking+text assistant message to be stripped on claude-sonnet-4-6, got %d messages: %s", len(messages), out)
	}
}

func TestPauseTurnAfterCompletedSearchPreservedOnPrefillRejectingModel(t *testing.T) {
	chunks := claudeWebSearchStreamChunks()
	stop := chunks[len(chunks)-1]
	chunks = append(chunks[:len(chunks)-1], []byte(`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":12}}`), stop)
	event, response := streamTerminal(t, chunks)
	if event != "response.completed" || response.Get("end_turn").Raw != "false" {
		t.Fatalf("terminal = %s end_turn=%s", event, response.Get("end_turn").Raw)
	}
	req := codexHistoryRequest(t, response)
	req, _ = sjson.SetBytes(req, "model", "claude-opus-5-5")
	claudeRequest, errTranslate := ConvertOpenAIResponsesRequestToClaude("claude-opus-5-5", req, true)
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	messages := gjson.GetBytes(claudeRequest, "messages").Array()
	if len(messages) != 2 || messages[1].Get("role").String() != "assistant" {
		t.Fatalf("expected paused assistant search turn to be preserved on claude-opus-5-5, got: %s", claudeRequest)
	}
	blocks := claudeAssistantBlocks(t, claudeRequest)
	if got, want := blockTypes(blocks), "server_tool_use,web_search_tool_result"; got != want {
		t.Fatalf("assistant blocks = %s, want %s: %s", got, want, claudeRequest)
	}
}
