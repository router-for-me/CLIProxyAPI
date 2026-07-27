package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func parseClaudeResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	var event string
	var data string
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatalf("SSE chunk has no data line: %s", string(chunk))
	}

	return event, gjson.Parse(data)
}

func translateClaudeResponsesStreamThroughRegistry(chunks [][]byte) [][]byte {
	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, "claude-test", nil, nil, chunk, &param)...)
	}
	return outputs
}

func TestConvertClaudeResponseToOpenAIResponses_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_123"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"internal "}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	var reasoningDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if data.Get("item.type").String() == "reasoning" {
				reasoningDone = data
			}
		case "response.completed":
			completed = data
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != signature {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := reasoningDone.Get("item.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("reasoning summary text = %q", got)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != signature {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := completed.Get("response.output.0.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("completed reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_SuppressesSignatureDeltaPassthrough(t *testing.T) {
	chunk := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"claude_sig_123"}}`)

	outputs := translateClaudeResponsesStreamThroughRegistry([][]byte{chunk})
	if len(outputs) != 0 {
		t.Fatalf("expected signature_delta to be suppressed, got %d chunks", len(outputs))
	}
}

func TestConvertClaudeResponseToOpenAIResponses_AggregatesTextBlocksUntilMessageStop(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"**Compare competitors**\n- "}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"content_block_start","index":5,"content_block":{"type":"server_tool_use","id":"srv_123","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Qwen3\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":5}`),
		[]byte(`data: {"type":"content_block_start","index":6,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_123","content":[{"type":"web_search_result","title":"Example","url":"https://example.com"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":6}`),
		[]byte(`data: {"type":"content_block_delta","index":5,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"Qwen 3.7 Max","url":"https://example.com","title":"Example"}}}`),
		[]byte(`data: {"type":"content_block_start","index":7,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"Qwen 3.7 Max leads."}}`),
		[]byte(`data: {"type":"content_block_stop","index":7}`),
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":12}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	counts := map[string]int{}
	var outputTextDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		counts[event]++
		if event == "response.output_text.done" {
			outputTextDone = data
		}
		if event == "response.completed" {
			completed = data
		}
		if strings.HasPrefix(event, "content_block_") || event == "message_delta" {
			t.Fatalf("unexpected anthropic-native event leaked: %s", event)
		}
	}

	if counts["response.output_item.added"] != 1 {
		t.Fatalf("response.output_item.added count = %d, want 1", counts["response.output_item.added"])
	}
	if counts["response.content_part.added"] != 1 {
		t.Fatalf("response.content_part.added count = %d, want 1", counts["response.content_part.added"])
	}
	if counts["response.output_text.done"] != 1 {
		t.Fatalf("response.output_text.done count = %d, want 1", counts["response.output_text.done"])
	}
	if counts["response.content_part.done"] != 1 {
		t.Fatalf("response.content_part.done count = %d, want 1", counts["response.content_part.done"])
	}
	if counts["response.output_item.done"] != 1 {
		t.Fatalf("response.output_item.done count = %d, want 1", counts["response.output_item.done"])
	}
	if counts["response.function_call_arguments.delta"] != 0 {
		t.Fatalf("response.function_call_arguments.delta count = %d, want 0", counts["response.function_call_arguments.delta"])
	}

	wantText := "**Compare competitors**\n- Qwen 3.7 Max leads."
	if got := outputTextDone.Get("text").String(); got != wantText {
		t.Fatalf("output_text.done text = %q, want %q", got, wantText)
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != wantText {
		t.Fatalf("completed message text = %q, want %q", got, wantText)
	}
	if got := completed.Get("response.output.0.content.0.annotations.0.type").String(); got != "web_search_result_location" {
		t.Fatalf("completed annotation type = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_FinalizesMessageBeforeFunctionCall(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Checking the workspace."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_123","name":"exec_command","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	messageAddedPosition := -1
	messageDonePosition := -1
	functionAddedPosition := -1
	functionDonePosition := -1
	messageDoneCount := 0
	functionDoneCount := 0
	var completed gjson.Result
	for position, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		itemType := data.Get("item.type").String()
		switch {
		case event == "response.output_item.added" && itemType == "message":
			messageAddedPosition = position
			if got := data.Get("output_index").Int(); got != 0 {
				t.Fatalf("message added output_index = %d, want 0", got)
			}
		case event == "response.output_item.done" && itemType == "message":
			messageDonePosition = position
			messageDoneCount++
			if got := data.Get("output_index").Int(); got != 0 {
				t.Fatalf("message done output_index = %d, want 0", got)
			}
		case event == "response.output_item.added" && itemType == "function_call":
			functionAddedPosition = position
			if got := data.Get("output_index").Int(); got != 1 {
				t.Fatalf("function added output_index = %d, want 1", got)
			}
		case event == "response.output_item.done" && itemType == "function_call":
			functionDonePosition = position
			functionDoneCount++
			if got := data.Get("output_index").Int(); got != 1 {
				t.Fatalf("function done output_index = %d, want 1", got)
			}
		case event == "response.completed":
			completed = data
		}
	}

	if messageAddedPosition < 0 || messageDonePosition < 0 || functionAddedPosition < 0 || functionDonePosition < 0 {
		t.Fatalf(
			"missing lifecycle event: message added=%d done=%d, function added=%d done=%d",
			messageAddedPosition,
			messageDonePosition,
			functionAddedPosition,
			functionDonePosition,
		)
	}
	if messageDonePosition >= functionAddedPosition {
		t.Fatalf(
			"message done position = %d, want before function added position %d",
			messageDonePosition,
			functionAddedPosition,
		)
	}
	if functionAddedPosition >= functionDonePosition {
		t.Fatalf("function added position = %d, want before done position %d", functionAddedPosition, functionDonePosition)
	}
	if messageDoneCount != 1 {
		t.Fatalf("message output_item.done count = %d, want 1", messageDoneCount)
	}
	if functionDoneCount != 1 {
		t.Fatalf("function output_item.done count = %d, want 1", functionDoneCount)
	}
	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if got := completed.Get("response.output.#").Int(); got != 2 {
		t.Fatalf("completed output count = %d, want 2", got)
	}
	if got := completed.Get("response.output.0.type").String(); got != "message" {
		t.Fatalf("completed output[0] type = %q, want message", got)
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != "Checking the workspace." {
		t.Fatalf("completed message text = %q", got)
	}
	if got := completed.Get("response.output.1.type").String(); got != "function_call" {
		t.Fatalf("completed output[1] type = %q, want function_call", got)
	}
	if got := completed.Get("response.output.1.call_id").String(); got != "call_123" {
		t.Fatalf("completed function call_id = %q, want call_123", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_UsesContiguousIndicesForReasoningTextAndTool(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_123","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Qwen3\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_123","content":[]}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"Inspect first."}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Checking the workspace."}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"call_123","name":"exec_command","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	seen := map[string]int{}
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		var itemType string
		var wantIndex int64
		switch {
		case event == "response.output_item.added" || event == "response.output_item.done":
			itemType = data.Get("item.type").String()
			switch itemType {
			case "reasoning":
				wantIndex = 0
			case "message":
				wantIndex = 1
			case "function_call":
				wantIndex = 2
			default:
				continue
			}
		case strings.HasPrefix(event, "response.reasoning_"):
			itemType = "reasoning"
			wantIndex = 0
		case strings.HasPrefix(event, "response.output_text.") || strings.HasPrefix(event, "response.content_part."):
			itemType = "message"
			wantIndex = 1
		case strings.HasPrefix(event, "response.function_call_arguments."):
			itemType = "function_call"
			wantIndex = 2
		case event == "response.completed":
			completed = data
			continue
		default:
			continue
		}

		if !data.Get("output_index").Exists() {
			t.Fatalf("%s %s event missing output_index: %s", itemType, event, data.Raw)
		}
		if got := data.Get("output_index").Int(); got != wantIndex {
			t.Fatalf("%s %s output_index = %d, want %d", itemType, event, got, wantIndex)
		}
		seen[itemType]++
	}

	for _, itemType := range []string{"reasoning", "message", "function_call"} {
		if seen[itemType] == 0 {
			t.Fatalf("no indexed %s events observed", itemType)
		}
	}
	if got := completed.Get("response.output.#").Int(); got != 3 {
		t.Fatalf("completed output count = %d, want 3", got)
	}
	for index, wantType := range []string{"reasoning", "message", "function_call"} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.type", index)).String(); got != wantType {
			t.Fatalf("completed output[%d].type = %q, want %q", index, got, wantType)
		}
	}
}

func TestConvertClaudeResponseToOpenAIResponses_HiddenServerToolsDoNotCreateOutputIndexGaps(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Searching. "}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srv_123","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Qwen3\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_123","content":[]}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Found it."}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"call_123","name":"exec_command","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	messageAddedCount := 0
	messageDoneCount := 0
	var outputTextDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch {
		case event == "response.output_item.added" && data.Get("item.type").String() == "message":
			messageAddedCount++
			if got := data.Get("output_index").Int(); got != 0 {
				t.Fatalf("message added output_index = %d, want 0", got)
			}
		case event == "response.output_item.done" && data.Get("item.type").String() == "message":
			messageDoneCount++
			if got := data.Get("output_index").Int(); got != 0 {
				t.Fatalf("message done output_index = %d, want 0", got)
			}
		case strings.HasPrefix(event, "response.output_text.") || strings.HasPrefix(event, "response.content_part."):
			if got := data.Get("output_index").Int(); got != 0 {
				t.Fatalf("%s output_index = %d, want 0", event, got)
			}
			if event == "response.output_text.done" {
				outputTextDone = data
			}
		case event == "response.output_item.added" && data.Get("item.type").String() == "function_call",
			event == "response.output_item.done" && data.Get("item.type").String() == "function_call",
			strings.HasPrefix(event, "response.function_call_arguments."):
			if got := data.Get("output_index").Int(); got != 1 {
				t.Fatalf("%s output_index = %d, want 1", event, got)
			}
		case event == "response.completed":
			completed = data
		}
	}

	if messageAddedCount != 1 || messageDoneCount != 1 {
		t.Fatalf("message lifecycle counts: added=%d done=%d, want 1 each", messageAddedCount, messageDoneCount)
	}
	if got := outputTextDone.Get("text").String(); got != "Searching. Found it." {
		t.Fatalf("aggregated message text = %q, want %q", got, "Searching. Found it.")
	}
	if got := completed.Get("response.output.#").Int(); got != 2 {
		t.Fatalf("completed output count = %d, want 2", got)
	}
	if got := completed.Get("response.output.0.type").String(); got != "message" {
		t.Fatalf("completed output[0].type = %q, want message", got)
	}
	if got := completed.Get("response.output.1.type").String(); got != "function_call" {
		t.Fatalf("completed output[1].type = %q, want function_call", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_StartsNewMessageAfterFunctionCall(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Before tool."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_123","name":"exec_command","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"After tool."}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	var lifecycle []string
	var messageIDs []string
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		if event == "response.output_item.added" || event == "response.output_item.done" {
			itemType := data.Get("item.type").String()
			lifecycle = append(lifecycle, fmt.Sprintf("%s:%d:%s", event, data.Get("output_index").Int(), itemType))
			if event == "response.output_item.added" && itemType == "message" {
				messageIDs = append(messageIDs, data.Get("item.id").String())
			}
		}
		if event == "response.completed" {
			completed = data
		}
	}

	wantLifecycle := strings.Join([]string{
		"response.output_item.added:0:message",
		"response.output_item.done:0:message",
		"response.output_item.added:1:function_call",
		"response.output_item.done:1:function_call",
		"response.output_item.added:2:message",
		"response.output_item.done:2:message",
	}, ",")
	if got := strings.Join(lifecycle, ","); got != wantLifecycle {
		t.Fatalf("item lifecycle = %q, want %q", got, wantLifecycle)
	}
	if len(messageIDs) != 2 || messageIDs[0] == messageIDs[1] {
		t.Fatalf("message IDs = %v, want two unique IDs", messageIDs)
	}
	if got := completed.Get("response.output.#").Int(); got != 3 {
		t.Fatalf("completed output count = %d, want 3", got)
	}
	for index, wantType := range []string{"message", "function_call", "message"} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.type", index)).String(); got != wantType {
			t.Fatalf("completed output[%d].type = %q, want %q", index, got, wantType)
		}
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != "Before tool." {
		t.Fatalf("first completed message text = %q", got)
	}
	if got := completed.Get("response.output.2.content.0.text").String(); got != "After tool." {
		t.Fatalf("second completed message text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_FinalizesMessageBeforeReasoning(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Visible first."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"Reason later."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	var lifecycle []string
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		if event == "response.output_item.added" || event == "response.output_item.done" {
			lifecycle = append(lifecycle, fmt.Sprintf("%s:%d:%s", event, data.Get("output_index").Int(), data.Get("item.type").String()))
		}
		if event == "response.completed" {
			completed = data
		}
	}

	wantLifecycle := strings.Join([]string{
		"response.output_item.added:0:message",
		"response.output_item.done:0:message",
		"response.output_item.added:1:reasoning",
		"response.output_item.done:1:reasoning",
	}, ",")
	if got := strings.Join(lifecycle, ","); got != wantLifecycle {
		t.Fatalf("item lifecycle = %q, want %q", got, wantLifecycle)
	}
	for index, wantType := range []string{"message", "reasoning"} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.type", index)).String(); got != wantType {
			t.Fatalf("completed output[%d].type = %q, want %q", index, got, wantType)
		}
	}
}

func TestConvertClaudeResponseToOpenAIResponses_PreservesMultipleReasoningItems(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"First reason."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"Second reason."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Visible response."}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	reasoningDoneCount := 0
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		if event == "response.output_item.done" && data.Get("item.type").String() == "reasoning" {
			if got := data.Get("output_index").Int(); got != int64(reasoningDoneCount) {
				t.Fatalf("reasoning done output_index = %d, want %d", got, reasoningDoneCount)
			}
			reasoningDoneCount++
		}
		if event == "response.completed" {
			completed = data
		}
	}

	if reasoningDoneCount != 2 {
		t.Fatalf("reasoning done count = %d, want 2", reasoningDoneCount)
	}
	if got := completed.Get("response.output.#").Int(); got != 3 {
		t.Fatalf("completed output count = %d, want 3", got)
	}
	for index, wantType := range []string{"reasoning", "reasoning", "message"} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.type", index)).String(); got != wantType {
			t.Fatalf("completed output[%d].type = %q, want %q", index, got, wantType)
		}
	}
	for index, wantText := range []string{"First reason.", "Second reason."} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.summary.0.text", index)).String(); got != wantText {
			t.Fatalf("completed reasoning[%d] text = %q, want %q", index, got, wantText)
		}
	}
}

func TestConvertClaudeResponseToOpenAIResponses_NormalizesEmptyFunctionArguments(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_123","name":"exec_command","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	var functionDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		if event == "response.output_item.done" && data.Get("item.type").String() == "function_call" {
			functionDone = data
		}
		if event == "response.completed" {
			completed = data
		}
	}

	if got := functionDone.Get("item.arguments").String(); got != "{}" {
		t.Fatalf("function done arguments = %q, want {}", got)
	}
	if got := completed.Get("response.output.0.arguments").String(); got != "{}" {
		t.Fatalf("completed function arguments = %q, want {}", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_IncludesEmptyReasoningInCompletedOutput(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Visible response."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	var reasoningDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		if event == "response.output_item.done" && data.Get("item.type").String() == "reasoning" {
			reasoningDone = data
		}
		if event == "response.completed" {
			completed = data
		}
	}

	if got := reasoningDone.Get("item.summary.#").Int(); got != 1 {
		t.Fatalf("reasoning done summary count = %d, want 1", got)
	}
	if got := completed.Get("response.output.#").Int(); got != 2 {
		t.Fatalf("completed output count = %d, want 2", got)
	}
	if got := completed.Get("response.output.0.type").String(); got != "reasoning" {
		t.Fatalf("completed output[0].type = %q, want reasoning", got)
	}
	if got := completed.Get("response.output.0.summary.#").Int(); got != 1 {
		t.Fatalf("completed reasoning summary count = %d, want 1", got)
	}
	if got := completed.Get("response.output.1.type").String(); got != "message" {
		t.Fatalf("completed output[1].type = %q, want message", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_ReportsCacheTokens(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":13,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}}}`),
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var completed gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				completed = data
			}
		}
	}

	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if got := completed.Get("response.usage.input_tokens").Int(); got != 22044 {
		t.Fatalf("response usage input_tokens = %d, want %d", got, 22044)
	}
	if got := completed.Get("response.usage.input_tokens_details.cached_tokens").Int(); got != 22000 {
		t.Fatalf("response usage cached_tokens = %d, want %d", got, 22000)
	}
	if got := completed.Get("response.usage.output_tokens").Int(); got != 4 {
		t.Fatalf("response usage output_tokens = %d, want %d", got, 4)
	}
	if got := completed.Get("response.usage.total_tokens").Int(); got != 22048 {
		t.Fatalf("response usage total_tokens = %d, want %d", got, 22048)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_nonstream"
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"nonstream reasoning"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("output.0.encrypted_content").String(); got != signature {
		t.Fatalf("non-stream reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := root.Get("output.0.summary.0.text").String(); got != "nonstream reasoning" {
		t.Fatalf("non-stream reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ReportsCacheTokens(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":13,"output_tokens":1,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("usage.input_tokens").Int(); got != 22044 {
		t.Fatalf("non-stream usage input_tokens = %d, want %d", got, 22044)
	}
	if got := root.Get("usage.input_tokens_details.cached_tokens").Int(); got != 22000 {
		t.Fatalf("non-stream usage cached_tokens = %d, want %d", got, 22000)
	}
	if got := root.Get("usage.output_tokens").Int(); got != 4 {
		t.Fatalf("non-stream usage output_tokens = %d, want %d", got, 4)
	}
	if got := root.Get("usage.total_tokens").Int(); got != 22048 {
		t.Fatalf("non-stream usage total_tokens = %d, want %d", got, 22048)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_RestoresNamespaceFunctionCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"tools":[
			{
				"type":"namespace",
				"name":"mcp__node_repl",
				"tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{}}}]
			}
		]
	}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_abc","name":"mcp__node_repl__js","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{"code":"nodeRepl.write('hello')"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var added gjson.Result
	var done gjson.Result
	var completed gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "function_call" {
					added = data
				}
			case "response.output_item.done":
				if data.Get("item.type").String() == "function_call" {
					done = data
				}
			case "response.completed":
				completed = data
			}
		}
	}

	for _, tc := range []struct {
		label string
		got   gjson.Result
	}{
		{"added", added},
		{"done", done},
	} {
		if !tc.got.Exists() {
			t.Fatalf("expected function_call %s event", tc.label)
		}
		if got := tc.got.Get("item.name").String(); got != "js" {
			t.Fatalf("%s item.name = %q, want js", tc.label, got)
		}
		if got := tc.got.Get("item.namespace").String(); got != "mcp__node_repl" {
			t.Fatalf("%s item.namespace = %q, want mcp__node_repl", tc.label, got)
		}
	}

	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if got := completed.Get("response.output.0.name").String(); got != "js" {
		t.Fatalf("completed output name = %q, want js", got)
	}
	if got := completed.Get("response.output.0.namespace").String(); got != "mcp__node_repl" {
		t.Fatalf("completed output namespace = %q, want mcp__node_repl", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RestoresNamespaceFunctionCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"tools":[
			{
				"type":"namespace",
				"name":"mcp__node_repl",
				"tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{}}}]
			}
		]
	}`)
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_abc","name":"mcp__node_repl__js","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"nodeRepl.write('hello')\"}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("output.0.name").String(); got != "js" {
		t.Fatalf("non-stream output name = %q, want js", got)
	}
	if got := root.Get("output.0.namespace").String(); got != "mcp__node_repl" {
		t.Fatalf("non-stream output namespace = %q, want mcp__node_repl", got)
	}
}
