package responses

import (
	"context"
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
