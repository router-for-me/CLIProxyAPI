package claude

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToInteractionsMapsMessagesToolsAndStream(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"max_tokens":1024,"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}],"messages":[{"role":"user","content":[{"type":"text","text":"今天北京的天气怎么样？"}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, true)
	if got := gjson.GetBytes(out, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Output: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should be true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 1024 {
		t.Fatalf("max_output_tokens = %d, want 1024. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("input text = %q. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.location.type").String(); got != "string" {
		t.Fatalf("tool schema was not mapped. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsMapsToolUseAndResult(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"北京"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"晴"}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.id").String(); got != "toolu_1" {
		t.Fatalf("id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.call_id").Exists() {
		t.Fatalf("function_call should not have call_id parameter. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "get_weather" {
		t.Fatalf("name = %q, want get_weather. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "toolu_1" {
		t.Fatalf("call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.1.id").Exists() {
		t.Fatalf("function_result should not have id parameter. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.1.result").String(); got != "晴" {
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
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	// Since AlignClaudeToolResults aligns tool results with tool_use order (toolu_1 then toolu_2):
	// input.0: function_call toolu_1 (lookup)
	// input.1: function_call toolu_2 (weather)
	// input.2: function_result toolu_1 (lookup)
	// input.3: function_result toolu_2 (weather)
	if got := gjson.GetBytes(out, "input.2.call_id").String(); got != "toolu_1" {
		t.Fatalf("input.2.call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.name").String(); got != "lookup" {
		t.Fatalf("input.2.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.3.call_id").String(); got != "toolu_2" {
		t.Fatalf("input.3.call_id = %q, want toolu_2. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.3.name").String(); got != "weather" {
		t.Fatalf("input.3.name = %q, want weather. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsPropagatesIsError(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_err","content":"command failed","is_error":true}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if !gjson.GetBytes(out, "input.0.is_error").Bool() {
		t.Fatalf("expected input.0.is_error = true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "toolu_err" {
		t.Fatalf("call_id = %q, want toolu_err. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.id").Exists() {
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
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	inputs := gjson.GetBytes(out, "input").Array()

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
	if payload := findClaudeEventPayload(out, "message_start"); gjson.GetBytes(payload, "message.model").String() != "gemini-3.1-flash-lite" {
		t.Fatalf("message_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); gjson.GetBytes(payload, "delta.text").String() != "北京今天晴" {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); gjson.GetBytes(payload, "usage.output_tokens").Int() != 4 {
		t.Fatalf("message_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_stop"); gjson.GetBytes(payload, "type").String() != "message_stop" {
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
	if payload := findClaudeEventPayload(out, "content_block_start"); gjson.GetBytes(payload, "content_block.type").String() != "tool_use" {
		t.Fatalf("content_block_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); gjson.GetBytes(payload, "content_block.signature").String() != "sig_1" {
		t.Fatalf("content_block_start signature payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); gjson.GetBytes(payload, "delta.partial_json").String() != `{"location":"北京"}` {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); gjson.GetBytes(payload, "delta.stop_reason").String() != "tool_use" {
		t.Fatalf("message_delta payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToClaude(context.Background(), "claude-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_tokens":8}}}`), &param)
	payload := findClaudeEventPayload(out, "message_delta")
	if len(payload) == 0 {
		t.Fatalf("message_delta payload not found")
	}
	if got := gjson.GetBytes(payload, "usage.input_tokens").Int(); got != 2 {
		t.Fatalf("input_tokens = %d, want 2. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.output_tokens").Int(); got != 6 {
		t.Fatalf("output_tokens = %d, want 6. Payload: %s", got, string(payload))
	}
}

func TestConvertInteractionsResponseToClaudeNonStream(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","model":"gemini-3.1-flash-lite","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"function_call","call_id":"toolu_1","signature":"sig_1","name":"lookup","arguments":{"q":"x"}}],"usage":{"total_input_tokens":3,"total_output_tokens":4}}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "ok" {
		t.Fatalf("text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.1.type").String(); got != "tool_use" {
		t.Fatalf("tool block type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.1.signature").String(); got != "sig_1" {
		t.Fatalf("tool signature = %q, want sig_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 3 {
		t.Fatalf("input_tokens = %d, want 3. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToClaude_IncompleteMaxTokens(t *testing.T) {
	// Non-stream
	raw := []byte(`{"id":"interaction_1","model":"devin/swe-2","status":"incomplete","finish_reason":"length","steps":[{"type":"model_output","content":[{"type":"text","text":"cut short"}]}],"usage":{"total_input_tokens":3,"total_output_tokens":4}}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "max_tokens" {
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
	if got := gjson.GetBytes(msgDelta, "delta.stop_reason").String(); got != "max_tokens" {
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
		usage := gjson.GetBytes(msgDelta, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
		usage := gjson.GetBytes(out, "usage")
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
