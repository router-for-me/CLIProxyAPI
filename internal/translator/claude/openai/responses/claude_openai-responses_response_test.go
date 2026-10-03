package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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

func TestConvertClaudeResponseToOpenAIResponses_CreatedIncludesOriginalRequestModel(t *testing.T) {
	request := []byte(`{"model":"original-claude-model"}`)
	translatedRequest := []byte(`{"model":"translated-claude-model"}`)
	chunk := []byte(`data: {"type":"message_start","message":{"id":"msg_123"}}`)

	var param any
	outputs := ConvertClaudeResponseToOpenAIResponses(context.Background(), "fallback-model", request, translatedRequest, chunk, &param)
	if len(outputs) < 2 {
		t.Fatalf("expected response.created and response.in_progress outputs, got %d", len(outputs))
	}

	var createdModels string
	var inProgressModels string
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.created":
			createdModels = data.Get("response.model").String()
		case "response.in_progress":
			inProgressModels = data.Get("response.model").String()
		}
	}
	if createdModels != "original-claude-model" {
		t.Fatalf("response.created models = %q, want original-claude-model", createdModels)
	}
	if inProgressModels != "original-claude-model" {
		t.Fatalf("response.in_progress models = %q, want original-claude-model", inProgressModels)
	}
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

func TestConvertClaudeResponseToOpenAIResponses_RedactedThinkingBecomesMarkedReasoningItem(t *testing.T) {
	const data = "EroBCkYIBRgCKkA"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"` + data + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	want := ClaudeResponsesRedactedThinkingPrefix + data
	var reasoningDone, completed gjson.Result
	for _, output := range outputs {
		event, parsed := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if parsed.Get("item.type").String() == "reasoning" {
				reasoningDone = parsed
			}
		case "response.completed":
			completed = parsed
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event for redacted_thinking")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != want {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, want)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != want {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, want)
	}
	if got := completed.Get("response.output.1.type").String(); got != "message" {
		t.Fatalf("completed output[1].type = %q, want message", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RedactedThinkingBecomesMarkedReasoningItem(t *testing.T) {
	const data = "EroBCkYIBRgCKkA"
	raw := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"` + data + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n")

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, []byte(raw), nil)
	parsed := gjson.ParseBytes(out)
	if got := parsed.Get("output.0.type").String(); got != "reasoning" {
		t.Fatalf("output.0.type = %q, want reasoning; body=%s", got, out)
	}
	want := ClaudeResponsesRedactedThinkingPrefix + data
	if got := parsed.Get("output.0.encrypted_content").String(); got != want {
		t.Fatalf("output.0.encrypted_content = %q, want %q", got, want)
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
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		counts[event]++
		if event == "response.completed" {
			completed = data
		}
		if strings.HasPrefix(event, "content_block_") || event == "message_delta" {
			t.Fatalf("unexpected anthropic-native event leaked: %s", event)
		}
	}

	if counts["response.output_item.added"] != 3 {
		t.Fatalf("response.output_item.added count = %d, want 3", counts["response.output_item.added"])
	}
	if counts["response.content_part.added"] != 2 {
		t.Fatalf("response.content_part.added count = %d, want 2", counts["response.content_part.added"])
	}
	if counts["response.output_text.done"] != 2 {
		t.Fatalf("response.output_text.done count = %d, want 2", counts["response.output_text.done"])
	}
	if counts["response.content_part.done"] != 2 {
		t.Fatalf("response.content_part.done count = %d, want 2", counts["response.content_part.done"])
	}
	if counts["response.output_item.done"] != 3 {
		t.Fatalf("response.output_item.done count = %d, want 3", counts["response.output_item.done"])
	}
	if counts["response.function_call_arguments.delta"] != 0 {
		t.Fatalf("response.function_call_arguments.delta count = %d, want 0", counts["response.function_call_arguments.delta"])
	}

	if got := completed.Get("response.output.0.content.0.text").String(); got != "**Compare competitors**\n- " {
		t.Fatalf("completed message[0] text = %q, want %q", got, "**Compare competitors**\n- ")
	}
	if got := completed.Get("response.output.1.type").String(); got != "web_search_call" {
		t.Fatalf("completed output[1].type = %q, want web_search_call", got)
	}
	if got := completed.Get("response.output.2.content.0.text").String(); got != "Qwen 3.7 Max leads." {
		t.Fatalf("completed message[2] text = %q, want %q", got, "Qwen 3.7 Max leads.")
	}
	if got := completed.Get("response.output.2.content.0.annotations.0.type").String(); got != "web_search_result_location" {
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
			case "web_search_call":
				wantIndex = 0
			case "reasoning":
				wantIndex = 1
			case "message":
				wantIndex = 2
			case "function_call":
				wantIndex = 3
			default:
				continue
			}
		case strings.HasPrefix(event, "response.reasoning_"):
			itemType = "reasoning"
			wantIndex = 1
		case strings.HasPrefix(event, "response.output_text.") || strings.HasPrefix(event, "response.content_part."):
			itemType = "message"
			wantIndex = 2
		case strings.HasPrefix(event, "response.function_call_arguments."):
			itemType = "function_call"
			wantIndex = 3
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
	if got := completed.Get("response.output.#").Int(); got != 4 {
		t.Fatalf("completed output count = %d, want 4", got)
	}
	for index, wantType := range []string{"web_search_call", "reasoning", "message", "function_call"} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.type", index)).String(); got != wantType {
			t.Fatalf("completed output[%d].type = %q, want %q", index, got, wantType)
		}
	}
}

func TestConvertClaudeResponseToOpenAIResponses_ServerToolsSurfaceWithoutOutputIndexGaps(t *testing.T) {
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
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch {
		case event == "response.output_item.added" && data.Get("item.type").String() == "message":
			messageAddedCount++
		case event == "response.output_item.done" && data.Get("item.type").String() == "message":
			messageDoneCount++
		case event == "response.output_item.added" && data.Get("item.type").String() == "function_call",
			event == "response.output_item.done" && data.Get("item.type").String() == "function_call",
			strings.HasPrefix(event, "response.function_call_arguments."):
			if got := data.Get("output_index").Int(); got != 3 {
				t.Fatalf("%s output_index = %d, want 3", event, got)
			}
		case event == "response.completed":
			completed = data
		}
	}

	if messageAddedCount != 2 || messageDoneCount != 2 {
		t.Fatalf("message lifecycle counts: added=%d done=%d, want 2 each", messageAddedCount, messageDoneCount)
	}
	if got := completed.Get("response.output.#").Int(); got != 4 {
		t.Fatalf("completed output count = %d, want 4", got)
	}
	for index, wantType := range []string{"message", "web_search_call", "message", "function_call"} {
		if got := completed.Get(fmt.Sprintf("response.output.%d.type", index)).String(); got != wantType {
			t.Fatalf("completed output[%d].type = %q, want %q", index, got, wantType)
		}
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

func TestConvertClaudeResponseToOpenAIResponsesNonStream_PreservesContentBlockOrder(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream_order","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_order","name":"exec_command","input":{}}}`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`,
		`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"done"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"content_block_stop","index":3}`,
		`data: {"type":"content_block_start","index":4,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":4,"delta":{"type":"thinking_delta","thinking":"more"}}`,
		`data: {"type":"content_block_stop","index":4}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	root := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil))
	wantTypes := []string{"message", "reasoning", "function_call", "message", "reasoning"}
	if got := root.Get("output.#").Int(); got != int64(len(wantTypes)) {
		t.Fatalf("non-stream output count = %d, want %d", got, len(wantTypes))
	}
	for index, wantType := range wantTypes {
		if got := root.Get(fmt.Sprintf("output.%d.type", index)).String(); got != wantType {
			t.Fatalf("non-stream output.%d.type = %q, want %q", index, got, wantType)
		}
	}
	if got := root.Get("output.0.content.0.text").String(); got != "" {
		t.Fatalf("empty text block content = %q, want empty string", got)
	}
	if got := root.Get("output.1.summary.0.text").String(); got != "plan" {
		t.Fatalf("first reasoning text = %q, want %q", got, "plan")
	}
	if got := root.Get("output.2.call_id").String(); got != "call_order" {
		t.Fatalf("function call id = %q, want %q", got, "call_order")
	}
	if got := root.Get("output.2.arguments").String(); got != `{"cmd":"pwd"}` {
		t.Fatalf("function call arguments = %q, want %q", got, `{"cmd":"pwd"}`)
	}
	if got := root.Get("output.3.content.0.text").String(); got != "done" {
		t.Fatalf("second message text = %q, want %q", got, "done")
	}
	if got := root.Get("output.4.summary.0.text").String(); got != "more" {
		t.Fatalf("second reasoning text = %q, want %q", got, "more")
	}
	if got := root.Get("usage.output_tokens_details.reasoning_tokens").Int(); got != 2 {
		t.Fatalf("reasoning tokens = %d, want 2", got)
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

func TestConvertClaudeResponseToOpenAIResponses_RestoresAdditionalNamespaceCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"input":[{"type":"additional_tools","role":"developer","tools":[
			{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}
		]}]
	}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_custom","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom","name":"functions__exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var added, inputDone, done, completed gjson.Result
	functionEvents := 0
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "custom_tool_call" {
					added = data
				}
			case "response.custom_tool_call_input.done":
				inputDone = data
			case "response.output_item.done":
				if data.Get("item.type").String() == "custom_tool_call" {
					done = data
				}
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				functionEvents++
			case "response.completed":
				completed = data
			}
		}
	}

	if !added.Exists() || !inputDone.Exists() || !done.Exists() || !completed.Exists() {
		t.Fatalf("missing custom tool lifecycle events: added=%v input_done=%v done=%v completed=%v", added.Exists(), inputDone.Exists(), done.Exists(), completed.Exists())
	}
	if functionEvents != 0 {
		t.Fatalf("function call events = %d, want 0", functionEvents)
	}
	for _, test := range []struct {
		label string
		item  gjson.Result
	}{
		{label: "added", item: added.Get("item")},
		{label: "done", item: done.Get("item")},
		{label: "completed", item: completed.Get("response.output.0")},
	} {
		if got := test.item.Get("name").String(); got != "exec" {
			t.Fatalf("%s name = %q, want exec", test.label, got)
		}
		if got := test.item.Get("namespace").String(); got != "functions" {
			t.Fatalf("%s namespace = %q, want functions", test.label, got)
		}
	}
	if got := inputDone.Get("input").String(); got != "pwd" {
		t.Fatalf("custom input.done input = %q, want pwd", got)
	}
	if got := done.Get("item.input").String(); got != "pwd" {
		t.Fatalf("done input = %q, want pwd", got)
	}
	if got := completed.Get("response.output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("completed output type = %q, want custom_tool_call", got)
	}
	if got := completed.Get("response.output.0.input").String(); got != "pwd" {
		t.Fatalf("completed input = %q, want pwd", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_DirectCustomWinsNamespaceCollision(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"tools":[
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"x"}]},
			{"type":"custom","name":"n__x"}
		]
	}`)
	streamChunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_collision","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_collision","name":"n__x","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var streamCompleted gjson.Result
	for _, chunk := range streamChunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				streamCompleted = data
			}
		}
	}
	if got := streamCompleted.Get("response.output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("stream output type = %q, want custom_tool_call", got)
	}
	if got := streamCompleted.Get("response.output.0.input").String(); got != "pwd" {
		t.Fatalf("stream output input = %q, want pwd", got)
	}
	item := streamCompleted.Get("response.output.0")
	if got := item.Get("name").String(); got != "n__x" {
		t.Fatalf("name = %q, want n__x", got)
	}
	if item.Get("namespace").Exists() {
		t.Fatalf("unexpected namespace: %s", item.Get("namespace").Raw)
	}

	nonStreamRaw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_collision_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_collision_nonstream","name":"n__x","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	nonStream := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, nonStreamRaw, nil))
	if got := nonStream.Get("output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("non-stream output type = %q, want custom_tool_call", got)
	}
	if got := nonStream.Get("output.0.input").String(); got != "pwd" {
		t.Fatalf("non-stream output input = %q, want pwd", got)
	}
	item = nonStream.Get("output.0")
	if got := item.Get("name").String(); got != "n__x" {
		t.Fatalf("name = %q, want n__x", got)
	}
	if item.Get("namespace").Exists() {
		t.Fatalf("unexpected namespace: %s", item.Get("namespace").Raw)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RestoresAdditionalNamespaceCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"input":[{"type":"additional_tools","role":"developer","tools":[
			{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}
		]}]
	}`)
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_custom_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_nonstream","name":"functions__exec","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	root := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil))
	if got := root.Get("output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("non-stream output type = %q, want custom_tool_call; output=%s", got, root.Raw)
	}
	if got := root.Get("output.0.input").String(); got != "pwd" {
		t.Fatalf("non-stream input = %q, want pwd", got)
	}
	if got := root.Get("output.0.call_id").String(); got != "call_custom_nonstream" {
		t.Fatalf("non-stream call_id = %q, want call_custom_nonstream", got)
	}
	if got := root.Get("output.0.name").String(); got != "exec" {
		t.Fatalf("non-stream name = %q, want exec; output=%s", got, root.Raw)
	}
	if got := root.Get("output.0.namespace").String(); got != "functions" {
		t.Fatalf("non-stream namespace = %q, want functions; output=%s", got, root.Raw)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_CustomToolEmptyInputMatchesNonStream(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"exec"}]
	}`)
	streamChunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_custom_empty","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_empty","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var streamCompleted gjson.Result
	for _, chunk := range streamChunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				streamCompleted = data
			}
		}
	}
	if got := streamCompleted.Get("response.output.0.input").String(); got != "" {
		t.Fatalf("stream empty custom input = %q, want empty string", got)
	}

	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_custom_empty_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_empty","name":"exec","input":{}}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	nonStream := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil))
	if got := nonStream.Get("output.0.input").String(); got != "" {
		t.Fatalf("non-stream empty custom input = %q, want empty string", got)
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

func TestConvertClaudeResponseToOpenAIResponses_MaxTokensEmitsIncomplete(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_max_tokens","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"unfinished reasoning"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_max_tokens"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var incomplete gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				t.Fatalf("max_tokens response emitted response.completed: %s", output)
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.status").String(); got != "incomplete" {
		t.Fatalf("response.status = %q, want incomplete", got)
	}
	if got := incomplete.Get("response.incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens", got)
	}
	if got := incomplete.Get("response.output.0.type").String(); got != "reasoning" {
		t.Fatalf("response.output.0.type = %q, want reasoning; response=%s", got, incomplete.Raw)
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "incomplete" {
		t.Fatalf("response.output.0.status = %q, want incomplete", got)
	}
	if got := incomplete.Get("response.output.0.summary.0.text").String(); got != "unfinished reasoning" {
		t.Fatalf("reasoning summary = %q, want unfinished reasoning", got)
	}
	if got := incomplete.Get("response.usage.output_tokens").Int(); got != 64000 {
		t.Fatalf("output_tokens = %d, want 64000", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_MaxTokensPreservesPartialText(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_partial","usage":{"input_tokens":10,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial answer"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-fable-5-1", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)
	if got := root.Get("status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; response=%s", got, out)
	}
	if got := root.Get("incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens", got)
	}
	if got := root.Get("output.0.status").String(); got != "incomplete" {
		t.Fatalf("output.0.status = %q, want incomplete", got)
	}
	if got := root.Get("output.0.content.0.text").String(); got != "partial answer" {
		t.Fatalf("partial text = %q, want partial answer", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_MaxTokensWithToolCallAndTextEmitsIncomplete(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_tool_incomplete","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"calling tool"}}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_inc_1","name":"get_weather","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"San"}}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var incomplete gjson.Result
	var funcDone gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "function_call" {
				funcDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !funcDone.Exists() {
		t.Fatal("expected function_call response.output_item.done event")
	}
	if got := funcDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("funcDone item.status = %q, want incomplete", got)
	}
	if got := funcDone.Get("item.arguments").String(); got != "{\"city\":\"San" {
		t.Fatalf("funcDone item.arguments = %q, want '{\"city\":\"San'", got)
	}

	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.status").String(); got != "incomplete" {
		t.Fatalf("response.status = %q, want incomplete", got)
	}
	if got := incomplete.Get("response.incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens", got)
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "completed" {
		t.Fatalf("output.0.status = %q, want completed", got)
	}
	if got := incomplete.Get("response.output.0.content.0.text").String(); got != "calling tool" {
		t.Fatalf("output.0 text = %q, want 'calling tool'", got)
	}
	if got := incomplete.Get("response.output.1.status").String(); got != "incomplete" {
		t.Fatalf("output.1.status = %q, want incomplete", got)
	}
	if got := incomplete.Get("response.output.1.type").String(); got != "function_call" {
		t.Fatalf("output.1.type = %q, want function_call", got)
	}
	if got := incomplete.Get("response.output.1.arguments").String(); got != "{\"city\":\"San" {
		t.Fatalf("output.1.arguments = %q, want '{\"city\":\"San'", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_MaxTokensWithWebSearchEmitsIncomplete(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws_incomplete","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"golang\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var incomplete gjson.Result
	var wsDone gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "web_search_call" {
				wsDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !wsDone.Exists() {
		t.Fatal("expected web_search_call response.output_item.done event")
	}
	if got := wsDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("wsDone item.status = %q, want incomplete", got)
	}
	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "incomplete" {
		t.Fatalf("response.output.0.status = %q, want incomplete", got)
	}

	// Non-stream
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_ws_incomplete","usage":{"input_tokens":10,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"golang\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	nonStreamOut := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-fable-5-1", nil, nil, raw, nil)
	nsRoot := gjson.ParseBytes(nonStreamOut)
	if got := nsRoot.Get("status").String(); got != "incomplete" {
		t.Fatalf("ns status = %q, want incomplete", got)
	}
	if got := nsRoot.Get("output.0.status").String(); got != "incomplete" {
		t.Fatalf("ns output.0.status = %q, want incomplete", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_MaxTokensReasoningWithoutBlockStop(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_reasoning_nostop","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"partial thought before cut"}}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var incomplete gjson.Result
	var rsDone gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "reasoning" {
				rsDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !rsDone.Exists() {
		t.Fatal("expected reasoning response.output_item.done event")
	}
	if got := rsDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("rsDone item.status = %q, want incomplete", got)
	}
	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "incomplete" {
		t.Fatalf("response.output.0.status = %q, want incomplete", got)
	}
	if got := incomplete.Get("response.output.0.type").String(); got != "reasoning" {
		t.Fatalf("response.output.0.type = %q, want reasoning", got)
	}
	if got := incomplete.Get("response.output.0.summary.0.text").String(); got != "partial thought before cut" {
		t.Fatalf("summary = %q, want 'partial thought before cut'", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_MaxTokensWithToolBlockStopEmitsIncomplete(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_tool_blockstop","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_stop_1","name":"do_work","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"step\":1}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var incomplete gjson.Result
	var funcDone gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "function_call" {
				funcDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !funcDone.Exists() {
		t.Fatal("expected function_call response.output_item.done event")
	}
	if got := funcDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("funcDone item.status = %q, want incomplete", got)
	}
	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "incomplete" {
		t.Fatalf("output.0.status = %q, want incomplete", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_MaxTokensWithWebSearchResultsEmitsIncomplete(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws_res_incomplete","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_2","name":"web_search"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"golang\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_2","content":[{"type":"web_search_result","title":"Go","url":"https://golang.org"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var incomplete gjson.Result
	var wsDone gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "web_search_call" {
				wsDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !wsDone.Exists() {
		t.Fatal("expected web_search_call response.output_item.done event")
	}
	if got := wsDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("wsDone item.status = %q, want incomplete", got)
	}
	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "incomplete" {
		t.Fatalf("response.output.0.status = %q, want incomplete", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_ReasoningEventsNotDuplicated(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_rs_once","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"full thought"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":10}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	counts := make(map[string]int)
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, _ := parseClaudeResponsesSSEEvent(t, output)
			counts[event]++
		}
	}

	if counts["response.reasoning_summary_text.done"] != 1 {
		t.Fatalf("reasoning_summary_text.done count = %d, want 1", counts["response.reasoning_summary_text.done"])
	}
	if counts["response.reasoning_summary_part.done"] != 1 {
		t.Fatalf("reasoning_summary_part.done count = %d, want 1", counts["response.reasoning_summary_part.done"])
	}
	if counts["response.output_item.done"] != 1 {
		t.Fatalf("output_item.done count = %d, want 1", counts["response.output_item.done"])
	}
}

func TestConvertClaudeResponseToOpenAIResponses_CustomToolTruncatedInputUnwrapped(t *testing.T) {
	originalRequest := []byte(`{"tools":[{"type":"custom","name":"bash"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_custom_trunc","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_c1","name":"bash","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"echo \\u4F60"}}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var customDone gjson.Result
	var incomplete gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "custom_tool_call" {
				customDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !customDone.Exists() {
		t.Fatal("expected custom_tool_call response.output_item.done event")
	}
	if got := customDone.Get("item.input").String(); got != "echo 你" {
		t.Fatalf("customDone item.input = %q, want 'echo 你'", got)
	}
	if got := customDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("customDone item.status = %q, want incomplete", got)
	}
	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.output.0.input").String(); got != "echo 你" {
		t.Fatalf("response.output.0.input = %q, want 'echo 你'", got)
	}
	if got := incomplete.Get("response.output.0.status").String(); got != "incomplete" {
		t.Fatalf("response.output.0.status = %q, want incomplete", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_EmptyFunctionArgsConsistentOnTruncation(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_empty_args_trunc","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_empty","name":"get_info","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":64000}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var funcDone gjson.Result
	var argsDone gjson.Result
	var incomplete gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-fable-5-1", nil, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.function_call_arguments.done" {
				argsDone = data
			}
			if event == "response.output_item.done" && data.Get("item.type").String() == "function_call" {
				funcDone = data
			}
			if event == "response.incomplete" {
				incomplete = data
			}
		}
	}

	if !argsDone.Exists() {
		t.Fatal("expected response.function_call_arguments.done event")
	}
	if !funcDone.Exists() {
		t.Fatal("expected function_call response.output_item.done event")
	}
	if got := argsDone.Get("arguments").String(); got != "" {
		t.Fatalf("argsDone arguments = %q, want empty string", got)
	}
	if got := funcDone.Get("item.arguments").String(); got != "" {
		t.Fatalf("funcDone arguments = %q, want empty string", got)
	}
	if got := funcDone.Get("item.status").String(); got != "incomplete" {
		t.Fatalf("funcDone item.status = %q, want incomplete", got)
	}
	if !incomplete.Exists() {
		t.Fatal("expected response.incomplete event")
	}
	if got := incomplete.Get("response.output.0.arguments").String(); got != "" {
		t.Fatalf("response.output.0.arguments = %q, want empty string", got)
	}
}

func applyPatchClaudeStart(index int, id, name string) []byte {
	return []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, index, id, name))
}
func applyPatchClaudeFragment(index int, fragment string) []byte {
	return []byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, index, fragment))
}
func applyPatchClaudeEnd() [][]byte {
	return [][]byte{[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`), []byte(`data: {"type":"message_stop"}`)}
}
func applyPatchClaudeEvents(t *testing.T, chunks [][]byte) []gjson.Result {
	t.Helper()
	var events []gjson.Result
	for _, chunk := range chunks {
		_, data := parseClaudeResponsesSSEEvent(t, chunk)
		events = append(events, data)
	}
	return events
}

const applyPatchClaudeRequest = `{"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`

func TestApplyPatchClaudePreviewBeforeDone(t *testing.T) {
	request := []byte(applyPatchClaudeRequest)
	var param any
	feed := func(chunk []byte) []gjson.Result {
		return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &param))
	}
	feed(applyPatchClaudeStart(0, "c1", "apply_patch"))
	preview := feed(applyPatchClaudeFragment(0, `{"input":"*** Begin Patch\n*** Add File: a.txt\n+hello\n`))
	if len(preview) != 1 || preview[0].Get("type").String() != "response.custom_tool_call_input.delta" || preview[0].Get("delta").String() != "*** Begin Patch\n*** Add File: a.txt\n+hello\n" {
		t.Fatalf("missing real decoded preview: %v", preview)
	}
	if preview[0].Get("call_id").String() != "c1" || preview[0].Get("item_id").String() != "ctc_c1" {
		t.Fatalf("preview identity: %s", preview[0].Raw)
	}
	var events []gjson.Result
	events = append(events, preview...)
	events = append(events, feed(applyPatchClaudeFragment(0, `*** End Patch"}`))...)
	for _, chunk := range applyPatchClaudeEnd() {
		events = append(events, feed(chunk)...)
	}
	want := "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch"
	var deltas strings.Builder
	seenDone, seenItem, seenCompleted := false, false, false
	lastSequence := 0
	for _, event := range events {
		if sequence := int(event.Get("sequence_number").Int()); sequence <= lastSequence {
			t.Fatalf("non-monotonic sequence: %s", event.Raw)
		} else {
			lastSequence = sequence
		}
		switch event.Get("type").String() {
		case "response.custom_tool_call_input.delta":
			deltas.WriteString(event.Get("delta").String())
		case "response.custom_tool_call_input.done":
			seenDone = true
			if deltas.String() != want || event.Get("input").String() != want || event.Get("call_id").String() != "c1" {
				t.Fatalf("input.done mismatch: %s", event.Raw)
			}
		case "response.output_item.done":
			seenItem = true
			if !seenDone || event.Get("item.input").String() != want {
				t.Fatalf("item.done mismatch: %s", event.Raw)
			}
		case "response.completed":
			seenCompleted = true
			if !seenItem || event.Get("response.output.0.input").String() != want {
				t.Fatalf("completed mismatch: %s", event.Raw)
			}
		}
	}
	if !seenDone || !seenItem || !seenCompleted {
		t.Fatalf("missing terminal events: %v", events)
	}
}

func TestApplyPatchClaudeLateIdentityAndInterleavedCalls(t *testing.T) {
	var param any
	feed := func(chunk []byte) []gjson.Result {
		return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
	}
	feed(applyPatchClaudeStart(0, "", ""))
	for _, event := range feed(applyPatchClaudeFragment(0, `{"input":"first\n`)) {
		if strings.Contains(event.Get("type").String(), "arguments.delta") || event.Get("type").String() == "response.custom_tool_call_input.delta" {
			t.Fatalf("emitted before identity: %s", event.Raw)
		}
	}
	var events []gjson.Result
	events = append(events, feed(applyPatchClaudeStart(0, "c1", "apply_patch"))...)
	events = append(events, feed(applyPatchClaudeStart(1, "c2", "apply_patch"))...)
	events = append(events, feed(applyPatchClaudeFragment(1, `{"input":"second`))...)
	events = append(events, feed(applyPatchClaudeFragment(0, `tail"}`))...)
	events = append(events, feed(applyPatchClaudeFragment(1, ` tail"}`))...)
	for _, chunk := range applyPatchClaudeEnd() {
		events = append(events, feed(chunk)...)
	}
	inputs := map[string]string{}
	indices := map[string]int64{}
	done := map[string]string{}
	for _, event := range events {
		switch event.Get("type").String() {
		case "response.custom_tool_call_input.delta":
			id := event.Get("call_id").String()
			inputs[id] += event.Get("delta").String()
			indices[id] = event.Get("output_index").Int()
		case "response.custom_tool_call_input.done":
			done[event.Get("call_id").String()] = event.Get("input").String()
		case "response.failed":
			t.Fatalf("interleaved calls failed: %s", event.Raw)
		}
	}
	if inputs["c1"] != "first\ntail" || inputs["c2"] != "second tail" || done["c1"] != inputs["c1"] || done["c2"] != inputs["c2"] || indices["c1"] == indices["c2"] {
		t.Fatalf("mixed calls: inputs=%v done=%v indices=%v", inputs, done, indices)
	}
}

func TestApplyPatchClaudeInvalidArgumentsFailOnce(t *testing.T) {
	for _, arguments := range []string{`plain patch`, `{}`, `{"input":42}`, `{"input":"x","extra":1}`, `{"input":"x","input":"y"}`, `{"input":"x"} {}`, `{"input":"unfinished`, `{"input":"bad\q"}`, `{"input":"\ud800"}`} {
		t.Run(arguments, func(t *testing.T) {
			var param any
			var events []gjson.Result
			feed := func(chunk []byte) {
				events = append(events, applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))...)
			}
			feed(applyPatchClaudeStart(0, "c1", "apply_patch"))
			feed(applyPatchClaudeFragment(0, arguments))
			for _, chunk := range applyPatchClaudeEnd() {
				feed(chunk)
			}
			for _, chunk := range applyPatchClaudeEnd() {
				feed(chunk)
			}
			failureCount := 0
			for _, event := range events {
				switch event.Get("type").String() {
				case "response.failed":
					failureCount++
					if event.Get("response.error.code").String() != "invalid_tool_arguments" {
						t.Fatalf("wrong failure: %s", event.Raw)
					}
				case "response.completed", "response.incomplete", "response.custom_tool_call_input.done", "response.output_item.done":
					t.Fatalf("invalid arguments succeeded: %s", event.Raw)
				}
			}
			state, ok := param.(interface{ ToolInputError() error })
			if failureCount != 1 || !ok || state.ToolInputError() == nil {
				t.Fatalf("missing retained failure: count=%d state=%T", failureCount, param)
			}
		})
	}
}

func TestApplyPatchClaudeWinnerAndNamespace(t *testing.T) {
	for _, tc := range []struct{ name, request, upstream, wantType, wantName, namespace string }{
		{"function", `{"tools":[{"type":"function","name":"apply_patch"}]}`, "apply_patch", "function_call", "apply_patch", ""},
		{"function wins", `{"tools":[{"type":"function","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch", "function_call", "apply_patch", ""},
		{"custom wins", `{"tools":[{"type":"custom","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"apply_patch"}]}]}`, "apply_patch", "custom_tool_call", "apply_patch", ""},
		{"namespace", `{"tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "editor__apply_patch", "custom_tool_call", "apply_patch", "editor"},
		{"flat collision", `{"tools":[{"type":"function","name":"editor__apply_patch"},{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "editor__apply_patch", "function_call", "editor__apply_patch", ""},
		{"same source custom first", `{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"apply_patch"}]}`, "apply_patch", "custom_tool_call", "apply_patch", ""},
		{"same source function first", `{"tools":[{"type":"function","name":"apply_patch"},{"type":"custom","name":"apply_patch"}]}`, "apply_patch", "function_call", "apply_patch", ""},
		{"namespace before flat", `{"tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"editor__apply_patch"}]}`, "editor__apply_patch", "function_call", "editor__apply_patch", ""},
		{"sanitized namespace", `{"tools":[{"type":"namespace","name":"mcp.editor","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "mcp_editor__apply_patch", "custom_tool_call", "apply_patch", "mcp.editor"},
		{"other custom", `{"tools":[{"type":"custom","name":"edit"}]}`, "edit", "custom_tool_call", "edit", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var param any
			request := []byte(tc.request)
			feed := func(chunk []byte) []gjson.Result {
				return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &param))
			}
			feed(applyPatchClaudeStart(0, "c1", tc.upstream))
			args := `{"input":"x"}`
			if tc.wantType == "function_call" || tc.name == "other custom" {
				args = `{"not_input":42}`
			}
			events := feed(applyPatchClaudeFragment(0, args))
			for _, chunk := range applyPatchClaudeEnd() {
				events = append(events, feed(chunk)...)
			}
			var item gjson.Result
			for _, event := range events {
				if event.Get("type").String() == "response.completed" {
					item = event.Get("response.output.0")
				}
			}
			if item.Get("type").String() != tc.wantType || item.Get("name").String() != tc.wantName || item.Get("namespace").String() != tc.namespace {
				t.Fatalf("wrong winning identity: %s", item.Raw)
			}
			if tc.wantType == "function_call" && item.Get("arguments").String() != args {
				t.Fatalf("ordinary function was unwrapped: %s", item.Raw)
			}
			if tc.name == "other custom" && item.Get("input").String() != args {
				t.Fatalf("other custom changed: %s", item.Raw)
			}
		})
	}
}

func TestApplyPatchClaudeNonStreamStrictInput(t *testing.T) {
	for _, tc := range []struct {
		arguments, want string
		invalid         bool
	}{
		{`{"input":"*** Begin Patch\n*** End Patch"}`, "*** Begin Patch\n*** End Patch", false},
		{`{"input":12}`, "", true}, {`{"input":"truncated`, "", true}, {`{"input":"x","extra":true}`, "", true}, {`{"input":"\ud800"}`, "", true},
	} {
		t.Run(tc.arguments, func(t *testing.T) {
			var param any
			raw := []byte(string(applyPatchClaudeStart(0, "c1", "apply_patch")) + "\n" + string(applyPatchClaudeFragment(0, tc.arguments)) + "\n" + `data: {"type":"message_stop"}`)
			result := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, raw, &param))
			if tc.invalid {
				state, ok := param.(interface{ ToolInputError() error })
				if result.Get("status").String() != "failed" || result.Get("error.code").String() != "invalid_tool_arguments" || !ok || state.ToolInputError() == nil {
					t.Fatalf("invalid non-stream input succeeded: %s state=%T", result.Raw, param)
				}
			} else if result.Get("output.0.input").String() != tc.want {
				t.Fatalf("non-stream input mismatch: %s", result.Raw)
			}
		})
	}
}

func TestApplyPatchClaudeTerminalValidatesTruncatedCall(t *testing.T) {
	var param any
	feed := func(chunk []byte) []gjson.Result {
		return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
	}
	feed(applyPatchClaudeStart(0, "c1", "apply_patch"))
	feed(applyPatchClaudeFragment(0, `{"input":"unfinished`))
	chunks := applyPatchClaudeEnd()
	events := feed(chunks[len(chunks)-1])
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatalf("truncated terminal did not fail: %v", events)
	}
}

func TestApplyPatchClaudeUnicodeFragments(t *testing.T) {
	arguments := `{"input":"line\n\u4f60\u597d \ud83d\ude00 \" \\ \u96ea"}`
	for split := 1; split < len(arguments); split++ {
		var param any
		feed := func(chunk []byte) []gjson.Result {
			return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
		}
		feed(applyPatchClaudeStart(0, "c1", "apply_patch"))
		events := feed(applyPatchClaudeFragment(0, arguments[:split]))
		events = append(events, feed(applyPatchClaudeFragment(0, arguments[split:]))...)
		for _, chunk := range applyPatchClaudeEnd() {
			events = append(events, feed(chunk)...)
		}
		var input strings.Builder
		for _, event := range events {
			if event.Get("type").String() == "response.custom_tool_call_input.delta" {
				input.WriteString(event.Get("delta").String())
			}
		}
		if input.String() != "line\n你好 😀 \" \\ 雪" {
			t.Fatalf("split %d: input = %q", split, input.String())
		}
	}
}

func TestApplyPatchClaudeIdentityFieldsArriveSeparately(t *testing.T) {
	for _, tc := range []struct{ id, name string }{{"c1", ""}, {"", "apply_patch"}} {
		var param any
		feed := func(chunk []byte) []gjson.Result {
			return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
		}
		feed(applyPatchClaudeStart(0, tc.id, tc.name))
		events := feed(applyPatchClaudeFragment(0, `{"input":"preview`))
		if len(events) != 0 {
			t.Fatalf("preview without complete identity: %v", events)
		}
		events = feed(applyPatchClaudeStart(0, "c1", "apply_patch"))
		found := false
		for _, event := range events {
			if event.Get("type").String() == "response.custom_tool_call_input.delta" {
				found = event.Get("delta").String() == "preview" && event.Get("call_id").String() == "c1"
			}
		}
		if !found {
			t.Fatalf("buffer not released after identity: %v", events)
		}
	}
}

func TestApplyPatchClaudeConflictingIdentityFails(t *testing.T) {
	for _, tc := range []struct{ id, name string }{{"other", "apply_patch"}, {"c1", "other"}} {
		var param any
		feed := func(chunk []byte) []gjson.Result {
			return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
		}
		feed(applyPatchClaudeStart(0, "c1", "apply_patch"))
		feed(applyPatchClaudeFragment(0, `{"input":"preview`))
		events := feed(applyPatchClaudeStart(0, tc.id, tc.name))
		for _, chunk := range applyPatchClaudeEnd() {
			events = append(events, feed(chunk)...)
		}
		if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
			t.Fatalf("conflicting identity did not fail once: %v", events)
		}
	}
}

func TestApplyPatchClaudeNonStreamOriginalDeclarationWins(t *testing.T) {
	original := []byte(`{"tools":[{"type":"function","name":"apply_patch"}]}`)
	var param any
	raw := []byte(string(applyPatchClaudeStart(0, "c1", "apply_patch")) + "\n" + string(applyPatchClaudeFragment(0, `{"not_input":42}`)) + "\n" + `data: {"type":"message_stop"}`)
	result := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", original, []byte(applyPatchClaudeRequest), raw, &param))
	if result.Get("output.0.type").String() != "function_call" || result.Get("output.0.arguments").String() != `{"not_input":42}` {
		t.Fatalf("converted declaration overrode original: %s", result.Raw)
	}
}

func TestApplyPatchClaudeSnapshotsValidateAndDoNotPreview(t *testing.T) {
	for _, tc := range []struct {
		name, fragment, snapshot, want string
		invalid                        bool
	}{
		{"snapshot only", "", `{"input":"whole"}`, "whole", false},
		{"matching suffix", `{"input":"pre`, `{"input":"prefix"}`, "prefix", false},
		{"conflict", `{"input":"wrong`, `{"input":"prefix"}`, "", true},
		{"completed conflict", `{"input":"pre"}`, `{"input":"prefix"}`, "", true},
		{"invalid snapshot", `{"input":"pre`, `{"input":7}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var param any
			request := []byte(applyPatchClaudeRequest)
			feed := func(chunk []byte) []gjson.Result {
				return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &param))
			}
			start := applyPatchClaudeStart(0, "c1", "apply_patch")
			events := feed(start)
			var raw []byte
			raw = append(raw, start...)
			raw = append(raw, '\n')
			if tc.fragment != "" {
				chunk := applyPatchClaudeFragment(0, tc.fragment)
				events = append(events, feed(chunk)...)
				raw = append(raw, chunk...)
				raw = append(raw, '\n')
			}
			snapshot := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"apply_patch","input":%s}}`, tc.snapshot))
			snapshotEvents := feed(snapshot)
			for _, event := range snapshotEvents {
				if event.Get("type").String() == "response.custom_tool_call_input.delta" {
					t.Fatalf("snapshot fabricated a preview: %s", event.Raw)
				}
			}
			events = append(events, snapshotEvents...)
			raw = append(raw, snapshot...)
			raw = append(raw, '\n')
			for _, chunk := range applyPatchClaudeEnd() {
				events = append(events, feed(chunk)...)
				raw = append(raw, chunk...)
				raw = append(raw, '\n')
			}
			var terminal gjson.Result
			var input strings.Builder
			for _, event := range events {
				if event.Get("type").String() == "response.custom_tool_call_input.delta" {
					input.WriteString(event.Get("delta").String())
				}
				if event.Get("type").String() == "response.failed" || event.Get("type").String() == "response.completed" {
					terminal = event
				}
			}
			var nonStreamParam any
			nonStream := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", request, nil, raw, &nonStreamParam))
			if tc.invalid {
				if terminal.Get("type").String() != "response.failed" || nonStream.Get("status").String() != "failed" {
					t.Fatalf("conflicting/invalid snapshot succeeded: stream=%s nonstream=%s", terminal.Raw, nonStream.Raw)
				}
			} else if input.String() != tc.want || terminal.Get("response.output.0.input").String() != tc.want || nonStream.Get("output.0.input").String() != tc.want {
				t.Fatalf("snapshot mismatch: deltas=%q stream=%s nonstream=%s", input.String(), terminal.Raw, nonStream.Raw)
			}
		})
	}
}

func TestApplyPatchClaudeNonStreamConflictingIdentityFails(t *testing.T) {
	var param any
	raw := []byte(string(applyPatchClaudeStart(0, "c1", "apply_patch")) + "\n" + string(applyPatchClaudeFragment(0, `{"input":"x"}`)) + "\n" + string(applyPatchClaudeStart(0, "c2", "apply_patch")) + "\n" + `data: {"type":"message_stop"}`)
	result := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, raw, &param))
	state, ok := param.(interface{ ToolInputError() error })
	if result.Get("status").String() != "failed" || !ok || state.ToolInputError() == nil {
		t.Fatalf("conflicting non-stream identity succeeded: %s", result.Raw)
	}
}

func TestApplyPatchClaudeConflictingSnapshotsFail(t *testing.T) {
	var param any
	request := []byte(applyPatchClaudeRequest)
	var raw []byte
	var events []gjson.Result
	for _, input := range []string{`{"input":"first"}`, `{"input":"replacement"}`} {
		chunk := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"apply_patch","input":%s}}`, input))
		raw = append(raw, chunk...)
		raw = append(raw, '\n')
		events = append(events, applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &param))...)
	}
	for _, chunk := range applyPatchClaudeEnd() {
		raw = append(raw, chunk...)
		raw = append(raw, '\n')
		events = append(events, applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &param))...)
	}
	failures := 0
	for _, event := range events {
		if event.Get("type").String() == "response.failed" {
			failures++
		}
		if event.Get("type").String() == "response.completed" || event.Get("type").String() == "response.custom_tool_call_input.done" {
			t.Fatalf("silently replaced snapshot: %s", event.Raw)
		}
	}
	var nonStreamParam any
	result := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", request, nil, raw, &nonStreamParam))
	if failures != 1 || result.Get("status").String() != "failed" {
		t.Fatalf("snapshot replacement did not fail: stream failures=%d nonstream=%s", failures, result.Raw)
	}
}

func TestApplyPatchClaudePendingIdentityConflictFails(t *testing.T) {
	var param any
	feed := func(chunk []byte) []gjson.Result {
		return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
	}
	feed(applyPatchClaudeStart(0, "c1", ""))
	feed(applyPatchClaudeFragment(0, `{"input":"x"}`))
	events := feed(applyPatchClaudeStart(0, "c2", "apply_patch"))
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatalf("pending ID silently replaced: %v", events)
	}
}

func TestApplyPatchClaudeMissingIDSynthesizedAtTerminal(t *testing.T) {
	var param any
	feed := func(chunk []byte) []gjson.Result {
		return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
	}
	feed(applyPatchClaudeStart(0, "", "apply_patch"))
	feed(applyPatchClaudeFragment(0, `{"input":"x"}`))
	var events []gjson.Result
	for _, chunk := range applyPatchClaudeEnd() {
		events = append(events, feed(chunk)...)
	}
	var completed gjson.Result
	for _, event := range events {
		if event.Get("type").String() == "response.completed" {
			completed = event
		}
	}
	item := completed.Get("response.output.0")
	if item.Get("type").String() != "custom_tool_call" || item.Get("input").String() != "x" || item.Get("call_id").String() == "" {
		t.Fatalf("missing ID lost patch: %s", completed.Raw)
	}
}

func TestApplyPatchClaudeNonStreamLateIdentity(t *testing.T) {
	var param any
	raw := []byte(string(applyPatchClaudeStart(0, "", "apply_patch")) + "\n" + string(applyPatchClaudeFragment(0, `{"input":"x"}`)) + "\n" + string(applyPatchClaudeStart(0, "c1", "")) + "\n" + `data: {"type":"message_stop"}`)
	result := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, raw, &param))
	item := result.Get("output.0")
	if item.Get("type").String() != "custom_tool_call" || item.Get("input").String() != "x" || item.Get("id").String() != "ctc_c1" {
		t.Fatalf("late ID/name lost: %s", result.Raw)
	}
}

func assertApplyPatchClaudeDeferredFailure(t *testing.T, events []gjson.Result, param any) {
	t.Helper()
	failures := 0
	for _, event := range events {
		switch event.Get("type").String() {
		case "response.failed":
			failures++
			if event.Get("response.error.code").String() != "invalid_tool_arguments" {
				t.Fatalf("wrong failure: %s", event.Raw)
			}
		case "response.completed", "response.incomplete", "response.custom_tool_call_input.done", "response.output_item.done":
			t.Fatalf("invalid pending evidence succeeded: %s", event.Raw)
		}
	}
	state := param.(interface{ ToolInputError() error })
	if failures != 1 || state.ToolInputError() == nil {
		t.Fatalf("missing retained failure: count=%d error=%v", failures, state.ToolInputError())
	}
}

func TestApplyPatchClaudeDeferredIdentityConflict(t *testing.T) {
	for _, tc := range []struct {
		name, request, resolvedID, resolvedName, wantType string
		invalid                                           bool
	}{
		{"keep second ID", applyPatchClaudeRequest, "c2", "apply_patch", "", true},
		{"return to first ID", applyPatchClaudeRequest, "c1", "apply_patch", "", true},
		{"omit final ID", applyPatchClaudeRequest, "", "apply_patch", "", true},
		{"ordinary function", `{"tools":[{"type":"function","name":"apply_patch"}]}`, "c2", "apply_patch", "function_call", false},
		{"function winner", `{"tools":[{"type":"function","name":"apply_patch"},{"type":"custom","name":"apply_patch"}]}`, "c2", "apply_patch", "function_call", false},
		{"other custom", `{"tools":[{"type":"custom","name":"edit"}]}`, "c2", "edit", "custom_tool_call", false},
	} {
		chunks := [][]byte{applyPatchClaudeStart(0, "c1", ""), applyPatchClaudeStart(0, "c2", ""), applyPatchClaudeStart(0, tc.resolvedID, tc.resolvedName), applyPatchClaudeFragment(0, `{"input":"x"}`)}
		chunks = append(chunks, applyPatchClaudeEnd()...)
		for _, stream := range []bool{true, false} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var param any
				var result gjson.Result
				if stream {
					var events []gjson.Result
					for i, chunk := range chunks {
						current := applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(tc.request), nil, chunk, &param))
						if i < 2 && len(current) != 0 {
							t.Fatalf("unclassified identity emitted events: %v", current)
						}
						events = append(events, current...)
					}
					if tc.invalid {
						assertApplyPatchClaudeDeferredFailure(t, events, param)
						return
					}
					for _, event := range events {
						if event.Get("type").String() == "response.completed" {
							result = event.Get("response")
						}
					}
				} else {
					result = gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", []byte(tc.request), nil, bytes.Join(chunks, []byte("\n")), &param))
					if tc.invalid {
						if result.Get("status").String() != "failed" || result.Get("error.code").String() != "invalid_tool_arguments" || param.(interface{ ToolInputError() error }).ToolInputError() == nil {
							t.Fatalf("lost pending conflict: %s", result.Raw)
						}
						return
					}
				}
				item := result.Get("output.0")
				if result.Get("status").String() != "completed" || item.Get("type").String() != tc.wantType || item.Get("call_id").String() != "c2" || param.(interface{ ToolInputError() error }).ToolInputError() != nil {
					t.Fatalf("unrelated tool changed: %s", result.Raw)
				}
				if tc.wantType == "function_call" && item.Get("arguments").String() != `{"input":"x"}` {
					t.Fatalf("ordinary arguments changed: %s", item.Raw)
				}
				if tc.wantType == "custom_tool_call" && item.Get("input").String() != "x" {
					t.Fatalf("other custom input changed: %s", item.Raw)
				}
			})
		}
	}
}

func TestApplyPatchClaudeDeferredSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, fragment, toolName, request, want string
		invalid                                                bool
	}{
		{name: "different complete snapshots", first: `{"input":"first"}`, second: `{"input":"replacement"}`, invalid: true},
		{name: "complete snapshot cannot extend", first: `{"input":"pre"}`, second: `{"input":"prefix"}`, invalid: true},
		{name: "early wrong type", first: `{"input":42}`, second: `{"input":"replacement"}`, invalid: true},
		{name: "early extra field", first: `{"input":"first","extra":1}`, second: `{"input":"replacement"}`, invalid: true},
		{name: "early duplicate key", first: `{"input":"first","input":"replacement"}`, second: `{"input":"replacement"}`, invalid: true},
		{name: "early invalid surrogate", first: `{"input":"\ud800"}`, second: `{"input":"replacement"}`, invalid: true},
		{name: "early invalid then placeholder", first: `{"input":null}`, second: `{}`, fragment: `{"input":"replacement"}`, invalid: true},
		{name: "early invalid then equal decoded input", first: `{"input":"first","extra":1}`, second: `{"input":"first"}`, invalid: true},
		{name: "equivalent complete snapshots", first: `{"input":"\u0078\n"}`, second: ` { "input" : "x\n" } `, want: "x\n"},
		{name: "single snapshot and placeholders", first: `{"input":"whole"}`, second: `{}`, want: "whole"},
		{name: "partial source extension", first: `{}`, second: `{"input":"prefix"}`, fragment: `{"input":"pre`, want: "prefix"},
		{name: "complete source conflict", first: `{}`, second: `{"input":"prefix"}`, fragment: `{"input":"pre"}`, invalid: true},
		{name: "ordinary placeholders", first: `{}`, second: `{}`, fragment: `{"input":"x"}`, want: "x"},
		{name: "ordinary function ignores patch evidence", first: `{"input":42}`, second: `{"input":"replacement"}`, fragment: `{"other":42}`, request: `{"tools":[{"type":"function","name":"apply_patch"}]}`, want: `{"other":42}`},
		{name: "other custom ignores patch evidence", first: `{"input":42}`, second: `{"input":"replacement"}`, fragment: `{"other":42}`, toolName: "edit", request: `{"tools":[{"type":"custom","name":"edit"}]}`, want: `{"other":42}`},
	} {
		request, toolName := tc.request, tc.toolName
		if request == "" {
			request = applyPatchClaudeRequest
		}
		if toolName == "" {
			toolName = "apply_patch"
		}
		snapshot := func(input string) []byte {
			return []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"","input":%s}}`, input))
		}
		chunks := [][]byte{snapshot(tc.first)}
		if tc.fragment != "" {
			chunks = append(chunks, applyPatchClaudeFragment(0, tc.fragment))
		}
		chunks = append(chunks, snapshot(tc.second), applyPatchClaudeStart(0, "c1", toolName))
		chunks = append(chunks, applyPatchClaudeEnd()...)
		for _, stream := range []bool{true, false} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var param any
				var result gjson.Result
				if stream {
					var events []gjson.Result
					var deltas strings.Builder
					for i, chunk := range chunks {
						current := applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(request), nil, chunk, &param))
						if i < len(chunks)-3 && len(current) != 0 {
							t.Fatalf("unclassified snapshots fabricated output: %v", current)
						}
						events = append(events, current...)
					}
					if tc.invalid {
						assertApplyPatchClaudeDeferredFailure(t, events, param)
						return
					}
					for _, event := range events {
						if event.Get("type").String() == "response.custom_tool_call_input.delta" {
							deltas.WriteString(event.Get("delta").String())
						}
						if event.Get("type").String() == "response.completed" {
							result = event.Get("response")
						}
					}
					if tc.request == "" && deltas.String() != tc.want {
						t.Fatalf("snapshot/partial input mismatch: delta=%q want=%q", deltas.String(), tc.want)
					}
				} else {
					result = gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", []byte(request), nil, bytes.Join(chunks, []byte("\n")), &param))
					if tc.invalid {
						if result.Get("status").String() != "failed" || result.Get("error.code").String() != "invalid_tool_arguments" || param.(interface{ ToolInputError() error }).ToolInputError() == nil {
							t.Fatalf("lost pending snapshot evidence: %s", result.Raw)
						}
						return
					}
				}
				field := "output.0.input"
				if tc.request != "" && toolName == "apply_patch" {
					field = "output.0.arguments"
				}
				if result.Get("status").String() != "completed" || result.Get(field).String() != tc.want || param.(interface{ ToolInputError() error }).ToolInputError() != nil {
					t.Fatalf("pending snapshots changed valid/unrelated input: %s", result.Raw)
				}
			})
		}
	}
}

func TestApplyPatchClaudeSnapshotAfterItemCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, snapshot string
		invalid        bool
	}{
		{"conflicting complete snapshot", `{"input":"y"}`, true},
		{"complete snapshot cannot extend finished input", `{"input":"xy"}`, true},
		{"equivalent reencoded snapshot", ` { "input" : "\u0078" } `, false},
		{"placeholder after item completion", `{}`, false},
	} {
		// Reproduce the six-step sequence with a second, still-open patch call.
		chunks := [][]byte{
			applyPatchClaudeStart(0, "c1", "apply_patch"),
			applyPatchClaudeFragment(0, `{"input":"x"}`),
			[]byte(`data: {"type":"content_block_stop","index":0}`),
			applyPatchClaudeStart(1, "c2", "apply_patch"),
			applyPatchClaudeFragment(1, `{"input":"ok"}`),
			[]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"apply_patch","input":%s}}`, tc.snapshot)),
		}
		chunks = append(chunks, applyPatchClaudeEnd()...)
		for _, stream := range []bool{true, false} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				request := []byte(applyPatchClaudeRequest)
				var param any
				var result gjson.Result
				if stream {
					feed := func(chunk []byte) []gjson.Result {
						return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &param))
					}
					var events []gjson.Result
					for _, chunk := range chunks[:5] {
						events = append(events, feed(chunk)...)
					}
					inputDone, itemDone := 0, 0
					var preview strings.Builder
					for _, event := range events {
						switch event.Get("type").String() {
						case "response.custom_tool_call_input.delta":
							if event.Get("call_id").String() == "c1" {
								preview.WriteString(event.Get("delta").String())
							}
						case "response.custom_tool_call_input.done":
							inputDone++
							if event.Get("call_id").String() != "c1" || event.Get("input").String() != "x" {
								t.Fatalf("wrong early input completion: %s", event.Raw)
							}
						case "response.output_item.done":
							itemDone++
							if event.Get("item.call_id").String() != "c1" || event.Get("item.input").String() != "x" {
								t.Fatalf("wrong early item completion: %s", event.Raw)
							}
						case "response.completed", "response.incomplete", "response.failed":
							t.Fatalf("response ended before the late snapshot: %s", event.Raw)
						}
					}
					st := param.(*claudeToResponsesState)
					if inputDone != 1 || itemDone != 1 || preview.String() != "x" || !st.FuncItemDone[0] || st.FuncInputSnapshot[0] != "" {
						t.Fatalf("first call was not finalized from real arguments without a snapshot: inputDone=%d itemDone=%d preview=%q", inputDone, itemDone, preview.String())
					}
					snapshotEvents := feed(chunks[5])
					if !tc.invalid && len(snapshotEvents) != 0 {
						t.Fatalf("late snapshot fabricated events: %v", snapshotEvents)
					}
					after := append([]gjson.Result(nil), snapshotEvents...)
					for _, chunk := range chunks[6:] {
						after = append(after, feed(chunk)...)
					}
					if tc.invalid {
						assertApplyPatchClaudeDeferredFailure(t, after, param)
						if len(snapshotEvents) != 1 || snapshotEvents[0].Get("type").String() != "response.failed" {
							t.Fatalf("late conflicting snapshot was not rejected immediately: %v", snapshotEvents)
						}
						return
					}
					completions := 0
					for _, event := range after {
						switch event.Get("type").String() {
						case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
							if event.Get("call_id").String() != "c2" {
								t.Fatalf("finalized first call emitted duplicate input events: %s", event.Raw)
							}
						case "response.output_item.done":
							if event.Get("item.call_id").String() != "c2" {
								t.Fatalf("finalized first call emitted duplicate item completion: %s", event.Raw)
							}
						case "response.completed":
							completions++
							result = event.Get("response")
						}
					}
					if completions != 1 {
						t.Fatalf("expected one successful response completion, got %d", completions)
					}
				} else {
					result = gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", request, nil, bytes.Join(chunks, []byte("\n")), &param))
					if tc.invalid {
						if result.Get("status").String() != "failed" || result.Get("error.code").String() != "invalid_tool_arguments" || param.(interface{ ToolInputError() error }).ToolInputError() == nil {
							t.Fatalf("late conflicting snapshot succeeded: %s", result.Raw)
						}
						return
					}
				}
				if result.Get("status").String() != "completed" || result.Get("output.0.call_id").String() != "c1" || result.Get("output.0.input").String() != "x" || result.Get("output.1.call_id").String() != "c2" || result.Get("output.1.input").String() != "ok" || param.(interface{ ToolInputError() error }).ToolInputError() != nil {
					t.Fatalf("late matching snapshot changed successful input: %s", result.Raw)
				}
			})
		}
	}
}

func TestApplyPatchClaudeSuccessfulTerminalSealsState(t *testing.T) {
	for _, tc := range []struct {
		name string
		late []byte
	}{
		{"duplicate message_stop", []byte(`data: {"type":"message_stop"}`)},
		{"post-terminal fragment", applyPatchClaudeFragment(0, `{"input":"late"}`)},
		{"post-terminal conflicting snapshot", []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"apply_patch","input":{"input":"y"}}}`)},
	} {
		chunks := [][]byte{applyPatchClaudeStart(0, "c1", "apply_patch"), applyPatchClaudeFragment(0, `{"input":"x"}`)}
		chunks = append(chunks, applyPatchClaudeEnd()...)
		for _, stream := range []bool{true, false} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var param any
				if stream {
					feed := func(chunk []byte) []gjson.Result {
						return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, chunk, &param))
					}
					completions := 0
					for _, chunk := range chunks {
						for _, event := range feed(chunk) {
							if event.Get("type").String() == "response.completed" {
								completions++
								if event.Get("response.output.0.input").String() != "x" {
									t.Fatalf("incorrect completion: %s", event.Raw)
								}
							}
						}
					}
					if completions != 1 {
						t.Fatalf("expected one successful terminal: count=%d", completions)
					}
					for _, chunk := range [][]byte{tc.late, []byte(`data: {"type":"message_stop"}`)} {
						if late := feed(chunk); len(late) != 0 {
							t.Fatalf("sealed response emitted more events: %v", late)
						}
					}
				} else {
					raw := bytes.Join(chunks, []byte("\n"))
					raw = append(raw, '\n')
					raw = append(raw, tc.late...)
					result := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "test", []byte(applyPatchClaudeRequest), nil, raw, &param))
					if result.Get("status").String() != "completed" || result.Get("output.0.input").String() != "x" {
						t.Fatalf("successful terminal became a failure: %s", result.Raw)
					}
				}
				if errToolInputError := param.(interface{ ToolInputError() error }).ToolInputError(); errToolInputError != nil {
					t.Fatalf("successful terminal retained a failure: %v", errToolInputError)
				}
			})
		}
	}
}
