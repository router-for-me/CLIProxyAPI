package chat_completions

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToInteractionsStreamUsageOnlyTerminalChunk(t *testing.T) {
	var param any
	finishRaw := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	usageRaw := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	doneRaw := []byte(`data: [DONE]`)

	finishOut := ConvertOpenAIResponseToInteractions(context.Background(), "gpt-test", nil, nil, finishRaw, &param)
	usageOut := ConvertOpenAIResponseToInteractions(context.Background(), "gpt-test", nil, nil, usageRaw, &param)
	doneOut := ConvertOpenAIResponseToInteractions(context.Background(), "gpt-test", nil, nil, doneRaw, &param)

	if got := countInteractionsEvents(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countInteractionsEvents(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1", got)
	}
	if got := countInteractionsEvents(doneOut, "interaction.completed"); got != 0 {
		t.Fatalf("done interaction.completed count = %d, want 0", got)
	}
	if got := countInteractionsEvents(doneOut, "done"); got != 1 {
		t.Fatalf("done event count = %d, want 1", got)
	}
	payload := findInteractionsEventPayload(usageOut, "interaction.completed")
	if got := gjson.GetBytes(payload, "interaction.usage.total_input_tokens").Int(); got != 3 {
		t.Fatalf("total_input_tokens = %d, want 3. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "interaction.usage.total_output_tokens").Int(); got != 4 {
		t.Fatalf("total_output_tokens = %d, want 4. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "interaction.usage.total_tokens").Int(); got != 7 {
		t.Fatalf("total_tokens = %d, want 7. Payload: %s", got, string(payload))
	}
}

func TestConvertOpenAIResponseToInteractionsCompletesOnDoneWithoutUsage(t *testing.T) {
	var param any
	finishRaw := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	doneRaw := []byte(`data: [DONE]`)

	finishOut := ConvertOpenAIResponseToInteractions(context.Background(), "gpt-test", nil, nil, finishRaw, &param)
	doneOut := ConvertOpenAIResponseToInteractions(context.Background(), "gpt-test", nil, nil, doneRaw, &param)

	if got := countInteractionsEvents(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countInteractionsEvents(doneOut, "interaction.completed"); got != 1 {
		t.Fatalf("done interaction.completed count = %d, want 1", got)
	}
	if got := countInteractionsEvents(doneOut, "done"); got != 1 {
		t.Fatalf("done event count = %d, want 1", got)
	}
}

func TestConvertOpenAIResponseToInteractionsStreamCreatedUsesChunkIdentity(t *testing.T) {
	var param any
	raw := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`)
	out := ConvertOpenAIResponseToInteractions(context.Background(), "", nil, nil, raw, &param)
	payload := findInteractionsEventPayload(out, "interaction.created")
	if got := gjson.GetBytes(payload, "interaction.id").String(); got != "chatcmpl_1" {
		t.Fatalf("interaction.id = %q, want chatcmpl_1. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "interaction.model").String(); got != "gpt-test" {
		t.Fatalf("interaction.model = %q, want gpt-test. Payload: %s", got, string(payload))
	}
}

func TestConvertOpenAIResponseToInteractionsNonStreamDirectToolCall(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	out := ConvertOpenAIResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.type").String(); got != "function_call" {
		t.Fatalf("step type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.0.id").String(); got != "call_1" {
		t.Fatalf("id = %q, want call_1. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "steps.0.call_id").Exists() {
		t.Fatalf("steps.0 should not have call_id parameter. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "steps.0.arguments.q").String(); got != "x" {
		t.Fatalf("arguments.q = %q, want x. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponseToInteractionsStreamToolCall(t *testing.T) {
	var param any
	raw := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`)
	out := ConvertOpenAIResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)
	payload := findInteractionsEventPayload(out, "step.start")
	if got := gjson.GetBytes(payload, "step.type").String(); got != "function_call" {
		t.Fatalf("step.type = %q, want function_call. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "step.id").String(); got != "call_1" {
		t.Fatalf("step.id = %q, want call_1. Payload: %s", got, string(payload))
	}
	if gjson.GetBytes(payload, "step.call_id").Exists() {
		t.Fatalf("step.call_id must be omitted in stream. Payload: %s", string(payload))
	}
	if got := gjson.GetBytes(payload, "step.name").String(); got != "lookup" {
		t.Fatalf("step.name = %q, want lookup. Payload: %s", got, string(payload))
	}
}

func TestConvertInteractionsResponseToOpenAIStreamToolCall(t *testing.T) {
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"gemini-3.1-flash-lite"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"get_weather","arguments":{}}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"location\":\"北京\"}"}}`),
		[]byte(`data: {"event_type":"step.stop","index":0}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"requires_action","usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5}}}`),
	}
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAI(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	toolStart := findOpenAIChatChunk(out, "choices.0.delta.tool_calls.0.function.name")
	if got := gjson.GetBytes(toolStart, "choices.0.delta.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("tool call id = %q, want call_1. Payload: %s", got, string(toolStart))
	}
	if got := gjson.GetBytes(toolStart, "choices.0.delta.tool_calls.0.function.name").String(); got != "get_weather" {
		t.Fatalf("tool name = %q, want get_weather. Payload: %s", got, string(toolStart))
	}
	toolArgs := findOpenAIChatChunkValue(out, "choices.0.delta.tool_calls.0.function.arguments", `{"location":"北京"}`)
	if got := gjson.GetBytes(toolArgs, "choices.0.delta.tool_calls.0.function.arguments").String(); got != `{"location":"北京"}` {
		t.Fatalf("tool args = %q, want location JSON. Payload: %s", got, string(toolArgs))
	}
	completed := findOpenAIChatChunkValue(out, "choices.0.finish_reason", "tool_calls")
	if got := gjson.GetBytes(completed, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "usage.prompt_tokens").Int(); got != 2 {
		t.Fatalf("prompt_tokens = %d, want 2. Payload: %s", got, string(completed))
	}
}

func TestConvertInteractionsResponseToOpenAIStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToOpenAI(context.Background(), "gpt-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_cached_tokens":1,"total_tokens":11}}}`), &param)
	completed := findOpenAIChatChunkValue(out, "choices.0.finish_reason", "stop")
	if len(completed) == 0 {
		t.Fatalf("completion chunk not found")
	}
	if got := gjson.GetBytes(completed, "usage.prompt_tokens").Int(); got != 2 {
		t.Fatalf("prompt_tokens = %d, want 2. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "usage.completion_tokens").Int(); got != 6 {
		t.Fatalf("completion_tokens = %d, want 6. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "usage.completion_tokens_details.reasoning_tokens").Int(); got != 3 {
		t.Fatalf("reasoning_tokens = %d, want 3. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "usage.prompt_tokens_details.cached_tokens").Int(); got != 1 {
		t.Fatalf("cached_tokens = %d, want 1. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "usage.total_tokens").Int(); got != 11 {
		t.Fatalf("total_tokens = %d, want 11. Payload: %s", got, string(completed))
	}
}

func TestConvertInteractionsResponseToOpenAINonStreamToolCall(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"gemini-3.1-flash-lite","steps":[{"type":"function_call","id":"call_1","name":"get_weather","arguments":{"location":"北京"}}],"usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5}}`)
	out := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("tool call id = %q, want call_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "get_weather" {
		t.Fatalf("tool name = %q, want get_weather. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"location":"北京"}` {
		t.Fatalf("tool args = %q, want location JSON. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToOpenAINonStream_PreservesEnvironmentID(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"antigravity-preview-05-2026","environment_id":"env_chat123","steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}],"usage":{"total_tokens":5}}`)
	out := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "antigravity-preview-05-2026", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "environment_id").String(); got != "env_chat123" {
		t.Fatalf("environment_id = %q, want env_chat123. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToOpenAIStream_PreservesEnvironmentID(t *testing.T) {
	var param any
	chunk := []byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"antigravity-preview-05-2026","environment_id":"env_chat_stream456"}}`)
	out := ConvertInteractionsResponseToOpenAI(context.Background(), "antigravity-preview-05-2026", nil, nil, chunk, &param)
	if len(out) == 0 {
		t.Fatalf("no output chunks generated")
	}
	if got := gjson.GetBytes(out[0], "environment_id").String(); got != "env_chat_stream456" {
		t.Fatalf("environment_id = %q, want env_chat_stream456. Chunk: %s", got, string(out[0]))
	}
}

func TestConvertInteractionsResponseToOpenAINonStreamRestoresAntigravityToolName(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"antigravity-preview-05-2026","steps":[{"type":"function_call","id":"call_1","name":"external_read_file","arguments":{"path":"/etc/hosts"}}],"usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5}}`)
	out := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "antigravity-preview-05-2026", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "read_file" {
		t.Fatalf("tool name = %q, want read_file (restored from external_read_file). Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToOpenAIStreamRestoresAntigravityToolName(t *testing.T) {
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"antigravity-preview-05-2026"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"external_read_file","arguments":{}}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"/etc/hosts\"}"}}`),
		[]byte(`data: {"event_type":"step.stop","index":0}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"requires_action"}}`),
	}
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAI(context.Background(), "antigravity-preview-05-2026", nil, nil, chunk, &param)...)
	}
	toolStart := findOpenAIChatChunk(out, "choices.0.delta.tool_calls.0.function.name")
	if got := gjson.GetBytes(toolStart, "choices.0.delta.tool_calls.0.function.name").String(); got != "read_file" {
		t.Fatalf("stream tool name = %q, want read_file (restored from external_read_file). Payload: %s", got, string(toolStart))
	}
}

func TestConvertInteractionsResponseToOpenAIPreservesNonCollidingAndNonAntigravityNames(t *testing.T) {
	rawNonColliding := []byte(`{"id":"i1","model":"antigravity-preview-05-2026","steps":[{"type":"function_call","id":"call_1","name":"external_lookup","arguments":{"q":"test"}}]}`)
	outNonColliding := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "antigravity-preview-05-2026", nil, nil, rawNonColliding, nil)
	if got := gjson.GetBytes(outNonColliding, "choices.0.message.tool_calls.0.function.name").String(); got != "external_lookup" {
		t.Fatalf("tool name = %q, want external_lookup (preserved). Output: %s", got, string(outNonColliding))
	}

	rawNonAnti := []byte(`{"id":"i2","model":"gemini-3.1-flash-lite","steps":[{"type":"function_call","id":"call_2","name":"external_read_file","arguments":{"path":"/etc/hosts"}}]}`)
	outNonAnti := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, rawNonAnti, nil)
	if got := gjson.GetBytes(outNonAnti, "choices.0.message.tool_calls.0.function.name").String(); got != "external_read_file" {
		t.Fatalf("tool name = %q, want external_read_file (preserved for non-antigravity). Output: %s", got, string(outNonAnti))
	}
}

func findInteractionsEventPayload(events [][]byte, eventType string) []byte {
	for _, event := range events {
		payload := interactionsSSEPayload(event)
		if interactionsEventName(event, payload) == eventType {
			return payload
		}
	}
	return nil
}

func countInteractionsEvents(events [][]byte, eventType string) int {
	count := 0
	for _, event := range events {
		payload := interactionsSSEPayload(event)
		if interactionsEventName(event, payload) == eventType {
			count++
		}
	}
	return count
}

func interactionsEventName(event, payload []byte) string {
	if eventType := gjson.GetBytes(payload, "event_type").String(); eventType != "" {
		return eventType
	}
	const prefix = "event: "
	lineEnd := bytes.IndexByte(event, '\n')
	if lineEnd < 0 || !bytes.HasPrefix(event, []byte(prefix)) {
		return ""
	}
	return string(event[len(prefix):lineEnd])
}

func interactionsSSEPayload(event []byte) []byte {
	const prefix = "\ndata: "
	idx := bytes.Index(event, []byte(prefix))
	if idx < 0 {
		return nil
	}
	return event[idx+len(prefix):]
}

func findOpenAIChatChunk(chunks [][]byte, path string) []byte {
	for _, chunk := range chunks {
		if gjson.GetBytes(chunk, path).Exists() {
			return chunk
		}
	}
	return nil
}

func findOpenAIChatChunkValue(chunks [][]byte, path, want string) []byte {
	for _, chunk := range chunks {
		if gjson.GetBytes(chunk, path).String() == want {
			return chunk
		}
	}
	return nil
}

func TestConvertInteractionsResponseToOpenAIStreamToolCall_ContiguousZeroBasedIndex(t *testing.T) {
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
		// Step 0: thought
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"thought"}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","text":"planning..."}}`),
		[]byte(`data: {"event_type":"step.stop","index":0}`),
		// Step 1: model_output
		[]byte(`data: {"event_type":"step.start","index":1,"step":{"type":"model_output"}}`),
		[]byte(`data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":"I will call tool."}}`),
		[]byte(`data: {"event_type":"step.stop","index":1}`),
		// Step 2: function_call 1 (step index = 2, tool call index should be 0)
		[]byte(`data: {"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_first","name":"write_file","arguments":{}}}`),
		[]byte(`data: {"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a\"}"}}`),
		[]byte(`data: {"event_type":"step.stop","index":2}`),
		// Step 3: function_call 2 (step index = 3, tool call index should be 1)
		[]byte(`data: {"event_type":"step.start","index":3,"step":{"type":"function_call","id":"call_second","name":"read_file","arguments":{}}}`),
		[]byte(`data: {"event_type":"step.delta","index":3,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"b\"}"}}`),
		[]byte(`data: {"event_type":"step.stop","index":3}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`),
		[]byte(`data: [DONE]`),
	}

	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAI(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
	}

	// Find the start chunk for call_first
	var firstToolStart []byte
	var secondToolStart []byte
	for _, chunk := range out {
		if gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.id").String() == "call_first" {
			firstToolStart = chunk
		}
		if gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.id").String() == "call_second" {
			secondToolStart = chunk
		}
	}

	if firstToolStart == nil {
		t.Fatalf("call_first chunk not found")
	}
	if got := gjson.GetBytes(firstToolStart, "choices.0.delta.tool_calls.0.index").Int(); got != 0 {
		t.Fatalf("first tool call index = %d, want 0 (0-based contiguous index). Payload: %s", got, string(firstToolStart))
	}

	if secondToolStart == nil {
		t.Fatalf("call_second chunk not found")
	}
	if got := gjson.GetBytes(secondToolStart, "choices.0.delta.tool_calls.0.index").Int(); got != 1 {
		t.Fatalf("second tool call index = %d, want 1 (0-based contiguous index). Payload: %s", got, string(secondToolStart))
	}
}

func TestConvertInteractionsResponseToOpenAIStream_IncompleteFinishReasonLength(t *testing.T) {
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"write_file"}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a\""}}`),
		[]byte(`data: {"event_type":"step.stop","index":0}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete","finish_reason":"length"}}`),
		[]byte(`data: [DONE]`),
	}

	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAI(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
	}

	finishChunk := findOpenAIChatChunkValue(out, "choices.0.finish_reason", "length")
	if finishChunk == nil {
		t.Fatalf("expected finish_reason 'length' for truncated stream, got chunks: %s", string(bytes.Join(out, []byte("\n"))))
	}
}

func TestConvertInteractionsResponseToOpenAI_ContentFilterFinishReason(t *testing.T) {
	// 1. Stream
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"blocked"}}`),
		[]byte(`data: {"event_type":"step.stop","index":0}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete","finish_reason":"content_filter"}}`),
		[]byte(`data: [DONE]`),
	}
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAI(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
	}
	finishChunk := findOpenAIChatChunkValue(out, "choices.0.finish_reason", "content_filter")
	if finishChunk == nil {
		t.Fatalf("expected finish_reason 'content_filter' for stream, got: %s", string(bytes.Join(out, []byte("\n"))))
	}

	// 2. Non-stream
	raw := []byte(`{"id":"i1","model":"devin/swe-2","status":"incomplete","finish_reason":"content_filter","steps":[{"type":"model_output","content":[{"type":"text","text":"blocked"}]}]}`)
	outNonStream := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := gjson.GetBytes(outNonStream, "choices.0.finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q, want content_filter. Output: %s", got, string(outNonStream))
	}
}
