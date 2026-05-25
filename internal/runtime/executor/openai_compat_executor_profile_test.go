package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorCompactFallsBackToChatCompletionsForProfile(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"kimi-k2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("newapi-provider", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "newapi-provider",
			Kind: "newapi",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "newapi-provider",
		"compat_kind": "newapi",
	}}
	payload := []byte(`{"model":"kimi-k2","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if !gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("expected chat completions payload, got %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("unexpected responses input payload, got %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "response" {
		t.Fatalf("response object = %q, want %q; payload=%s", got, "response", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorParsesRetryAfterHints(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		body       string
		want       time.Duration
		wantStatus int
	}{
		{
			name:       "header",
			header:     "7",
			body:       `{"error":{"message":"rate limit exceeded"}}`,
			want:       7 * time.Second,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "body",
			body:       `{"error":{"message":"quota exhausted","retry_after":9}}`,
			want:       9 * time.Second,
			wantStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.header != "" {
					w.Header().Set("Retry-After", tt.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL + "/v1",
				"api_key":  "test",
			}}
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5",
				Payload: []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai"),
			})
			if err == nil {
				t.Fatal("expected error")
			}
			status, ok := err.(statusErr)
			if !ok {
				t.Fatalf("error type = %T, want statusErr", err)
			}
			if status.StatusCode() != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status.StatusCode(), tt.wantStatus)
			}
			retryAfter := status.RetryAfter()
			if retryAfter == nil {
				t.Fatal("expected retry-after hint")
			}
			if *retryAfter != tt.want {
				t.Fatalf("retry-after = %v, want %v", *retryAfter, tt.want)
			}
		})
	}
}

func TestOpenAICompatExecutorStreamScrubsUnsupportedFieldsForProfile(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("newapi-provider", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "newapi-provider",
			Kind: "newapi",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "newapi-provider",
		"compat_kind": "newapi",
	}}

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "kimi-k2",
		Payload: []byte(`{
			"model":"kimi-k2",
			"messages":[{"role":"assistant","content":"thinking","reasoning_content":"hidden"}],
			"stream":true,
			"parallel_tool_calls":true,
			"reasoning":{"effort":"high"},
			"metadata":{"tenant":"demo"},
			"store":true
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	for _, path := range []string{
		"stream_options",
		"parallel_tool_calls",
		"reasoning",
		"metadata",
		"store",
		"messages.0.reasoning_content",
	} {
		if gjson.GetBytes(gotBody, path).Exists() {
			t.Fatalf("unexpected field %s in payload: %s", path, string(gotBody))
		}
	}
}

func TestOpenAICompatExecutorClaudeSourceNormalizesKimiToolReferences(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"kimi-k2.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("kimi-provider", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "kimi-provider",
			Kind: "kimi",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "kimi-provider",
		"compat_kind": "kimi",
	}}

	payload := []byte(`{
		"model":"kimi-k2.6",
		"max_tokens":1024,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"read it"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read:file","input":{"path":"/tmp/a.txt"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"ok"},
				{"type":"text","text":"continue"}
			]}
		],
		"tools":[{
			"name":"read:file",
			"description":"Read a file",
			"input_schema":{
				"type":"object",
				"properties":{"path":{"type":"string"}},
				"required":["path"]
			}
		}],
		"tool_choice":{"type":"tool","name":"read:file"}
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function: %s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.0.function.name").String(); got != "read_file" {
		t.Fatalf("tool name = %q, want read_file: %s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tool_choice.function.name").String(); got != "read_file" {
		t.Fatalf("tool_choice name = %q, want read_file: %s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.1.tool_calls.0.function.name").String(); got != "read_file" {
		t.Fatalf("tool_call name = %q, want read_file: %s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "tools.0.input_schema").Exists() {
		t.Fatalf("input_schema should be converted away: %s", string(gotBody))
	}
}

func TestOpenAICompatExecutorClaudeSourceDowngradesToolSearch(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"glm-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("zhipu-provider", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "zhipu-provider",
			Kind: "zhipu",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "zhipu-provider",
		"compat_kind": "zhipu",
	}}

	payload := []byte(`{
		"model":"glm-4.6",
		"max_tokens":1024,
		"messages":[{"role":"user","content":[{"type":"text","text":"read it"}]}],
		"tools":[
			{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},
			{"name":"mcp__files__read","description":"Read files","defer_loading":true,"input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-4.6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := len(gjson.GetBytes(gotBody, "tools").Array()); got != 1 {
		t.Fatalf("tools length = %d, want 1: %s", got, string(gotBody))
	}
	if strings.HasPrefix(gjson.GetBytes(gotBody, "tools.0.function.name").String(), "tool_search_tool_") {
		t.Fatalf("tool search tool should not reach upstream: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.0.function.name").String(); got != "mcp__files__read" {
		t.Fatalf("tool name = %q, want mcp__files__read: %s", got, string(gotBody))
	}
}

func TestInferOpenAICompatKindFromBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "kimi moonshot", baseURL: "https://api.moonshot.ai/v1", want: "kimi"},
		{name: "kimi moonshot cn", baseURL: "https://api.moonshot.cn/v1", want: "kimi"},
		{name: "kimi coding", baseURL: "https://api.kimi.com/coding/v1", want: "kimi"},
		{name: "minimax openai", baseURL: "https://api.minimax.io/v1", want: "minimax"},
		{name: "zhipu coding", baseURL: "https://open.bigmodel.cn/api/coding/paas/v4", want: "zhipu"},
		{name: "zai", baseURL: "https://api.z.ai/api/paas/v4", want: "zhipu"},
		{name: "deepseek", baseURL: "https://api.deepseek.com/v1", want: "deepseek"},
		{name: "xiaomi openai", baseURL: "https://api.xiaomimimo.com/v1", want: "xiaomi"},
		{name: "xiaomi token plan", baseURL: "https://token-plan-cn.xiaomimimo.com/v1", want: "xiaomi"},
		{name: "xiaomi token plan singapore", baseURL: "https://token-plan-sgp.xiaomimimo.com/v1", want: "xiaomi"},
		{name: "xiaomi token plan europe anthropic", baseURL: "https://token-plan-ams.xiaomimimo.com/anthropic", want: "xiaomi"},
		{name: "unknown", baseURL: "https://example.com/v1", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferOpenAICompatKindFromBaseURL(tt.baseURL); got != tt.want {
				t.Fatalf("inferOpenAICompatKindFromBaseURL(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestOpenAICompatPayloadRepairsUnansweredToolCalls(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":"will call tools","tool_calls":[
				{"id":"call_01","type":"function","function":{"name":"read_file","arguments":"{}"}},
				{"id":"call_02","type":"function","function":{"name":"glob","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_01","content":"ok"},
			{"role":"user","content":"continue"}
		]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.moonshot.ai/v1")

	if got := len(gjson.GetBytes(out, "messages.1.tool_calls").Array()); got != 1 {
		t.Fatalf("tool_calls length = %d, want 1: %s", got, string(out))
	}
	if gjson.GetBytes(out, `messages.1.tool_calls.#(id=="call_02")`).Exists() {
		t.Fatalf("unanswered call_02 should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_01" {
		t.Fatalf("kept tool result id = %q, want call_01: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadDropsToolOnlyAssistantMessage(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_01","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"user","content":"continue"}
		]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.moonshot.ai/v1")

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("remaining role = %q, want user: %s", got, string(out))
	}
}

func TestOpenAICompatHTTPRequestBodyRepairsKimiOrphanReplyToolCall(t *testing.T) {
	payload := `{
		"model":"kimi-k2.5",
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":"reply pending","tool_calls":[{"id":"reply:0","type":"function","function":{"name":"reply","arguments":"{}"}}]},
			{"role":"user","content":"continue"}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "https://api.kimi.com/coding/v1/chat/completions", strings.NewReader(payload))

	if err := sanitizeOpenAICompatHTTPRequestBody(req, openAICompatProfileForKind("kimi"), "https://api.kimi.com/coding/v1"); err != nil {
		t.Fatalf("sanitizeOpenAICompatHTTPRequestBody() error = %v", err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if gjson.GetBytes(body, "messages.1.tool_calls").Exists() {
		t.Fatalf("orphan reply tool_call should be removed: %s", string(body))
	}
	if got := gjson.GetBytes(body, "messages.1.content").String(); got != "reply pending" {
		t.Fatalf("assistant content = %q, want preserved text: %s", got, string(body))
	}
}

func TestOpenAICompatPayloadDropsEmptyMessages(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5.5",
		"messages":[
			{"role":"user","content":[]},
			{"role":"assistant","content":[{"type":"text","text":""}]},
			{"role":"user","content":"continue"}
		]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "gpt-5.5", "https://api.openai.com/v1")

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), string(out))
	}
	if got := messages[0].Get("content").String(); got != "continue" {
		t.Fatalf("remaining content = %q, want continue: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadDropsMalformedToolCallsAndResults(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5.5",
		"messages":[
			{"role":"assistant","content":"checking","tool_calls":[
				{"id":"call_ok","type":"function","function":{"name":"read:file","arguments":{"path":"README.md"}}},
				{"id":"call_bad","type":"function","function":{"arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_ok","content":"ok"},
			{"role":"tool","tool_call_id":"call_bad","content":"bad"},
			{"role":"user","content":"next"}
		]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "gpt-5.5", "https://api.openai.com/v1")

	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 1 {
		t.Fatalf("tool_calls length = %d, want 1: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String(); got != "read_file" {
		t.Fatalf("normalized function.name = %q, want read_file: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.arguments").String(); got != `{"path":"README.md"}` {
		t.Fatalf("normalized function.arguments = %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_ok" {
		t.Fatalf("kept tool result id = %q, want call_ok: %s", got, string(out))
	}
	if gjson.GetBytes(out, `messages.#(tool_call_id=="call_bad")`).Exists() {
		t.Fatalf("malformed tool result should be removed: %s", string(out))
	}
}

func TestOpenAICompatPayloadDropsDuplicateToolCalls(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5.5",
		"messages":[
			{"role":"assistant","content":"checking","tool_calls":[
				{"id":"call_dup","type":"function","function":{"name":"read_file","arguments":"{}"}},
				{"id":"call_dup","type":"function","function":{"name":"read_file","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_dup","content":"ok"},
			{"role":"user","content":"next"}
		]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "gpt-5.5", "https://api.openai.com/v1")

	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 1 {
		t.Fatalf("tool_calls length = %d, want 1: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_dup" {
		t.Fatalf("kept tool result id = %q, want call_dup: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadKimiNormalizesToolsAndDisablesStrict(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"read:file",
			"description":"Read a file",
			"input_schema":{
				"type":"object",
				"properties":{"path":{"type":"string"}},
				"required":["path"]
			},
			"strict":true
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.moonshot.ai/v1")

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "read_file" {
		t.Fatalf("tool name = %q, want read_file: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.strict").Bool(); got {
		t.Fatalf("kimi strict should be disabled for schema compatibility: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.0.input_schema").Exists() {
		t.Fatalf("input_schema should be converted away: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.path.type").String(); got != "string" {
		t.Fatalf("converted parameters missing path type, got %q: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadKimiSanitizesMoonshotSchemaFlavor(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"inspect",
			"description":"Inspect values",
			"input_schema":{
				"type":"object",
				"properties":{
					"type":"object",
					"additionalProperties":"object"
				},
				"additionalProperties":"object",
				"required":null
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.moonshot.ai/v1")

	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.type.type").String(); got != "object" {
		t.Fatalf("properties.type should be an object schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.additionalProperties.type").String(); got != "object" {
		t.Fatalf("properties.additionalProperties should be an object schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.additionalProperties.type").String(); got != "object" {
		t.Fatalf("root additionalProperties should be an object schema, got %q: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.function.parameters.required").Exists() {
		t.Fatalf("required=null should be removed: %s", string(out))
	}
}

func TestOpenAICompatPayloadKimiRemovesParentTypeFromAnyOfSchema(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"inspect",
			"description":"Inspect values",
			"input_schema":{
				"type":"object",
				"properties":{
					"modules":{
						"type":"array",
						"anyOf":[
							{"type":"array","items":{"type":"string"}},
							{"type":"null"}
						]
					}
				}
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.moonshot.ai/v1")

	if gjson.GetBytes(out, "tools.0.function.parameters.properties.modules.type").Exists() {
		t.Fatalf("anyOf parent type should be removed for moonshot schema flavor: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.modules.anyOf.0.type").String(); got != "array" {
		t.Fatalf("anyOf branch type should be preserved, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.modules.anyOf.0.items.type").String(); got != "string" {
		t.Fatalf("anyOf branch items should be preserved, got %q: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadZhipuForcesAutoToolChoice(t *testing.T) {
	payload := []byte(`{
		"model":"glm-4.6",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}],
		"tool_choice":"required"
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("zhipu"), "glm-4.6", "https://open.bigmodel.cn/api/paas/v4")

	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadZhipuConvertsDataURLImagesToRawBase64(t *testing.T) {
	payload := []byte(`{
		"model":"glm-4.5v",
		"messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}},
			{"type":"text","text":"describe"}
		]}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("zhipu"), "glm-4.5v", "https://open.bigmodel.cn/api/paas/v4")

	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "AAAA" {
		t.Fatalf("image_url.url = %q, want raw base64: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.detail").String(); got != "high" {
		t.Fatalf("image detail = %q, want high: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadKimiPreservesDataURLImages(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-latest",
		"messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
		]}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-latest", "https://api.moonshot.ai/v1")

	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "data:image/png;base64,AAAA" {
		t.Fatalf("image_url.url = %q, want data URL preserved: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadKimiPreservesAssistantReasoningContent(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[
			{"role":"assistant","content":"planning","reasoning_content":"actual reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		],
		"reasoning_effort":"high"
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.kimi.com/coding/v1")

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "actual reasoning" {
		t.Fatalf("messages.0.reasoning_content = %q, want actual reasoning: %s", got, string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed for kimi compat payload: %s", string(out))
	}
}

func TestOpenAICompatPayloadKimiRepairsMissingAssistantToolCallReasoning(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[
			{"role":"assistant","content":"I will read it","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.kimi.com/coding/v1")

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "I will read it" {
		t.Fatalf("messages.0.reasoning_content = %q, want content fallback: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadXiaomiScrubsUnsupportedOpenAIExtras(t *testing.T) {
	payload := []byte(`{
		"model":"mimo-v2.5",
		"messages":[{"role":"assistant","content":"thinking","reasoning_content":"hidden"}],
		"stream_options":{"include_usage":true},
		"parallel_tool_calls":true,
		"reasoning_effort":"high",
		"metadata":{"tenant":"demo"},
		"store":true,
		"thinking":{"type":"enabled"}
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("xiaomi"), "mimo-v2.5", "https://api.xiaomimimo.com/v1")

	for _, path := range []string{
		"stream_options",
		"parallel_tool_calls",
		"reasoning_effort",
		"metadata",
		"store",
		"messages.0.reasoning_content",
	} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("unexpected field %s in payload: %s", path, string(out))
		}
	}
	if !gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("native thinking field should be preserved: %s", string(out))
	}
}

func TestOpenAICompatPayloadXiaomiNormalizesClaudeStyleTools(t *testing.T) {
	payload := []byte(`{
		"model":"mimo-v2.5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"inspect",
			"description":"Inspect values",
			"input_schema":{
				"type":"object",
				"properties":{"type":"object"},
				"required":null
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("xiaomi"), "mimo-v2.5", "https://api.xiaomimimo.com/v1")

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.input_schema").Exists() {
		t.Fatalf("input_schema should be converted away: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.type.type").String(); got != "object" {
		t.Fatalf("properties.type should be an object schema, got %q: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadNormalizesFunctionNameReferences(t *testing.T) {
	payload := []byte(`{
		"model":"kimi-k2.6",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read:file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		],
		"tools":[{"type":"function","function":{"name":"read:file","parameters":{"type":"object","properties":{}}}}],
		"tool_choice":{"type":"function","function":{"name":"read:file"}}
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "kimi-k2.6", "https://api.moonshot.ai/v1")

	for _, path := range []string{
		"tools.0.function.name",
		"tool_choice.function.name",
		"messages.0.tool_calls.0.function.name",
	} {
		if got := gjson.GetBytes(out, path).String(); got != "read_file" {
			t.Fatalf("%s = %q, want read_file: %s", path, got, string(out))
		}
	}
}

func TestOpenAICompatPayloadDeepSeekStripsStrictOutsideBeta(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"lookup",
				"description":"Lookup records",
				"strict":true,
				"parameters":{
					"type":"object",
					"properties":{"query":{"type":"string"}},
					"additionalProperties":false
				}
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/v1")

	if gjson.GetBytes(out, "tools.0.function.strict").Exists() {
		t.Fatalf("strict should be removed outside beta endpoint: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.query.type").String(); got != "string" {
		t.Fatalf("parameters were not preserved, got query.type=%q payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function; payload=%s", got, string(out))
	}
}

func TestOpenAICompatPayloadDeepSeekConvertsInputSchemaTools(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"read_file",
			"description":"Read a file",
			"input_schema":{
				"type":"object",
				"properties":{"path":{"type":"string"}},
				"required":["path"]
			},
			"strict":true
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/v1")

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function; payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "read_file" {
		t.Fatalf("tool name = %q, want read_file; payload=%s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.input_schema").Exists() {
		t.Fatalf("input_schema should be converted away: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.0.function.strict").Exists() {
		t.Fatalf("strict should be removed outside beta endpoint: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.path.type").String(); got != "string" {
		t.Fatalf("converted parameters missing path type, got %q payload=%s", got, string(out))
	}
}

func TestOpenAICompatPayloadDeepSeekSanitizesLooseSchemaValues(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"inspect",
				"parameters":{
					"type":"object",
					"properties":{
						"type":"object",
						"required":"array"
					},
					"additionalProperties":"object",
					"required":null
				}
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/v1")

	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.type.type").String(); got != "object" {
		t.Fatalf("properties.type should be an object schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.required.type").String(); got != "array" {
		t.Fatalf("properties.required should be an array schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.additionalProperties.type").String(); got != "object" {
		t.Fatalf("additionalProperties should be an object schema, got %q: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.function.parameters.required").Exists() {
		t.Fatalf("required=null should be removed: %s", string(out))
	}
}

func TestOpenAICompatPayloadDeepSeekNormalizesThinkingBudget(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"thinking_budget":50,
		"thinking":{"type":"enabled","budget_tokens":99999},
		"reasoning_effort":"xhigh"
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/v1")

	if got := gjson.GetBytes(out, "thinking_budget").Int(); got != 100 {
		t.Fatalf("thinking_budget = %d, want 100: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != 32768 {
		t.Fatalf("thinking.budget_tokens = %d, want 32768: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want max: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadDeepSeekRemovesBudgetWhenThinkingDisabled(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"thinking_budget":50,
		"thinking":{"type":"disabled","budget_tokens":50}
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/v1")

	for _, path := range []string{"thinking_budget", "thinking.budget_tokens"} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s should be removed when thinking is disabled: %s", path, string(out))
		}
	}
}

func TestOpenAICompatPayloadDeepSeekReasoningNoneDisablesThinking(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"thinking_budget":50,
		"reasoning_effort":"none"
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/v1")

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled: %s", got, string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed when disabling DeepSeek thinking: %s", string(out))
	}
	if gjson.GetBytes(out, "thinking_budget").Exists() {
		t.Fatalf("thinking_budget should be removed when disabling DeepSeek thinking: %s", string(out))
	}
}

func TestOpenAICompatPayloadDeepSeekBudgetScrubSkipsOtherCompatProfiles(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"thinking_budget":50
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, openAICompatProfileForKind("kimi"), "deepseek-v4-pro", "https://api.kimi.com/coding/v1")

	if got := gjson.GetBytes(out, "thinking_budget").Int(); got != 50 {
		t.Fatalf("thinking_budget = %d, want unchanged 50 for kimi compat: %s", got, string(out))
	}
}

func TestOpenAICompatPayloadGenericSanitizesFunctionSchemaWithoutDroppingStrict(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"inspect",
				"strict":true,
				"parameters":{
					"type":"object",
					"properties":{"type":"object"},
					"additionalProperties":"object",
					"required":null
				}
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "gpt-5", "https://api.openai.com/v1")

	if got := gjson.GetBytes(out, "tools.0.function.strict").Bool(); !got {
		t.Fatalf("generic strict flag should be preserved: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.type.type").String(); got != "object" {
		t.Fatalf("properties.type should be an object schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.additionalProperties.type").String(); got != "object" {
		t.Fatalf("additionalProperties should be an object schema, got %q: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.function.parameters.required").Exists() {
		t.Fatalf("required=null should be removed: %s", string(out))
	}
}

func TestOpenAICompatPayloadDeepSeekKeepsStrictOnBetaAndNormalizesSchema(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"lookup",
				"description":"Lookup records",
				"strict":true,
				"parameters":{
					"type":"object",
					"properties":{
						"query":{"type":"string"},
						"limit":{"type":"integer"}
					},
					"required":["query"]
				}
			}
		}]
	}`)

	out := scrubOpenAICompatPayloadForModel(payload, genericOpenAICompatProfile(), "deepseek-v4-pro", "https://api.deepseek.com/beta")

	if got := gjson.GetBytes(out, "tools.0.function.strict").Bool(); !got {
		t.Fatalf("strict should be kept on beta endpoint: %s", string(out))
	}
	additionalProperties := gjson.GetBytes(out, "tools.0.function.parameters.additionalProperties")
	if !additionalProperties.Exists() {
		t.Fatalf("additionalProperties=false should be present in strict schema: %s", string(out))
	}
	if additionalProperties.Raw != "false" {
		t.Fatalf("additionalProperties should be false, got %s: %s", additionalProperties.Raw, string(out))
	}
	if !requiredContains(out, "tools.0.function.parameters.required", "query") ||
		!requiredContains(out, "tools.0.function.parameters.required", "limit") {
		t.Fatalf("strict schema should require all object properties: %s", string(out))
	}
}

func requiredContains(payload []byte, path string, want string) bool {
	values := gjson.GetBytes(payload, path)
	if !values.IsArray() {
		return false
	}
	for _, value := range values.Array() {
		if value.String() == want {
			return true
		}
	}
	return false
}
