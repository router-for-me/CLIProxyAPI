package executor

import "testing"

func TestIsDeepSeekModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		// Positive cases — DeepSeek prefix, various casings
		{name: "lowercase prefix", model: "deepseek-v3", want: true},
		{name: "mixed case prefix", model: "DeepSeek-R1", want: true},
		{name: "uppercase prefix", model: "DEEPSEEK-chat", want: true},
		{name: "deepseek-coder", model: "deepseek-coder", want: true},
		{name: "deepseek-reasoner", model: "deepseek-reasoner", want: true},
		{name: "leading space trimmed", model: "  deepseek-v3  ", want: true},
		// Negative cases — other providers or empty
		{name: "empty string", model: "", want: false},
		{name: "gpt-4", model: "gpt-4", want: false},
		{name: "claude-3", model: "claude-3-opus", want: false},
		{name: "gemini model", model: "gemini-2.5-flash", want: false},
		{name: "kimi model", model: "kimi-k2", want: false},
		{name: "contains deepseek but no prefix", model: "x-deepseek-v3", want: false},
		// Boundary: "deepseek" alone (no dash suffix) should not match since prefix requires "deepseek-"
		{name: "deepseek without dash", model: "deepseek", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDeepSeekModel(tt.model)
			if got != tt.want {
				t.Errorf("isDeepSeekModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}