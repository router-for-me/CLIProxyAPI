package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPayloadBarrierCodexImageFilter(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-luna"} {
				t.Run(transport+"/"+model+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
					captured := make(chan []byte, 1)
					completed := []byte(`{"type":"response.completed","response":{"id":"resp_barrier","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
							captured <- body
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
						captured <- body
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write(append(append([]byte("data: "), completed...), '\n', '\n'))
					}))
					defer server.Close()
					cfg := &config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gpt-5.6-sol"}}, Params: []string{`tools.#(type=="image_generation")#`, "instructions", "prompt_cache_key"}}}}}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if transport == "websocket" {
						executor = NewCodexWebsocketsExecutor(cfg)
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "plan_type": "pro"}}
					req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hello","prompt_cache_key":"injected-cache"}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
					if stream {
						result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					body := <-captured
					image := gjson.GetBytes(body, `tools.#(type=="image_generation")`).Exists()
					if image != (model == "gpt-5.6-luna") {
						t.Fatalf("image tool presence = %v: %s", image, body)
					}
					if model == "gpt-5.6-sol" && (gjson.GetBytes(body, "instructions").Exists() || gjson.GetBytes(body, "prompt_cache_key").Exists()) {
						t.Fatalf("late injection undid filter: %s", body)
					}
				})
			}
		}
	}
}

func TestPayloadBarrierAntigravityRebuiltAttempts(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: map[string]any{"generationConfig.maxOutputTokens": 123, "toolConfig.functionCallingConfig.mode": "CUSTOM"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: []string{"sessionId", "contents.0"}}},
	}}
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview"}
	body := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"user","parts":[{"text":"second"}]}]}}`)
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", body, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}))
	executor := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-test"}}
	for attempt := 0; attempt < 2; attempt++ {
		request, errBuild := executor.buildRequest(ctx, auth, "token", req.Model, body, attempt == 1, "", "https://example.invalid", "injected-session")
		if errBuild != nil {
			t.Fatal(errBuild)
		}
		wire, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errClose := request.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if gjson.GetBytes(wire, "request.generationConfig.maxOutputTokens").Int() != 123 {
			t.Fatalf("built-in cleanup undid override: %s", wire)
		}
		if gjson.GetBytes(wire, "request.sessionId").Exists() {
			t.Fatalf("session reinjected: %s", wire)
		}
		if got := gjson.GetBytes(wire, "request.contents.#").Int(); got != 1 {
			t.Fatalf("filter applied more than once: %s", wire)
		}
	}
}

func TestPayloadBarrierAIStudio(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"generationConfig.maxOutputTokens": 900000, "generationConfig.thinkingConfig.thinkingLevel": "low"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"contents"}}},
	}}
	executor := NewAIStudioExecutor(cfg, "aistudio", nil)
	for _, stream := range []bool{false, true} {
		body, _, errTranslate := executor.translateRequest(context.Background(), cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"contents":[{"role":"model","parts":[{"text":"hi"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}, stream)
		if errTranslate != nil {
			t.Fatal(errTranslate)
		}
		if gjson.GetBytes(body, "contents").Exists() || gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int() != 900000 || gjson.GetBytes(body, "generationConfig.thinkingConfig.thinkingLevel").String() != "low" {
			t.Fatalf("late normalization undid rules: %s", body)
		}
	}
}

func TestPayloadBarrierXAIWebsocketRetry(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"store": false, "instructions": "configured"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input.0", "previous_response_id"}}},
	}}
	req := cliproxyexecutor.Request{Model: "grok-4.3"}
	body := []byte(`{"previous_response_id":"previous","input":[{"content":"first"},{"content":"second"}]}`)
	finalize := helps.NewPayloadFinalizer(cfg, "xai", req.Model, "codex", "", body, req, cliproxyexecutor.Options{})
	first := buildXAIWebsocketRequestBody(body, finalize)
	retry := buildXAIWebsocketRequestBody(body, finalize)
	if !bytes.Equal(first, retry) {
		t.Fatalf("retry changed configured body: %s != %s", first, retry)
	}
	if gjson.GetBytes(first, "store").Bool() || gjson.GetBytes(first, "instructions").String() != "configured" || gjson.GetBytes(first, "previous_response_id").Exists() || gjson.GetBytes(first, "input.#").Int() != 1 {
		t.Fatalf("invalid final payload: %s", first)
	}
	if gjson.GetBytes(first, "type").String() != "response.create" {
		t.Fatalf("missing frame type: %s", first)
	}
}

func TestPayloadBarrierClaudeDoesNotReplayFiltersOrReinjectIdentity(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"system", "metadata", "diagnostics", "context_management", "messages.0"}}},
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"max_tokens": 1}}},
	}}
	for _, stream := range []bool{false, true} {
		body := executeClaudeContextManagementRequest(t, cfg, []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}]}`), stream)
		for _, path := range []string{"system", "metadata", "diagnostics", "context_management"} {
			if gjson.GetBytes(body, path).Exists() {
				t.Fatalf("%s was reinjected: %s", path, body)
			}
		}
		if gjson.GetBytes(body, "messages.#").Int() != 2 || gjson.GetBytes(body, "max_tokens").Int() != 1 {
			t.Fatalf("built-in processing changed final rule semantics: %s", body)
		}
	}
}
