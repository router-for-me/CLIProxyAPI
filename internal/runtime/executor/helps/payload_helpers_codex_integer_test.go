package helps

import (
	"net/http"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithTrackedPathsForExecutorCodexIntegerNormalizationUsesExecutor(t *testing.T) {
	headers := make(http.Header)
	headers.Set("User-Agent", "codex_cli_rs/0.1")
	input := []byte(`{"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}]}`)

	tests := []struct {
		name     string
		executor string
		protocol string
		wantType string
	}{
		{name: "Codex executor with another protocol keeps number", executor: "codex", protocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "number"},
		{name: "xAI executor with Codex protocol normalizes", executor: "xai", protocol: sdktranslator.FormatCodex.String(), wantType: "integer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ApplyPayloadConfigWithTrackedPathsForExecutor(nil, tt.executor, "model", tt.protocol, "", "", input, nil, "", "", headers)
			if typ := gjson.GetBytes(got, "tools.0.parameters.properties.yield_time_ms.type").String(); typ != tt.wantType {
				t.Fatalf("yield_time_ms.type = %q, want %q; payload=%s", typ, tt.wantType, got)
			}
		})
	}
}
