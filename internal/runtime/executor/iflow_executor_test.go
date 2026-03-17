package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

func TestIFlowExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "glm-4", "glm-4", ""},
		{"glm with suffix", "glm-4.1-flash(high)", "glm-4.1-flash", "high"},
		{"minimax no suffix", "minimax-m2", "minimax-m2", ""},
		{"minimax with suffix", "minimax-m2.1(medium)", "minimax-m2.1", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestPreserveReasoningContentInMessages(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  []byte // nil means output should equal input
	}{
		{
			"non-glm model passthrough",
			[]byte(`{"model":"gpt-4","messages":[]}`),
			nil,
		},
		{
			"glm model with empty messages",
			[]byte(`{"model":"glm-4","messages":[]}`),
			nil,
		},
		{
			"glm model preserves existing reasoning_content",
			[]byte(`{"model":"glm-4","messages":[{"role":"assistant","content":"hi","reasoning_content":"thinking..."}]}`),
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preserveReasoningContentInMessages(tt.input)
			want := tt.want
			if want == nil {
				want = tt.input
			}
			if string(got) != string(want) {
				t.Errorf("preserveReasoningContentInMessages() = %s, want %s", got, want)
			}
		})
	}
}
