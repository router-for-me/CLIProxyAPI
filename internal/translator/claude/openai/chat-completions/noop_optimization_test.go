package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAINonStreamFinishReasons(t *testing.T) {
	tests := []struct {
		name       string
		stopReason string
		want       string
	}{
		{name: "missing", want: "stop"},
		{name: "end_turn", stopReason: "end_turn", want: "stop"},
		{name: "stop_sequence", stopReason: "stop_sequence", want: "stop"},
		{name: "max_tokens", stopReason: "max_tokens", want: "length"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			raw := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"` + testCase.stopReason + `"}}`)
			output := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, raw, nil)
			if got := gjson.GetBytes(output, "choices.0.finish_reason").String(); got != testCase.want {
				t.Fatalf("finish_reason = %q, want %q", got, testCase.want)
			}
		})
	}
}
