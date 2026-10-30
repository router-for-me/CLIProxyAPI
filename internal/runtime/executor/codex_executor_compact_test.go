package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
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
			if instructions := gjson.GetBytes(gotBody, "instructions"); instructions.Type != gjson.String || instructions.String() != "" {
				t.Fatalf("instructions = %s, want empty string; body=%s", instructions.Raw, gotBody)
			}
			if gjson.GetBytes(gotBody, "tools").Exists() {
				t.Fatalf("compact request injected image_generation tool: %s", gotBody)
			}
			input := gjson.GetBytes(gotBody, "input").Array()
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
	for _, omitted := range []string{"instructions", "max_output_tokens", "prompt_cache_retention", "prompt_cache_options", "stream", "store"} {
		if gjson.GetBytes(gotBody, omitted).Exists() {
			t.Fatalf("field %q should be omitted in SIWC compact body: %s", omitted, gotBody)
		}
	}
	// PayloadFinalizer runs last and can override temperature after built-in SIWC stripping.
	if gotTemp := gjson.GetBytes(gotBody, "temperature").Float(); gotTemp != 0.25 {
		t.Fatalf("PayloadFinalizer temperature override = %v, want 0.25; body=%s", gotTemp, gotBody)
	}
}

func TestCodexExecutorReplayedWebSearchCallHistoryEnsuresWebSearchTool_Issue6770(t *testing.T) {
	hasWebSearchCallInInput := func(body []byte) bool {
		for _, item := range gjson.GetBytes(body, "input").Array() {
			if item.Get("type").String() == "web_search_call" {
				return true
			}
		}
		return false
	}
	countWebSearchTools := func(body []byte) int {
		count := 0
		for _, tool := range gjson.GetBytes(body, "tools").Array() {
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
		if got := len(gjson.GetBytes(gotBody, "tools").Array()); got != 1 {
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
		if !gjson.GetBytes(prepared.clientBody, "tools.0.external_web_access").Bool() {
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
		if gjson.GetBytes(prepared.clientBody, "tools").Exists() {
			t.Fatalf("Responses Lite request must not inject top-level tools: %s", prepared.clientBody)
		}
		if hasWebSearchCallInInput(prepared.clientBody) {
			t.Fatalf("Responses Lite request must strip replayed web_search_call items: %s", prepared.clientBody)
		}
		inputItems := gjson.GetBytes(prepared.clientBody, "input").Array()
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
		if gjson.GetBytes(gotBody, "tools").Exists() {
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
		input := gjson.GetBytes(body, "input").Array()
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
		if gjson.GetBytes(expandedCountResp.Payload, "response.usage.input_tokens").Int() <= gjson.GetBytes(baseCountResp.Payload, "response.usage.input_tokens").Int() {
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
			evType := gjson.GetBytes(chunk.Payload, "type").String()
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
			input := gjson.GetBytes(frame, "input").Array()
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
