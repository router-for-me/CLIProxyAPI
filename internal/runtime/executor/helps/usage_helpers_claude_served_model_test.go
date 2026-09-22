package helps

import (
	"testing"
)

func TestParseClaudeServedModel(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "non-stream response body",
			payload: `{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":10}}`,
			want:    "claude-opus-5",
		},
		{
			name:    "stream message_start data line",
			payload: `data: {"type":"message_start","message":{"model":"claude-haiku-4-5"}}`,
			want:    "claude-haiku-4-5",
		},
		{
			name:    "top-level model on stream data line",
			payload: `data: {"type":"message_delta","model":"claude-opus-5"}`,
			want:    "claude-opus-5",
		},
		{
			name:    "nested message.model when top-level absent",
			payload: `{"message":{"model":"claude-sonnet-5"}}`,
			want:    "claude-sonnet-5",
		},
		{
			name:    "nested message.model preferred over top-level model",
			payload: `{"model":"claude-opus-5","message":{"model":"claude-haiku-4-5"}}`,
			want:    "claude-haiku-4-5",
		},
		{
			name:    "model absent",
			payload: `{"usage":{"input_tokens":10}}`,
			want:    "",
		},
		{
			name:    "event prefix line carries no json",
			payload: `event: message_start`,
			want:    "",
		},
		{
			name:    "not json",
			payload: `data: [DONE]`,
			want:    "",
		},
		{
			name:    "empty payload",
			payload: ``,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseClaudeServedModel([]byte(tt.payload))
			if got != tt.want {
				t.Fatalf("ParseClaudeServedModel(%q) = %q, want %q", tt.payload, got, tt.want)
			}
		})
	}
}
