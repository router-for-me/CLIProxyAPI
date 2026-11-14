package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type codexHostedImageCapturedRequest struct {
	body    []byte
	headers http.Header
}

func newCodexHostedImageTestServer(t *testing.T, transport string) (*httptest.Server, <-chan codexHostedImageCapturedRequest, *atomic.Int32) {
	t.Helper()
	captured := make(chan codexHostedImageCapturedRequest, 16)
	calls := &atomic.Int32{}
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_hosted_image","object":"response","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if transport == "websocket" {
			upgrader := websocket.Upgrader{}
			conn, errUpgrade := upgrader.Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Error(errUpgrade)
				return
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			_, body, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Error(errRead)
				return
			}
			captured <- codexHostedImageCapturedRequest{body: body, headers: r.Header.Clone()}
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				t.Error(errWrite)
			}
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
			return
		}
		captured <- codexHostedImageCapturedRequest{body: body, headers: r.Header.Clone()}
		w.Header().Set("Content-Type", "text/event-stream")
		if _, errWrite := w.Write(append(append([]byte("data: "), completed...), '\n', '\n')); errWrite != nil {
			t.Error(errWrite)
		}
	}))
	t.Cleanup(server.Close)
	return server, captured, calls
}

func assertCodexHostedImageRequestError(t *testing.T, err error) {
	t.Helper()
	var scoped cliproxyexecutor.RequestScopedError
	var status interface{ StatusCode() int }
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() || !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected request-scoped HTTP 400, got %v", err)
	}
	if !strings.Contains(err.Error(), "/v1/images/generations") {
		t.Fatalf("missing Images API guidance: %v", err)
	}
}

func TestCodexHostedImageRejectionDoesNotRotateCooldownOrHideModel(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			for _, model := range []string{"gpt-6-sol", "gpt-6.1-sol"} {
				t.Run(transport+"/"+model+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
					server, _, calls := newCodexHostedImageTestServer(t, transport)
					cfg := &config.Config{}
					m := cliproxyauth.NewManager(nil, nil, nil)
					m.SetConfig(cfg)
					m.SetRetryConfig(3, time.Second, 0)
					if transport == "websocket" {
						m.RegisterExecutor(NewCodexWebsocketsExecutor(cfg))
					} else {
						m.RegisterExecutor(NewCodexExecutor(cfg))
					}
					reg := registry.GetGlobalRegistry()
					authIDs := []string{t.Name() + "/a", t.Name() + "/b"}
					for _, id := range authIDs {
						auth := &cliproxyauth.Auth{
							ID: id, Provider: "codex", Status: cliproxyauth.StatusActive,
							Attributes: map[string]string{"base_url": server.URL, "plan_type": "pro"},
							Metadata:   map[string]any{"access_token": "test-token", "account_id": "test-account"},
						}
						if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
							t.Fatal(errRegister)
						}
						reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model, Type: "codex", Object: "model", OwnedBy: "openai"}})
						t.Cleanup(func() { reg.UnregisterClient(id) })
					}
					doc := ojson.NewDocString(`{"input":"draw a cat","tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`)
					_ = doc.Set("model", model)
					req := cliproxyexecutor.Request{Model: model, Payload: doc.Bytes()}
					selected := 0
					opts := cliproxyexecutor.Options{
						Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse,
						Metadata: map[string]any{cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) { selected++ }},
					}
					var errExecute error
					if stream {
						_, errExecute = m.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
					} else {
						_, errExecute = m.Execute(context.Background(), []string{"codex"}, req, opts)
					}
					assertCodexHostedImageRequestError(t, errExecute)
					if selected != 1 || calls.Load() != 0 {
						t.Fatalf("selected=%d upstream calls=%d, want one selection and no upstream calls", selected, calls.Load())
					}
					for _, id := range authIDs {
						auth, ok := m.GetByID(id)
						if !ok || auth.Unavailable || !auth.NextRetryAfter.IsZero() || len(auth.ModelStates) != 0 || len(cliproxyauth.CooldownSnapshotForAuth(auth, time.Now())) != 0 {
							t.Fatalf("request rejection changed credential availability: %#v", auth)
						}
						if reg.IsModelSuspendedForClient(id, model) {
							t.Fatalf("original model suspended for %s", id)
						}
					}
					visible := false
					for _, info := range reg.GetAvailableModels("codex") {
						visible = visible || info["id"] == model
					}
					if !visible {
						t.Fatal("original model disappeared from the Codex model list")
					}
				})
			}
		}
	}
}

func TestCodexHostedImageContinuationRejectsBeforeWritingUpstream(t *testing.T) {
	var connections, frames atomic.Int32
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		defer close(done)
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Error(errRead)
			return
		}
		frames.Add(1)
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"started","output":[]}}`,
			`{"type":"response.completed","response":{"id":"started","output":[]}}`,
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
				t.Error(errWrite)
				return
			}
		}
		if _, _, errRead := conn.ReadMessage(); errRead == nil {
			frames.Add(1)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := make(chan cliproxyexecutor.WebsocketInput, 1)
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	exec := NewCodexWebsocketsExecutor(cfg)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	m := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	m.SetConfig(cfg)
	m.SetRetryConfig(3, time.Second, 0)
	m.RegisterExecutor(exec)
	reg := registry.GetGlobalRegistry()
	authIDs := []string{t.Name() + "/a", t.Name() + "/b"}
	for _, id := range authIDs {
		auth := &cliproxyauth.Auth{ID: id, Provider: "codex", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"base_url": server.URL, "websockets": "true"}, Metadata: map[string]any{"access_token": "test-token", "account_id": "test-account"}}
		if _, errRegister := m.Register(ctx, auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-6-sol"}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	result, errStream := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-sol", Payload: []byte(`{"model":"gpt-6-sol","input":[]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}})
	if errStream != nil {
		t.Fatal(errStream)
	}
	completed := false
	var continuationErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			continuationErr = chunk.Err
			continue
		}
		if !completed && ojson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
			completed = true
			input <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","previous_response_id":"started","input":[],"tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`)}
		}
	}
	if !completed {
		t.Fatal("initial response never completed")
	}
	assertCodexHostedImageRequestError(t, continuationErr)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("rejected continuation did not release the upstream socket")
	}
	if connections.Load() != 1 || frames.Load() != 1 {
		t.Fatalf("continuation was forwarded or replayed: connections=%d frames=%d", connections.Load(), frames.Load())
	}
	for _, id := range authIDs {
		auth, _ := m.GetByID(id)
		if auth.Unavailable || len(cliproxyauth.CooldownSnapshotForAuth(auth, time.Now())) != 0 || reg.IsModelSuspendedForClient(id, "gpt-6-sol") {
			t.Fatalf("rejected continuation changed credential availability: %#v", auth)
		}
	}
}

func TestCodexHostedImageValidationUsesFinalPayload(t *testing.T) {
	imageBody := `{"input":[{"role":"user","content":"first"},{"role":"user","content":"second"}],"tools":[{"type":"image_generation","complexity":"medium"}],"tool_choice":{"type":"image_generation"}}`
	for _, tc := range []struct {
		name      string
		body      string
		model     string
		cfg       config.Config
		headers   http.Header
		attrs     map[string]string
		reject    bool
		wantModel string
		wantLite  string
		check     func(*testing.T, []byte)
	}{
		{name: "forced", reject: true},
		{name: "legacy_hosted_images", model: "gpt-5.4", check: func(t *testing.T, body []byte) {
			if ojson.GetBytes(body, "tools.0.type").String() != "image_generation" || ojson.GetBytes(body, "tools.0.complexity").Exists() {
				t.Fatalf("legacy hosted image normalization changed: %s", body)
			}
		}},
		{name: "legacy_additional_tools", model: "gpt-5.4", body: `{"input":[{"type":"additional_tools","tools":[{"type":"image_generation","complexity":"high"}]},{"role":"user","content":"draw a cat"}],"tool_choice":{"type":"image_generation"}}`, check: func(t *testing.T, body []byte) {
			if ojson.GetBytes(body, "input.0.tools.0.type").String() != "image_generation" || ojson.GetBytes(body, "input.0.tools.0.complexity").Exists() {
				t.Fatalf("legacy additional image tool normalization changed: %s", body)
			}
		}},
		{name: "image_base_override_does_not_replace_chat_model", cfg: config.Config{SDKConfig: config.SDKConfig{GPTImage2BaseModel: "gpt-5.5"}}, reject: true},
		{name: "filtered", cfg: config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"tools", "tool_choice", "input.0"}}}}}, check: func(t *testing.T, body []byte) {
			if ojson.GetBytes(body, "tools").Exists() || ojson.GetBytes(body, "tool_choice").Exists() || ojson.GetBytes(body, "input.#").Int() != 1 || ojson.GetBytes(body, "input.0.content").String() != "second" {
				t.Fatalf("filter was undone or applied more than once: %s", body)
			}
		}},
		{name: "filter_header_matches", headers: http.Header{"X-Drop-Image": {"true"}}, cfg: config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Headers: map[string]string{"X-Drop-Image": "true"}}}, Params: []string{"tools", "tool_choice"}}}}}},
		{name: "filter_header_misses", headers: http.Header{"X-Drop-Image": {"false"}}, cfg: config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Headers: map[string]string{"X-Drop-Image": "true"}}}, Params: []string{"tools", "tool_choice"}}}}}, reject: true},
		{name: "filter_protocol_misses", cfg: config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "claude"}}, Params: []string{"tools", "tool_choice"}}}}}, reject: true},
		{name: "override_auto", cfg: config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"tool_choice": "auto", "tools.0.complexity": "high"}}}}}, check: func(t *testing.T, body []byte) {
			if ojson.GetBytes(body, "tool_choice").String() != "auto" || ojson.GetBytes(body, "tools.0.complexity").String() != "high" {
				t.Fatalf("built-in normalization overwrote the configured payload: %s", body)
			}
		}},
		{name: "override_supported_model", cfg: config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": "gpt-5.5"}}}}}, wantModel: "gpt-5.5"},
		{name: "override_lite_model", model: "gpt-5.4", cfg: config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": "gpt-6-sol"}}}}}, reject: true},
		{name: "override_raw_injects_force", body: `{"input":"hello"}`, cfg: config.Config{Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"tools": `[{"type":"image_generation"}]`, "tool_choice": `{"type":"image_generation"}`}}}}}, reject: true},
		{name: "default_injects_force", body: `{"input":"hello"}`, cfg: config.Config{Payload: config.PayloadConfig{Default: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"tools": []any{map[string]any{"type": "image_generation"}}, "tool_choice": map[string]any{"type": "image_generation"}}}}}}, reject: true},
		{name: "lite_header", model: "gpt-5.4", headers: http.Header{http.CanonicalHeaderKey(codexResponsesLiteHeader): {"true"}}, reject: true},
		{name: "provider_enables_lite", model: "gpt-5.4", attrs: map[string]string{"header:" + codexResponsesLiteHeader: "true"}, reject: true},
		{name: "provider_disables_lite", model: "gpt-5.4", headers: http.Header{http.CanonicalHeaderKey(codexResponsesLiteHeader): {"true"}}, attrs: map[string]string{"header:" + codexResponsesLiteHeader: "false"}, wantLite: "false"},
	} {
		for _, transport := range []string{"http", "websocket"} {
			for _, stream := range []bool{false, true} {
				t.Run(tc.name+"/"+transport+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
					server, captured, calls := newCodexHostedImageTestServer(t, transport)
					cfg := tc.cfg
					var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(&cfg)
					if transport == "websocket" {
						exec = NewCodexWebsocketsExecutor(&cfg)
					}
					model := tc.model
					if model == "" {
						model = "gpt-6-sol"
					}
					body := tc.body
					if body == "" {
						body = imageBody
					}
					doc := ojson.NewDocString(body)
					_ = doc.Set("model", model)
					req := cliproxyexecutor.Request{Model: model, Payload: doc.Bytes()}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"base_url": server.URL, "plan_type": "pro"}, Metadata: map[string]any{"access_token": "test-token", "account_id": "test-account"}}
					for key, value := range tc.attrs {
						auth.Attributes[key] = value
					}
					finalized := 0
					opts := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: tc.headers, Metadata: map[string]any{cliproxyexecutor.FinalizedResponseFormatCallbackMetadataKey: func(string) { finalized++ }}}
					var errExecute error
					if stream {
						var result *cliproxyexecutor.StreamResult
						result, errExecute = exec.ExecuteStream(context.Background(), auth, req, opts)
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									errExecute = chunk.Err
								}
							}
						}
					} else {
						_, errExecute = exec.Execute(context.Background(), auth, req, opts)
					}
					if finalized != 1 {
						t.Fatalf("payload rules evaluated %d times, want once", finalized)
					}
					if tc.reject {
						assertCodexHostedImageRequestError(t, errExecute)
						if calls.Load() != 0 {
							t.Fatalf("rejected payload reached upstream %d times", calls.Load())
						}
						return
					}
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					got := <-captured
					wantModel := tc.wantModel
					if wantModel == "" {
						wantModel = model
					}
					if ojson.GetBytes(got.body, "model").String() != wantModel || got.headers.Get("X-Codex-Routing-Hint") != "model="+wantModel {
						t.Fatalf("model or routing hint changed: body=%s hint=%s", got.body, got.headers.Get("X-Codex-Routing-Hint"))
					}
					if tc.wantLite != "" && got.headers.Get(codexResponsesLiteHeader) != tc.wantLite {
						t.Fatalf("Responses-Lite header = %q, want %q", got.headers.Get(codexResponsesLiteHeader), tc.wantLite)
					}
					if tc.check != nil {
						tc.check(t, got.body)
					}
				})
			}
		}
	}
}
