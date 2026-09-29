package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIResponsesPreparationStripsUnsupportedConfigurationUpdates(t *testing.T) {
	const source = `{"model":"grok-4.5","reasoning":{"effort":"medium","summary":"auto"},"input":[{"role":"user","content":"hi"},{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","tools":[]},{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"again"}]}`
	for _, tc := range []struct {
		name, model, effort string
		stream              bool
	}{
		{name: "nonstream latest update", model: "grok-4.5", effort: "high"},
		{name: "stream suffix wins", model: "grok-4.5(low)", effort: "low", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := NewXAIExecutor(&config.Config{})
			prepared, errPrepare := exec.prepareResponsesRequest(t.Context(), cliproxyexecutor.Request{Model: tc.model, Payload: []byte(source)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, tc.stream)
			if errPrepare != nil {
				t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
			}
			if got := gjson.GetBytes(prepared.body, "reasoning.effort").String(); got != tc.effort {
				t.Fatalf("target effort = %q, want %q; body=%s", got, tc.effort, prepared.body)
			}
			if got := thinking.ExtractReasoningEffort([]byte(source), "openai-response", tc.model); got != "high" {
				t.Fatalf("source usage effort = %q, want high", got)
			}
			if got := thinking.ExtractTranslatedReasoningEffort(prepared.body, "xai"); got != tc.effort {
				t.Fatalf("target usage effort = %q, want %q", got, tc.effort)
			}
			input := gjson.GetBytes(prepared.body, "input").Array()
			if len(input) != 2 || input[0].Get("content").String() != "hi" || input[1].Get("content").String() != "again" {
				t.Fatalf("unsupported updates remain or messages changed: %s", prepared.body)
			}
			if got := gjson.GetBytes(prepared.body, "reasoning.summary").String(); got != "auto" {
				t.Fatalf("reasoning.summary = %q, want auto; body=%s", got, prepared.body)
			}
		})
	}
}

func TestXAIResponsesPreparationHonorsExplicitSupportOnly(t *testing.T) {
	const source = `{"model":"opaque-xai-route","reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hi"}]}`
	info := &registry.ModelInfo{ID: "opaque-xai-route", Type: "xai", SupportConfigurationUpdate: true, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}
	for _, tc := range []struct{ model, top string }{
		{model: "opaque-xai-route", top: "medium"},
		{model: "opaque-xai-route(low)", top: "low"},
	} {
		exec := NewXAIExecutor(&config.Config{})
		prepared, errPrepare := exec.prepareResponsesRequest(t.Context(), cliproxyexecutor.Request{Model: tc.model, Payload: []byte(source), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": info}}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
		if errPrepare != nil {
			t.Fatal(errPrepare)
		}
		if got := gjson.GetBytes(prepared.body, "reasoning.effort").String(); got != tc.top {
			t.Fatalf("top-level effort = %q, want %q; body=%s", got, tc.top, prepared.body)
		}
		if got := gjson.GetBytes(prepared.body, "input.0.reasoning.effort").String(); got != "high" {
			t.Fatalf("explicitly supported update was lost: %s", prepared.body)
		}
		if got := thinking.ExtractTranslatedReasoningEffort(prepared.body, "xai"); got != "high" {
			t.Fatalf("target usage effort = %q, want high", got)
		}
	}
}
