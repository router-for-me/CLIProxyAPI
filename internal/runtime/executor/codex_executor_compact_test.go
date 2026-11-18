package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexExecutorCompactAddsDefaultInstructionsWithoutInjectingImageTool(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing instructions",
			payload: `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
		},
		{
			name:    "null instructions",
			payload: `{"model":"gpt-5.4","instructions":null,"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			}}

			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Alt:          "responses/compact",
				Stream:       false,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotPath != "/responses/compact" {
				t.Fatalf("path = %q, want %q", gotPath, "/responses/compact")
			}
			if instructions := ojson.GetBytes(gotBody, "instructions"); instructions.Type != ojson.String || instructions.String() != "" {
				t.Fatalf("instructions = %s, want empty string; body=%s", instructions.Raw, gotBody)
			}
			if ojson.GetBytes(gotBody, "tools").Exists() {
				t.Fatalf("compact request injected image_generation tool: %s", gotBody)
			}
			input := ojson.GetBytes(gotBody, "input").Array()
			if len(input) != 2 || input[1].Get("type").String() != "compaction_trigger" {
				t.Fatalf("compact input order changed: %s", gotBody)
			}
			if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
				t.Fatalf("payload = %s", string(resp.Payload))
			}
		})
	}
}

func TestCodexExecutorCompactUseSIWCAppliesSIWCRulesBeforePayloadFinalizer(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_siwc_compact","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5.4", Protocol: "openai-response"}},
					Params: map[string]any{"temperature": 0.25},
				},
			},
		},
	}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID:       "codex-siwc-compact",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-tok",
			"account_id":   "acct-1",
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-compact-tok",
				"client_id":    "oaiapp_compact",
			},
		},
	}

	payload := `{"model":"gpt-5.4","instructions":null,"temperature":0.9,"max_output_tokens":512,"prompt_cache_retention":"in_memory","prompt_cache_options":{"ttl":60},"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(payload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want /v1/responses/compact", gotPath)
	}
	if gotHeaders.Get("Authorization") != "Bearer siwc-compact-tok" {
		t.Fatalf("Authorization = %q, want Bearer siwc-compact-tok", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("Chatgpt-Account-Id") != "" || gotHeaders.Get("Originator") != "" {
		t.Fatalf("unexpected Codex headers on SIWC compact request: %v", gotHeaders)
	}
	if maxOutputTokens := ojson.GetBytes(gotBody, "max_output_tokens").Int(); maxOutputTokens != 512 {
		t.Fatalf("max_output_tokens = %d, want 512; body=%s", maxOutputTokens, gotBody)
	}
	for _, omitted := range []string{"instructions", "prompt_cache_retention", "prompt_cache_options", "stream", "store"} {
		if ojson.GetBytes(gotBody, omitted).Exists() {
			t.Fatalf("field %q should be omitted in SIWC compact body: %s", omitted, gotBody)
		}
	}
	// PayloadFinalizer runs last and can override temperature after built-in SIWC stripping.
	if gotTemp := ojson.GetBytes(gotBody, "temperature").Float(); gotTemp != 0.25 {
		t.Fatalf("PayloadFinalizer temperature override = %v, want 0.25; body=%s", gotTemp, gotBody)
	}
}

func TestCodexExecutorReplayedWebSearchCallHistoryEnsuresWebSearchTool_Issue6770(t *testing.T) {
	hasWebSearchCallInInput := func(body []byte) bool {
		for _, item := range ojson.GetBytes(body, "input").Array() {
			if item.Get("type").String() == "web_search_call" {
				return true
			}
		}
		return false
	}
	countWebSearchTools := func(body []byte) int {
		count := 0
		for _, tool := range ojson.GetBytes(body, "tools").Array() {
			if tool.Get("type").String() == "web_search" {
				count++
			}
		}
		return count
	}

	compactionPayloadWithEmptyTools := `{"model":"gpt-6.1-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search docs"}]},{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"openai responses web search"}},{"id":"ws_2","type":"web_search_call","status":"completed","action":{"type":"open_page","url":"https://platform.openai.com/docs"}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Found docs."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize conversation"}]}],"tools":[],"tool_choice":"auto"}`

	t.Run("ExecuteStream with empty tools injects web_search and avoids 502 response protection error", func(t *testing.T) {
		var gotBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = body
			w.Header().Set("Content-Type", "text/event-stream")
			if hasWebSearchCallInInput(body) && countWebSearchTools(body) == 0 {
				_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"message\":\"response protection is unavailable\",\"type\":\"internal_error\",\"param\":null,\"code\":null}}\n\n"))
				return
			}
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"summary\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"))
		}))
		defer server.Close()

		executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: true}})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url":  server.URL,
				"api_key":   "test",
				"plan_type": "plus",
			},
		}

		streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(compactionPayloadWithEmptyTools),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
		if got := countWebSearchTools(gotBody); got != 1 {
			t.Fatalf("web_search tool count = %d, want 1; body=%s", got, gotBody)
		}
	})

	t.Run("Execute non-stream with image generation disabled still injects web_search", func(t *testing.T) {
		var gotBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = body
			w.Header().Set("Content-Type", "text/event-stream")
			if hasWebSearchCallInInput(body) && countWebSearchTools(body) == 0 {
				_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"message\":\"response protection is unavailable\",\"type\":\"internal_error\",\"param\":null,\"code\":null}}\n\n"))
				return
			}
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"))
		}))
		defer server.Close()

		executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			},
		}

		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(compactionPayloadWithEmptyTools),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if got := countWebSearchTools(gotBody); got != 1 {
			t.Fatalf("web_search tool count = %d, want 1; body=%s", got, gotBody)
		}
		if got := len(ojson.GetBytes(gotBody, "tools").Array()); got != 1 {
			t.Fatalf("tools len = %d, want 1; body=%s", got, gotBody)
		}
	})

	t.Run("Execute compact endpoint injects web_search when input replays web_search_call", func(t *testing.T) {
		var gotBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = body
			if hasWebSearchCallInInput(body) && countWebSearchTools(body) == 0 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":{"message":"response protection is unavailable","type":"internal_error","param":null,"code":null}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_compact_1","object":"response.compaction","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`))
		}))
		defer server.Close()

		executor := NewCodexExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			},
		}

		compactPayload := `{"model":"gpt-6.1-sol","input":[{"type":"message","role":"user","content":"history"},{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}},{"type":"compaction_trigger"}]}`
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(compactPayload),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Alt:          "responses/compact",
		})
		if err != nil {
			t.Fatalf("Execute compact error = %v", err)
		}
		if got := countWebSearchTools(gotBody); got != 1 {
			t.Fatalf("compact web_search tool count = %d, want 1; body=%s", got, gotBody)
		}
	})

	t.Run("prepareCodexWebsocketStream injects web_search when input replays web_search_call", func(t *testing.T) {
		wsExecutor := NewCodexWebsocketsExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url":  "http://127.0.0.1:1",
				"api_key":   "test",
				"plan_type": "plus",
			},
		}
		prepared, err := wsExecutor.prepareCodexWebsocketStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(compactionPayloadWithEmptyTools),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("prepareCodexWebsocketStream() error = %v", err)
		}
		if got := countWebSearchTools(prepared.clientBody); got != 1 {
			t.Fatalf("websocket clientBody web_search tool count = %d, want 1; body=%s", got, prepared.clientBody)
		}
	})

	t.Run("existing web_search tool declaration is preserved without duplication", func(t *testing.T) {
		payloadWithWebSearch := `{"model":"gpt-6.1-sol","input":[{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}],"tools":[{"type":"web_search","external_web_access":true}]}`
		wsExecutor := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": "http://127.0.0.1:1",
				"api_key":  "test",
			},
		}
		prepared, err := wsExecutor.prepareCodexWebsocketStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(payloadWithWebSearch),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("prepareCodexWebsocketStream() error = %v", err)
		}
		if got := countWebSearchTools(prepared.clientBody); got != 1 {
			t.Fatalf("web_search tool count = %d, want 1; body=%s", got, prepared.clientBody)
		}
		if !ojson.GetBytes(prepared.clientBody, "tools.0.external_web_access").Bool() {
			t.Fatalf("existing web_search options not preserved: %s", prepared.clientBody)
		}
	})

	t.Run("Responses Lite request strips replayed web_search_call items without injecting top-level tools", func(t *testing.T) {
		litePayload := `{"model":"gpt-6.1-sol","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","description":"","tools":[{"type":"custom","name":"exec"}]}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]},{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}},{"id":"rs_1","type":"reasoning","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"compaction_trigger"}],"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`
		wsExecutor := NewCodexWebsocketsExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": "http://127.0.0.1:1",
				"api_key":  "test",
			},
		}
		headers := make(http.Header)
		headers.Set(codexResponsesLiteHeader, "true")

		prepared, err := wsExecutor.prepareCodexWebsocketStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(litePayload),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Headers:      headers,
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("prepareCodexWebsocketStream() error = %v", err)
		}
		if ojson.GetBytes(prepared.clientBody, "tools").Exists() {
			t.Fatalf("Responses Lite request must not inject top-level tools: %s", prepared.clientBody)
		}
		if hasWebSearchCallInInput(prepared.clientBody) {
			t.Fatalf("Responses Lite request must strip replayed web_search_call items: %s", prepared.clientBody)
		}
		inputItems := ojson.GetBytes(prepared.clientBody, "input").Array()
		if len(inputItems) != 5 {
			t.Fatalf("input len = %d, want 5; body=%s", len(inputItems), prepared.clientBody)
		}
	})

	t.Run("PayloadFinalizer runs after ensureWebSearchToolForHistory", func(t *testing.T) {
		var gotBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = body
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"))
		}))
		defer server.Close()

		cfg := &config.Config{
			SDKConfig: config.SDKConfig{
				DisableImageGeneration: config.DisableImageGenerationAll,
			},
			Payload: config.PayloadConfig{
				Filter: []config.PayloadFilterRule{
					{
						Models: []config.PayloadModelRule{{Name: "gpt-6.1-sol", Protocol: "codex"}},
						Params: []string{"tools"},
					},
				},
			},
		}
		executor := NewCodexExecutor(cfg)
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			},
		}

		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(compactionPayloadWithEmptyTools),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if ojson.GetBytes(gotBody, "tools").Exists() {
			t.Fatalf("expected user payload filter rule to remove tools after built-in injection, got body=%s", gotBody)
		}
	})
}

func TestCodexExecutorExpandsRecognizedCPACompactionCapsules_Issue7118(t *testing.T) {
	summaryText := "Claude compacted summary before switching to GPT."
	capsule, errSeal := helps.SealAntigravityCompaction(summaryText, "claude-sonnet-4-6")
	if errSeal != nil {
		t.Fatalf("SealAntigravityCompaction() error = %v", errSeal)
	}

	mixedPayload := `{"model":"gpt-6.1-sol","input":[` +
		`{"id":"cmp_ag_compact_1","type":"compaction","encrypted_content":"` + capsule + `"},` +
		`{"id":"cmp_native_1","type":"compaction","encrypted_content":"gAAAAABnative-openai-compaction"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue on GPT"}]}` +
		`]}`

	assertExpandedAndNativePreserved := func(t *testing.T, body []byte) {
		t.Helper()
		if bytes.Contains(body, []byte("cpa-ag-compact-v1:")) || bytes.Contains(body, []byte("cmp_ag_compact_1")) {
			t.Fatalf("CPA compaction capsule leaked to Codex upstream: %s", body)
		}
		input := ojson.GetBytes(body, "input").Array()
		if len(input) != 3 {
			t.Fatalf("input len = %d, want 3; body=%s", len(input), body)
		}
		if input[0].Get("type").String() != "message" || input[0].Get("role").String() != "developer" {
			t.Fatalf("input[0] = %s, want developer message", input[0].Raw)
		}
		if !strings.Contains(input[0].Get("content.0.text").String(), summaryText) {
			t.Fatalf("input[0] text = %q, want summary %q", input[0].Get("content.0.text").String(), summaryText)
		}
		if input[1].Get("type").String() != "compaction" || input[1].Get("encrypted_content").String() != "gAAAAABnative-openai-compaction" {
			t.Fatalf("native OpenAI compaction item not preserved at input[1]: %s", input[1].Raw)
		}
	}

	t.Run("ExecuteStream expands CPA capsule and preserves native OpenAI compaction", func(t *testing.T) {
		var gotBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = body
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n"))
		}))
		defer server.Close()

		executor := NewCodexExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			},
		}

		streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(mixedPayload),
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: []byte(mixedPayload),
			Stream:          true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
		assertExpandedAndNativePreserved(t, gotBody)
	})

	t.Run("Execute and executeCompact expand CPA capsule and preserve native OpenAI compaction", func(t *testing.T) {
		var gotResponsesBody []byte
		var gotCompactBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path == "/responses/compact" {
				gotCompactBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_compact_1","object":"response.compaction","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`))
				return
			}
			gotResponsesBody = body
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n"))
		}))
		defer server.Close()

		executor := NewCodexExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			},
		}

		_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(mixedPayload),
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: []byte(mixedPayload),
		})
		if errExec != nil {
			t.Fatalf("Execute() error = %v", errExec)
		}
		assertExpandedAndNativePreserved(t, gotResponsesBody)

		_, errCompact := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(mixedPayload),
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: []byte(mixedPayload),
			Alt:             "responses/compact",
		})
		if errCompact != nil {
			t.Fatalf("Execute(responses/compact) error = %v", errCompact)
		}
		assertExpandedAndNativePreserved(t, gotCompactBody)
	})

	t.Run("prepareCodexWebsocketStream and CountTokens expand CPA capsule", func(t *testing.T) {
		wsExecutor := NewCodexWebsocketsExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": "http://127.0.0.1:1",
				"api_key":  "test",
			},
		}
		prepared, errPrep := wsExecutor.prepareCodexWebsocketStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(mixedPayload),
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: []byte(mixedPayload),
			Stream:          true,
		})
		if errPrep != nil {
			t.Fatalf("prepareCodexWebsocketStream() error = %v", errPrep)
		}
		assertExpandedAndNativePreserved(t, prepared.clientBody)

		executor := NewCodexExecutor(&config.Config{})
		withoutCapsulePayload := `{"model":"gpt-6.1-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue on GPT"}]}]}`
		baseCountResp, errBaseCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(withoutCapsulePayload),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatOpenAIResponse,
		})
		if errBaseCount != nil {
			t.Fatalf("CountTokens(base) error = %v", errBaseCount)
		}
		expandedCountResp, errExpandedCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(mixedPayload),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatOpenAIResponse,
		})
		if errExpandedCount != nil {
			t.Fatalf("CountTokens(expanded) error = %v", errExpandedCount)
		}
		if ojson.GetBytes(expandedCountResp.Payload, "response.usage.input_tokens").Int() <= ojson.GetBytes(baseCountResp.Payload, "response.usage.input_tokens").Int() {
			t.Fatalf("expected expanded capsule token count (%s) > base token count (%s)", expandedCountResp.Payload, baseCountResp.Payload)
		}
	})

	t.Run("damaged CPA capsule returns 400 without forwarding to upstream", func(t *testing.T) {
		upstreamHits := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamHits++
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		executor := NewCodexExecutor(&config.Config{})
		wsExecutor := NewCodexWebsocketsExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			},
		}

		assertBadRequest := func(t *testing.T, name string, err error) {
			t.Helper()
			if err == nil {
				t.Fatalf("%s with damaged capsule succeeded, want 400 error", name)
			}
			var se statusErr
			if statusCode := err.(interface{ StatusCode() int }).StatusCode(); statusCode != http.StatusBadRequest {
				t.Fatalf("%s status = %d (%v), want %d", name, statusCode, se, http.StatusBadRequest)
			}
		}

		corruptedPayloads := []string{
			`{"model":"gpt-6.1-sol","input":[{"id":"cmp_ag_compact_bad","type":"compaction","encrypted_content":"cpa-ag-compact-v1:corrupted"},{"type":"message","role":"user","content":"hi"}]}`,
			`{"model":"gpt-6.1-sol","input":[{"id":"cmp_ag_compact_bad","type":"compaction","encrypted_content":"truncated"},{"type":"message","role":"user","content":"hi"}]}`,
		}

		for _, badPayload := range corruptedPayloads {
			req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(badPayload)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: []byte(badPayload)}

			_, errExec := executor.Execute(context.Background(), auth, req, opts)
			assertBadRequest(t, "Execute()", errExec)

			_, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
			assertBadRequest(t, "ExecuteStream()", errStream)

			compactOpts := opts
			compactOpts.Alt = "responses/compact"
			_, errCompact := executor.Execute(context.Background(), auth, req, compactOpts)
			assertBadRequest(t, "Execute(responses/compact)", errCompact)

			_, errTokens := executor.CountTokens(context.Background(), auth, req, opts)
			assertBadRequest(t, "CountTokens()", errTokens)

			_, errWSExec := wsExecutor.Execute(context.Background(), auth, req, opts)
			assertBadRequest(t, "wsExecutor.Execute()", errWSExec)

			_, errWSStream := wsExecutor.ExecuteStream(context.Background(), auth, req, opts)
			assertBadRequest(t, "wsExecutor.ExecuteStream()", errWSStream)
		}
		if upstreamHits != 0 {
			t.Fatalf("upstreamHits = %d, want 0 when CPA capsule is damaged", upstreamHits)
		}
	})

	t.Run("websocket wire frames and duplex follow-up frames expand CPA capsule before PayloadFinalizer", func(t *testing.T) {
		receivedFrames := make(chan []byte, 4)
		serverDone := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(serverDone)
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade websocket: %v", errUpgrade)
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

			// 1. Initial response.create
			_, first, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read initial create: %v", errRead)
				return
			}
			receivedFrames <- first
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))

			// 2. Duplex response.steer
			_, second, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read steer: %v", errRead)
				return
			}
			receivedFrames <- second
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`))

			// 3. Duplex follow-up response.create (Wait for damaged steer to abort before reaching upstream)
			_, third, errRead := conn.ReadMessage()
			if errRead == nil {
				receivedFrames <- third
			}
		}))
		defer server.Close()

		cfg := &config.Config{
			Codex: config.CodexConfig{ResponseSteering: true},
			Payload: config.PayloadConfig{
				Override: []config.PayloadRule{
					{
						Models: []config.PayloadModelRule{{Name: "gpt-6.1-sol", Protocol: "codex"}},
						Params: map[string]any{"input.0.content.0.text": "User rule override after CPA expansion"},
					},
				},
			},
		}
		wsExec := NewCodexWebsocketsExecutor(cfg)
		wsExec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		auth := &cliproxyauth.Auth{
			ID:       "ws-compact-auth",
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":    "test-key",
				"base_url":   server.URL,
				"websockets": "true",
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		inputCh := make(chan cliproxyexecutor.WebsocketInput, 4)
		ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), inputCh)

		streamRes, errStream := wsExec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model:   "gpt-6.1-sol",
			Payload: []byte(mixedPayload),
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			OriginalRequest: []byte(mixedPayload),
			Stream:          true,
			Metadata:        map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "duplex-compact-session"},
		})
		if errStream != nil {
			t.Fatalf("wsExec.ExecuteStream() error = %v", errStream)
		}

		steerPayload := `{"type":"response.steer","previous_response_id":"r1","input":[` +
			`{"id":"cmp_ag_compact_2","type":"compaction","encrypted_content":"` + capsule + `"},` +
			`{"id":"cmp_native_2","type":"compaction","encrypted_content":"gAAAAABnative-openai-compaction"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"steer on GPT"}]}` +
			`]}`
		badSteerPayload := `{"type":"response.steer","previous_response_id":"r1","input":[` +
			`{"id":"cmp_ag_compact_bad","type":"compaction","encrypted_content":"cpa-ag-compact-v1:corrupted"}` +
			`]}`

		sawStreamErr := false
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				sawStreamErr = true
				break
			}
			evType := ojson.GetBytes(chunk.Payload, "type").String()
			switch evType {
			case "response.created":
				inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(steerPayload)}
			case "response.completed":
				inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(badSteerPayload)}
			}
		}
		if !sawStreamErr {
			t.Fatal("expected stream error when duplex response.steer carries damaged CPA capsule")
		}
		cancel()
		<-serverDone
		close(receivedFrames)

		var frames [][]byte
		for f := range receivedFrames {
			frames = append(frames, f)
		}
		if len(frames) != 2 {
			t.Fatalf("upstream received %d websocket frames, want 2 (initial create + valid steer; damaged steer must be rejected locally)", len(frames))
		}
		for idx, frame := range frames {
			if bytes.Contains(frame, []byte("cpa-ag-compact-v1:")) || bytes.Contains(frame, []byte("cmp_ag_compact_")) {
				t.Fatalf("frame[%d] leaked CPA compaction capsule: %s", idx, frame)
			}
			input := ojson.GetBytes(frame, "input").Array()
			if len(input) != 3 {
				t.Fatalf("frame[%d] input len = %d, want 3; frame=%s", idx, len(input), frame)
			}
			if input[0].Get("type").String() != "message" || input[0].Get("role").String() != "developer" {
				t.Fatalf("frame[%d] input[0] = %s, want developer message", idx, input[0].Raw)
			}
			// Verify PayloadFinalizer ran after capsule expansion and overrode input.0.content.0.text.
			if gotText := input[0].Get("content.0.text").String(); gotText != "User rule override after CPA expansion" {
				t.Fatalf("frame[%d] input[0].content.0.text = %q, want %q (PayloadFinalizer must run after capsule expansion)", idx, gotText, "User rule override after CPA expansion")
			}
			if input[1].Get("type").String() != "compaction" || input[1].Get("encrypted_content").String() != "gAAAAABnative-openai-compaction" {
				t.Fatalf("frame[%d] native compaction item not preserved at input[1]: %s", idx, input[1].Raw)
			}
		}
	})
}

func TestCodexExecutor_IsCompatResponsesCompactionTriggerAndReplay_Issue7406(t *testing.T) {
	const marker = "COBALT-7406"
	var seenPaths []string
	var seenBodies [][]byte
	var returnIncomplete bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		seenPaths = append(seenPaths, r.URL.Path)
		seenBodies = append(seenBodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		if returnIncomplete {
			_, _ = io.WriteString(w,
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_inc\",\"type\":\"message\",\"status\":\"incomplete\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Partial truncated summary.\"}]}}\n\n"+
					"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_inc\",\"object\":\"response\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"id\":\"msg_inc\",\"type\":\"message\",\"status\":\"incomplete\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Partial truncated summary.\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":6,\"total_tokens\":18}}}\n\n",
			)
			return
		}

		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_sum\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"Summarizing session context.\"}]}}\n\n"+
				"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"msg_sum\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Summary retaining "+marker+".\"}]}}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_sum\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":14,\"output_tokens\":8,\"total_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":5},\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n",
		)
	}))
	defer server.Close()

	cfg := &config.Config{
		CodexKey: []config.CodexKey{
			{
				APIKey:  "test-compat-key",
				BaseURL: server.URL + "/v1",
				Models: []config.CodexModel{
					{Name: "deepseek-v4.1-flash", IsCompat: true},
					{Name: "gpt-5.4", IsCompat: false},
				},
			},
		},
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "deepseek-v4.1-flash", Protocol: "codex"}},
					Params: map[string]any{
						"max_output_tokens": 2048,
					},
				},
			},
		},
	}

	executor := NewCodexExecutor(cfg)
	wsExecutor := NewCodexWebsocketsExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
			"api_key":  "test-compat-key",
		},
	}

	triggerPayload := []byte(`{
		"model": "deepseek-v4.1-flash",
		"stream": true,
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "exec_command",
						"description": "Execute a shell command",
						"parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}
					}
				]
			},
			{
				"id": "ws_hist_1",
				"type": "web_search_call",
				"status": "completed",
				"action": {"type": "search", "query": "deepseek responses"}
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Remember project marker ` + marker + `."}]
			},
			{"type": "compaction_trigger"}
		]
	}`)

	assertSummaryRequest := func(t *testing.T, path string, body []byte, label string) {
		t.Helper()
		if path != "/v1/responses" {
			t.Fatalf("%s upstream path = %q, want /v1/responses", label, path)
		}
		if bytes.Contains(body, []byte("compaction_trigger")) {
			t.Fatalf("%s summary request leaked compaction_trigger: %s", label, body)
		}
		if ojson.GetBytes(body, "tools").Exists() {
			t.Fatalf("%s summary request unexpectedly carried tools (including image_generation or web_search): %s", label, body)
		}
		if gotMax := ojson.GetBytes(body, "max_output_tokens").Int(); gotMax != 2048 {
			t.Fatalf("%s max_output_tokens = %d, want 2048 from payload override; body=%s", label, gotMax, body)
		}
	}

	// 1. Streaming ExecuteStream(compaction_trigger) on is-compat codex endpoint.
	streamResult, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: triggerPayload,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream(compaction_trigger) error = %v", errStream)
	}
	var streamBuf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamBuf.Write(chunk.Payload)
	}
	completed := claudeCompactionCompletedResponse(t, streamBuf.String())
	outputItems := completed.Get("response.output").Array()
	if len(outputItems) != 1 || outputItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in stream response, got: %s", completed.Raw)
	}
	capsule := outputItems[0].Get("encrypted_content").String()
	if !strings.HasPrefix(capsule, "cpa-ag-compact-v1:") {
		t.Fatalf("expected CPA compaction capsule prefix, got %q", capsule)
	}
	if gotCached := completed.Get("response.usage.input_tokens_details.cached_tokens").Int(); gotCached != 5 {
		t.Fatalf("cached_tokens = %d, want 5", gotCached)
	}
	if gotReasoning := completed.Get("response.usage.output_tokens_details.reasoning_tokens").Int(); gotReasoning != 3 {
		t.Fatalf("reasoning_tokens = %d, want 3", gotReasoning)
	}
	assertSummaryRequest(t, seenPaths[0], seenBodies[0], "ExecuteStream")

	// 2. Non-streaming Execute(compaction_trigger) on is-compat codex endpoint.
	nonStreamResp, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errExec != nil {
		t.Fatalf("Execute(compaction_trigger) error = %v", errExec)
	}
	if gotObj := ojson.GetBytes(nonStreamResp.Payload, "object").String(); gotObj != "response.compaction" {
		t.Fatalf("Execute object = %q, want response.compaction; payload=%s", gotObj, nonStreamResp.Payload)
	}
	nonStreamItems := ojson.GetBytes(nonStreamResp.Payload, "output").Array()
	if len(nonStreamItems) != 1 || nonStreamItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in non-stream response, got: %s", nonStreamResp.Payload)
	}
	assertSummaryRequest(t, seenPaths[1], seenBodies[1], "Execute")

	// 3. WebSocket executor delegation for compaction_trigger on is-compat codex endpoint.
	wsResp, errWSExec := wsExecutor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errWSExec != nil {
		t.Fatalf("wsExecutor.Execute(compaction_trigger) error = %v", errWSExec)
	}
	if gotObj := ojson.GetBytes(wsResp.Payload, "object").String(); gotObj != "response.compaction" {
		t.Fatalf("wsExecutor.Execute object = %q, want response.compaction", gotObj)
	}
	wsStreamRes, errWSStream := wsExecutor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: triggerPayload,
	})
	if errWSStream != nil {
		t.Fatalf("wsExecutor.ExecuteStream(compaction_trigger) error = %v", errWSStream)
	}
	var wsStreamBuf bytes.Buffer
	for chunk := range wsStreamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("ws stream chunk error = %v", chunk.Err)
		}
		wsStreamBuf.Write(chunk.Payload)
	}
	wsCompleted := claudeCompactionCompletedResponse(t, wsStreamBuf.String())
	if wsItems := wsCompleted.Get("response.output").Array(); len(wsItems) != 1 || wsItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in ws stream response, got: %s", wsCompleted.Raw)
	}

	// 4. Subsequent replay on is-compat expands CPA capsule and drops foreign compaction items.
	replayPayload := []byte(fmt.Sprintf(`{
		"model": "deepseek-v4.1-flash",
		"stream": true,
		"input": [
			{"id": "cmp_foreign", "type": "compaction", "encrypted_content": "gAAAAABforeign-openai-compaction"},
			{"id": %q, "type": "compaction", "encrypted_content": %q},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "What is the project marker?"}]}
		]
	}`, outputItems[0].Get("id").String(), capsule))

	contStream, errCont := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: replayPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: replayPayload,
	})
	if errCont != nil {
		t.Fatalf("ExecuteStream(replay) error = %v", errCont)
	}
	for chunk := range contStream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("replay stream chunk error = %v", chunk.Err)
		}
	}
	replaySeen := seenBodies[len(seenBodies)-1]
	if bytes.Contains(replaySeen, []byte("cpa-ag-compact-v1:")) || bytes.Contains(replaySeen, []byte("gAAAAABforeign-openai-compaction")) {
		t.Fatalf("expected both CPA capsule and foreign compaction item to be removed from is-compat upstream payload: %s", replaySeen)
	}
	if !bytes.Contains(replaySeen, []byte(marker)) {
		t.Fatalf("expected expanded summary marker %q in replay upstream payload: %s", marker, replaySeen)
	}

	// CountTokens on is-compat also drops foreign compaction items and expands CPA capsule cleanly.
	if _, errTokens := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: replayPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: replayPayload,
	}); errTokens != nil {
		t.Fatalf("CountTokens(replay) error = %v", errTokens)
	}

	// 5. Chained second compaction_trigger expands prior CPA capsule before summarizing.
	secondTriggerPayload := []byte(fmt.Sprintf(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"id": %q, "type": "compaction", "encrypted_content": %q},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "And remember SILVER-99."}]},
			{"type": "compaction_trigger"}
		]
	}`, outputItems[0].Get("id").String(), capsule))

	secondResp, errSecond := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: secondTriggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: secondTriggerPayload,
	})
	if errSecond != nil {
		t.Fatalf("Execute(second compaction_trigger) error = %v", errSecond)
	}
	if gotObj := ojson.GetBytes(secondResp.Payload, "object").String(); gotObj != "response.compaction" {
		t.Fatalf("second compaction object = %q, want response.compaction", gotObj)
	}
	secondSeen := seenBodies[len(seenBodies)-1]
	if bytes.Contains(secondSeen, []byte("compaction_trigger")) || bytes.Contains(secondSeen, []byte("cpa-ag-compact-v1:")) {
		t.Fatalf("second summary request leaked compaction_trigger or unexpanded capsule: %s", secondSeen)
	}
	if !bytes.Contains(secondSeen, []byte(marker)) {
		t.Fatalf("expected prior summary marker %q in second summary request: %s", marker, secondSeen)
	}

	// 6. Incomplete summary must fail closed rather than sealing a truncated capsule.
	returnIncomplete = true
	if _, errIncExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	}); errIncExec == nil {
		t.Fatal("expected Execute(compaction_trigger) to fail on incomplete summary, got nil")
	}
	if _, errIncStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: triggerPayload,
	}); errIncStream == nil {
		t.Fatal("expected ExecuteStream(compaction_trigger) to fail on incomplete summary, got nil")
	}
	returnIncomplete = false

	// 7. Native (is-compat: false) Codex model forwards compaction_trigger untouched without local summary interception.
	nativePayload := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`)
	nativeResp, errNative := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: nativePayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: nativePayload,
	})
	if errNative != nil {
		t.Fatalf("Execute(native gpt-5.4) error = %v", errNative)
	}
	nativeSeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(nativeSeen, []byte("compaction_trigger")) {
		t.Fatalf("expected native is-compat=false model to forward compaction_trigger untouched, got: %s", nativeSeen)
	}
	if gotObj := ojson.GetBytes(nativeResp.Payload, "object").String(); gotObj != "response" {
		t.Fatalf("native response object = %q, want response", gotObj)
	}
}

func TestCodexExecutor_IsCompatCompactionValidationAndUsage_Issue7406(t *testing.T) {
	var upstreamHits int
	var mode string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = io.ReadAll(r.Body)
		switch mode {
		case "http500":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream boom"}}`)
		case "incomplete":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"incomplete\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}}\n\n"+
					"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_inc\",\"object\":\"response\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":10,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":4}}}}\n\n",
			)
		case "empty_summary":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_only\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"only thinking\"}]}}\n\n"+
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":10,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":4}}}}\n\n",
			)
		case "empty_incomplete":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_empty_inc\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":10,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":4}}}}\n\n",
			)
		case "response_failed":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fail\",\"object\":\"response\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed mid-summary\"},\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":10,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":4}}}}\n\n",
			)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_ok\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Summary text ok.\"}]}}\n\n"+
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":10,\"total_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":4}}}}\n\n",
			)
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "test-compat-key",
			BaseURL: server.URL,
			Models:  []config.CodexModel{{Name: "deepseek-v4.1-flash", IsCompat: true}},
		}},
	}
	executor := NewCodexExecutor(cfg)
	wsExecutor := NewCodexWebsocketsExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"base_url": server.URL, "api_key": "test-compat-key"},
	}

	// 1. Incremental previous_response_id + compaction_trigger must fail closed without hitting upstream.
	incTrigger := []byte(`{"model":"deepseek-v4.1-flash","event_id":"ev_inc","previous_response_id":"resp_real_1","input":[{"type":"compaction_trigger"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: incTrigger}
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: incTrigger}, opts); err == nil {
		t.Fatal("expected HTTP Execute with previous_response_id + compaction_trigger to fail")
	}
	wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	_, errWSInc := wsExecutor.ExecuteStream(wsCtx, auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: incTrigger}, opts)
	if !cliproxyexecutor.IsUpstreamWebsocketTurnError(errWSInc) {
		t.Fatalf("expected UpstreamWebsocketTurnError on websocket incremental trigger, got %v", errWSInc)
	}

	// 2. Empty conversation input (only compaction_trigger + additional_tools) must fail closed.
	emptyTrigger := []byte(`{"model":"deepseek-v4.1-flash","input":[{"type":"additional_tools","tools":[]},{"type":"compaction_trigger"}]}`)
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: emptyTrigger}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: emptyTrigger}); err == nil {
		t.Fatal("expected empty-history compaction_trigger to fail")
	}

	// 3. Synthetic resp_ag_compact_* previous_response_id on normal turn must never reach upstream.
	synthCont := []byte(`{"model":"deepseek-v4.1-flash","previous_response_id":"resp_ag_compact_12345","input":[{"type":"message","role":"user","content":"hi"}]}`)
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: synthCont}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: synthCont}); err == nil {
		t.Fatal("expected synthetic previous_response_id on HTTP Execute to fail")
	}
	if _, err := wsExecutor.ExecuteStream(wsCtx, auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: synthCont}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: synthCont}); !cliproxyexecutor.IsUpstreamWebsocketTurnError(err) {
		t.Fatalf("expected UpstreamWebsocketTurnError for synthetic previous_response_id on WS, got %v", err)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want 0 for rejected validation cases", upstreamHits)
	}

	// 4. CountTokens semantic consistency: foreign compaction dropped + CPA capsule expanded matches expanded developer message.
	capsule, errSeal := helps.SealAntigravityCompaction("Token count summary", "deepseek-v4.1-flash")
	if errSeal != nil {
		t.Fatalf("SealAntigravityCompaction: %v", errSeal)
	}
	mixedTokensPayload := []byte(fmt.Sprintf(`{"model":"deepseek-v4.1-flash","input":[{"id":"cmp_foreign","type":"compaction","encrypted_content":"gAAAAABforeign"},{"id":"cmp_ag_compact_1","type":"compaction","encrypted_content":%q},{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`, capsule))
	expectedExpandedPayload := []byte(`{"model":"deepseek-v4.1-flash","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Context summary from previous turns:\nToken count summary"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)
	gotCountResp, errGotCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: mixedTokensPayload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errGotCount != nil {
		t.Fatalf("CountTokens(mixed): %v", errGotCount)
	}
	wantCountResp, errWantCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: "deepseek-v4.1-flash", Payload: expectedExpandedPayload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errWantCount != nil {
		t.Fatalf("CountTokens(expected): %v", errWantCount)
	}
	if string(gotCountResp.Payload) != string(wantCountResp.Payload) {
		t.Fatalf("CountTokens mismatch: got %s, want %s", gotCountResp.Payload, wantCountResp.Payload)
	}

	// 5. Usage outcome & token accounting across valid, incomplete, empty_summary, http500, and canceled.
	validTrigger := []byte(`{"model":"deepseek-v4.1-flash","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`)
	for _, tc := range []struct {
		name       string
		mode       string
		cancelCtx  bool
		wantErr    bool
		wantTokens int64
	}{
		{name: "valid", mode: "ok", wantErr: false, wantTokens: 30},
		{name: "incomplete", mode: "incomplete", wantErr: true, wantTokens: 30},
		{name: "empty_summary", mode: "empty_summary", wantErr: true, wantTokens: 30},
		{name: "empty_incomplete_with_usage", mode: "empty_incomplete", wantErr: true, wantTokens: 30},
		{name: "response_failed_with_usage", mode: "response_failed", wantErr: true, wantTokens: 30},
		{name: "http500", mode: "http500", wantErr: true, wantTokens: 0},
		{name: "canceled", mode: "ok", cancelCtx: true, wantErr: true, wantTokens: 0},
	} {
		t.Run("usage_"+tc.name, func(t *testing.T) {
			mode = tc.mode
			alias := t.Name()
			capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{})
			})
			callCtx := coreusage.WithRequestedModelAlias(context.Background(), alias)
			if tc.cancelCtx {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(callCtx)
				cancel()
			}
			_, errExec := executor.Execute(callCtx, auth, cliproxyexecutor.Request{
				Model:   "deepseek-v4.1-flash",
				Payload: validTrigger,
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: validTrigger,
			})
			if (errExec != nil) != tc.wantErr {
				t.Fatalf("Execute err = %v, wantErr = %t", errExec, tc.wantErr)
			}
			rec := capture.await(t)
			if rec.Failed != tc.wantErr {
				t.Fatalf("usage record Failed = %t, want %t (fail=%+v)", rec.Failed, tc.wantErr, rec.Fail)
			}
			if rec.Detail.TotalTokens != tc.wantTokens {
				t.Fatalf("usage record TotalTokens = %d, want %d (detail=%+v)", rec.Detail.TotalTokens, tc.wantTokens, rec.Detail)
			}
			if tc.wantTokens > 0 && (rec.Detail.InputTokens != 20 || rec.Detail.OutputTokens != 10 || rec.Detail.ReasoningTokens != 4 || rec.Detail.CachedTokens != 6) {
				t.Fatalf("unexpected token breakdown: %+v", rec.Detail)
			}
			select {
			case extra := <-capture.records:
				t.Fatalf("unexpected duplicate usage record emitted: %+v", extra)
			default:
			}
		})
	}
}

func TestCodexExecutor_IsCompatCompactionMatrix_Issue7406(t *testing.T) {
	var lastBody []byte
	var lastAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuthHeader = r.Header.Get("Authorization")
		lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_m\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Matrix summary.\"}]}}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_m\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":4,\"total_tokens\":12}}}\n\n",
		)
	}))
	defer server.Close()

	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "outer-key",
			BaseURL: server.URL,
			Models: []config.CodexModel{{
				Name:     "deepseek-v4.1-flash",
				IsCompat: true,
				Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			}},
		}},
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: "deepseek-v4.1-flash", Protocol: "codex"}},
				Params: []string{"service_tier"},
			}},
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "deepseek-v4.1-flash", Protocol: "codex"}},
				Params: map[string]any{"max_output_tokens": 1536},
			}},
		},
	}
	executor := NewCodexExecutor(cfg)
	siwcAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"base_url": server.URL, "api_key": "outer-key", "use_siwc": "true"},
		Metadata:   map[string]any{"siwc": map[string]any{"access_token": "siwc-secret-token"}},
	}

	trigger := []byte(`{"model":"deepseek-v4.1-flash(high)","service_tier":"priority","input":[{"type":"message","role":"user","content":"summarize me"},{"type":"compaction_trigger"}]}`)
	resp, err := executor.Execute(context.Background(), siwcAuth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash(high)",
		Payload: trigger,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: trigger,
	})
	if err != nil {
		t.Fatalf("Execute(SIWC + suffix + payload rules) error = %v", err)
	}
	if ojson.GetBytes(resp.Payload, "object").String() != "response.compaction" {
		t.Fatalf("expected response.compaction, got: %s", resp.Payload)
	}
	if lastAuthHeader != "Bearer siwc-secret-token" {
		t.Fatalf("Authorization = %q, want Bearer siwc-secret-token", lastAuthHeader)
	}
	if gotEffort := ojson.GetBytes(lastBody, "reasoning.effort").String(); gotEffort != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", gotEffort, lastBody)
	}
	if ojson.GetBytes(lastBody, "service_tier").Exists() {
		t.Fatalf("service_tier should be filtered by payload filter rule: %s", lastBody)
	}
	if gotMax := ojson.GetBytes(lastBody, "max_output_tokens").Int(); gotMax != 1536 {
		t.Fatalf("max_output_tokens = %d, want 1536; body=%s", gotMax, lastBody)
	}

	// Authoritative ResolvedModelInfo{IsCompat: false} overrides local config IsCompat: true.
	overrideResp, errOverride := executor.Execute(context.Background(), siwcAuth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: trigger,
		Metadata: map[string]any{
			"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "deepseek-v4.1-flash", IsCompat: false},
		},
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: trigger,
	})
	if errOverride != nil {
		t.Fatalf("Execute(authoritative IsCompat=false) error = %v", errOverride)
	}
	if !bytes.Contains(lastBody, []byte("compaction_trigger")) {
		t.Fatalf("expected authoritative IsCompat=false to forward compaction_trigger untouched: %s", lastBody)
	}
	if ojson.GetBytes(overrideResp.Payload, "object").String() != "response" {
		t.Fatalf("expected normal response when authoritative IsCompat=false, got: %s", overrideResp.Payload)
	}
}

func TestCodexExecutor_IsCompatCompactionDuplexSession_Issue7406(t *testing.T) {
	const marker = "DUPLEX-COMPACT-7406"
	wsFrames := make(chan []byte, 8)
	httpBodies := make(chan []byte, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, errUp := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUp != nil {
				t.Errorf("upgrade ws: %v", errUp)
				return
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close upstream websocket: %v", errClose)
				}
			}()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

			// Turn 1: initial WS response.create
			_, frame1, errRead1 := conn.ReadMessage()
			if errRead1 != nil {
				return
			}
			wsFrames <- frame1
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_ws_1","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ack-1"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`))

			// Turn 3 (post-compaction root response.create; Turn 2 ran locally via HTTP!)
			_, frame2, errRead2 := conn.ReadMessage()
			if errRead2 != nil {
				return
			}
			wsFrames <- frame2
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_ws_3","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ack-3"}]}],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}}`))

			// Turn 4 (incremental continuation referencing resp_ws_3)
			_, frame3, errRead3 := conn.ReadMessage()
			if errRead3 != nil {
				return
			}
			wsFrames <- frame3
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_ws_4","previous_response_id":"resp_ws_3","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ack-4"}]}],"usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}}}`))

			_, _, _ = conn.ReadMessage()
			return
		}

		body, _ := io.ReadAll(r.Body)
		httpBodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_sum\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Duplex summary with "+marker+".\"}]}}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http_sum\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n",
		)
	}))
	defer server.Close()

	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "test-compat-key",
			BaseURL: server.URL,
			Models:  []config.CodexModel{{Name: "deepseek-v4.1-flash", IsCompat: true}},
		}},
	}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true

	wsExec := NewCodexWebsocketsExecutor(cfg)
	const sessID = "duplex-compat-compaction-7406"
	t.Cleanup(func() { wsExec.CloseExecutionSession(sessID) })

	auth := &cliproxyauth.Auth{
		ID:         "auth-duplex-compat-7406",
		Provider:   "codex",
		Attributes: map[string]string{"base_url": server.URL, "api_key": "test-compat-key", "websockets": "true"},
	}

	inputCh := make(chan cliproxyexecutor.WebsocketInput, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), inputCh)

	turn1 := []byte(`{"type":"response.create","model":"client-alias","instructions":"Keep concise.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Remember ` + marker + `"}]}]}`)
	streamRes, errStream := wsExec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: turn1,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
		Metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessID},
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream(turn1) error = %v", errStream)
	}

	readNextEvent := func(wantTypes ...string) ojson.Result {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case chunk, ok := <-streamRes.Chunks:
				if !ok {
					t.Fatalf("duplex stream closed unexpectedly while waiting for %v", wantTypes)
				}
				if chunk.Err != nil {
					t.Fatalf("duplex stream chunk error while waiting for %v: %v", wantTypes, chunk.Err)
				}
				trimmed := bytes.TrimSpace(chunk.Payload)
				for _, line := range bytes.Split(trimmed, []byte("\n")) {
					line = bytes.TrimSpace(line)
					if bytes.HasPrefix(line, []byte("data:")) {
						line = bytes.TrimSpace(line[len("data:"):])
					}
					if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
						continue
					}
					parsed := ojson.ParseBytes(line)
					gotType := parsed.Get("type").String()
					wantError := false
					for _, want := range wantTypes {
						if want == "error" {
							wantError = true
						}
						if gotType == want {
							return parsed
						}
					}
					if gotType == "error" && !wantError {
						t.Fatalf("unexpected error event while waiting for %v: %s", wantTypes, line)
					}
				}
			case <-deadline:
				t.Fatalf("timed out waiting for duplex event %v", wantTypes)
			}
		}
	}

	// Drain Turn 1 completion.
	_ = readNextEvent("response.completed")

	// 2a. response.steer carrying compaction_trigger is rejected locally without hitting upstream WS.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.steer","event_id":"ev_steer_bad","previous_response_id":"resp_ws_1","input":[{"type":"compaction_trigger"}]}`)}
	steerErrEv := readNextEvent("error")
	if steerErrEv.Get("status").Int() != http.StatusBadRequest || steerErrEv.Get("error.event_id").String() != "ev_steer_bad" {
		t.Fatalf("unexpected steer error event: %s", steerErrEv.Raw)
	}

	// 2b. Incremental response.create with previous_response_id + compaction_trigger returns 409 previous_response_not_found.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","event_id":"ev_inc_compact","previous_response_id":"resp_ws_1","input":[{"type":"compaction_trigger"}]}`)}
	incErrEv := readNextEvent("error")
	if incErrEv.Get("status").Int() != http.StatusConflict || incErrEv.Get("error.code").String() != "previous_response_not_found" || incErrEv.Get("error.event_id").String() != "ev_inc_compact" {
		t.Fatalf("unexpected incremental compaction error event: %s", incErrEv.Raw)
	}

	// 2c. Full response.create with updated instructions="INSTRUCTIONS_B" + compaction_trigger executes local HTTP summary.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"client-alias","instructions":"INSTRUCTIONS_B","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Remember ` + marker + `"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ack-1"}]},{"type":"compaction_trigger"}]}`)}
	compactCompleted := readNextEvent("response.completed")
	synthRespID := compactCompleted.Get("response.id").String()
	if !strings.HasPrefix(synthRespID, "resp_ag_compact_") {
		t.Fatalf("expected synthetic compaction response ID, got %q (%s)", synthRespID, compactCompleted.Raw)
	}
	if compactCompleted.Get("response.output.0.type").String() != "compaction" {
		t.Fatalf("expected compaction item in duplex compaction output: %s", compactCompleted.Raw)
	}
	summaryHTTPBody := <-httpBodies
	if got := ojson.GetBytes(summaryHTTPBody, "model").String(); got != "deepseek-v4.1-flash" {
		t.Fatalf("HTTP summary model = %q, want real model; body=%s", got, summaryHTTPBody)
	}
	if bytes.Contains(summaryHTTPBody, []byte("compaction_trigger")) || ojson.GetBytes(summaryHTTPBody, "tools").Exists() {
		t.Fatalf("duplex HTTP summary body leaked compaction_trigger or tools: %s", summaryHTTPBody)
	}

	// Invalid synthetic-parent inputs must be rejected without consuming the parent.
	for _, invalid := range []string{`{}`, `42`, `true`, `null`} {
		payload := fmt.Sprintf(`{"type":"response.create","event_id":"invalid-input","previous_response_id":%q,"input":%s}`, synthRespID, invalid)
		inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(payload)}
		ev := readNextEvent("error")
		if ev.Get("status").Int() != http.StatusBadRequest || ev.Get("error.event_id").String() != "invalid-input" {
			t.Fatalf("input %s: expected correlated 400, got %s", invalid, ev.Raw)
		}
	}

	// 3. response.append without instructions or previous_response_id inherits INSTRUCTIONS_B (not initial Keep concise.) and rewrites to expanded root response.create.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.append","input":"What was the marker?"}`)}
	turn3Completed := readNextEvent("response.completed")
	if turn3Completed.Get("response.id").String() != "resp_ws_3" {
		t.Fatalf("turn 3 response.id = %q, want resp_ws_3", turn3Completed.Get("response.id").String())
	}
	firstFrame := <-wsFrames
	if got := ojson.GetBytes(firstFrame, "model").String(); got != "deepseek-v4.1-flash" {
		t.Fatalf("initial model = %q, want real model; frame=%s", got, firstFrame)
	}
	wsTurn3 := <-wsFrames
	if got := ojson.GetBytes(wsTurn3, "model").String(); got != "deepseek-v4.1-flash" {
		t.Fatalf("continuation model = %q, want real model; frame=%s", got, wsTurn3)
	}
	items := ojson.GetBytes(wsTurn3, "input").Array()
	last := items[len(items)-1]
	if last.Get("role").String() != "user" || !strings.Contains(last.Raw, "What was the marker?") {
		t.Fatalf("synthetic-parent string input lost or not a user message: %s", wsTurn3)
	}
	if ojson.GetBytes(wsTurn3, "previous_response_id").Exists() {
		t.Fatalf("synthetic previous_response_id leaked to upstream WS on turn 3: %s", wsTurn3)
	}
	if gotInst := ojson.GetBytes(wsTurn3, "instructions").String(); gotInst != "INSTRUCTIONS_B" {
		t.Fatalf("turn 3 instructions = %q, want INSTRUCTIONS_B (from compaction turn, not initial); frame=%s", gotInst, wsTurn3)
	}
	if bytes.Contains(wsTurn3, []byte("cpa-ag-compact-v1:")) || !bytes.Contains(wsTurn3, []byte(marker)) {
		t.Fatalf("expected expanded summary marker without raw capsule on turn 3 WS frame: %s", wsTurn3)
	}

	// 4. Subsequent incremental continuation referencing resp_ws_3 forwards previous_response_id on upstream WS.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"client-alias","previous_response_id":"resp_ws_3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Follow up 4"}]}]}`)}
	turn4Completed := readNextEvent("response.completed")
	if turn4Completed.Get("response.id").String() != "resp_ws_4" {
		t.Fatalf("turn 4 response.id = %q, want resp_ws_4", turn4Completed.Get("response.id").String())
	}
	wsTurn4 := <-wsFrames
	if got := ojson.GetBytes(wsTurn4, "model").String(); got != "deepseek-v4.1-flash" {
		t.Fatalf("ordinary continuation model = %q, want real model; frame=%s", got, wsTurn4)
	}
	if gotPrev := ojson.GetBytes(wsTurn4, "previous_response_id").String(); gotPrev != "resp_ws_3" {
		t.Fatalf("turn 4 WS previous_response_id = %q, want resp_ws_3; frame=%s", gotPrev, wsTurn4)
	}

	// 5. Unknown synthetic resp_ag_compact_* is rejected locally with 409 previous_response_not_found.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","event_id":"ev_stale_synth","previous_response_id":"resp_ag_compact_unknown","input":[{"type":"message","role":"user","content":"stale"}]}`)}
	staleErrEv := readNextEvent("error")
	if staleErrEv.Get("status").Int() != http.StatusConflict || staleErrEv.Get("error.code").String() != "previous_response_not_found" {
		t.Fatalf("unexpected stale synthetic ID error: %s", staleErrEv.Raw)
	}
}

func TestCodexExecutor_IsCompatCompactionDuplexEOFCancelsHTTP_Issue7406(t *testing.T) {
	httpStarted := make(chan struct{})
	httpCanceled := make(chan struct{})
	closeUpstreamWS := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, errUp := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUp != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, _, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_ws_1","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`))
			<-closeUpstreamWS
			return
		}

		_, _ = io.ReadAll(r.Body)
		close(httpStarted)
		<-r.Context().Done()
		close(httpCanceled)
	}))
	defer server.Close()

	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "test-compat-key",
			BaseURL: server.URL,
			Models:  []config.CodexModel{{Name: "deepseek-v4.1-flash", IsCompat: true}},
		}},
	}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true

	wsExec := NewCodexWebsocketsExecutor(cfg)
	const sessID = "duplex-compat-eof-7406"
	t.Cleanup(func() { wsExec.CloseExecutionSession(sessID) })
	auth := &cliproxyauth.Auth{
		ID:         "auth-duplex-eof-7406",
		Provider:   "codex",
		Attributes: map[string]string{"base_url": server.URL, "api_key": "test-compat-key", "websockets": "true"},
	}

	inputCh := make(chan cliproxyexecutor.WebsocketInput, 4)
	ctx := cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), inputCh)
	streamRes, errStream := wsExec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"type":"response.create","model":"deepseek-v4.1-flash","input":[{"type":"message","role":"user","content":"turn1"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
		Metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessID},
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v", errStream)
	}

	// Drain Turn 1 response.completed.
	for chunk := range streamRes.Chunks {
		if ojson.GetBytes(bytes.TrimSpace(chunk.Payload), "type").String() == "response.completed" {
			break
		}
	}

	// Trigger in-flight HTTP compaction, wait at synchronization barrier, then drop upstream WS.
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","event_id":"ev_compact_eof","model":"deepseek-v4.1-flash","input":[{"type":"message","role":"user","content":"turn1"},{"type":"compaction_trigger"}]}`)}
	select {
	case <-httpStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for duplex HTTP compaction to start")
	}
	close(closeUpstreamWS)

	select {
	case <-httpCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream WS EOF did not cancel in-flight duplex HTTP compaction")
	}

	var gotTurnErr *cliproxyexecutor.UpstreamWebsocketTurnError
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			if uErr, ok := chunk.Err.(*cliproxyexecutor.UpstreamWebsocketTurnError); ok {
				gotTurnErr = uErr
			}
		}
	}
	if gotTurnErr == nil {
		t.Fatal("expected UpstreamWebsocketTurnError on duplex WS EOF during compaction")
	}
	if len(gotTurnErr.Abandoned) != 1 || gotTurnErr.Abandoned[0].EventID != "ev_compact_eof" || !gotTurnErr.Abandoned[0].NotSent {
		t.Fatalf("expected correlated NotSent abandoned error for ev_compact_eof, got idle=%t abandoned=%+v", gotTurnErr.Idle, gotTurnErr.Abandoned)
	}
}

func TestCodexExecutor_IsCompatCompactionDuplexDeliveryOrder_Issue7406(t *testing.T) {
	sendTurn1Complete := make(chan struct{})
	httpStarted := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, errUp := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUp != nil {
				return
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close upstream websocket: %v", errClose)
				}
			}()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, _, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_turn1","output":[]}}`))
			<-sendTurn1Complete
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_turn1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`))
			_, _, _ = conn.ReadMessage()
			return
		}

		_, _ = io.ReadAll(r.Body)
		close(httpStarted)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_s\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Ordered summary.\"}]}}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n",
		)
	}))
	defer server.Close()

	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "test-compat-key",
			BaseURL: server.URL,
			Models:  []config.CodexModel{{Name: "deepseek-v4.1-flash", IsCompat: true}},
		}},
	}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true

	wsExec := NewCodexWebsocketsExecutor(cfg)
	terminalPending := make(chan struct{}, 1)
	compactionReady := make(chan bool, 8)
	wsExec.duplexStateObserver = func(event string, ready bool) {
		switch event {
		case "terminal_pending":
			terminalPending <- struct{}{}
		case "compaction_wait":
			compactionReady <- ready
		}
	}
	const sessID = "duplex-compat-order-7406"
	t.Cleanup(func() { wsExec.CloseExecutionSession(sessID) })
	auth := &cliproxyauth.Auth{
		ID:         "auth-duplex-order-7406",
		Provider:   "codex",
		Attributes: map[string]string{"base_url": server.URL, "api_key": "test-compat-key", "websockets": "true"},
	}

	inputCh := make(chan cliproxyexecutor.WebsocketInput, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), inputCh)

	streamRes, errStream := wsExec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"type":"response.create","model":"deepseek-v4.1-flash","input":[{"type":"message","role":"user","content":"turn1"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
		Metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessID},
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v", errStream)
	}

	// Read Turn 1 response.created so inputReady is open.
	firstChunk := <-streamRes.Chunks
	if ojson.GetBytes(bytes.TrimSpace(firstChunk.Payload), "type").String() != "response.created" {
		t.Fatalf("expected Turn 1 response.created first, got %s", firstChunk.Payload)
	}

	// Let the reader finish terminal bookkeeping, but leave its unbuffered send
	// blocked. Only then enqueue compaction and observe the writer evaluating its
	// fence. This proves both goroutines reached the disputed delivery window.
	close(sendTurn1Complete)
	select {
	case <-terminalPending:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not reach terminal delivery")
	}
	inputCh <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"deepseek-v4.1-flash","input":[{"type":"message","role":"user","content":"turn1"},{"type":"compaction_trigger"}]}`)}
	select {
	case ready := <-compactionReady:
		if ready {
			t.Fatal("compaction fence opened before prior terminal delivery")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not evaluate the compaction fence")
	}

	secondChunk := <-streamRes.Chunks
	if ojson.GetBytes(bytes.TrimSpace(secondChunk.Payload), "type").String() != "response.completed" ||
		ojson.GetBytes(bytes.TrimSpace(secondChunk.Payload), "response.id").String() != "resp_turn1" {
		t.Fatalf("expected Turn 1 response.completed before any Turn 2 synthetic event, got: %s", secondChunk.Payload)
	}

	// Now Turn 2 synthetic events must follow in order ending with synthetic response.completed.
	var sawSynthCreated, sawSynthCompleted bool
	for !sawSynthCompleted {
		chunk := <-streamRes.Chunks
		for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("data:")) {
				line = bytes.TrimSpace(line[len("data:"):])
			}
			if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
				continue
			}
			evType := ojson.GetBytes(line, "type").String()
			if evType == "response.created" {
				sawSynthCreated = true
			}
			if evType == "response.completed" {
				if !sawSynthCreated {
					t.Fatalf("synthetic response.completed arrived before response.created: %s", line)
				}
				sawSynthCompleted = true
			}
		}
	}
}
