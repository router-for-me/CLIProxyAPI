package helps

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithTrackedPathsForExecutorCodexIntegerNormalizationUsesExecutor(t *testing.T) {
	headers := make(http.Header)
	headers.Set("User-Agent", "codex_cli_rs/0.1")
	input := []byte(`{"model":"copilot-model","tools":[{"type":"function","name":"collaboration__wait_agent","description":"Wait for a delegated task.","parameters":{"type":"object","properties":{"timeout_ms":{"type":"number","minimum":0}},"required":["timeout_ms"],"additionalProperties":false}}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"collaboration__wait_agent","description":"Wait for a delegated task.","parameters":{"type":"object","properties":{"timeout_ms":{"type":"number","minimum":0}},"required":["timeout_ms"],"additionalProperties":false}}]}]}`)

	tests := []struct {
		name         string
		executor     string
		protocol     string
		fromProtocol string
		wantType     string
	}{
		{name: "native Codex executor keeps number", executor: "codex", protocol: sdktranslator.FormatOpenAIResponse.String(), fromProtocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "number"},
		{name: "Copilot native Responses request keeps reserved number types", executor: "copilot", protocol: sdktranslator.FormatOpenAIResponse.String(), fromProtocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "number"},
		{name: "Copilot Messages target normalizes Codex tools", executor: "copilot", protocol: sdktranslator.FormatClaude.String(), fromProtocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "integer"},
		{name: "Copilot Chat target normalizes Codex tools", executor: "copilot", protocol: sdktranslator.FormatOpenAI.String(), fromProtocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "integer"},
		{name: "Copilot Responses from a non-Responses source normalizes", executor: "copilot", protocol: sdktranslator.FormatOpenAIResponse.String(), fromProtocol: sdktranslator.FormatClaude.String(), wantType: "integer"},
		{name: "another Responses executor normalizes Codex tools", executor: "xai", protocol: sdktranslator.FormatOpenAIResponse.String(), fromProtocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "integer"},
		{name: "xAI Codex protocol normalizes", executor: "xai", protocol: sdktranslator.FormatCodex.String(), fromProtocol: sdktranslator.FormatOpenAIResponse.String(), wantType: "integer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ApplyPayloadConfigWithTrackedPathsForExecutor(nil, tt.executor, "copilot-model", tt.protocol, tt.fromProtocol, "", input, nil, "copilot-model", "", headers)
			for _, path := range []string{"tools.0.parameters.properties.timeout_ms.type", "input.0.tools.0.parameters.properties.timeout_ms.type"} {
				if typ := gjson.GetBytes(got, path).String(); typ != tt.wantType {
					t.Fatalf("%s = %q, want %q; payload=%s", path, typ, tt.wantType, got)
				}
			}
			if tt.wantType == "number" && !bytes.Equal(got, input) {
				t.Fatalf("native reserved schema request changed: %s", got)
			}
		})
	}
}

func TestApplyPayloadConfigWithTrackedPathsForExecutorCopilotCodexSchemaOverride(t *testing.T) {
	headers := http.Header{"User-Agent": []string{"codex_cli_rs/0.1"}}
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "copilot-model", Protocol: "openai-response", FromProtocol: "responses"}},
		Params: map[string]any{"input.0.tools.0.parameters.properties.timeout_ms.type": "integer"},
	}}}}
	input := []byte(`{"model":"copilot-model","input":[{"type":"additional_tools","tools":[{"type":"function","name":"collaboration__wait_agent","description":"Wait for a delegated task.","parameters":{"type":"object","properties":{"timeout_ms":{"type":"number","minimum":0}},"required":["timeout_ms"],"additionalProperties":false}}]}]}`)
	got, _ := ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, "copilot", "copilot-model", "openai-response", "openai-response", "", input, input, "copilot-model", "", headers)
	if typ := gjson.GetBytes(got, "input.0.tools.0.parameters.properties.timeout_ms.type").String(); typ != "integer" {
		t.Fatalf("explicit payload override timeout_ms.type = %q, want integer; payload=%s", typ, got)
	}
	if gjson.GetBytes(got, "input.0.tools.0.parameters.properties.timeout_ms.minimum").Int() != 0 || !gjson.GetBytes(got, "input.0.tools.0.parameters.required.0").Exists() || !gjson.GetBytes(got, "input.0.tools.0.parameters.additionalProperties").Exists() {
		t.Fatalf("explicit payload override changed unrelated function schema: %s", got)
	}
}
