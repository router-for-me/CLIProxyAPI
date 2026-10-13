package common_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	chat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	responses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/tidwall/gjson"
)

func TestClaudeNativeResponseNonStreamModelAndContent(t *testing.T) {
	text := "line 1\n\"quoted\" \\ 雪 <tag>"
	input := map[string]any{"nested": map[string]any{"text": text, "items": []any{true, nil, float64(2)}}, "empty": map[string]any{}}
	raw, errMarshal := json.Marshal(map[string]any{
		"id": "msg_native", "type": "message", "role": "assistant", "model": "claude-native",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": text, "signature": "sig\n\"native"},
			map[string]any{"type": "redacted_thinking", "data": "opaque-data"},
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "tool_use", "id": "tool_native", "name": "lookup", "input": input},
		},
		"stop_reason": "tool_use", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 11, "output_tokens": 9},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, provider := range []struct {
		name      string
		convert   func(context.Context, string, []byte, []byte, []byte, *any) []byte
		textPath  string
		thinkPath string
		argsPath  string
	}{
		{"chat", chat.ConvertClaudeResponseToOpenAINonStream, "choices.0.message.content", "choices.0.message.reasoning_content", "choices.0.message.tool_calls.0.function.arguments"},
		{"responses", responses.ConvertClaudeResponseToOpenAIResponsesNonStream, "output.2.content.0.text", "output.0.summary.0.text", "output.3.arguments"},
	} {
		t.Run(provider.name, func(t *testing.T) {
			for _, request := range []string{`{}`, `{"model":"request-alias"}`} {
				t.Run(request, func(t *testing.T) {
					out := provider.convert(context.Background(), "executor-alias", []byte(request), []byte(request), raw, nil)
					root := gjson.ParseBytes(out)
					for path, want := range map[string]string{"model": "claude-native", "id": "msg_native", provider.textPath: text, provider.thinkPath: text} {
						if got := root.Get(path).String(); got != want {
							t.Errorf("%s = %q, want %q", path, got, want)
						}
					}
					var gotInput map[string]any
					if errUnmarshal := json.Unmarshal([]byte(root.Get(provider.argsPath).String()), &gotInput); errUnmarshal != nil {
						t.Errorf("invalid tool arguments: %v", errUnmarshal)
					} else if !reflect.DeepEqual(gotInput, input) {
						t.Errorf("tool arguments = %#v, want %#v", gotInput, input)
					}
					if provider.name == "responses" {
						for path, want := range map[string]string{
							"output.0.type": "reasoning", "output.0.encrypted_content": "sig\n\"native",
							"output.1.type": "reasoning", "output.1.encrypted_content": responses.ClaudeResponsesRedactedThinkingPrefix + "opaque-data",
							"output.3.call_id": "tool_native", "output.3.name": "lookup",
						} {
							if got := root.Get(path).String(); got != want {
								t.Errorf("%s = %q, want %q", path, got, want)
							}
						}
					}
				})
			}
		})
	}
}
