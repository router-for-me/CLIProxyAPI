package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func codexIncludeCaptureUpstream(t *testing.T) (*httptest.Server, <-chan []byte) {
	t.Helper()
	requests := make(chan []byte, 4)
	completed := `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-6-luna","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, errUpgrade := upgrader.Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade websocket: %v", errUpgrade)
				return
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()
			_, body, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read websocket request: %v", errRead)
				return
			}
			requests <- body
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(completed)); errWrite != nil {
				t.Errorf("write websocket response: %v", errWrite)
			}
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read HTTP request: %v", errRead)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+completed+"\n\n")
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func codexSIWCTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "codex-siwc-7833-test",
		Provider:   "codex",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "oauth-outer-token",
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-access-token",
			},
		},
	}
}

func parseIncludeStrings(body []byte) []string {
	arr := ojson.GetBytes(body, "include").Array()
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, item.String())
	}
	return out
}

func TestCodexResponsesIncludePreservation_Issue7833(t *testing.T) {
	allRequestedPayload := `{
		"model": "gpt-6-luna",
		"stream": true,
		"store": false,
		"input": "Use hosted image search to find photos.",
		"include": [
			"file_search_call.results",
			"web_search_call.results",
			"web_search_call.action.sources",
			"message.input_image.image_url",
			"computer_call_output.output.image_url",
			"code_interpreter_call.outputs",
			"reasoning.encrypted_content",
			"message.output_text.logprobs",
			"output.encrypted_content",
			"unknown.custom.value",
			42,
			null
		],
		"tools": [{
			"type": "web_search",
			"search_content_types": ["image", "text"],
			"image_settings": {"max_results": 3, "caption": true},
			"external_web_access": true
		}]
	}`
	omittedIncludePayload := `{"model":"gpt-6-luna","input":"hello"}`

	chatGPTAllowed := []string{
		"reasoning.encrypted_content",
		"web_search_call.action.sources",
		"web_search_call.results",
		"output.encrypted_content",
	}
	openAIAllowed := []string{
		"reasoning.encrypted_content",
		"web_search_call.action.sources",
		"web_search_call.results",
		"file_search_call.results",
		"message.input_image.image_url",
		"computer_call_output.output.image_url",
		"code_interpreter_call.outputs",
		"message.output_text.logprobs",
		"output.encrypted_content",
	}

	modelRule := []config.PayloadModelRule{{Name: "gpt-6-luna", Protocol: "codex"}}

	cases := []struct {
		name        string
		authBuilder func(baseURL string) *cliproxyauth.Auth
		payload     string
		rules       config.PayloadConfig
		wantInclude []string
		wantAbsent  bool
	}{
		{
			name:        "chatgpt oauth keeps verified codex includes only",
			authBuilder: codexOAuthTestAuth,
			payload:     allRequestedPayload,
			wantInclude: chatGPTAllowed,
		},
		{
			name:        "codex api key preserves all official responses includables plus output replay",
			authBuilder: codexAPIKeyTestAuth,
			payload:     allRequestedPayload,
			wantInclude: openAIAllowed,
		},
		{
			name:        "codex siwc preserves all official responses includables plus output replay",
			authBuilder: codexSIWCTestAuth,
			payload:     allRequestedPayload,
			wantInclude: openAIAllowed,
		},
		{
			name:        "codex api key omitted include keeps default reasoning only",
			authBuilder: codexAPIKeyTestAuth,
			payload:     omittedIncludePayload,
			wantInclude: []string{"reasoning.encrypted_content"},
		},
		{
			name:        "payload override-raw stays final barrier on codex api key",
			authBuilder: codexAPIKeyTestAuth,
			payload:     allRequestedPayload,
			rules: config.PayloadConfig{
				OverrideRaw: []config.PayloadRule{{
					Models: modelRule,
					Params: map[string]any{"include": `["reasoning.encrypted_content","custom.override"]`},
				}},
			},
			wantInclude: []string{"reasoning.encrypted_content", "custom.override"},
		},
		{
			name:        "payload filter stays final barrier on codex api key",
			authBuilder: codexAPIKeyTestAuth,
			payload:     allRequestedPayload,
			rules: config.PayloadConfig{
				Filter: []config.PayloadFilterRule{{
					Models: modelRule,
					Params: []string{"include"},
				}},
			},
			wantAbsent: true,
		},
	}

	for _, tc := range cases {
		for _, useWebsocket := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/websocket=%t/stream=%t", tc.name, useWebsocket, stream), func(t *testing.T) {
					server, requests := codexIncludeCaptureUpstream(t)
					cfg := &config.Config{Payload: tc.rules}
					var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if useWebsocket {
						exec = NewCodexWebsocketsExecutor(cfg)
					}
					auth := tc.authBuilder(server.URL)
					req := cliproxyexecutor.Request{Model: "gpt-6-luna", Payload: []byte(tc.payload)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}

					var execErr error
					if stream {
						res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
						execErr = errStream
						if errStream == nil {
							for chunk := range res.Chunks {
								if chunk.Err != nil && execErr == nil {
									execErr = chunk.Err
								}
							}
						}
					} else {
						_, execErr = exec.Execute(context.Background(), auth, req, opts)
					}
					if execErr != nil {
						t.Fatalf("execution error: %v", execErr)
					}

					select {
					case body := <-requests:
						if tc.wantAbsent {
							if ojson.GetBytes(body, "include").Exists() {
								t.Fatalf("expected include to be filtered, got %s", ojson.GetBytes(body, "include").Raw)
							}
							return
						}
						got := parseIncludeStrings(body)
						if !slices.Equal(got, tc.wantInclude) {
							t.Fatalf("upstream include = %v, want %v; raw=%s", got, tc.wantInclude, ojson.GetBytes(body, "include").Raw)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("upstream received no request within 10s")
					}
				})
			}
		}
	}
}

func TestCodexResponsesIncludeIndividualValues_Issue7833(t *testing.T) {
	allIncludables := []string{
		"file_search_call.results",
		"web_search_call.results",
		"web_search_call.action.sources",
		"message.input_image.image_url",
		"computer_call_output.output.image_url",
		"code_interpreter_call.outputs",
		"reasoning.encrypted_content",
		"message.output_text.logprobs",
		"output.encrypted_content",
	}

	for _, value := range allIncludables {
		t.Run(value, func(t *testing.T) {
			server, requests := codexIncludeCaptureUpstream(t)
			exec := NewCodexExecutor(&config.Config{})
			auth := codexAPIKeyTestAuth(server.URL)
			payload := []byte(fmt.Sprintf(`{"model":"gpt-6-luna","input":"hello","include":[%q]}`, value))
			_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-6-luna",
				Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errExec != nil {
				t.Fatalf("Execute() error = %v", errExec)
			}
			body := <-requests
			got := parseIncludeStrings(body)
			want := []string{"reasoning.encrypted_content"}
			if value != "reasoning.encrypted_content" {
				want = append(want, value)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("upstream include for %q = %v, want %v", value, got, want)
			}
		})
	}
}

func TestShouldPreserveFullCodexOpenAIInclude_Issue7833(t *testing.T) {
	codexHeaders := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
	tests := []struct {
		name    string
		auth    *cliproxyauth.Auth
		baseURL string
		headers http.Header
		want    bool
	}{
		{
			name:    "chatgpt oauth default backend",
			auth:    codexOAuthTestAuth(""),
			baseURL: "https://chatgpt.com/backend-api/codex",
			want:    false,
		},
		{
			name:    "chatgpt oauth custom proxy",
			auth:    codexOAuthTestAuth("https://proxy.example.com"),
			baseURL: "https://proxy.example.com",
			want:    false,
		},
		{
			name:    "api key targeting chatgpt codex backend",
			auth:    codexAPIKeyTestAuth("https://chatgpt.com/backend-api/codex"),
			baseURL: "https://chatgpt.com/backend-api/codex",
			want:    false,
		},
		{
			name:    "api key targeting chatgpt codex backend with trailing dns dot",
			auth:    codexAPIKeyTestAuth("https://chatgpt.com./backend-api/codex"),
			baseURL: "https://chatgpt.com./backend-api/codex",
			want:    false,
		},
		{
			name:    "api key targeting openai api",
			auth:    codexAPIKeyTestAuth("https://api.openai.com/v1"),
			baseURL: "https://api.openai.com/v1",
			want:    true,
		},
		{
			name:    "siwc non-codex client targeting openai api",
			auth:    codexSIWCTestAuth(""),
			baseURL: defaultOpenAISIWCBaseURL,
			want:    true,
		},
		{
			name:    "siwc codex client stays on chatgpt codex backend",
			auth:    codexSIWCTestAuth("https://proxy.example.com"),
			baseURL: "https://proxy.example.com",
			headers: codexHeaders,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPreserveFullCodexOpenAIInclude(context.Background(), tt.auth, tt.baseURL, tt.headers); got != tt.want {
				t.Fatalf("shouldPreserveFullCodexOpenAIInclude() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexSharedFormatNonOpenAIIncludeIsolation_Issue7833(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":"hello","include":["reasoning.encrypted_content","web_search_call.action.sources","web_search_call.results","output.encrypted_content","file_search_call.results"]}`)

	t.Run("xai strips openai-only output.encrypted_content and web_search_call.results", func(t *testing.T) {
		exec := NewXAIExecutor(&config.Config{})
		prepared, errPrepare := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
			Model:   "grok-4.5",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
		if errPrepare != nil {
			t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
		}
		got := parseIncludeStrings(prepared.body)
		want := []string{"reasoning.encrypted_content", "web_search_call.action.sources"}
		if !slices.Equal(got, want) {
			t.Fatalf("xai include = %v, want %v", got, want)
		}
	})

	t.Run("xai payload override-raw stays final barrier", func(t *testing.T) {
		cfg := &config.Config{
			Payload: config.PayloadConfig{
				OverrideRaw: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "grok-4.5", Protocol: "codex"}},
					Params: map[string]any{"include": `["reasoning.encrypted_content","output.encrypted_content"]`},
				}},
			},
		}
		exec := NewXAIExecutor(cfg)
		prepared, errPrepare := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
			Model:   "grok-4.5",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
		if errPrepare != nil {
			t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
		}
		finalBody := prepared.finalizePayload(prepared.body)
		got := parseIncludeStrings(finalBody)
		want := []string{"reasoning.encrypted_content", "output.encrypted_content"}
		if !slices.Equal(got, want) {
			t.Fatalf("xai finalized include = %v, want %v", got, want)
		}
	})

	t.Run("meta strips openai-only output.encrypted_content and web_search_call.results", func(t *testing.T) {
		exec := NewMetaExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider:   "meta",
			Attributes: map[string]string{"base_url": "https://meta.example.invalid", "api_key": "test"},
		}
		prepared, errPrepare := exec.prepareResponsesRequest(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "muse-spark-1.3",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
		if errPrepare != nil {
			t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
		}
		got := parseIncludeStrings(prepared.body)
		want := []string{"reasoning.encrypted_content", "web_search_call.action.sources"}
		if !slices.Equal(got, want) {
			t.Fatalf("meta include = %v, want %v", got, want)
		}
	})

	t.Run("meta payload override-raw stays final barrier", func(t *testing.T) {
		cfg := &config.Config{
			Payload: config.PayloadConfig{
				OverrideRaw: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "muse-spark-1.3", Protocol: "meta"}},
					Params: map[string]any{"include": `["reasoning.encrypted_content","web_search_call.results"]`},
				}},
			},
		}
		exec := NewMetaExecutor(cfg)
		auth := &cliproxyauth.Auth{
			Provider:   "meta",
			Attributes: map[string]string{"base_url": "https://meta.example.invalid", "api_key": "test"},
		}
		prepared, errPrepare := exec.prepareResponsesRequest(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "muse-spark-1.3",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
		if errPrepare != nil {
			t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
		}
		got := parseIncludeStrings(prepared.body)
		want := []string{"reasoning.encrypted_content", "web_search_call.results"}
		if !slices.Equal(got, want) {
			t.Fatalf("meta finalized include = %v, want %v", got, want)
		}
	})
}
