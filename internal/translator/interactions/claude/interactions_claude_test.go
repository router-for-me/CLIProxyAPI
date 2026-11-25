package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	claudeinteractions "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/interactions"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/interactions/responses"
)

func TestConvertClaudeRequestToInteractionsMapsMessagesToolsAndStream(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"max_tokens":1024,"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}],"messages":[{"role":"user","content":[{"type":"text","text":"今天北京的天气怎么样？"}]}]}`)
	out, _ := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, true)
	if got := ojson.GetBytesNoCopy(out, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Output: %s", got, string(out))
	}
	if !ojson.GetBytesNoCopy(out, "stream").Bool() {
		t.Fatalf("stream should be true. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "generation_config.max_output_tokens").Int(); got != 1024 {
		t.Fatalf("max_output_tokens = %d, want 1024. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("input text = %q. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "tools.0.parameters.properties.location.type").String(); got != "string" {
		t.Fatalf("tool schema was not mapped. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsMapsToolUseAndResult(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"北京"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"晴"}]}]}`)
	out, _ := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := ojson.GetBytesNoCopy(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.id").String(); got != "toolu_1" {
		t.Fatalf("id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "input.0.call_id").Exists() {
		t.Fatalf("function_call should not have call_id parameter. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.name").String(); got != "get_weather" {
		t.Fatalf("name = %q, want get_weather. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.call_id").String(); got != "toolu_1" {
		t.Fatalf("call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "input.1.id").Exists() {
		t.Fatalf("function_result should not have id parameter. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.result").String(); got != "晴" {
		t.Fatalf("result = %q, want 晴. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsInfersToolNamesForOutOfOrderResults(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": {"q": "x"}},
					{"type": "tool_use", "id": "toolu_2", "name": "weather", "input": {"city": "bj"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_2", "content": "sunny"},
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "found"}
				]
			}
		]
	}`)
	out, _ := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	// Since AlignClaudeToolResults aligns tool results with tool_use order (toolu_1 then toolu_2):
	// input.0: function_call toolu_1 (lookup)
	// input.1: function_call toolu_2 (weather)
	// input.2: function_result toolu_1 (lookup)
	// input.3: function_result toolu_2 (weather)
	if got := ojson.GetBytesNoCopy(out, "input.2.call_id").String(); got != "toolu_1" {
		t.Fatalf("input.2.call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.2.name").String(); got != "lookup" {
		t.Fatalf("input.2.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.3.call_id").String(); got != "toolu_2" {
		t.Fatalf("input.3.call_id = %q, want toolu_2. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.3.name").String(); got != "weather" {
		t.Fatalf("input.3.name = %q, want weather. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsPropagatesIsError(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_err","content":"command failed","is_error":true}]}]}`)
	out, _ := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if !ojson.GetBytesNoCopy(out, "input.0.is_error").Bool() {
		t.Fatalf("expected input.0.is_error = true. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.call_id").String(); got != "toolu_err" {
		t.Fatalf("call_id = %q, want toolu_err. Output: %s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "input.0.id").Exists() {
		t.Fatalf("input.0.id must not exist on function_result. Output: %s", string(out))
	}
}

func TestConvertClaudeRequestToInteractions_PreservesToolAdjacencyWithInterveningSystemMessage(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Execute tools"}]},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "tool_one", "input": {"a": 1}},
					{"type": "tool_use", "id": "call_2", "name": "tool_two", "input": {"b": 2}}
				]
			},
			{"role": "system", "content": "Context reminder between tool_use and tool_result"},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_2", "content": "result 2"},
					{"type": "tool_result", "tool_use_id": "call_1", "content": "result 1"},
					{"type": "text", "text": "Now summarize"}
				]
			}
		]
	}`)
	out, _ := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	inputs := ojson.GetBytesNoCopy(out, "input").Array()

	// Expected types:
	// 0: user_input ("Execute tools")
	// 1: function_call (call_1)
	// 2: function_call (call_2)
	// 3: function_result (call_1)
	// 4: function_result (call_2)
	// 5: user_input (<system-reminder>...)
	// 6: user_input ("Now summarize")
	types := make([]string, 0, len(inputs))
	for _, item := range inputs {
		types = append(types, item.Get("type").String())
	}
	wantTypes := []string{"user_input", "function_call", "function_call", "function_result", "function_result", "user_input", "user_input"}
	if len(types) != len(wantTypes) {
		t.Fatalf("unexpected step count %d: got %v, want %v. Output: %s", len(types), types, wantTypes, string(out))
	}
	for i, wt := range wantTypes {
		if types[i] != wt {
			t.Fatalf("step %d type = %q, want %q", i, types[i], wt)
		}
	}

	if inputs[3].Get("call_id").String() != "call_1" {
		t.Fatalf("expected result 0 to respond to call_1, got %q", inputs[3].Get("call_id").String())
	}
	if inputs[4].Get("call_id").String() != "call_2" {
		t.Fatalf("expected result 1 to respond to call_2, got %q", inputs[4].Get("call_id").String())
	}
	if inputs[5].Get("content.0.text").String() != "<system-reminder>\nContext reminder between tool_use and tool_result\n</system-reminder>" {
		t.Fatalf("unexpected system reminder content: %q", inputs[5].Get("content.0.text").String())
	}
	if inputs[6].Get("content.0.text").String() != "Now summarize" {
		t.Fatalf("unexpected user text: %q", inputs[6].Get("content.0.text").String())
	}
}

func TestConvertInteractionsResponseToClaudeStream(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`event: interaction.created
data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`event: step.start
data: {"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"type":"text","text":"北京今天晴"},"event_type":"step.delta"}`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}`),
		[]byte(`event: interaction.completed
data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite","usage":{"total_input_tokens":3,"total_output_tokens":4}},"event_type":"interaction.completed"}`),
		[]byte(`event: done
data: [DONE]`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	if payload := findClaudeEventPayload(out, "message_start"); ojson.GetBytesNoCopy(payload, "message.model").String() != "gemini-3.1-flash-lite" {
		t.Fatalf("message_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); ojson.GetBytesNoCopy(payload, "delta.text").String() != "北京今天晴" {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); ojson.GetBytesNoCopy(payload, "usage.output_tokens").Int() != 4 {
		t.Fatalf("message_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_stop"); ojson.GetBytesNoCopy(payload, "type").String() != "message_stop" {
		t.Fatalf("message_stop payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamToolCall(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`data: {"index":0,"step":{"type":"function_call","id":"toolu_1","signature":"sig_1","name":"get_weather","arguments":{}},"event_type":"step.start"}`),
		[]byte(`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"location\":\"北京\"}"},"event_type":"step.delta"}`),
		[]byte(`data: {"index":0,"event_type":"step.stop"}`),
		[]byte(`data: {"interaction":{"usage":{"total_input_tokens":1,"total_output_tokens":2}},"event_type":"interaction.completed"}`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); ojson.GetBytesNoCopy(payload, "content_block.type").String() != "tool_use" {
		t.Fatalf("content_block_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); ojson.GetBytesNoCopy(payload, "content_block.signature").String() != "sig_1" {
		t.Fatalf("content_block_start signature payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); ojson.GetBytesNoCopy(payload, "delta.partial_json").String() != `{"location":"北京"}` {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); ojson.GetBytesNoCopy(payload, "delta.stop_reason").String() != "tool_use" {
		t.Fatalf("message_delta payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamToolCallInlineArguments(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`data: {"index":0,"step":{"type":"function_call","id":"call_1","name":"lookup","arguments":{"q":"x"}},"event_type":"step.start"}`),
		[]byte(`data: {"index":0,"event_type":"step.stop"}`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	payload := findClaudeEventPayload(out, "content_block_delta")
	if got := ojson.GetBytesNoCopy(payload, "delta.type").String(); got != "input_json_delta" {
		t.Fatalf("delta.type = %q, want input_json_delta. Payload: %s", got, payload)
	}
	if got := ojson.GetBytesNoCopy(payload, "delta.partial_json").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.partial_json = %q, want object JSON. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamToolCallEmptyInlineArguments(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`data: {"index":0,"step":{"type":"function_call","id":"call_1","name":"lookup","arguments":{ }},"event_type":"step.start"}`),
		[]byte(`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"x\"}"},"event_type":"step.delta"}`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	var partials []string
	for _, event := range out {
		if payload := ojson.GetBytesNoCopy(event, "delta.type").String(); payload == "input_json_delta" {
			partials = append(partials, ojson.GetBytesNoCopy(event, "delta.partial_json").String())
		}
	}
	if len(partials) != 1 || partials[0] != `{"q":"x"}` {
		t.Fatalf("partial_json values = %#v, want one non-empty argument delta", partials)
	}
}

func TestConvertInteractionsResponseToClaudeStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToClaude(context.Background(), "claude-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_tokens":8}}}`), &param)
	payload := findClaudeEventPayload(out, "message_delta")
	if len(payload) == 0 {
		t.Fatalf("message_delta payload not found")
	}
	if got := ojson.GetBytesNoCopy(payload, "usage.input_tokens").Int(); got != 2 {
		t.Fatalf("input_tokens = %d, want 2. Payload: %s", got, string(payload))
	}
	if got := ojson.GetBytesNoCopy(payload, "usage.output_tokens").Int(); got != 6 {
		t.Fatalf("output_tokens = %d, want 6. Payload: %s", got, string(payload))
	}
}

func TestConvertInteractionsResponseToClaudeNonStream(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","model":"gemini-3.1-flash-lite","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"function_call","call_id":"toolu_1","signature":"sig_1","name":"lookup","arguments":{"q":"x"}}],"usage":{"total_input_tokens":3,"total_output_tokens":4}}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := ojson.GetBytesNoCopy(out, "content.0.text").String(); got != "ok" {
		t.Fatalf("text = %q, want ok. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "content.1.type").String(); got != "tool_use" {
		t.Fatalf("tool block type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "content.1.signature").String(); got != "sig_1" {
		t.Fatalf("tool signature = %q, want sig_1. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "usage.input_tokens").Int(); got != 3 {
		t.Fatalf("input_tokens = %d, want 3. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToClaude_IncompleteMaxTokens(t *testing.T) {
	// Non-stream
	raw := []byte(`{"id":"interaction_1","model":"devin/swe-2","status":"incomplete","finish_reason":"length","steps":[{"type":"model_output","content":[{"type":"text","text":"cut short"}]}],"usage":{"total_input_tokens":3,"total_output_tokens":4}}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := ojson.GetBytesNoCopy(out, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens. Output: %s", got, string(out))
	}

	// Stream
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"cut short"}}`),
		[]byte(`data: {"event_type":"step.stop","index":0}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete","finish_reason":"length"}}`),
		[]byte(`data: [DONE]`),
	}
	var outStream [][]byte
	for _, chunk := range chunks {
		outStream = append(outStream, ConvertInteractionsResponseToClaude(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
	}
	msgDelta := findClaudeEventPayload(outStream, "message_delta")
	if msgDelta == nil {
		t.Fatalf("missing message_delta event")
	}
	if got := ojson.GetBytesNoCopy(msgDelta, "delta.stop_reason").String(); got != "max_tokens" {
		t.Fatalf("delta.stop_reason = %q, want max_tokens. Payload: %s", got, string(msgDelta))
	}
}

func findClaudeEventPayload(events [][]byte, eventName string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		if !bytes.Contains(event, []byte("event: "+eventName)) {
			continue
		}
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, prefix) {
				return bytes.TrimSpace(line[len(prefix):])
			}
		}
	}
	return nil
}

func TestConvertInteractionsResponseToClaude_PreservesCacheReadUsage(t *testing.T) {
	t.Run("streaming_cache_hit", func(t *testing.T) {
		var param any
		chunks := [][]byte{
			[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
			[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`),
			[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hello"}}`),
			[]byte(`data: {"event_type":"step.stop","index":0}`),
			[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","model":"devin/swe-2","status":"completed","usage":{"total_input_tokens":10411,"total_output_tokens":76,"total_cached_tokens":10340,"total_tokens":10487}}}`),
			[]byte(`data: [DONE]`),
		}
		var outStream [][]byte
		for _, chunk := range chunks {
			outStream = append(outStream, ConvertInteractionsResponseToClaude(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
		}
		msgDelta := findClaudeEventPayload(outStream, "message_delta")
		if msgDelta == nil {
			t.Fatalf("missing message_delta event")
		}
		usage := ojson.GetBytesNoCopy(msgDelta, "usage")
		if got := usage.Get("input_tokens").Int(); got != 71 {
			t.Fatalf("usage.input_tokens = %d, want 71. Payload: %s", got, string(msgDelta))
		}
		if got := usage.Get("output_tokens").Int(); got != 76 {
			t.Fatalf("usage.output_tokens = %d, want 76. Payload: %s", got, string(msgDelta))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 10340 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 10340. Payload: %s", got, string(msgDelta))
		}
	})

	t.Run("non_streaming_cache_hit", func(t *testing.T) {
		raw := []byte(`{"id":"i2","model":"devin/glm-5-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"total_input_tokens":96724,"total_output_tokens":269,"total_cached_tokens":30784,"total_tokens":96993}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/glm-5-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 65940 {
			t.Fatalf("usage.input_tokens = %d, want 65940. Output: %s", got, string(out))
		}
		if got := usage.Get("output_tokens").Int(); got != 269 {
			t.Fatalf("usage.output_tokens = %d, want 269. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 30784 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 30784. Output: %s", got, string(out))
		}
	})

	t.Run("zero_cache_tokens", func(t *testing.T) {
		raw := []byte(`{"id":"i3","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"total_input_tokens":100,"total_output_tokens":50,"total_cached_tokens":0,"total_tokens":150}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 100 {
			t.Fatalf("usage.input_tokens = %d, want 100. Output: %s", got, string(out))
		}
		if got := usage.Get("output_tokens").Int(); got != 50 {
			t.Fatalf("usage.output_tokens = %d, want 50. Output: %s", got, string(out))
		}
		if usage.Get("cache_read_input_tokens").Exists() {
			t.Fatalf("usage.cache_read_input_tokens should not be present when zero. Output: %s", string(out))
		}
	})

	t.Run("explicit_uncached_and_cache_creation", func(t *testing.T) {
		raw := []byte(`{"id":"i4","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":71,"total_input_tokens":10411,"total_output_tokens":76,"cache_read_input_tokens":10340,"cache_creation_input_tokens":25,"total_tokens":10487}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 71 {
			t.Fatalf("usage.input_tokens = %d, want 71. Output: %s", got, string(out))
		}
		if got := usage.Get("output_tokens").Int(); got != 76 {
			t.Fatalf("usage.output_tokens = %d, want 76. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 10340 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 10340. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_creation_input_tokens").Int(); got != 25 {
			t.Fatalf("usage.cache_creation_input_tokens = %d, want 25. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_with_cache_write_in_total", func(t *testing.T) {
		// Total input (10436) = uncached (71) + read (10340) + write (25)
		raw := []byte(`{"id":"i5","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":71,"total_input_tokens":10436,"total_output_tokens":76,"cache_read_input_tokens":10340,"cache_creation_input_tokens":25,"total_tokens":10512}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 71 {
			t.Fatalf("usage.input_tokens = %d, want 71. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 10340 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 10340. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_creation_input_tokens").Int(); got != 25 {
			t.Fatalf("usage.cache_creation_input_tokens = %d, want 25. Output: %s", got, string(out))
		}
	})

	t.Run("full_cache_hit", func(t *testing.T) {
		raw := []byte(`{"id":"i6","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"total_input_tokens":500,"total_output_tokens":50,"total_cached_tokens":500,"total_tokens":550}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 0 {
			t.Fatalf("usage.input_tokens = %d, want 0. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 500 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 500. Output: %s", got, string(out))
		}
	})

	t.Run("cached_tokens_exceeds_input", func(t *testing.T) {
		raw := []byte(`{"id":"i7","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"total_input_tokens":50,"total_output_tokens":50,"total_cached_tokens":100,"total_tokens":150}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 0 {
			t.Fatalf("usage.input_tokens = %d, want 0. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 100 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 100. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_without_total_input_tokens", func(t *testing.T) {
		raw := []byte(`{"id":"i8","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":71,"output_tokens":76,"cache_read_input_tokens":10340}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 71 {
			t.Fatalf("usage.input_tokens = %d, want 71. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 10340 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 10340. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_without_total_input_tokens_greater", func(t *testing.T) {
		raw := []byte(`{"id":"i8_gt","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":100,"output_tokens":30,"cache_read_input_tokens":80}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 100 {
			t.Fatalf("usage.input_tokens = %d, want 100. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 80 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 80. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_without_total_input_tokens_equal", func(t *testing.T) {
		raw := []byte(`{"id":"i8_eq","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":80,"output_tokens":30,"cache_read_input_tokens":80}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 80 {
			t.Fatalf("usage.input_tokens = %d, want 80. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 80 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 80. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_zero_with_total", func(t *testing.T) {
		raw := []byte(`{"id":"i10","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":0,"total_input_tokens":500,"output_tokens":30,"cache_read_input_tokens":500}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 0 {
			t.Fatalf("usage.input_tokens = %d, want 0. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 500 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 500. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_equal_to_total_input", func(t *testing.T) {
		raw := []byte(`{"id":"i11","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":100,"total_input_tokens":100,"output_tokens":30,"cache_read_input_tokens":20}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 100 {
			t.Fatalf("usage.input_tokens = %d, want 100. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 20 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 20. Output: %s", got, string(out))
		}
	})

	t.Run("explicit_uncached_zero_with_partial_cache", func(t *testing.T) {
		raw := []byte(`{"id":"i12","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"input_tokens":0,"total_input_tokens":500,"output_tokens":30,"cache_read_input_tokens":200}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 0 {
			t.Fatalf("usage.input_tokens = %d, want 0. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 200 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 200. Output: %s", got, string(out))
		}
	})

	t.Run("inclusive_total_without_input_tokens_large_scale", func(t *testing.T) {
		raw := []byte(`{"id":"i13","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"total_input_tokens":10436,"output_tokens":76,"cache_read_input_tokens":10340,"cache_creation_input_tokens":25}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 71 {
			t.Fatalf("usage.input_tokens = %d, want 71. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 10340 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 10340. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_creation_input_tokens").Int(); got != 25 {
			t.Fatalf("usage.cache_creation_input_tokens = %d, want 25. Output: %s", got, string(out))
		}
	})

	t.Run("prompt_tokens_fallback", func(t *testing.T) {
		raw := []byte(`{"id":"i14","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"response text"}]}],"usage":{"prompt_tokens":100,"output_tokens":20,"cached_tokens":30}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		usage := ojson.GetBytesNoCopy(out, "usage")
		if got := usage.Get("input_tokens").Int(); got != 70 {
			t.Fatalf("usage.input_tokens = %d, want 70. Output: %s", got, string(out))
		}
		if got := usage.Get("output_tokens").Int(); got != 20 {
			t.Fatalf("usage.output_tokens = %d, want 20. Output: %s", got, string(out))
		}
		if got := usage.Get("cache_read_input_tokens").Int(); got != 30 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 30. Output: %s", got, string(out))
		}
	})
}

func TestConvertClaudeRequestToInteractionsPreservesImagesInToolResult(t *testing.T) {
	raw := []byte(`{
		"model": "devin/swe-2",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "tool_image_1", "name": "screenshot", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "tool_image_1",
						"content": [
							{
								"type": "text",
								"text": "Captured desktop"
							},
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
								}
							}
						]
					}
				]
			}
		]
	}`)

	out, _ := ConvertClaudeRequestToInteractions("devin/swe-2", raw, false)
	res := ojson.GetBytesNoCopy(out, "input.1.result")
	if !res.IsArray() {
		t.Fatalf("expected input.1.result to be array, got: %s", res.Raw)
	}

	foundText := false
	foundImage := false
	for _, item := range res.Array() {
		switch item.Get("type").String() {
		case "text":
			if item.Get("text").String() == "Captured desktop" {
				foundText = true
			}
		case "image":
			if item.Get("mime_type").String() == "image/png" && item.Get("data").String() != "" {
				foundImage = true
			}
		}
	}

	if !foundText {
		t.Fatalf("expected text part 'Captured desktop' in result, got: %s", res.Raw)
	}
	if !foundImage {
		t.Fatalf("expected image part in result, got: %s", res.Raw)
	}
}

func TestConvertClaudeRequestToInteractionsPreservesBusinessObjectsInToolResultArray(t *testing.T) {
	raw := []byte(`{
		"model": "devin/swe-2",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "tool_call_1",
						"content": [
							{"text": "failed", "exit_code": 1, "retryable": true},
							{"type": "text", "text": "failed with code", "exit_code": 2}
						]
					}
				]
			}
		]
	}`)

	out, _ := ConvertClaudeRequestToInteractions("devin/swe-2", raw, false)
	res := ojson.GetBytesNoCopy(out, "input.0.result")
	if !res.IsArray() {
		t.Fatalf("expected input.0.result to be array, got: %s", res.Raw)
	}

	resStr := res.Raw
	if !strings.Contains(resStr, `"exit_code": 1`) || !strings.Contains(resStr, `"retryable": true`) {
		t.Errorf("expected exit_code 1 and retryable to be preserved in result: %s", resStr)
	}
	if !strings.Contains(resStr, `"exit_code": 2`) {
		t.Errorf("expected exit_code 2 to be preserved in result: %s", resStr)
	}
}

func TestConvertInteractionsResponseToClaude_ResponseFailed(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantMsg string
	}{
		{
			name:    "response_failed_top_level",
			payload: `data: {"event_type":"response.failed","error":{"message":"devin upstream error (permission_denied): Unable to process request due to an MCP configuration issue.","code":"403"}}`,
			wantMsg: "permission_denied",
		},
		{
			name:    "interaction_failed_nested",
			payload: `data: {"event_type":"interaction.failed","interaction":{"error":{"message":"service unavailable","type":"server_error"}}}`,
			wantMsg: "service unavailable",
		},
		{
			name:    "fallback_defaults",
			payload: `data: {"event_type":"response.failed"}`,
			wantMsg: "upstream error occurred",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			events := ConvertInteractionsResponseToClaude(context.Background(), "devin/kimi-k3", nil, nil, []byte(tt.payload), &param)
			if len(events) == 0 {
				t.Fatalf("expected non-empty events for %s, got 0", tt.name)
			}
			payload := findClaudeEventPayload(events, "error")
			if len(payload) == 0 {
				t.Fatalf("expected error event payload, got: %s", string(bytes.Join(events, []byte("\n"))))
			}
			if got := ojson.GetBytesNoCopy(payload, "error.message").String(); !strings.Contains(got, tt.wantMsg) {
				t.Fatalf("error.message = %q, want containing %q", got, tt.wantMsg)
			}
		})
	}
}

func TestConvertClaudeRequestToInteractions_GenerationConstraintsAndOutputConfigFormat(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.5-flash",
		"max_tokens": 1030323,
		"stop_sequences": ["STOP", "\n", "", 123, null],
		"output_config": {
			"effort": "low",
			"format": {
				"type": "json_schema",
				"schema": {
					"type": "object",
					"properties": {"city": {"type": "string"}},
					"required": ["city"],
					"additionalProperties": false
				}
			}
		},
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
	if errConvert != nil {
		t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
	}
	if got := ojson.GetBytesNoCopy(out, "generation_config.max_output_tokens").Raw; got != "1030323" {
		t.Fatalf("max_output_tokens raw = %q, want 1030323; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_level").String(); got != "low" {
		t.Fatalf("thinking_level = %q, want low; out=%s", got, out)
	}
	stops := ojson.GetBytesNoCopy(out, "generation_config.stop_sequences").Array()
	if len(stops) != 2 || stops[0].String() != "STOP" || stops[1].String() != "\n" {
		t.Fatalf("stop_sequences = %s, want [STOP \\n]; out=%s", ojson.GetBytesNoCopy(out, "generation_config.stop_sequences").Raw, out)
	}
	if got := ojson.GetBytesNoCopy(out, "response_format.type").String(); got != "text" {
		t.Fatalf("response_format.type = %q, want text; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "response_format.mime_type").String(); got != "application/json" {
		t.Fatalf("response_format.mime_type = %q, want application/json; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "response_format.schema.properties.city.type").String(); got != "string" {
		t.Fatalf("response_format.schema.properties.city.type = %q, want string; out=%s", got, out)
	}

	zeroRaw := []byte(`{
		"model": "gemini-3.5-flash",
		"max_tokens": 0,
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	zeroOut, errZero := ConvertClaudeRequestToInteractions("gemini-3.5-flash", zeroRaw, false)
	if errZero != nil {
		t.Fatalf("ConvertClaudeRequestToInteractions() zero error = %v", errZero)
	}
	if got := ojson.GetBytesNoCopy(zeroOut, "generation_config.max_output_tokens").Raw; got != "0" {
		t.Fatalf("max_output_tokens raw = %q, want 0; out=%s", got, zeroOut)
	}

	invalidRaw := []byte(`{
		"model": "gemini-3.5-flash",
		"max_tokens": null,
		"stop_sequences": ["", 123],
		"output_config": {"format": {"type": "json_schema"}},
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	invalidOut, errInvalid := ConvertClaudeRequestToInteractions("gemini-3.5-flash", invalidRaw, false)
	if errInvalid != nil {
		t.Fatalf("ConvertClaudeRequestToInteractions() invalid error = %v", errInvalid)
	}
	for _, path := range []string{"generation_config.max_output_tokens", "generation_config.stop_sequences", "response_format"} {
		if ojson.GetBytesNoCopy(invalidOut, path).Exists() {
			t.Fatalf("%s should be omitted for invalid input; out=%s", path, invalidOut)
		}
	}
}

func TestConvertClaudeRequestToInteractions_ToolChoiceNone(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolChoice string
	}{
		{name: "string_none", toolChoice: `"none"`},
		{name: "object_none", toolChoice: `{"type":"none"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"model":"gemini-3.5-flash","tool_choice":` + tc.toolChoice + `,"messages":[{"role":"user","content":"hi"}]}`)
			out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
			if errConvert != nil {
				t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
			}
			if got := ojson.GetBytesNoCopy(out, "generation_config.tool_choice").String(); got != "none" {
				t.Fatalf("generation_config.tool_choice = %q, want none; out=%s", got, out)
			}
		})
	}
}

func TestConvertClaudeRequestToInteractions_SamplingNullAndTopK(t *testing.T) {
	t.Run("forwards_valid_top_k_and_sampling_numbers", func(t *testing.T) {
		raw := []byte(`{"model":"gemini-3.5-flash","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hi"}]}`)
		out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
		if errConvert != nil {
			t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.temperature").Raw; got != "0.7" {
			t.Fatalf("temperature = %q, want 0.7; out=%s", got, out)
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.top_p").Raw; got != "0.9" {
			t.Fatalf("top_p = %q, want 0.9; out=%s", got, out)
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.top_k").Raw; got != "40" {
			t.Fatalf("top_k = %q, want 40; out=%s", got, out)
		}
	})

	t.Run("skips_null_and_non_number_sampling_fields", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{
				name: "null_fields",
				raw:  `{"model":"gemini-3.5-flash","temperature":null,"top_p":null,"top_k":null,"max_tokens":null,"stop_sequences":null,"messages":[{"role":"user","content":"hi"}]}`,
			},
			{
				name: "non_number_fields",
				raw:  `{"model":"gemini-3.5-flash","temperature":"0.7","top_p":true,"top_k":{},"max_tokens":"100","stop_sequences":123,"messages":[{"role":"user","content":"hi"}]}`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", []byte(tc.raw), false)
				if errConvert != nil {
					t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
				}
				for _, path := range []string{
					"generation_config.temperature",
					"generation_config.top_p",
					"generation_config.top_k",
					"generation_config.max_output_tokens",
					"generation_config.stop_sequences",
				} {
					if ojson.GetBytesNoCopy(out, path).Exists() {
						t.Fatalf("%s should be omitted for %s; out=%s", path, tc.name, out)
					}
				}
			})
		}
	})
}

func TestConvertClaudeRequestToInteractions_ThinkingNullBudgetBlankEffortAndOverride(t *testing.T) {
	t.Run("non_number_budget_tokens_falls_back_to_high_without_writing_budget", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			budget string
		}{
			{name: "null", budget: "null"},
			{name: "string", budget: `"4096"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw := []byte(`{"model":"gemini-3.5-flash","thinking":{"type":"enabled","budget_tokens":` + tc.budget + `},"messages":[{"role":"user","content":"hi"}]}`)
				out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
				if errConvert != nil {
					t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
				}
				if ojson.GetBytesNoCopy(out, "generation_config.thinking_config.thinking_budget").Exists() {
					t.Fatalf("thinking_budget should be omitted when budget_tokens is %s; out=%s", tc.name, out)
				}
				if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_level").String(); got != "high" {
					t.Fatalf("thinking_level = %q, want high; out=%s", got, out)
				}
			})
		}
	})

	t.Run("blank_effort_preserves_disabled_thinking_level", func(t *testing.T) {
		raw := []byte(`{"model":"gemini-3.5-flash","thinking":{"type":"disabled"},"output_config":{"effort":""},"messages":[{"role":"user","content":"hi"}]}`)
		out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
		if errConvert != nil {
			t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_level").String(); got != "none" {
			t.Fatalf("thinking_level = %q, want none; out=%s", got, out)
		}
	})

	t.Run("blank_effort_does_not_write_empty_thinking_level_or_clear_budget", func(t *testing.T) {
		raw := []byte(`{"model":"gemini-3.5-flash","thinking":{"type":"enabled","budget_tokens":2048},"output_config":{"effort":"   "},"messages":[{"role":"user","content":"hi"}]}`)
		out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
		if errConvert != nil {
			t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
		}
		if ojson.GetBytesNoCopy(out, "generation_config.thinking_level").Exists() {
			t.Fatalf("thinking_level should be omitted for blank effort; out=%s", out)
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_config.thinking_budget").Raw; got != "2048" {
			t.Fatalf("thinking_budget = %q, want 2048; out=%s", got, out)
		}
	})

	t.Run("non_empty_effort_clears_thinking_budget_and_empty_thinking_config", func(t *testing.T) {
		raw := []byte(`{"model":"gemini-3.5-flash","thinking":{"type":"enabled","budget_tokens":4096},"output_config":{"effort":" Low "},"messages":[{"role":"user","content":"hi"}]}`)
		out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.5-flash", raw, false)
		if errConvert != nil {
			t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_level").String(); got != "low" {
			t.Fatalf("thinking_level = %q, want low; out=%s", got, out)
		}
		if ojson.GetBytesNoCopy(out, "generation_config.thinking_config.thinking_budget").Exists() {
			t.Fatalf("thinking_budget should be cleared when effort sets thinking_level; out=%s", out)
		}
		if ojson.GetBytesNoCopy(out, "generation_config.thinking_config").Exists() {
			t.Fatalf("empty thinking_config should be removed after clearing thinking_budget; out=%s", out)
		}
	})
}

func TestConvertClaudeRequestToInteractions_PreservesThinkingAndToolUseSignatures(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"messages": [
			{"role": "user", "content": "check weather"},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "plan step", "signature": " sig_thought_1 "},
					{"type": "thinking", "thinking": "", "signature": "sig_only_2"},
					{"type": "thinking", "thinking": "no sig plan", "signature": "   "},
					{"type": "thinking", "thinking": "", "signature": "   "},
					{"type": "thinking", "thinking": "", "signature": 123},
					{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris"}, "signature": " sig_tool_1 "},
					{"type": "tool_use", "id": "toolu_2", "name": "get_time", "input": {"tz": "UTC"}, "signature": "   "},
					{"type": "tool_use", "id": "toolu_3", "name": "get_locale", "input": {}, "signature": 456}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny"},
					{"type": "tool_result", "tool_use_id": "toolu_2", "content": "12:00"},
					{"type": "tool_result", "tool_use_id": "toolu_3", "content": "fr-FR"}
				]
			}
		]
	}`)

	out, errConvert := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if errConvert != nil {
		t.Fatalf("ConvertClaudeRequestToInteractions() error = %v", errConvert)
	}

	// Expected input steps:
	// 0: user_input
	// 1: thought (text + signature)
	// 2: thought (signature-only, no content)
	// 3: thought (text only, whitespace signature omitted)
	// (empty thinking + whitespace/non-string signature dropped)
	// 4: function_call toolu_1 (with signature)
	// 5: function_call toolu_2 (whitespace signature omitted)
	// 6: function_call toolu_3 (non-string signature omitted)
	// 7..9: function_result toolu_1..toolu_3
	if got := ojson.GetBytesNoCopy(out, "input.#").Int(); got != 10 {
		t.Fatalf("input.# = %d, want 10; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.type").String(); got != "thought" {
		t.Fatalf("input.1.type = %q, want thought; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.content.0.text").String(); got != "plan step" {
		t.Fatalf("input.1.content.0.text = %q, want plan step; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.signature").String(); got != "sig_thought_1" {
		t.Fatalf("input.1.signature = %q, want sig_thought_1; out=%s", got, out)
	}

	if got := ojson.GetBytesNoCopy(out, "input.2.type").String(); got != "thought" {
		t.Fatalf("input.2.type = %q, want thought; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "input.2.signature").String(); got != "sig_only_2" {
		t.Fatalf("input.2.signature = %q, want sig_only_2; out=%s", got, out)
	}
	if ojson.GetBytesNoCopy(out, "input.2.content").Exists() {
		t.Fatalf("signature-only thought should omit empty content in default mode; out=%s", out)
	}

	if got := ojson.GetBytesNoCopy(out, "input.3.type").String(); got != "thought" {
		t.Fatalf("input.3.type = %q, want thought; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "input.3.content.0.text").String(); got != "no sig plan" {
		t.Fatalf("input.3.content.0.text = %q, want no sig plan; out=%s", got, out)
	}
	if ojson.GetBytesNoCopy(out, "input.3.signature").Exists() {
		t.Fatalf("whitespace signature should be omitted on thought; out=%s", out)
	}

	if got := ojson.GetBytesNoCopy(out, "input.4.type").String(); got != "function_call" {
		t.Fatalf("input.4.type = %q, want function_call; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "input.4.signature").String(); got != "sig_tool_1" {
		t.Fatalf("input.4.signature = %q, want sig_tool_1; out=%s", got, out)
	}

	if got := ojson.GetBytesNoCopy(out, "input.5.type").String(); got != "function_call" {
		t.Fatalf("input.5.type = %q, want function_call; out=%s", got, out)
	}
	if ojson.GetBytesNoCopy(out, "input.5.signature").Exists() {
		t.Fatalf("whitespace signature should be omitted on function_call; out=%s", out)
	}

	if got := ojson.GetBytesNoCopy(out, "input.6.type").String(); got != "function_call" {
		t.Fatalf("input.6.type = %q, want function_call; out=%s", got, out)
	}
	if ojson.GetBytesNoCopy(out, "input.6.signature").Exists() {
		t.Fatalf("non-string signature should be omitted on function_call; out=%s", out)
	}

	// Compat mode preserves empty content alongside signature on signature-only thinking blocks.
	sigOnlyPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig_compat"}]}]}`)
	compatOut, errCompat := ConvertClaudeRequestToInteractionsWithCompat("gemini-3.1-flash-lite", sigOnlyPayload, false)
	if errCompat != nil {
		t.Fatalf("ConvertClaudeRequestToInteractionsWithCompat() error = %v", errCompat)
	}
	if got := ojson.GetBytesNoCopy(compatOut, "input.0.type").String(); got != "thought" {
		t.Fatalf("compat input.0.type = %q, want thought; out=%s", got, compatOut)
	}
	if got := ojson.GetBytesNoCopy(compatOut, "input.0.signature").String(); got != "sig_compat" {
		t.Fatalf("compat input.0.signature = %q, want sig_compat; out=%s", got, compatOut)
	}
	if !ojson.GetBytesNoCopy(compatOut, "input.0.content").IsArray() || ojson.GetBytesNoCopy(compatOut, "input.0.content.0.type").String() != "text" || ojson.GetBytesNoCopy(compatOut, "input.0.content.0.text").String() != "" {
		t.Fatalf("compat signature-only thought missing empty content text; out=%s", compatOut)
	}
}

func TestConvertInteractionsResponseToClaudeNonStream_SignatureOnlyThought(t *testing.T) {
	raw := []byte(`{"id":"interaction_sig","model":"gemini-3-pro","steps":[{"type":"thought","signature":"sig_only_1"},{"type":"thought","thought_signature":"sig_only_2","content":[]},{"type":"thought","content":[]},{"type":"model_output","content":[{"type":"text","text":"answer"}]}]}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3-pro", nil, nil, raw, nil)

	if got := ojson.GetBytesNoCopy(out, "content.#").Int(); got != 3 {
		t.Fatalf("content.# = %d, want 3; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "content.0.type").String(); got != "thinking" {
		t.Fatalf("content.0.type = %q, want thinking; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "content.0.thinking").String(); got != "" {
		t.Fatalf("content.0.thinking = %q, want empty; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "content.0.signature").String(); got != "sig_only_1" {
		t.Fatalf("content.0.signature = %q, want sig_only_1; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "content.1.type").String(); got != "thinking" {
		t.Fatalf("content.1.type = %q, want thinking; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "content.1.signature").String(); got != "sig_only_2" {
		t.Fatalf("content.1.signature = %q, want sig_only_2; out=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "content.2.type").String(); got != "text" {
		t.Fatalf("content.2.type = %q, want text; out=%s", got, out)
	}
}

func TestConvertInteractionsResponseToClaudeStream_StandaloneThoughtSignature(t *testing.T) {
	t.Run("standalone_without_step_start", func(t *testing.T) {
		var param any
		var out [][]byte
		chunks := [][]byte{
			[]byte(`data: {"index":0,"delta":{"type":"thought_signature","signature":"sig_standalone"},"event_type":"step.delta"}`),
			[]byte(`data: {"index":0,"event_type":"step.stop"}`),
		}
		for _, chunk := range chunks {
			out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, chunk, &param)...)
		}
		start := findClaudeEventPayload(out, "content_block_start")
		if got := ojson.GetBytesNoCopy(start, "content_block.type").String(); got != "thinking" {
			t.Fatalf("content_block_start type = %q, want thinking; events=%s", got, string(bytes.Join(out, []byte("\n"))))
		}
		delta := findClaudeEventPayload(out, "content_block_delta")
		if got := ojson.GetBytesNoCopy(delta, "delta.type").String(); got != "signature_delta" {
			t.Fatalf("delta.type = %q, want signature_delta; events=%s", got, string(bytes.Join(out, []byte("\n"))))
		}
		if got := ojson.GetBytesNoCopy(delta, "delta.signature").String(); got != "sig_standalone" {
			t.Fatalf("delta.signature = %q, want sig_standalone; payload=%s", got, delta)
		}
	})

	t.Run("alternate_signature_keys", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			payload string
			want    string
		}{
			{
				name:    "snake_case_thought_signature",
				payload: `data: {"index":0,"delta":{"type":"thought_signature","thought_signature":"sig_snake"},"event_type":"step.delta"}`,
				want:    "sig_snake",
			},
			{
				name:    "camelCase_thoughtSignature",
				payload: `data: {"index":0,"delta":{"type":"thought_signature","thoughtSignature":"sig_camel"},"event_type":"step.delta"}`,
				want:    "sig_camel",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var param any
				out := ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, []byte(tc.payload), &param)
				delta := findClaudeEventPayload(out, "content_block_delta")
				if got := ojson.GetBytesNoCopy(delta, "delta.signature").String(); got != tc.want {
					t.Fatalf("delta.signature = %q, want %q; events=%s", got, tc.want, string(bytes.Join(out, []byte("\n"))))
				}
			})
		}
	})

	t.Run("empty_signature_emits_nothing", func(t *testing.T) {
		var param any
		out := ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, []byte(`data: {"index":0,"delta":{"type":"thought_signature","signature":"   "},"event_type":"step.delta"}`), &param)
		if len(out) != 0 {
			t.Fatalf("expected no events for empty signature, got %s", string(bytes.Join(out, []byte("\n"))))
		}
	})
}

func TestConvertInteractionsResponseToClaudeStream_IgnoresNonTextDeltas(t *testing.T) {
	t.Run("non_text_delta_mid_thinking", func(t *testing.T) {
		var param any
		var out [][]byte
		chunks := [][]byte{
			[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i_nontext","model":"gemini-3-pro"}}`),
			[]byte(`data: {"index":0,"step":{"type":"thought"},"event_type":"step.start"}`),
			[]byte(`data: {"index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"thinking"}},"event_type":"step.delta"}`),
			[]byte(`data: {"index":1,"delta":{"type":"function_result","call_id":"call_1","result":"ok"},"event_type":"step.delta"}`),
			[]byte(`data: {"index":0,"delta":{"type":"thought_signature","signature":"sig_after"},"event_type":"step.delta"}`),
			[]byte(`data: {"index":0,"event_type":"step.stop"}`),
		}
		for _, chunk := range chunks {
			out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, chunk, &param)...)
		}

		var foundSignatureAtZero bool
		for _, event := range out {
			if bytes.Contains(event, []byte(`"type":"text"`)) {
				t.Fatalf("unexpected text content_block_start on non-text delta; events=%s", string(bytes.Join(out, []byte("\n"))))
			}
			if bytes.Contains(event, []byte(`"type":"signature_delta"`)) &&
				bytes.Contains(event, []byte(`"index":0`)) &&
				bytes.Contains(event, []byte(`"signature":"sig_after"`)) {
				foundSignatureAtZero = true
			}
		}
		if !foundSignatureAtZero {
			t.Fatalf("expected signature_delta on still-active thinking block index 0; events=%s", string(bytes.Join(out, []byte("\n"))))
		}
	})

	t.Run("full_function_result_step_sequence", func(t *testing.T) {
		var param any
		var out [][]byte
		chunks := [][]byte{
			[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i_fr_seq","model":"gemini-3-pro"}}`),
			[]byte(`data: {"index":0,"step":{"type":"function_result","name":"lookup"},"event_type":"step.start"}`),
			[]byte(`data: {"index":0,"delta":{"type":"function_result","name":"lookup","result":{"ok":true}},"event_type":"step.delta"}`),
			[]byte(`data: {"index":0,"event_type":"step.stop"}`),
			[]byte(`data: {"index":1,"step":{"type":"model_output"},"event_type":"step.start"}`),
			[]byte(`data: {"index":1,"delta":{"type":"text","text":"done"},"event_type":"step.delta"}`),
			[]byte(`data: {"index":1,"event_type":"step.stop"}`),
		}
		for _, chunk := range chunks {
			out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, chunk, &param)...)
		}

		textBlockStarts := 0
		for _, event := range out {
			if bytes.Contains(event, []byte("event: content_block_start")) {
				if !bytes.Contains(event, []byte(`"type":"text"`)) || !bytes.Contains(event, []byte(`"index":0`)) {
					t.Fatalf("unexpected content_block_start; events=%s", string(bytes.Join(out, []byte("\n"))))
				}
				textBlockStarts++
			}
		}
		if textBlockStarts != 1 {
			t.Fatalf("text content_block_start count = %d, want 1; events=%s", textBlockStarts, string(bytes.Join(out, []byte("\n"))))
		}
		delta := findClaudeEventPayload(out, "content_block_delta")
		if got := ojson.GetBytesNoCopy(delta, "delta.text").String(); got != "done" {
			t.Fatalf("delta.text = %q, want done; events=%s", got, string(bytes.Join(out, []byte("\n"))))
		}
	})
}

func TestConvertInteractionsResponseToClaude_UsageThoughtTokensAndInputCache(t *testing.T) {
	t.Run("stream_adds_separate_total_thought_tokens_to_output_tokens", func(t *testing.T) {
		var param any
		out := ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, []byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i_u1","model":"gemini-3-pro","status":"completed","usage":{"total_input_tokens":123,"total_output_tokens":36,"total_thought_tokens":240,"total_cached_tokens":5,"total_tokens":399}}}`), &param)
		payload := findClaudeEventPayload(out, "message_delta")
		if got := ojson.GetBytesNoCopy(payload, "usage.input_tokens").Int(); got != 118 {
			t.Fatalf("usage.input_tokens = %d, want 118; payload=%s", got, payload)
		}
		if got := ojson.GetBytesNoCopy(payload, "usage.output_tokens").Int(); got != 276 {
			t.Fatalf("usage.output_tokens = %d, want 276; payload=%s", got, payload)
		}
		if got := ojson.GetBytesNoCopy(payload, "usage.cache_read_input_tokens").Int(); got != 5 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 5; payload=%s", got, payload)
		}
	})

	t.Run("nonstream_adds_separate_reasoning_tokens_without_double_counting_inclusive_output", func(t *testing.T) {
		// Separate reasoning tokens (total_tokens = 1000 + 50 + 120 = 1170): add reasoning_tokens to output_tokens.
		separateRaw := []byte(`{"id":"i_u2","model":"gemini-3-pro","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]}],"usage":{"total_input_tokens":1000,"output_tokens":50,"reasoning_tokens":120,"cached_tokens":800,"cache_write_tokens":100,"total_tokens":1170}}`)
		separateOut := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3-pro", nil, nil, separateRaw, nil)
		if got := ojson.GetBytesNoCopy(separateOut, "usage.input_tokens").Int(); got != 100 {
			t.Fatalf("separate usage.input_tokens = %d, want 100; out=%s", got, separateOut)
		}
		if got := ojson.GetBytesNoCopy(separateOut, "usage.output_tokens").Int(); got != 170 {
			t.Fatalf("separate usage.output_tokens = %d, want 170; out=%s", got, separateOut)
		}

		// Separate native Interactions usage without total_tokens still adds total_thought_tokens.
		separateNoTotalRaw := []byte(`{"id":"i_u2_nototal","model":"gemini-3-pro","status":"completed","steps":[],"usage":{"total_input_tokens":100,"total_output_tokens":36,"total_thought_tokens":240}}`)
		separateNoTotalOut := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3-pro", nil, nil, separateNoTotalRaw, nil)
		if got := ojson.GetBytesNoCopy(separateNoTotalOut, "usage.output_tokens").Int(); got != 276 {
			t.Fatalf("separate without total_tokens usage.output_tokens = %d, want 276; out=%s", got, separateNoTotalOut)
		}

		// Inclusive reasoning tokens from OpenAI Responses -> Interactions (with and without total_tokens): do not double-count.
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{
				name: "with_total_tokens",
				raw:  `{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24,"input_tokens_details":{"cached_tokens":5},"output_tokens_details":{"reasoning_tokens":7}}}`,
			},
			{
				name: "without_total_tokens",
				raw:  `{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":13,"input_tokens_details":{"cached_tokens":5},"output_tokens_details":{"reasoning_tokens":7}}}`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				interactionsRaw := openairesponses.ConvertOpenAIResponsesResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, []byte(tc.raw), nil)
				inclusiveOut := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gpt-test", nil, nil, interactionsRaw, nil)
				if got := ojson.GetBytesNoCopy(inclusiveOut, "usage.output_tokens").Int(); got != 13 {
					t.Fatalf("inclusive usage.output_tokens = %d, want 13; interactions=%s out=%s", got, interactionsRaw, inclusiveOut)
				}
			})
		}
	})

	t.Run("thought_only_usage_sets_output_tokens", func(t *testing.T) {
		raw := []byte(`{"id":"i_u3","model":"gemini-3-pro","status":"incomplete","steps":[],"usage":{"total_input_tokens":50,"total_thought_tokens":90}}`)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3-pro", nil, nil, raw, nil)
		if got := ojson.GetBytesNoCopy(out, "usage.output_tokens").Int(); got != 90 {
			t.Fatalf("usage.output_tokens = %d, want 90; out=%s", got, out)
		}
	})

	t.Run("claude_to_interactions_roundtrip_preserves_uncached_input_and_output", func(t *testing.T) {
		claudeRaw := []byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"thinking","thinking":"reasoning"},{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}],"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":1,"cache_creation_input_tokens":4,"thinking_tokens":5}}`)
		interactionsRaw := claudeinteractions.ConvertClaudeResponseToInteractionsNonStream(context.Background(), "claude-test", nil, nil, claudeRaw, nil)
		out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "claude-test", nil, nil, interactionsRaw, nil)
		if got := ojson.GetBytesNoCopy(out, "usage.input_tokens").Int(); got != 3 {
			t.Fatalf("usage.input_tokens = %d, want 3; interactions=%s out=%s", got, interactionsRaw, out)
		}
		if got := ojson.GetBytesNoCopy(out, "usage.output_tokens").Int(); got != 2 {
			t.Fatalf("usage.output_tokens = %d, want 2; interactions=%s out=%s", got, interactionsRaw, out)
		}
		if got := ojson.GetBytesNoCopy(out, "usage.cache_read_input_tokens").Int(); got != 5 {
			t.Fatalf("usage.cache_read_input_tokens = %d, want 5; interactions=%s out=%s", got, interactionsRaw, out)
		}
	})
}

func TestConvertInteractionsResponseToClaudeStream_MultiLineDataSSE(t *testing.T) {
	var param any
	chunk := []byte("data: {\"event_type\":\"step.delta\",\n" +
		"data: \"index\":0,\n" +
		"data: \"delta\":{\"type\":\"text\",\"text\":\"hello multi-line\"}}")
	out := ConvertInteractionsResponseToClaude(context.Background(), "gemini-3-pro", nil, nil, chunk, &param)
	delta := findClaudeEventPayload(out, "content_block_delta")
	if got := ojson.GetBytesNoCopy(delta, "delta.text").String(); got != "hello multi-line" {
		t.Fatalf("delta.text = %q, want hello multi-line; events=%s", got, string(bytes.Join(out, []byte("\n"))))
	}
}
