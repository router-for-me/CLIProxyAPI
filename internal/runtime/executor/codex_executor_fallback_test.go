package executor

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCodexFallbackDisplayModel(t *testing.T) {
	tests := []struct {
		name     string
		opts     cliproxyexecutor.Options
		expected string
	}{
		{
			name:     "no fallback",
			opts:     cliproxyexecutor.Options{},
			expected: "",
		},
		{
			name: "with fallback",
			opts: cliproxyexecutor.Options{
				Metadata: map[string]any{
					cliproxyexecutor.CodexFallbackDisplayModelMetadataKey: "gpt-5.5",
				},
			},
			expected: "gpt-5.5",
		},
		{
			name:     "nil metadata",
			opts:     cliproxyexecutor.Options{Metadata: nil},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexFallbackDisplayModel(tt.opts)
			if got != tt.expected {
				t.Errorf("codexFallbackDisplayModel() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRewriteCodexResponseModel(t *testing.T) {
	tests := []struct {
		name          string
		payload       []byte
		displayModel  string
		wantUnchanged bool
		wantModel     string
	}{
		{
			name:          "empty display model",
			payload:       []byte(`{"model":"gpt-5.4","choices":[]}`),
			displayModel:  "",
			wantUnchanged: true,
		},
		{
			name:          "empty payload",
			payload:       []byte{},
			displayModel:  "gpt-5.5",
			wantUnchanged: true,
		},
		{
			name:         "rewrite model field",
			payload:      []byte(`{"model":"gpt-5.4","choices":[]}`),
			displayModel: "gpt-5.5",
			wantModel:    "gpt-5.5",
		},
		{
			name:         "rewrite model and response.model",
			payload:      []byte(`{"model":"gpt-5.4","response":{"model":"gpt-5.4","output":[]}}`),
			displayModel: "gpt-5.5",
			wantModel:    "gpt-5.5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteCodexResponseModel(tt.payload, tt.displayModel)
			if tt.wantUnchanged {
				if string(got) != string(tt.payload) {
					t.Errorf("rewriteCodexResponseModel() changed payload unexpectedly: got %s", got)
				}
				return
			}
			modelVal := gjson.GetBytes(got, "model").String()
			if modelVal != tt.wantModel {
				t.Errorf("rewriteCodexResponseModel() model = %q, want %q", modelVal, tt.wantModel)
			}
			if gjson.GetBytes(tt.payload, "response.model").Exists() {
				responseModelVal := gjson.GetBytes(got, "response.model").String()
				if responseModelVal != tt.wantModel {
					t.Errorf("rewriteCodexResponseModel() response.model = %q, want %q", responseModelVal, tt.wantModel)
				}
			}
		})
	}
}
