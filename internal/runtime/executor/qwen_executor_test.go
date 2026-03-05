package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "qwen-max", "qwen-max", ""},
		{"with level suffix", "qwen-max(high)", "qwen-max", "high"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max", "16384"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest", "medium"},
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
