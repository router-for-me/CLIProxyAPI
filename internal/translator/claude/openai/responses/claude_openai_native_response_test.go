package responses

import (
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAIResponsesNonStream_NativeMessagesJSON(t *testing.T) {
	for _, tt := range []struct {
		name       string
		content    string
		stopReason string
		status     string
		tools      bool
	}{
		{"text", `[{"type":"text","text":"Hello "},{"type":"text","text":"world!"}]`, "end_turn", "completed", false},
		{"tools", `[{"type":"text","text":"Hello world!"},{"type":"tool_use","id":"toolu_weather","name":"get_weather","input":{"city":"Paris","days":2}},{"type":"tool_use","id":"toolu_clock","name":"get_time","input":{}}]`, "tool_use", "completed", true},
		{"max_tokens", `[{"type":"text","text":"Hello world!"}]`, "max_tokens", "incomplete", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"id":"msg_native","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":%s,"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":13,"cache_read_input_tokens":7,"cache_creation_input_tokens":3,"output_tokens":5}}`, tt.content, tt.stopReason))
			request := []byte(`{"model":"claude-sonnet-4-6"}`)
			out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-sonnet-4-6", request, request, raw, nil)
			if !gjson.ValidBytes(out) {
				t.Fatalf("invalid response JSON: %s", out)
			}
			root := gjson.ParseBytes(out)
			for path, want := range map[string]string{
				"id":                      "msg_native",
				"object":                  "response",
				"model":                   "claude-sonnet-4-6",
				"status":                  tt.status,
				"output.0.type":           "message",
				"output.0.role":           "assistant",
				"output.0.status":         tt.status,
				"output.0.content.0.type": "output_text",
				"output.0.content.0.text": "Hello world!",
			} {
				if got := root.Get(path).String(); got != want {
					t.Errorf("%s = %q, want %q", path, got, want)
				}
			}
			for path, want := range map[string]int64{
				"usage.input_tokens":                       23,
				"usage.output_tokens":                      5,
				"usage.total_tokens":                       28,
				"usage.input_tokens_details.cached_tokens": 7,
			} {
				if got := root.Get(path).Int(); got != want {
					t.Errorf("%s = %d, want %d", path, got, want)
				}
			}
			if tt.status == "incomplete" {
				if got := root.Get("incomplete_details.reason").String(); got != "max_output_tokens" {
					t.Errorf("incomplete reason = %q, want max_output_tokens", got)
				}
			} else if got := root.Get("incomplete_details"); !got.Exists() || got.Type != gjson.Null {
				t.Errorf("incomplete_details = %s, want null", got.Raw)
			}
			if !tt.tools {
				if got := len(root.Get("output").Array()); got != 1 {
					t.Errorf("output count = %d, want 1", got)
				}
				return
			}
			if got := len(root.Get("output").Array()); got != 3 {
				t.Errorf("output count = %d, want 3", got)
			}
			for path, want := range map[string]string{
				"output.1.call_id": "toolu_weather", "output.1.type": "function_call", "output.1.name": "get_weather", "output.1.status": "completed",
				"output.2.call_id": "toolu_clock", "output.2.type": "function_call", "output.2.name": "get_time", "output.2.status": "completed",
			} {
				if got := root.Get(path).String(); got != want {
					t.Errorf("%s = %q, want %q", path, got, want)
				}
			}
			args := gjson.Parse(root.Get("output.1.arguments").String())
			if !args.IsObject() || len(args.Map()) != 2 || args.Get("city").String() != "Paris" || args.Get("days").Int() != 2 {
				t.Errorf("weather arguments not preserved: %s", root.Get("output.1.arguments").Raw)
			}
			emptyArgs := gjson.Parse(root.Get("output.2.arguments").String())
			if !emptyArgs.IsObject() || len(emptyArgs.Map()) != 0 {
				t.Errorf("empty arguments not preserved: %s", root.Get("output.2.arguments").Raw)
			}
		})
	}
}

func TestNativeMessagesJSONCitations(t *testing.T) {
	raw := []byte(`{"id":"msg_citations","type":"message","model":"claude-native","content":[{"type":"text","text":"Answer.","citations":[{"type":"web_search_result_location","url":"https://example.com","title":"Source","cited_text":"Answer.","encrypted_index":"IDX"}]}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "", nil, nil, raw, nil)
	annotations := gjson.GetBytes(out, "output.0.content.0.annotations")
	if len(annotations.Array()) != 1 || annotations.Get("0.url").String() != "https://example.com" || annotations.Get("0.encrypted_index").String() != "IDX" {
		t.Fatalf("citations not preserved: %s", out)
	}
}
