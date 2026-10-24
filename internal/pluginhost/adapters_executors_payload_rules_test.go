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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
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

func (u *payloadUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func payloadRulesConfig(protocol, fromProtocol string, params map[string]any) *config.Config {
	models := []config.PayloadModelRule{{Name: "claude-model", Protocol: protocol, FromProtocol: fromProtocol}}
	return &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: models, Params: map[string]any{"temperature": 0.2, "defaulted": true}}},
		Override: []config.PayloadRule{{Models: models, Params: params}},
		Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"secret"}}},
	}}
}

// sendFromPlugin mimics a plugin that builds its own upstream body from the
// request (re-adding a filtered field and a conflicting value) and sends it
// through the host HTTP callback.
func sendFromPlugin(t *testing.T, host *Host, ctx context.Context, rawURL, method string, req pluginapi.ExecutorRequest) {
	t.Helper()
	body := req.Payload
	body, _ = sjson.SetBytes(body, "secret", "restored-by-plugin")
	body, _ = sjson.SetBytes(body, "max_tokens", 8)
	callbackID, closeCallback := host.openCallbackContext(ctx)
	defer closeCallback()
	raw, errMarshal := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodPost, URL: rawURL, Body: body})
	if errMarshal != nil {
		t.Fatalf("marshal host http request: %v", errMarshal)
	}
	response, errCall := host.callFromPlugin(context.Background(), method, raw)
	if errCall != nil {
		t.Fatalf("host call %s error = %v", method, errCall)
	}
	if method == pluginabi.MethodHostHTTPDoStream {
		var opened struct {
			StreamID string `json:"stream_id"`
		}
		decoded, errDecode := decodeRPCEnvelope[struct {
			StreamID string `json:"stream_id"`
		}](response)
		if errDecode != nil {
			t.Fatalf("decode stream open: %v", errDecode)
		}
		opened = decoded
		closeRaw, _ := json.Marshal(map[string]string{"stream_id": opened.StreamID})
		if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPStreamClose, closeRaw); errClose != nil {
			t.Fatalf("close stream: %v", errClose)
		}
	}
}

func TestExecutorAdapterAppliesPayloadRulesAtHostHTTPSendBoundary(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})
	upstream := newPayloadUpstream(t)

	var gotModel string
	adapter := newCurrentExecutorAdapterForTest(host, "payload-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotModel = req.Model
			sendFromPlugin(t, host, ctx, upstream.URL, pluginabi.MethodHostHTTPDo, req)
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
	sent := upstream.last(t)
	if got := gjson.GetBytes(sent, "max_tokens").Int(); got != 64 {
		t.Errorf("max_tokens = %d, want override 64 to win over the plugin's later value: %s", got, sent)
	}
	if gjson.GetBytes(sent, "secret").Exists() {
		t.Errorf("filtered field restored by the plugin reached upstream: %s", sent)
	}
	if got := gjson.GetBytes(sent, "temperature").Float(); got != 0.2 {
		t.Errorf("temperature = %v, want default 0.2: %s", got, sent)
	}
	if upstream.count() != 1 {
		t.Errorf("upstream requests = %d, want 1", upstream.count())
	}
}

func TestExecutorAdapterAppliesPayloadRulesToStreamingSends(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})
	upstream := newPayloadUpstream(t)

	adapter := newCurrentExecutorAdapterForTest(host, "payload-stream-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			sendFromPlugin(t, host, ctx, upstream.URL, pluginabi.MethodHostHTTPDoStream, req)
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
	sent := upstream.last(t)
	if got := gjson.GetBytes(sent, "max_tokens").Int(); got != 64 {
		t.Errorf("stream max_tokens = %d, want override 64: %s", got, sent)
	}
	if gjson.GetBytes(sent, "secret").Exists() {
		t.Errorf("filtered field reached upstream on the stream path: %s", sent)
	}
}

func TestExecutorAdapterPayloadRulesCanonicalizeSourceFormatAlias(t *testing.T) {
	host := New()
	// The rule is gated on the documented canonical source protocol "claude".
	host.runtimeConfig = payloadRulesConfig("openai", "claude", map[string]any{"max_tokens": 64})
	upstream := newPayloadUpstream(t)

	adapter := newCurrentExecutorAdapterForTest(host, "payload-alias-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			sendFromPlugin(t, host, ctx, upstream.URL, pluginabi.MethodHostHTTPDo, req)
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
	if got := gjson.GetBytes(upstream.last(t), "max_tokens").Int(); got != 64 {
		t.Errorf("max_tokens = %d, want 64: rule gated on from-protocol claude was skipped for alias source", got)
	}
}

func TestExecutorAdapterPayloadRulesUseAntigravityRequestRoot(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("antigravity", "", map[string]any{"generationConfig.maxOutputTokens": 64})
	upstream := newPayloadUpstream(t)

	adapter := newCurrentExecutorAdapterForTest(host, "payload-antigravity-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			callbackID, closeCallback := host.openCallbackContext(ctx)
			defer closeCallback()
			raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodPost, URL: upstream.URL, Body: req.Payload})
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, raw); errCall != nil {
				t.Fatalf("host call error = %v", errCall)
			}
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
	sent := upstream.last(t)
	if got := gjson.GetBytes(sent, "request.generationConfig.maxOutputTokens").Int(); got != 64 {
		t.Errorf("request.generationConfig.maxOutputTokens = %d, want 64: %s", got, sent)
	}
	if gjson.GetBytes(sent, "generationConfig").Exists() {
		t.Errorf("rule created a top-level generationConfig beside request: %s", sent)
	}
}

func TestExecutorAdapterWithoutPayloadRulesLeavesSentBodyUntouched(t *testing.T) {
	host := New()
	host.runtimeConfig = &config.Config{}
	upstream := newPayloadUpstream(t)

	body := `{"model":"claude-model","messages":[{"role":"user","content":"hello"}],"secret":"keep"}`
	adapter := newCurrentExecutorAdapterForTest(host, "payload-none-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			callbackID, closeCallback := host.openCallbackContext(ctx)
			defer closeCallback()
			raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodPost, URL: upstream.URL, Body: req.Payload})
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, raw); errCall != nil {
				t.Fatalf("host call error = %v", errCall)
			}
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	if _, err := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model: "claude-model", Format: sdktranslator.FormatOpenAI, Payload: []byte(body),
	}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(upstream.last(t)); got != body {
		t.Errorf("body changed without rules:\n got %s\nwant %s", got, body)
	}
}

func TestExecutorAdapterDoesNotApplyPayloadRulesToAuxiliaryJSON(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})
	upstream := newPayloadUpstream(t)

	adapter := newCurrentExecutorAdapterForTest(host, "payload-aux-plugin", &fakeExecutor{
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			callbackID, closeCallback := host.openCallbackContext(ctx)
			defer closeCallback()
			body := []byte(`{"access_token":"temporary","secret":"keep"}`)
			raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodPost, URL: upstream.URL, Body: body})
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, raw); errCall != nil {
				t.Fatalf("host call error = %v", errCall)
			}
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	if _, err := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model: "claude-model", Format: sdktranslator.FormatOpenAI, Payload: []byte(`{"model":"claude-model"}`),
	}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := string(upstream.last(t)), `{"access_token":"temporary","secret":"keep"}`; got != want {
		t.Errorf("auxiliary body = %s, want unchanged %s", got, want)
	}
}

func TestExecutorAdapterDoesNotRewriteSignedHostHTTPBody(t *testing.T) {
	host := New()
	host.runtimeConfig = payloadRulesConfig("openai", "", map[string]any{"max_tokens": 64})
	upstream := newPayloadUpstream(t)
	body := []byte(`{"model":"claude-model","max_tokens":8}`)

	adapter := newCurrentExecutorAdapterForTest(host, "payload-signed-plugin", &fakeExecutor{
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			callbackID, closeCallback := host.openCallbackContext(ctx)
			defer closeCallback()
			raw, _ := json.Marshal(rpcHostHTTPRequest{
				HostCallbackID: callbackID,
				Method:         http.MethodPost,
				URL:            upstream.URL,
				Headers: httpHeader{
					"Authorization":  {"AWS4-HMAC-SHA256 Credential=test, Signature=body-hash"},
					"Content-Digest": {"sha-256=:c2lnbmVkLWJvZHk=:"},
				},
				Body: body,
			})
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostHTTPDo, raw); errCall != nil {
				t.Fatalf("host call error = %v", errCall)
			}
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	if _, err := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model: "claude-model", Format: sdktranslator.FormatOpenAI, Payload: body,
	}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(upstream.last(t)); got != string(body) {
		t.Errorf("signed body = %s, want unchanged %s", got, body)
	}
}

func TestFinalizeHostHTTPRequestScopesPayloadRules(t *testing.T) {
	ctx := helps.WithPayloadFinalizer(context.Background(), func(body []byte) []byte {
		return append([]byte("rewritten:"), body...)
	})
	ctx = context.WithValue(ctx, executorPayloadModelKey{}, "model")
	for _, body := range [][]byte{nil, []byte(""), []byte("not json"), []byte(`["array"]`), []byte(`"string"`)} {
		if got := finalizeHostHTTPRequest(ctx, pluginapi.HTTPRequest{Method: http.MethodPost, Body: body}); string(got) != string(body) {
			t.Errorf("finalizeHostHTTPRequest(%q) = %q, want unchanged", body, got)
		}
	}
	if got := finalizeHostHTTPRequest(ctx, pluginapi.HTTPRequest{Method: http.MethodPost, Body: []byte(`{"model":"model","a":1}`)}); string(got) != `rewritten:{"model":"model","a":1}` {
		t.Errorf("finalizeHostHTTPRequest(model object) = %q, want finalizer applied", got)
	}
	for _, req := range []pluginapi.HTTPRequest{
		{Method: http.MethodGet, Body: []byte(`{"model":"model","a":1}`)},
		{Method: http.MethodPost, Body: []byte(`{"model":"another-model","a":1}`)},
		{Method: http.MethodPost, Body: []byte(`{"a":1}`)},
	} {
		if got := finalizeHostHTTPRequest(ctx, req); string(got) != string(req.Body) {
			t.Errorf("finalizeHostHTTPRequest(%s, %s) = %s, want unchanged", req.Method, req.Body, got)
		}
	}
}
