package pluginhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// payloadUpstream records the JSON bodies a plugin sends through host.http.
type payloadUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newPayloadUpstream(t *testing.T) *payloadUpstream {
	t.Helper()
	u := &payloadUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":11}}`))
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *payloadUpstream) last(t *testing.T) []byte {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		t.Fatal("upstream received no request")
	}
	return u.bodies[len(u.bodies)-1]
}

func payloadRulesConfig(protocol, fromProtocol string, params map[string]any) *config.Config {
	models := []config.PayloadModelRule{{Name: "claude-model", Protocol: protocol, FromProtocol: fromProtocol}}
	return &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: models, Params: map[string]any{"temperature": 0.2, "defaulted": true}}},
		Override: []config.PayloadRule{{Models: models, Params: params}},
		Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"secret"}}},
	}}
}

func TestExecutorAdapterAppliesPayloadRulesAfterTranslation(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})

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
		t.Errorf("temperature = %v, want default 0.2: %s", got, pluginPayload)
	}
	if !gjson.GetBytes(pluginPayload, "defaulted").Bool() {
		t.Errorf("defaulted rule not applied: %s", pluginPayload)
	}
	if got := gjson.GetBytes(pluginPayload, "max_tokens").Int(); got != 64 {
		t.Errorf("max_tokens = %d, want overridden 64: %s", got, pluginPayload)
	}
	if gjson.GetBytes(pluginPayload, "secret").Exists() {
		t.Errorf("filtered secret remains in plugin payload: %s", pluginPayload)
	}
}

func TestExecutorAdapterAppliesPayloadRulesToStreams(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})

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
		t.Errorf("stream max_tokens = %d, want override 64: %s", got, pluginPayload)
	}
	if gjson.GetBytes(pluginPayload, "secret").Exists() {
		t.Errorf("filtered field remained in stream plugin payload: %s", pluginPayload)
	}
}

func TestExecutorAdapterAppliesPayloadRulesToCountTokens(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})

	var gotModel string
	var pluginPayload []byte
	adapter := newCurrentExecutorAdapterForTest(host, "payload-count-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotModel = req.Model
			pluginPayload = append([]byte(nil), req.Payload...)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":13}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	_, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model(high)",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model(high)","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"secret":"remove"}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if gotModel != "claude-model(high)" {
		t.Errorf("plugin count_tokens model = %q, want unchanged claude-model(high)", gotModel)
	}
	if got := gjson.GetBytes(pluginPayload, "temperature").Float(); got != 0.2 {
		t.Errorf("count_tokens temperature = %v, want default 0.2: %s", got, pluginPayload)
	}
	if got := gjson.GetBytes(pluginPayload, "max_tokens").Int(); got != 64 {
		t.Errorf("count_tokens max_tokens = %d, want overridden 64: %s", got, pluginPayload)
	}
	if gjson.GetBytes(pluginPayload, "secret").Exists() {
		t.Errorf("filtered secret remains in count_tokens payload: %s", pluginPayload)
	}
}

func TestExecutorAdapterPayloadRulesCanonicalizeSourceFormatAlias(t *testing.T) {
	host := New()
	// The rule is gated on the documented canonical source protocol "claude".
	host.runtimeConfig = payloadRulesConfig("openai", "claude", map[string]any{"max_tokens": 64})

	var pluginPayload []byte
	adapter := newCurrentExecutorAdapterForTest(host, "payload-alias-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			pluginPayload = append([]byte(nil), req.Payload...)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	// "anthropic" is an accepted alias for the Claude source format.
	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.Format("anthropic"),
		Payload: []byte(`{"model":"claude-model","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.Format("anthropic"),
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(pluginPayload, "max_tokens").Int(); got != 64 {
		t.Errorf("max_tokens = %d, want 64: rule gated on from-protocol claude was skipped for alias source", got)
	}
}

func TestExecutorAdapterPayloadRulesUseAntigravityRequestRoot(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("antigravity", "", map[string]any{"generationConfig.maxOutputTokens": 64})

	var pluginPayload []byte
	adapter := newCurrentExecutorAdapterForTest(host, "payload-antigravity-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			pluginPayload = append([]byte(nil), req.Payload...)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"response":{}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatAntigravity}, []sdktranslator.Format{sdktranslator.FormatAntigravity})

	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatAntigravity,
		Payload: []byte(`{"model":"claude-model","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(pluginPayload, "request.generationConfig.maxOutputTokens").Int(); got != 64 {
		t.Errorf("request.generationConfig.maxOutputTokens = %d, want 64: %s", got, pluginPayload)
	}
	if gjson.GetBytes(pluginPayload, "generationConfig").Exists() {
		t.Errorf("rule created a top-level generationConfig beside request: %s", pluginPayload)
	}
}

func TestExecutorAdapterAppliesArrayIndexFilterOnlyOnceAcrossHostHTTP(t *testing.T) {
	host := New()
	models := []config.PayloadModelRule{{Name: "claude-model", Protocol: "openai"}}
	host.runtimeConfig = &config.Config{Payload: config.PayloadConfig{
		Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"messages.0"}}},
	}}
	upstream := newPayloadUpstream(t)

	adapter := newCurrentExecutorAdapterForTest(host, "payload-once-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			body, errSet := sjson.SetBytes(req.Payload, "user", "plugin-added")
			if errSet != nil {
				t.Fatalf("sjson.SetBytes() error = %v", errSet)
			}
			callbackID, closeCallback := host.openCallbackContext(ctx)
			defer closeCallback()
			raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodPost, URL: upstream.URL, Body: body})
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, raw); errCall != nil {
				t.Fatalf("host call error = %v", errCall)
			}
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"claude-model","messages":[{"role":"system","content":"drop"},{"role":"user","content":"keep"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	sent := upstream.last(t)
	if got := len(gjson.GetBytes(sent, "messages").Array()); got != 1 {
		t.Fatalf("messages count = %d, want 1: %s", got, sent)
	}
	if got := gjson.GetBytes(sent, "messages.0.content").String(); got != "keep" {
		t.Errorf("messages.0.content = %q, want keep: %s", got, sent)
	}
}

func TestExecutorAdapterDoesNotMutateSameModelAuxiliaryHostHTTPRequest(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})
	upstream := newPayloadUpstream(t)
	telemetryBody := `{"model":"claude-model","secret":"telemetry-value"}`

	adapter := newCurrentExecutorAdapterForTest(host, "payload-telemetry-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			callbackID, closeCallback := host.openCallbackContext(ctx)
			defer closeCallback()
			raw, _ := json.Marshal(rpcHostHTTPRequest{
				HostCallbackID: callbackID,
				Method:         http.MethodPost,
				URL:            upstream.URL,
				Body:           []byte(telemetryBody),
			})
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, raw); errCall != nil {
				t.Fatalf("host call error = %v", errCall)
			}
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	if _, err := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"claude-model","messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(upstream.last(t)); got != telemetryBody {
		t.Errorf("telemetry body = %s, want unchanged %s", got, telemetryBody)
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
