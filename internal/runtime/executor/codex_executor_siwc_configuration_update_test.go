package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// SIWC (Sign in with ChatGPT) subscriptions reject configuration_update input
// items with subscription_sharing_unsupported_capability, so the SIWC body
// rules must drop every such item and promote the last nonempty effort into
// the top-level reasoning.effort even though that invalidates the prompt cache.
func TestStripCodexSIWCUnsupportedFieldsConfigurationUpdates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		wantInput  string
		wantEffort string
	}{
		{
			name:       "latest update overrides baseline effort",
			body:       `{"reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hi"},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "high",
		},
		{
			name:       "effort is created when reasoning is missing",
			body:       `{"input":[{"type":"configuration_update","reasoning":{"effort":"xhigh"}},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "xhigh",
		},
		{
			name:       "update without reasoning effort keeps earlier effort",
			body:       `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","tools":[]},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "low",
		},
		{
			name:       "no update effort leaves top level unchanged",
			body:       `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","tools":[]},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "medium",
		},
		{
			name:       "invalid update efforts are ignored",
			body:       `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":42}},{"type":"configuration_update","reasoning":{"effort":null}},{"type":"configuration_update","reasoning":{"effort":"  "}},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "low",
		},
		{
			name:       "effort is trimmed and lowercased",
			body:       `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":" High "}},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "high",
		},
		{
			name:       "none passes through unchanged",
			body:       `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "none",
		},
		{
			name:       "auto passes through unchanged",
			body:       `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"auto"}},{"role":"user","content":"hi"}]}`,
			wantInput:  `[{"role":"user","content":"hi"}]`,
			wantEffort: "auto",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stripCodexSIWCUnsupportedFields([]byte(tc.body), false)
			if input := gjson.GetBytes(got, "input").Raw; input != tc.wantInput {
				t.Fatalf("input = %s, want %s; body=%s", input, tc.wantInput, got)
			}
			if effort := gjson.GetBytes(got, "reasoning.effort").String(); effort != tc.wantEffort {
				t.Fatalf("reasoning.effort = %q, want %q; body=%s", effort, tc.wantEffort, got)
			}
		})
	}

	t.Run("summary and other reasoning fields are preserved", func(t *testing.T) {
		body := `{"reasoning":{"effort":"medium","summary":"auto","other":7},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hi"}]}`
		got := stripCodexSIWCUnsupportedFields([]byte(body), false)
		if summary := gjson.GetBytes(got, "reasoning.summary").String(); summary != "auto" {
			t.Fatalf("reasoning.summary = %q, want auto; body=%s", summary, got)
		}
		if other := gjson.GetBytes(got, "reasoning.other").Int(); other != 7 {
			t.Fatalf("reasoning.other = %d, want 7; body=%s", other, got)
		}
	})

	t.Run("nonarray input remains untouched", func(t *testing.T) {
		body := `{"reasoning":{"effort":"medium"},"input":{"type":"configuration_update","reasoning":{"effort":"high"}}}`
		got := stripCodexSIWCUnsupportedFields([]byte(body), false)
		if string(got) != body {
			t.Fatalf("body changed: got %s, want %s", got, body)
		}
	})

	t.Run("payload without updates is byte identical", func(t *testing.T) {
		body := `{"reasoning":{"effort":"medium"},"input":[{"role":"user","content":"hi"}]}`
		got := stripCodexSIWCUnsupportedFields([]byte(body), false)
		if string(got) != body {
			t.Fatalf("body changed: got %s, want %s", got, body)
		}
	})
}

func codexSIWCAuthForUpdateTest(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "codex-siwc-update",
		Provider:   "codex",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-tok",
			"account_id":   "acct-1",
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-tok",
				"client_id":    "oaiapp_test",
			},
		},
	}
}

const codexSIWCUpdatePromptCacheKey = "siwc-update-session"

const codexSIWCUpdatePayload = `{"model":"gpt-6-astra","prompt_cache_key":"` + codexSIWCUpdatePromptCacheKey + `","reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`

func assertCodexSIWCUpdateStripped(t *testing.T, gotBody []byte, wantEffort string) {
	t.Helper()
	if len(gotBody) == 0 {
		t.Fatal("upstream body was not captured")
	}
	for _, item := range gjson.GetBytes(gotBody, "input").Array() {
		if item.Get("type").String() == "configuration_update" {
			t.Fatalf("SIWC upstream body still contains configuration_update: %s", gotBody)
		}
	}
	if effort := gjson.GetBytes(gotBody, "reasoning.effort").String(); effort != wantEffort {
		t.Fatalf("SIWC upstream reasoning.effort = %q, want %q; body=%s", effort, wantEffort, gotBody)
	}
}

func TestCodexExecutorUseSIWCStripsConfigurationUpdatesBeforePayloadFinalizer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        *config.Config
		wantEffort string
	}{
		{name: "latest update becomes top level effort", cfg: &config.Config{}, wantEffort: "high"},
		{
			name: "payload finalizer stays the last barrier",
			cfg: &config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{
						{
							Models: []config.PayloadModelRule{{Name: "gpt-6-astra", Protocol: "codex"}},
							Params: map[string]any{"reasoning.effort": "xhigh"},
						},
					},
				},
			},
			wantEffort: "xhigh",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_siwc_update","object":"response","status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
			}))
			defer server.Close()

			executor := NewCodexExecutor(tc.cfg)
			_, err := executor.Execute(context.Background(), codexSIWCAuthForUpdateTest(server.URL), cliproxyexecutor.Request{
				Model:   "gpt-6-astra",
				Payload: []byte(codexSIWCUpdatePayload),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			assertCodexSIWCUpdateStripped(t, gotBody, tc.wantEffort)
		})
	}
}

func TestCodexExecutorCompactUseSIWCStripsConfigurationUpdates(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_siwc_compact_update","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), codexSIWCAuthForUpdateTest(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	assertCodexSIWCUpdateStripped(t, gotBody, "high")
}

func TestCodexWebsocketsExecuteUseSIWCStripsConfigurationUpdates(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		captured <- append([]byte(nil), payload...)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp-ws-siwc-update","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	executor := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	_, err := executor.Execute(context.Background(), codexSIWCAuthForUpdateTest(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("websocket Execute error: %v", err)
	}

	select {
	case gotBody := <-captured:
		assertCodexSIWCUpdateStripped(t, gotBody, "high")
		if cacheKey := gjson.GetBytes(gotBody, "prompt_cache_key").String(); cacheKey != codexSIWCUpdatePromptCacheKey {
			t.Fatalf("prompt_cache_key = %q, want %q; body=%s", cacheKey, codexSIWCUpdatePromptCacheKey, gotBody)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexExecutorUseSIWCKeepsSuffixEffortOverConfigurationUpdate(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_siwc_suffix","object":"response","status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
	}))
	defer server.Close()

	// With an explicit model suffix the thinking layer resolves the operator's
	// override into the top-level effort. The SIWC strip must keep that
	// suffix intent instead of resurrecting the last configuration_update.
	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), codexSIWCAuthForUpdateTest(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-6-astra(xhigh)",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	assertCodexSIWCUpdateStripped(t, gotBody, "xhigh")
}

func TestCodexExecutorExecuteStreamUseSIWCStripsConfigurationUpdates(t *testing.T) {
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_siwc_stream","object":"response","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), codexSIWCAuthForUpdateTest(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Errorf("stream chunk error: %v", chunk.Err)
		}
	}
	assertCodexSIWCUpdateStripped(t, <-captured, "high")
}

func TestCodexWebsocketsPrepareStreamUseSIWCKeepsSuffixEffort(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	prepared, err := executor.prepareCodexWebsocketStream(context.Background(), codexSIWCAuthForUpdateTest("http://127.0.0.1:1"), cliproxyexecutor.Request{
		Model:   "gpt-6-astra(xhigh)",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("prepareCodexWebsocketStream error: %v", err)
	}
	assertCodexSIWCUpdateStripped(t, prepared.clientBody, "xhigh")
}

func TestCodexExecutorUseSIWCInvalidSuffixPromotesConfigurationUpdate(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_siwc_invalid_suffix","object":"response","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), codexSIWCAuthForUpdateTest(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-6-astra(invalid)",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	assertCodexSIWCUpdateStripped(t, gotBody, "high")
}

func TestCodexExecutorWithoutSIWCKeepsConfigurationUpdates(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_oauth_update","object":"response","status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), codexOAuthTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(codexSIWCUpdatePayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	updates := 0
	for _, item := range gjson.GetBytes(gotBody, "input").Array() {
		if item.Get("type").String() == "configuration_update" {
			updates++
		}
	}
	if updates != 2 {
		t.Fatalf("non-SIWC upstream configuration_update count = %d, want 2; body=%s", updates, gotBody)
	}
	if effort := gjson.GetBytes(gotBody, "reasoning.effort").String(); effort != "medium" {
		t.Fatalf("non-SIWC upstream reasoning.effort = %q, want pinned %q; body=%s", effort, "medium", gotBody)
	}
}
