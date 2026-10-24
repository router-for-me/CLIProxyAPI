package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func payloadRulesTestConfig() *config.Config {
	models := []config.PayloadModelRule{{Name: "claude-model", Protocol: "openai"}}
	return &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: models, Params: map[string]any{"temperature": 0.2, "defaulted": true}}},
		Override: []config.PayloadRule{{Models: models, Params: map[string]any{"max_tokens": 64}}},
		Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"secret"}}},
	}}
}

func TestExecutorAdapterAppliesPayloadRulesAfterTranslation(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesTestConfig()

	var gotModel string
	var pluginPayload []byte
	adapter := newCurrentExecutorAdapterForTest(host, "payload-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotModel = req.Model
			pluginPayload = append([]byte(nil), req.Payload...)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	// The thinking suffix is deliberately left in place: rules match on the
	// suffix-free model, and thinking handling is not part of this change.
	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model(high)",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model(high)","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"secret":"remove"}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotModel != "claude-model(high)" {
		t.Errorf("plugin request model = %q, want unchanged claude-model(high)", gotModel)
	}
	if got := gjson.GetBytes(pluginPayload, "temperature").Float(); got != 0.2 {
		t.Errorf("temperature = %v, want default 0.2", got)
	}
	if !gjson.GetBytes(pluginPayload, "defaulted").Bool() {
		t.Errorf("defaulted rule not applied: %s", pluginPayload)
	}
	if got := gjson.GetBytes(pluginPayload, "max_tokens").Int(); got != 64 {
		t.Errorf("max_tokens = %d, want overridden 64", got)
	}
	if gjson.GetBytes(pluginPayload, "secret").Exists() {
		t.Errorf("filtered secret remains in plugin payload: %s", pluginPayload)
	}
}

func TestExecutorAdapterAppliesPayloadRulesToStreams(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesTestConfig()

	var pluginPayload []byte
	adapter := newCurrentExecutorAdapterForTest(host, "payload-stream-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			pluginPayload = append([]byte(nil), req.Payload...)
			chunks := make(chan pluginapi.ExecutorStreamChunk)
			close(chunks)
			return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	result, errStream := adapter.ExecuteStream(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"claude-model","stream":true,"messages":[{"role":"user","content":"hello"}],"secret":"remove"}`),
	}, coreexecutor.Options{
		Stream:         true,
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range result.Chunks {
	}
	if got := gjson.GetBytes(pluginPayload, "max_tokens").Int(); got != 64 {
		t.Errorf("stream max_tokens = %d, want overridden 64: %s", got, pluginPayload)
	}
	if gjson.GetBytes(pluginPayload, "secret").Exists() {
		t.Errorf("filtered secret remains in stream payload: %s", pluginPayload)
	}
}

func TestExecutorAdapterWithoutPayloadRulesLeavesPayloadUntouched(t *testing.T) {
	host := New()
	host.runtimeConfig = &config.Config{}

	var pluginPayload []byte
	adapter := newCurrentExecutorAdapterForTest(host, "payload-none-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			pluginPayload = append([]byte(nil), req.Payload...)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	body := `{"model":"claude-model","messages":[{"role":"user","content":"hello"}],"secret":"keep"}`
	if _, err := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model: "claude-model", Format: sdktranslator.FormatOpenAI, Payload: []byte(body),
	}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(pluginPayload) != body {
		t.Errorf("payload changed without rules:\n got %s\nwant %s", pluginPayload, body)
	}
}
