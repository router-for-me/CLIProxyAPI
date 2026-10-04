package chat_completions

import (
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAINonStream_NativeMessagesJSON(t *testing.T) {
	for _, tt := range []struct {
		name         string
		content      string
		stopReason   string
		finishReason string
		tools        bool
	}{
		{"text", `[{"type":"text","text":"Hello "},{"type":"text","text":"world!"}]`, "end_turn", "stop", false},
		{"tools", `[{"type":"text","text":"Hello world!"},{"type":"tool_use","id":"toolu_weather","name":"get_weather","input":{"city":"Paris","days":2}},{"type":"tool_use","id":"toolu_clock","name":"get_time","input":{}}]`, "tool_use", "tool_calls", true},
		{"max_tokens", `[{"type":"text","text":"Hello world!"}]`, "max_tokens", "length", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"id":"msg_native","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":%s,"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":13,"cache_read_input_tokens":7,"cache_creation_input_tokens":3,"output_tokens":5}}`, tt.content, tt.stopReason))
			request := []byte(`{"model":"claude-sonnet-4-6"}`)
			out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-sonnet-4-6", request, request, raw, nil)
			if !gjson.ValidBytes(out) {
				t.Fatalf("invalid response JSON: %s", out)
			}
			root := gjson.ParseBytes(out)
			for path, want := range map[string]string{
				"id":                        "msg_native",
				"object":                    "chat.completion",
				"model":                     "claude-sonnet-4-6",
				"choices.0.message.role":    "assistant",
				"choices.0.message.content": "Hello world!",
				"choices.0.finish_reason":   tt.finishReason,
			} {
				if got := root.Get(path).String(); got != want {
					t.Errorf("%s = %q, want %q", path, got, want)
				}
			}
			for path, want := range map[string]int64{
				"choices.#":                                          1,
				"usage.prompt_tokens":                                23,
				"usage.completion_tokens":                            5,
				"usage.total_tokens":                                 28,
				"usage.prompt_tokens_details.cached_tokens":          7,
				"usage.prompt_tokens_details.cached_creation_tokens": 3,
			} {
				if got := root.Get(path).Int(); got != want {
					t.Errorf("%s = %d, want %d", path, got, want)
				}
			}
			calls := root.Get("choices.0.message.tool_calls")
			if !tt.tools {
				if len(calls.Array()) != 0 {
					t.Errorf("unexpected tool calls: %s", calls.Raw)
				}
				return
			}
			if got := len(calls.Array()); got != 2 {
				t.Errorf("tool call count = %d, want 2", got)
			}
			for path, want := range map[string]string{
				"0.id": "toolu_weather", "0.type": "function", "0.function.name": "get_weather",
				"1.id": "toolu_clock", "1.type": "function", "1.function.name": "get_time",
			} {
				if got := calls.Get(path).String(); got != want {
					t.Errorf("tool_calls.%s = %q, want %q", path, got, want)
				}
			}
			args := gjson.Parse(calls.Get("0.function.arguments").String())
			if !args.IsObject() || len(args.Map()) != 2 || args.Get("city").String() != "Paris" || args.Get("days").Int() != 2 {
				t.Errorf("weather arguments not preserved: %s", calls.Get("0.function.arguments").Raw)
			}
			emptyArgs := gjson.Parse(calls.Get("1.function.arguments").String())
			if !emptyArgs.IsObject() || len(emptyArgs.Map()) != 0 {
				t.Errorf("empty arguments not preserved: %s", calls.Get("1.function.arguments").Raw)
			}
		})
	}
}
