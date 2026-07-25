package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

func testContextWithAPIKey(apiKey string) context.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Set("userApiKey", apiKey)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestCountXAIInputTokensExcludesRequestStructure(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("tokenizer.Get() error = %v", err)
	}

	semanticBody := []byte(`{
		"instructions":"Follow the repository instructions.",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Review this implementation."}]},
			{"type":"function_call","name":"read_file","arguments":"{\"path\":\"main.go\"}"},
			{"type":"function_call_output","output":"package main"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"I will inspect the file."}]}
		],
		"tools":[{"type":"function","name":"read_file","description":"Reads a file.","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],
		"text":{"format":{"name":"result","schema":{"type":"object"}}}
	}`)
	structuralBody := []byte(`{
		"model":"grok-4.5", "stream":false, "reasoning":{"effort":"high"},
		"metadata":{"large_wrapper":"this metadata must not affect estimated input tokens"},
		"prompt_cache_key":"session-123", "max_output_tokens":4096,
		"instructions":"Follow the repository instructions.",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Review this implementation."}]},
			{"type":"function_call","name":"read_file","arguments":"{\"path\":\"main.go\"}"},
			{"type":"function_call_output","output":"package main"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"I will inspect the file."}]}
		],
		"tools":[{"type":"function","name":"read_file","description":"Reads a file.","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],
		"text":{"format":{"name":"result","schema":{"type":"object"}}}
	}`)

	semanticCount, err := countXAIInputTokens(enc, semanticBody)
	if err != nil {
		t.Fatalf("countXAIInputTokens() error = %v", err)
	}
	structuralCount, err := countXAIInputTokens(enc, structuralBody)
	if err != nil {
		t.Fatalf("countXAIInputTokens() error = %v", err)
	}
	if structuralCount != semanticCount {
		t.Fatalf("structural count = %d, want %d", structuralCount, semanticCount)
	}

	for name, tc := range map[string]struct {
		body     []byte
		expected string
	}{
		"instructions": {
			body:     []byte(`{"instructions":"unique instruction text"}`),
			expected: "unique instruction text",
		},
		"string input": {
			body:     []byte(`{"input":"unique input text"}`),
			expected: "unique input text",
		},
		"message content": {
			body:     []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"unique message text"}]}]}`),
			expected: "unique message text",
		},
		"refusal": {
			body:     []byte(`{"input":[{"type":"message","content":[{"type":"refusal","refusal":"unique refusal text"}]}]}`),
			expected: "unique refusal text",
		},
		"input image": {
			body:     []byte(`{"input":[{"type":"message","content":[{"type":"input_image","image_url":"https://example.com/unique.png"}]}]}`),
			expected: "https://example.com/unique.png",
		},
		"input file": {
			body:     []byte(`{"input":[{"type":"message","content":[{"type":"input_file","file_data":"unique file data","filename":"unique.txt"}]}]}`),
			expected: "unique file data\nunique.txt",
		},
		"input audio": {
			body:     []byte(`{"input":[{"type":"message","content":[{"type":"input_audio","data":"unique audio data"}]}]}`),
			expected: "unique audio data",
		},
		"function call": {
			body:     []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"unique_function","arguments":"{\"value\":\"unique argument\"}"}]}`),
			expected: "unique_function\n{\"value\":\"unique argument\"}",
		},
		"function call output": {
			body:     []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"unique tool output"}]}`),
			expected: "unique tool output",
		},
		"reasoning summary": {
			body:     []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"unique summary text"}]}]}`),
			expected: "unique summary text",
		},
		"function tool": {
			body:     []byte(`{"tools":[{"type":"function","name":"unique_tool","description":"unique tool description","parameters":{"type":"object","properties":{"value":{"type":"string"}}}}]}`),
			expected: "unique_tool\nunique tool description\n{\"type\":\"object\",\"properties\":{\"value\":{\"type\":\"string\"}}}",
		},
		"structured text format": {
			body:     []byte(`{"text":{"format":{"name":"unique_format","schema":{"type":"object","properties":{"value":{"type":"string"}}}}}}`),
			expected: "unique_format\n{\"type\":\"object\",\"properties\":{\"value\":{\"type\":\"string\"}}}",
		},
	} {
		t.Run(name, func(t *testing.T) {
			count, errCount := countXAIInputTokens(enc, tc.body)
			if errCount != nil {
				t.Fatalf("countXAIInputTokens() error = %v", errCount)
			}
			expected, errExpected := enc.Count(tc.expected)
			if errExpected != nil {
				t.Fatalf("encoder.Count() error = %v", errExpected)
			}
			if count != int64(expected) {
				t.Fatalf("countXAIInputTokens() = %d, want %d", count, expected)
			}
		})
	}
}

func TestXAIExecutorExecuteShapesResponsesRequest(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotGrokConvID string
	var gotOriginator string
	var gotAccountID string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotGrokConvID = r.Header.Get("x-grok-conv-id")
		gotOriginator = r.Header.Get("Originator")
		gotAccountID = r.Header.Get("Chatgpt-Account-Id")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"access_token": "xai-token",
			"email":        "user@example.com",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"test"}],"content":null,"encrypted_content":null},{"type":"reasoning","summary":[{"type":"summary_text","text":"second"}]},{"role":"user","content":"hello"}],"include":["reasoning.encrypted_content"],"reasoning":{"effort":"high"},"tools":[{"type":"tool_search"},{"type":"image_generation"},{"type":"custom","name":"apply_patch"},{"type":"custom","name":"custom_lookup"},{"type":"function","name":"lookup"},{"type":"web_search","external_web_access":true,"search_content_types":["text","image"]},{"type":"namespace","name":"codex_app","description":"Tools in the codex_app namespace.","tools":[{"type":"function","name":"automation_update"},{"type":"custom","name":"namespace_custom"},{"type":"tool_search"}]}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"automation_update","namespace":"codex_app"},{"type":"function","name":"lookup"},{"type":"web_search"}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "conv-xai-1",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q, want Bearer xai-token", gotAuth)
	}
	if gotGrokConvID != "conv-xai-1" {
		t.Fatalf("x-grok-conv-id = %q, want conv-xai-1", gotGrokConvID)
	}
	if gotOriginator != "" {
		t.Fatalf("Originator = %q, want empty", gotOriginator)
	}
	if gotAccountID != "" {
		t.Fatalf("Chatgpt-Account-Id = %q, want empty", gotAccountID)
	}
	if gjson.GetBytes(gotBody, "prompt_cache_key").String() != "conv-xai-1" {
		t.Fatalf("prompt_cache_key missing from body: %s", string(gotBody))
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream = false, want true; body=%s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "reasoning.effort").String() != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", gjson.GetBytes(gotBody, "reasoning.effort").String(), string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.0.content").Exists() {
		t.Fatalf("input.0.content exists, want removed; body=%s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.0.encrypted_content").Exists() {
		t.Fatalf("input.0.encrypted_content exists, want removed; body=%s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.summary.0.text").String(); got != "test" {
		t.Fatalf("input.0.summary.0.text = %q, want test; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.summary.1.text").String(); got != "second" {
		t.Fatalf("input.0.summary.1.text = %q, want second; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.2").Exists() {
		t.Fatalf("input.2 exists, want consecutive reasoning item merged; body=%s", string(gotBody))
	}
	tools := gjson.GetBytes(gotBody, "tools").Array()
	if len(tools) != 6 {
		t.Fatalf("tools length = %d, want 6; body=%s", len(tools), string(gotBody))
	}
	foundAutomationUpdate := false
	foundNamespaceCustom := false
	foundXSearch := false
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		if toolType == "image_generation" {
			t.Fatalf("tools.%d.type = image_generation, want removed; body=%s", i, string(gotBody))
		}
		if toolType != "function" && toolType != "web_search" && toolType != "x_search" {
			t.Fatalf("tools.%d.type = %q, want function, web_search, or x_search; body=%s", i, toolType, string(gotBody))
		}
		if toolType == "x_search" {
			foundXSearch = true
		}
		if toolType == "function" && !tool.Get("parameters").Exists() {
			t.Fatalf("tools.%d.parameters missing for xAI function tool; body=%s", i, string(gotBody))
		}
		if got := tool.Get("name").String(); got == "apply_patch" {
			t.Fatalf("tools.%d.name = apply_patch, want removed; body=%s", i, string(gotBody))
		}
		switch tool.Get("name").String() {
		case "codex_app__automation_update":
			foundAutomationUpdate = true
		case "codex_app__namespace_custom":
			foundNamespaceCustom = true
		}
		if toolType == "web_search" {
			if tool.Get("external_web_access").Exists() {
				t.Fatalf("tools.%d.external_web_access exists, want removed; body=%s", i, string(gotBody))
			}
			if got := tool.Get("search_content_types.1").String(); got != "image" {
				t.Fatalf("tools.%d.search_content_types missing image entry; body=%s", i, string(gotBody))
			}
		}
	}
	if !foundAutomationUpdate {
		t.Fatalf("namespace function tool was not moved to top-level tools; body=%s", string(gotBody))
	}
	if !foundNamespaceCustom {
		t.Fatalf("namespace custom tool was not moved to top-level tools; body=%s", string(gotBody))
	}
	if !foundXSearch {
		t.Fatalf("native x_search tool was not injected; body=%s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tool_choice.tools.0.name").String(); got != "codex_app__automation_update" {
		t.Fatalf("tool_choice.tools.0.name = %q, want codex_app__automation_update; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "tool_choice.tools.0.namespace").Exists() {
		t.Fatalf("tool_choice.tools.0.namespace should be removed for xAI upstream: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tool_choice.tools.1.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.tools.1.name = %q, want lookup; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tool_choice.tools.2.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.2.type = %q, want web_search; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tool_choice.tools.3.type").String(); got != "x_search" {
		t.Fatalf("tool_choice.tools.3.type = %q, want x_search; body=%s", got, string(gotBody))
	}
	xSearchAllowedCount := 0
	for _, tool := range gjson.GetBytes(gotBody, "tool_choice.tools").Array() {
		if tool.Get("type").String() == "x_search" {
			xSearchAllowedCount++
		}
	}
	if xSearchAllowedCount != 1 {
		t.Fatalf("allowed_tools x_search count = %d, want 1; body=%s", xSearchAllowedCount, string(gotBody))
	}
	foundEncryptedReasoningInclude := false
	for _, include := range gjson.GetBytes(gotBody, "include").Array() {
		if include.String() == "reasoning.encrypted_content" {
			foundEncryptedReasoningInclude = true
			break
		}
	}
	if !foundEncryptedReasoningInclude {
		t.Fatalf("xai request must preserve reasoning.encrypted_content include: %s", string(gotBody))
	}
}

func TestXAIExecutorPrepareResponsesRequestRewritesCodexAgentMessage(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}})
	payload := []byte(`{
		"model":"grok-4.5",
		"input":[{
			"type":"agent_message",
			"id":"amsg_019f92c3-6d77-7880-a6e4-f920867dc6a0",
			"author":"/root",
			"recipient":"/root/arithmetic_question",
			"content":[
				{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: /root/arithmetic_question\nSender: /root\nPayload:\n"},
				{"type":"encrypted_content","encrypted_content":"请出一道四则运算题。只回复题目本身，不要解答；使用中文。"}
			],
			"internal_chat_message_metadata_passthrough":{"turn_id":"019f92c3-6772-7213-8aac-8bd154d528f1"}
		}]
	}`)
	prepared, errPrepare := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      http.Header{"User-Agent": []string{"Codex Desktop/0.146.0-alpha.3.1"}},
	}, true)
	if errPrepare != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
	}

	message := gjson.GetBytes(prepared.body, "input.0")
	if message.Get("type").String() != "message" || message.Get("role").String() != "user" {
		t.Fatalf("agent message was not rewritten: %s", prepared.body)
	}
	if message.Get("content.1.type").String() != "input_text" {
		t.Fatalf("content[1].type = %q, want input_text; body=%s", message.Get("content.1.type").String(), prepared.body)
	}
	if text := message.Get("content.1.text").String(); text != "请出一道四则运算题。只回复题目本身，不要解答；使用中文。" {
		t.Fatalf("content[1].text = %q; body=%s", text, prepared.body)
	}
	if message.Get("content.1.encrypted_content").Exists() {
		t.Fatalf("encrypted_content was preserved: %s", prepared.body)
	}
	if message.Get("id").String() != "amsg_019f92c3-6d77-7880-a6e4-f920867dc6a0" || message.Get("author").String() != "/root" || message.Get("recipient").String() != "/root/arithmetic_question" {
		t.Fatalf("agent message identity fields changed: %s", prepared.body)
	}
	if turnID := message.Get("internal_chat_message_metadata_passthrough.turn_id").String(); turnID != "019f92c3-6772-7213-8aac-8bd154d528f1" {
		t.Fatalf("turn_id = %q; body=%s", turnID, prepared.body)
	}
}

func TestXAIExecutorExecuteRestoresAdditionalToolsNamespaceCalls(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"name\":\"mcp__exa__web_search_exa\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.3",
		Payload: []byte(`{
			"model":"grok-4.3",
			"input":[
				{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"web_search_exa","parameters":{"type":"object"}}]}]},
				{"role":"user","content":"use Exa"}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, item := range gjson.GetBytes(gotBody, "input").Array() {
		if got := item.Get("type").String(); got == "additional_tools" {
			t.Fatalf("upstream input contains unsupported additional_tools item: %s", gotBody)
		}
	}
	if got := gjson.GetBytes(gotBody, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user; body=%s", got, gotBody)
	}
	tool := gjson.GetBytes(gotBody, "tools.0")
	if got := tool.Get("name").String(); got != "mcp__exa__web_search_exa" {
		t.Fatalf("upstream tool name = %q, want qualified name; body=%s", got, gotBody)
	}
	if got := tool.Get("type").String(); got != "function" {
		t.Fatalf("upstream tool type = %q, want function; body=%s", got, gotBody)
	}
	if tool.Get("tools").Exists() {
		t.Fatalf("upstream tool should not contain namespace children: %s", gotBody)
	}
	output := gjson.GetBytes(resp.Payload, "output.0")
	if got := output.Get("name").String(); got != "web_search_exa" {
		t.Fatalf("response output name = %q, want child name; payload=%s", got, resp.Payload)
	}
	if got := output.Get("namespace").String(); got != "mcp__exa" {
		t.Fatalf("response output namespace = %q, want mcp__exa; payload=%s", got, resp.Payload)
	}
}

func TestXAIExecutorExecuteNormalizesCustomToolCallHistory(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		for _, item := range gjson.GetBytes(gotBody, "input").Array() {
			if strings.HasPrefix(item.Get("type").String(), "custom_tool_call") {
				http.Error(w, `{"error":"data did not match any variant of untagged enum ModelInput"}`, http.StatusUnprocessableEntity)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	payload := []byte(`{
		"model":"grok-4.5",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]},
			{"type":"custom_tool_call","name":"missing_call_id","input":"invalid"},
			{"type":"custom_tool_call_output","output":"missing call id"},
			{"type":"custom_tool_call","status":"completed","call_id":"xs_call-1","name":"x_semantic_search","input":"{\"query\":\"US stocks\",\"limit\":\"10\"}","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}},
			{"type":"custom_tool_call_output","call_id":"xs_call-1","output":"unsupported custom tool call: x_semantic_search","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}},
			{"type":"custom_tool_call","call_id":"call-2","name":"apply_patch","input":"*** Begin Patch"},
			{"type":"custom_tool_call_output","call_id":"call-2","output":[{"type":"input_text","text":"done"}]}
		],
		"tools":[{"type":"x_search"}],
		"tool_choice":"auto"
	}`)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	input := gjson.GetBytes(gotBody, "input").Array()
	if len(input) != 5 {
		t.Fatalf("input length = %d, want 5; body=%s", len(input), gotBody)
	}
	if got := input[1].Get("type").String(); got != "function_call" {
		t.Fatalf("input.1.type = %q, want function_call; body=%s", got, gotBody)
	}
	if got := gjson.Get(input[1].Get("arguments").String(), "query").String(); got != "US stocks" {
		t.Fatalf("input.1 arguments query = %q, want US stocks; body=%s", got, gotBody)
	}
	if input[1].Get("input").Exists() || input[1].Get("internal_chat_message_metadata_passthrough").Exists() {
		t.Fatalf("input.1 contains unsupported custom fields: %s", input[1].Raw)
	}
	if got := input[2].Get("type").String(); got != "function_call_output" {
		t.Fatalf("input.2.type = %q, want function_call_output; body=%s", got, gotBody)
	}
	if got := input[2].Get("output").String(); got != "unsupported custom tool call: x_semantic_search" {
		t.Fatalf("input.2.output = %q; body=%s", got, gotBody)
	}
	if got := gjson.Get(input[3].Get("arguments").String(), "input").String(); got != "*** Begin Patch" {
		t.Fatalf("input.3 freeform arguments = %q, want patch input; body=%s", got, gotBody)
	}
	if got := input[4].Get("output").String(); got != `[{"type":"input_text","text":"done"}]` {
		t.Fatalf("input.4 output = %q, want flattened JSON string; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "x_search" {
		t.Fatalf("tools.0.type = %q, want x_search; body=%s", got, gotBody)
	}
}

func TestXAIExecutorExecuteStreamFiltersInternalXSearchCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		names := []string{"x_user_search", "x_semantic_search", "x_keyword_search", "x_thread_fetch"}
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		for i, name := range names {
			itemID := fmt.Sprintf("ctc_%d", i)
			callID := fmt.Sprintf("xs_call-%d", i)
			_, _ = fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":{\"id\":%q,\"type\":\"custom_tool_call\",\"call_id\":%q,\"name\":%q,\"input\":\"\",\"status\":\"in_progress\"}}\n\n", i, itemID, callID, name)
			_, _ = fmt.Fprintf(w, "event: response.custom_tool_call_input.done\ndata: {\"type\":\"response.custom_tool_call_input.done\",\"output_index\":%d,\"item_id\":%q,\"input\":\"{}\"}\n\n", i, itemID)
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":{\"id\":%q,\"type\":\"custom_tool_call\",\"call_id\":%q,\"name\":%q,\"input\":\"{}\",\"status\":\"completed\"}}\n\n", i, itemID, callID, name)
			item := []byte(`{"id":"","type":"custom_tool_call","call_id":"","name":"","input":"{}","status":"completed"}`)
			item, _ = sjson.SetBytes(item, "id", itemID)
			item, _ = sjson.SetBytes(item, "call_id", callID)
			item, _ = sjson.SetBytes(item, "name", name)
			completed, _ = sjson.SetRawBytes(completed, "response.output.-1", item)
		}

		messageIndex := len(names)
		_, _ = fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"status\":\"in_progress\"}}\n\n", messageIndex)
		_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":%d,\"item_id\":\"msg_1\",\"content_index\":0,\"delta\":\"answer\"}\n\n", messageIndex)
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}],\"status\":\"completed\"}}\n\n", messageIndex)
		message := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}],"status":"completed"}`)
		completed, _ = sjson.SetRawBytes(completed, "response.output.-1", message)
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"search X","tools":[{"type":"x_search"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var stream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
		stream.WriteByte('\n')
	}
	streamText := stream.String()
	for _, name := range []string{"x_user_search", "x_semantic_search", "x_keyword_search", "x_thread_fetch"} {
		if strings.Contains(streamText, name) {
			t.Fatalf("internal x_search call %q leaked downstream: %s", name, streamText)
		}
	}
	if strings.Contains(streamText, "response.custom_tool_call_input") {
		t.Fatalf("custom tool input event leaked downstream: %s", streamText)
	}

	var completed gjson.Result
	messageIndexChecks := 0
	for _, line := range strings.Split(streamText, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !gjson.Valid(line) {
			continue
		}
		event := gjson.Parse(line)
		if event.Get("item.id").String() == "msg_1" || event.Get("item_id").String() == "msg_1" {
			messageIndexChecks++
			if got := event.Get("output_index").Int(); got != 0 {
				t.Fatalf("message output_index = %d, want 0; event=%s", got, line)
			}
		}
		if event.Get("type").String() == "response.completed" {
			completed = event
		}
	}
	if messageIndexChecks == 0 {
		t.Fatal("no message events found")
	}
	if got := completed.Get("response.output.#").Int(); got != 1 {
		t.Fatalf("completed output length = %d, want 1; completed=%s", got, completed.Raw)
	}
	if got := completed.Get("response.output.0.type").String(); got != "message" {
		t.Fatalf("completed output type = %q, want message; completed=%s", got, completed.Raw)
	}
}

func TestXAIExecutorExecuteFiltersInternalXSearchCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_user_search\",\"input\":\"{}\",\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}],\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_user_search\",\"input\":\"{}\"},{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"search X","tools":[{"type":"x_search"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(string(resp.Payload), "x_user_search") || strings.Contains(string(resp.Payload), "custom_tool_call") {
		t.Fatalf("internal X search call leaked into response: %s", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.#").Int(); got != 1 {
		t.Fatalf("response output length = %d, want 1; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "answer" {
		t.Fatalf("response text = %q, want answer; payload=%s", got, resp.Payload)
	}
}

func TestEnsureXAINativeXSearchTool(t *testing.T) {
	t.Parallel()

	// Missing tools array: inject a top-level x_search tool.
	out := ensureXAINativeXSearchTool([]byte(`{"model":"grok-4.5","input":"hi"}`))
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1; body=%s", len(tools), out)
	}
	if got := tools[0].Get("type").String(); got != "x_search" {
		t.Fatalf("tools.0.type = %q, want x_search; body=%s", got, out)
	}

	// Existing tools without x_search: append once.
	out = ensureXAINativeXSearchTool([]byte(`{"tools":[{"type":"web_search"},{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`))
	tools = gjson.GetBytes(out, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("tools length = %d, want 3; body=%s", len(tools), out)
	}
	if got := tools[2].Get("type").String(); got != "x_search" {
		t.Fatalf("tools.2.type = %q, want x_search; body=%s", got, out)
	}

	// Already present: leave body unchanged (no duplicate).
	in := []byte(`{"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"x_search"}]}`)
	out = ensureXAINativeXSearchTool(in)
	tools = gjson.GetBytes(out, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2; body=%s", len(tools), out)
	}
	xSearchCount := 0
	for _, tool := range tools {
		if tool.Get("type").String() == "x_search" {
			xSearchCount++
		}
	}
	if xSearchCount != 1 {
		t.Fatalf("x_search count = %d, want 1; body=%s", xSearchCount, out)
	}

	// allowed_tools without x_search: append once so Grok may select it.
	out = ensureXAINativeXSearchTool([]byte(`{
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"lookup"}]}
	}`))
	if got := gjson.GetBytes(out, "tools.1.type").String(); got != "x_search" {
		t.Fatalf("tools.1.type = %q, want x_search; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tool_choice.tools.1.type").String(); got != "x_search" {
		t.Fatalf("tool_choice.tools.1.type = %q, want x_search; body=%s", got, out)
	}

	// allowed_tools already lists x_search: do not duplicate.
	out = ensureXAINativeXSearchTool([]byte(`{
		"tools":[{"type":"web_search"},{"type":"x_search"}],
		"tool_choice":{"type":"allowed_tools","tools":[{"type":"web_search"},{"type":"x_search"}]}
	}`))
	tools = gjson.GetBytes(out, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2; body=%s", len(tools), out)
	}
	allowed := gjson.GetBytes(out, "tool_choice.tools").Array()
	if len(allowed) != 2 {
		t.Fatalf("tool_choice.tools length = %d, want 2; body=%s", len(allowed), out)
	}
	xSearchAllowed := 0
	for _, tool := range allowed {
		if tool.Get("type").String() == "x_search" {
			xSearchAllowed++
		}
	}
	if xSearchAllowed != 1 {
		t.Fatalf("allowed_tools x_search count = %d, want 1; body=%s", xSearchAllowed, out)
	}
}

func TestPruneXAIOrphanedToolChoice(t *testing.T) {
	t.Parallel()

	// Forced choice for a removed tool is dropped.
	out := pruneXAIOrphanedToolChoice([]byte(`{
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"tool_choice":{"type":"image_generation"}
	}`))
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("orphaned forced tool_choice should be removed: %s", out)
	}

	// allowed_tools keeps only still-available entries.
	out = pruneXAIOrphanedToolChoice([]byte(`{
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"web_search"}],
		"tool_choice":{"type":"allowed_tools","tools":[
			{"type":"function","name":"lookup"},
			{"type":"image_generation"},
			{"type":"web_search"}
		]}
	}`))
	allowed := gjson.GetBytes(out, "tool_choice.tools").Array()
	if len(allowed) != 2 {
		t.Fatalf("allowed_tools length = %d, want 2; body=%s", len(allowed), out)
	}
	if got := allowed[0].Get("name").String(); got != "lookup" {
		t.Fatalf("allowed_tools.0.name = %q, want lookup; body=%s", got, out)
	}
	if got := allowed[1].Get("type").String(); got != "web_search" {
		t.Fatalf("allowed_tools.1.type = %q, want web_search; body=%s", got, out)
	}

	// When every allowed entry is orphaned, drop tool_choice entirely.
	out = pruneXAIOrphanedToolChoice([]byte(`{
		"tools":[],
		"tool_choice":{"type":"allowed_tools","tools":[{"type":"image_generation"}]}
	}`))
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("fully orphaned allowed_tools should be removed: %s", out)
	}

	// String choices are not tool references.
	in := []byte(`{"tools":[{"type":"web_search"}],"tool_choice":"auto"}`)
	if got := pruneXAIOrphanedToolChoice(in); !bytes.Equal(got, in) {
		t.Fatalf("string tool_choice changed: got=%s want=%s", got, in)
	}
}

func TestXAIExecutorPrepareDropsOrphanedToolChoiceBeforeXSearchInject(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",
		// image_generation is stripped by normalizeXAITools; without pruning, the
		// forced choice would survive next to the injected x_search tool.
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"draw something",
			"tools":[{"type":"image_generation"}],
			"tool_choice":{"type":"image_generation"}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}

	tools := gjson.GetBytes(prepared.body, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1; body=%s", len(tools), prepared.body)
	}
	if got := tools[0].Get("type").String(); got != "x_search" {
		t.Fatalf("tools.0.type = %q, want x_search; body=%s", got, prepared.body)
	}
	if gjson.GetBytes(prepared.body, "tool_choice").Exists() {
		t.Fatalf("orphaned image_generation tool_choice must not reach upstream: %s", prepared.body)
	}
}

func TestXAIExecutorPrepareResponsesRequestAddsObjectTypeToRootUnionBranches(t *testing.T) {
	t.Parallel()

	cropParameters := `{
		"type":"object",
		"additionalProperties":false,
		"required":["imagePath","point"],
		"oneOf":[
			{"required":["radius"],"not":{"required":["size"]}},
			{"required":["size"],"not":{"required":["radius"]}}
		],
		"properties":{
			"imagePath":{"type":"string"},
			"point":{"type":"array"},
			"radius":{"type":"number"},
			"size":{"type":"object"}
		}
	}`
	tests := []struct {
		name         string
		sourceFormat sdktranslator.Format
		payload      []byte
	}{
		{
			name:         "OpenAI Responses",
			sourceFormat: sdktranslator.FormatOpenAIResponse,
			payload: []byte(`{
				"model":"grok-4.5",
				"input":"crop a region",
				"tools":[{
					"type":"function",
					"name":"crop_around_point",
					"parameters":` + cropParameters + `
				}]
			}`),
		},
		{
			name:         "OpenAI Chat Completions",
			sourceFormat: sdktranslator.FormatOpenAI,
			payload: []byte(`{
				"model":"grok-4.5",
				"messages":[{"role":"user","content":"crop a region"}],
				"tools":[{
					"type":"function",
					"function":{
						"name":"crop_around_point",
						"parameters":` + cropParameters + `
					}
				}]
			}`),
		},
	}

	exec := NewXAIExecutor(&config.Config{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
				Model:   "grok-4.5",
				Payload: tt.payload,
			}, cliproxyexecutor.Options{
				SourceFormat: tt.sourceFormat,
				Stream:       true,
			}, true)
			if err != nil {
				t.Fatalf("prepareResponsesRequest() error = %v", err)
			}

			var cropTool gjson.Result
			for _, tool := range gjson.GetBytes(prepared.body, "tools").Array() {
				if tool.Get("type").String() == xaiFunctionToolType && tool.Get("name").String() == "crop_around_point" {
					cropTool = tool
					break
				}
			}
			if !cropTool.Exists() {
				t.Fatalf("crop_around_point missing from upstream tools: %s", prepared.body)
			}

			parameters := cropTool.Get("parameters")
			branches := parameters.Get("oneOf").Array()
			if len(branches) != 2 {
				t.Fatalf("oneOf branch count = %d, want 2; parameters=%s", len(branches), parameters.Raw)
			}
			for index, branch := range branches {
				if got := branch.Get("type").String(); got != "object" {
					t.Fatalf("oneOf.%d.type = %q, want object; parameters=%s", index, got, parameters.Raw)
				}
			}
			for _, propertyName := range []string{"imagePath", "point", "radius", "size"} {
				if !parameters.Get("properties." + propertyName).Exists() {
					t.Fatalf("properties.%s missing: %s", propertyName, parameters.Raw)
				}
			}
			if parameters.Get("additionalProperties").Type != gjson.False {
				t.Fatalf("additionalProperties changed: %s", parameters.Raw)
			}
			if !branches[0].Get("not.required").Exists() || !branches[1].Get("not.required").Exists() {
				t.Fatalf("oneOf constraints changed: %s", parameters.Raw)
			}
		})
	}
}

func TestXAIExecutorPrepareAllowedToolsSyncsInjectedXSearch(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",
		// Only image_generation remains after client filtering of tool_search-like
		// tools is not relevant here: normalizeXAITools drops image_generation and
		// we inject x_search, while allowed_tools must be rewritten so Grok can
		// choose the injected tool and not a deleted one.
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"search X",
			"tools":[{"type":"image_generation"},{"type":"function","name":"lookup","parameters":{"type":"object"}}],
			"tool_choice":{"type":"allowed_tools","tools":[
				{"type":"image_generation"},
				{"type":"function","name":"lookup"}
			]}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}

	tools := gjson.GetBytes(prepared.body, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2; body=%s", len(tools), prepared.body)
	}
	foundLookup := false
	foundXSearch := false
	for _, tool := range tools {
		switch tool.Get("type").String() {
		case "function":
			if tool.Get("name").String() == "lookup" {
				foundLookup = true
			}
		case "x_search":
			foundXSearch = true
		case "image_generation":
			t.Fatalf("image_generation must be removed; body=%s", prepared.body)
		}
	}
	if !foundLookup || !foundXSearch {
		t.Fatalf("expected lookup + x_search tools; body=%s", prepared.body)
	}

	allowed := gjson.GetBytes(prepared.body, "tool_choice.tools").Array()
	if len(allowed) != 2 {
		t.Fatalf("tool_choice.tools length = %d, want 2; body=%s", len(allowed), prepared.body)
	}
	if got := allowed[0].Get("name").String(); got != "lookup" {
		t.Fatalf("tool_choice.tools.0.name = %q, want lookup; body=%s", got, prepared.body)
	}
	if got := allowed[1].Get("type").String(); got != "x_search" {
		t.Fatalf("tool_choice.tools.1.type = %q, want x_search; body=%s", got, prepared.body)
	}
	for _, tool := range allowed {
		if tool.Get("type").String() == "image_generation" {
			t.Fatalf("orphaned image_generation choice leaked: %s", prepared.body)
		}
	}
}

func TestXAIInternalXSearchResponseFilterRequiresNativeTool(t *testing.T) {
	if xaiRequestHasNativeXSearch([]byte(`{"tools":[{"type":"web_search"}]}`)) {
		t.Fatal("web_search must not enable internal X search filtering")
	}
	if !xaiRequestHasNativeXSearch([]byte(`{"tools":[{"type":"x_search"}]}`)) {
		t.Fatal("x_search should enable internal X search filtering")
	}

	event := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","name":"x_keyword_search"}}`)
	if got := newXAIInternalXSearchResponseFilter(false, nil).apply(event); !bytes.Equal(got, event) {
		t.Fatalf("disabled filter changed event: %s", got)
	}
	if got := newXAIInternalXSearchResponseFilter(true, nil).apply(event); got != nil {
		t.Fatalf("enabled filter retained internal call: %s", got)
	}
}

func TestXAIIsInternalXSearchCallPreservesClientDeclaredTools(t *testing.T) {
	clientTools := collectXAIClientDeclaredToolKeys([]byte(`{
		"tools":[
			{"type":"x_search"},
			{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}},
			{"type":"custom","name":"x_keyword_search"},
			{"type":"namespace","name":"acme","tools":[
				{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}},
				{"type":"custom","name":"x_keyword_search"}
			]}
		]
	}`))
	// Client custom tools are normalized to function before upstream send, so both
	// plain function and plain custom declarations share the effective function key.
	if _, ok := clientTools[xaiClientToolKey{namespace: "", name: "x_keyword_search", toolType: xaiFunctionToolType}]; !ok {
		t.Fatalf("plain client function/custom tool missing effective function key: %#v", clientTools)
	}
	if _, ok := clientTools[xaiClientToolKey{namespace: "", name: "x_keyword_search", toolType: xaiCustomToolType}]; ok {
		t.Fatalf("client custom tool must not be keyed as custom after normalization: %#v", clientTools)
	}
	if _, ok := clientTools[xaiClientToolKey{namespace: "acme", name: "x_keyword_search", toolType: xaiFunctionToolType}]; !ok {
		t.Fatalf("namespaced client tool missing from declared set: %#v", clientTools)
	}
	if _, ok := clientTools[xaiClientToolKey{namespace: "acme", name: "x_keyword_search", toolType: xaiCustomToolType}]; ok {
		t.Fatalf("namespaced client custom tool must not be keyed as custom after normalization: %#v", clientTools)
	}

	// Names not declared by the client remain internal X Search traces.
	internalCustom := gjson.Parse(`{"type":"custom_tool_call","name":"x_user_search"}`)
	if !xaiIsInternalXSearchCall(internalCustom, clientTools) {
		t.Fatal("undeclared internal custom_tool_call should be filtered")
	}
	internalFunction := gjson.Parse(`{"type":"function_call","name":"x_semantic_search"}`)
	if !xaiIsInternalXSearchCall(internalFunction, clientTools) {
		t.Fatal("undeclared internal function_call should be filtered")
	}

	// Same short name as a client-declared function/custom tool is preserved only for function_call
	// (the response shape after custom → function normalization).
	plainClient := gjson.Parse(`{"type":"function_call","name":"x_keyword_search","call_id":"call_plain"}`)
	if xaiIsInternalXSearchCall(plainClient, clientTools) {
		t.Fatal("client-declared plain x_keyword_search function_call must be preserved")
	}
	// Genuine internal custom_tool_call with the same short name must still be filtered,
	// even when the client also declared an ordinary function/custom tool of that name.
	internalSameName := gjson.Parse(`{"type":"custom_tool_call","call_id":"xs_call-1","name":"x_keyword_search"}`)
	if !xaiIsInternalXSearchCall(internalSameName, clientTools) {
		t.Fatal("genuine internal custom_tool_call x_keyword_search must be filtered despite client function declaration")
	}
	// Declaring only a function tool must not exempt a same-name custom_tool_call without xs_call either.
	functionOnlyTools := collectXAIClientDeclaredToolKeys([]byte(`{
		"tools":[{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}}]
	}`))
	plainInternalCustom := gjson.Parse(`{"type":"custom_tool_call","name":"x_keyword_search","call_id":"call_other"}`)
	if !xaiIsInternalXSearchCall(plainInternalCustom, functionOnlyTools) {
		t.Fatal("custom_tool_call must not be exempted by a function declaration of the same name")
	}
	// Client-declared custom tools are sent as function, so only function_call is the
	// legitimate client response shape; bare custom_tool_call remains internal.
	customOnlyTools := collectXAIClientDeclaredToolKeys([]byte(`{
		"tools":[{"type":"custom","name":"x_keyword_search"}]
	}`))
	if _, ok := customOnlyTools[xaiClientToolKey{namespace: "", name: "x_keyword_search", toolType: xaiFunctionToolType}]; !ok {
		t.Fatalf("client custom tool must be keyed as effective function: %#v", customOnlyTools)
	}
	clientCustomAsFunction := gjson.Parse(`{"type":"function_call","name":"x_keyword_search","call_id":"call_custom_fn"}`)
	if xaiIsInternalXSearchCall(clientCustomAsFunction, customOnlyTools) {
		t.Fatal("normalized client custom tool function_call must be preserved")
	}
	if !xaiIsInternalXSearchCall(plainInternalCustom, customOnlyTools) {
		t.Fatal("custom_tool_call must not be exempted by a client custom declaration normalized to function")
	}
	// Even with a client custom declaration, xs_call* remains an internal X Search trace.
	if !xaiIsInternalXSearchCall(internalSameName, customOnlyTools) {
		t.Fatal("xs_call internal custom_tool_call must stay filtered when client declares custom same-name tool")
	}
	// After restoreXAINamespaceToolCalls, namespaced tools regain namespace.
	namespacedClient := gjson.Parse(`{"type":"function_call","name":"x_keyword_search","namespace":"acme"}`)
	if xaiIsInternalXSearchCall(namespacedClient, clientTools) {
		t.Fatal("client-declared namespaced x_keyword_search must be preserved")
	}
	// Safety net even without an explicit declared-tool entry.
	if xaiIsInternalXSearchCall(namespacedClient, nil) {
		t.Fatal("namespaced tool call must never be treated as internal X Search")
	}
}

func TestXAIInternalXSearchResponseFilterPreservesClientToolsInCompletedOutput(t *testing.T) {
	clientTools := map[xaiClientToolKey]struct{}{
		{namespace: "", name: "x_keyword_search", toolType: xaiFunctionToolType}:     {},
		{namespace: "acme", name: "x_keyword_search", toolType: xaiFunctionToolType}: {},
	}
	filter := newXAIInternalXSearchResponseFilter(true, clientTools)
	event := []byte(`{
		"type":"response.completed",
		"response":{
			"output":[
				{"id":"ctc_1","type":"custom_tool_call","call_id":"xs_call-1","name":"x_keyword_search","input":"{}"},
				{"id":"fc_plain","type":"function_call","call_id":"call_plain","name":"x_keyword_search","arguments":"{}"},
				{"id":"fc_ns","type":"function_call","call_id":"call_ns","name":"x_keyword_search","namespace":"acme","arguments":"{}"},
				{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}
			]
		}
	}`)
	got := filter.apply(event)
	if got == nil {
		t.Fatal("filter dropped entire completed event")
	}
	if gjson.GetBytes(got, "response.output.#").Int() != 3 {
		t.Fatalf("completed output length = %d, want 3; event=%s", gjson.GetBytes(got, "response.output.#").Int(), got)
	}
	if gjson.GetBytes(got, `response.output.#(type=="custom_tool_call")`).Exists() {
		t.Fatalf("internal custom_tool_call x_keyword_search leaked: %s", got)
	}
	if gotName := gjson.GetBytes(got, "response.output.0.name").String(); gotName != "x_keyword_search" {
		t.Fatalf("output.0.name = %q, want x_keyword_search; event=%s", gotName, got)
	}
	if gotType := gjson.GetBytes(got, "response.output.0.type").String(); gotType != "function_call" {
		t.Fatalf("output.0.type = %q, want function_call; event=%s", gotType, got)
	}
	if gotNS := gjson.GetBytes(got, "response.output.1.namespace").String(); gotNS != "acme" {
		t.Fatalf("output.1.namespace = %q, want acme; event=%s", gotNS, got)
	}
}

func TestXAIExecutorExecutePreservesClientSameNameToolsWithXSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Collision case: internal X Search and client tools both named x_keyword_search.
		// Upstream still uses qualified names; restore happens before filtering.
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_keyword_search\",\"input\":\"{}\",\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_ns\",\"type\":\"function_call\",\"call_id\":\"call_ns\",\"name\":\"acme__x_keyword_search\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"id\":\"fc_plain\",\"type\":\"function_call\",\"call_id\":\"call_plain\",\"name\":\"x_keyword_search\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":3,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}],\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_keyword_search\",\"input\":\"{}\"},{\"id\":\"fc_ns\",\"type\":\"function_call\",\"call_id\":\"call_ns\",\"name\":\"acme__x_keyword_search\",\"arguments\":\"{}\"},{\"id\":\"fc_plain\",\"type\":\"function_call\",\"call_id\":\"call_plain\",\"name\":\"x_keyword_search\",\"arguments\":\"{}\"},{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"search X",
			"tools":[
				{"type":"x_search"},
				{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}},
				{"type":"namespace","name":"acme","tools":[
					{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}}
				]}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	payload := string(resp.Payload)
	if strings.Contains(payload, "xs_call") {
		t.Fatalf("internal X search call_id leaked into response: %s", payload)
	}
	if strings.Contains(payload, "custom_tool_call") {
		t.Fatalf("internal custom_tool_call leaked into response: %s", payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.#").Int(); got != 3 {
		t.Fatalf("response output length = %d, want 3; payload=%s", got, payload)
	}

	var foundPlain, foundNamespaced bool
	for _, item := range gjson.GetBytes(resp.Payload, "output").Array() {
		switch item.Get("type").String() {
		case "function_call":
			if item.Get("name").String() == "x_keyword_search" && item.Get("namespace").String() == "acme" {
				foundNamespaced = true
			}
			if item.Get("name").String() == "x_keyword_search" && item.Get("namespace").String() == "" && item.Get("call_id").String() == "call_plain" {
				foundPlain = true
			}
		case "custom_tool_call":
			t.Fatalf("internal custom_tool_call should have been filtered: %s", item.Raw)
		}
	}
	if !foundPlain {
		t.Fatalf("plain client x_keyword_search missing from response: %s", payload)
	}
	if !foundNamespaced {
		t.Fatalf("namespaced client acme.x_keyword_search missing from response: %s", payload)
	}
}

func TestXAIExecutorExecuteStreamPreservesClientSameNameToolsWithXSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Collision case: internal and client tools both named x_keyword_search.
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_keyword_search\",\"input\":\"{}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_ns\",\"type\":\"function_call\",\"call_id\":\"call_ns\",\"name\":\"acme__x_keyword_search\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"id\":\"fc_plain\",\"type\":\"function_call\",\"call_id\":\"call_plain\",\"name\":\"x_keyword_search\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":3,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}],\"status\":\"completed\"}}\n\n")
		completed := `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"xs_call-1","name":"x_keyword_search","input":"{}"},{"id":"fc_ns","type":"function_call","call_id":"call_ns","name":"acme__x_keyword_search","arguments":"{}"},{"id":"fc_plain","type":"function_call","call_id":"call_plain","name":"x_keyword_search","arguments":"{}"},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"search X",
			"tools":[
				{"type":"x_search"},
				{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}},
				{"type":"namespace","name":"acme","tools":[
					{"type":"function","name":"x_keyword_search","parameters":{"type":"object"}}
				]}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var stream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
		stream.WriteByte('\n')
	}
	streamText := stream.String()
	if strings.Contains(streamText, "xs_call") {
		t.Fatalf("internal X search call_id leaked downstream: %s", streamText)
	}
	if strings.Contains(streamText, "custom_tool_call") {
		t.Fatalf("internal custom_tool_call leaked downstream: %s", streamText)
	}

	var foundPlain, foundNamespaced bool
	var completed gjson.Result
	for _, line := range strings.Split(streamText, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !gjson.Valid(line) {
			continue
		}
		event := gjson.Parse(line)
		if event.Get("type").String() == "response.completed" {
			completed = event
		}
		item := event.Get("item")
		if !item.Exists() {
			continue
		}
		if item.Get("type").String() == "custom_tool_call" {
			t.Fatalf("internal custom_tool_call leaked in stream item: %s", item.Raw)
		}
		if item.Get("type").String() != "function_call" {
			continue
		}
		if item.Get("name").String() == "x_keyword_search" && item.Get("namespace").String() == "acme" {
			foundNamespaced = true
		}
		if item.Get("name").String() == "x_keyword_search" && item.Get("namespace").String() == "" && item.Get("call_id").String() == "call_plain" {
			foundPlain = true
		}
	}
	if !foundPlain {
		t.Fatalf("plain client x_keyword_search missing from SSE stream: %s", streamText)
	}
	if !foundNamespaced {
		t.Fatalf("namespaced client acme.x_keyword_search missing from SSE stream: %s", streamText)
	}
	if got := completed.Get("response.output.#").Int(); got != 3 {
		t.Fatalf("completed output length = %d, want 3; completed=%s", got, completed.Raw)
	}
	if completed.Get(`response.output.#(type=="custom_tool_call")`).Exists() {
		t.Fatalf("internal custom_tool_call present in completed output: %s", completed.Raw)
	}
	var completedPlain, completedNamespaced bool
	for _, item := range completed.Get("response.output").Array() {
		if item.Get("type").String() != "function_call" {
			continue
		}
		if item.Get("name").String() == "x_keyword_search" && item.Get("namespace").String() == "acme" {
			completedNamespaced = true
		}
		if item.Get("name").String() == "x_keyword_search" && item.Get("namespace").String() == "" && item.Get("call_id").String() == "call_plain" {
			completedPlain = true
		}
	}
	if !completedPlain || !completedNamespaced {
		t.Fatalf("completed output missing client tools plain=%v namespaced=%v; completed=%s", completedPlain, completedNamespaced, completed.Raw)
	}
}

// TestXAIExecutorExecutePreservesNormalizedCustomSameNameToolWithXSearch exercises the
// real request path: client custom tools are normalized to upstream function, so the
// mock must assert the outgoing function tool and feed back a function_call (not a
// fabricated custom_tool_call that cannot occur after normalization).
func TestXAIExecutorExecutePreservesNormalizedCustomSameNameToolWithXSearch(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Internal X Search trace + legitimate client function_call for the normalized custom tool.
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_keyword_search\",\"input\":\"{}\",\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_custom\",\"type\":\"function_call\",\"call_id\":\"call_custom\",\"name\":\"x_keyword_search\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}],\"status\":\"completed\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_keyword_search\",\"input\":\"{}\"},{\"id\":\"fc_custom\",\"type\":\"function_call\",\"call_id\":\"call_custom\",\"name\":\"x_keyword_search\",\"arguments\":\"{}\"},{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"search X",
			"tools":[
				{"type":"x_search"},
				{"type":"custom","name":"x_keyword_search"}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Assert the client custom tool was normalized to function in the upstream request.
	var foundNormalizedFunction bool
	var foundRawCustom bool
	for _, tool := range gjson.GetBytes(gotBody, "tools").Array() {
		switch tool.Get("type").String() {
		case "function":
			if tool.Get("name").String() == "x_keyword_search" {
				foundNormalizedFunction = true
			}
		case "custom":
			if tool.Get("name").String() == "x_keyword_search" {
				foundRawCustom = true
			}
		}
	}
	if !foundNormalizedFunction {
		t.Fatalf("upstream request missing normalized function tool x_keyword_search; body=%s", gotBody)
	}
	if foundRawCustom {
		t.Fatalf("upstream request still contains client custom tool type; body=%s", gotBody)
	}

	payload := string(resp.Payload)
	if strings.Contains(payload, "xs_call") {
		t.Fatalf("internal X search call_id leaked into response: %s", payload)
	}
	if strings.Contains(payload, "custom_tool_call") {
		t.Fatalf("internal custom_tool_call leaked into response: %s", payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.#").Int(); got != 2 {
		t.Fatalf("response output length = %d, want 2; payload=%s", got, payload)
	}
	var foundClientFunction bool
	for _, item := range gjson.GetBytes(resp.Payload, "output").Array() {
		if item.Get("type").String() == "function_call" &&
			item.Get("name").String() == "x_keyword_search" &&
			item.Get("call_id").String() == "call_custom" {
			foundClientFunction = true
		}
		if item.Get("type").String() == "custom_tool_call" {
			t.Fatalf("internal custom_tool_call should have been filtered: %s", item.Raw)
		}
	}
	if !foundClientFunction {
		t.Fatalf("normalized client custom tool function_call missing from response: %s", payload)
	}
}

func TestXAIExecutorExecuteStreamPreservesNormalizedCustomSameNameToolWithXSearch(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"ctc_1\",\"type\":\"custom_tool_call\",\"call_id\":\"xs_call-1\",\"name\":\"x_keyword_search\",\"input\":\"{}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_custom\",\"type\":\"function_call\",\"call_id\":\"call_custom\",\"name\":\"x_keyword_search\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}],\"status\":\"completed\"}}\n\n")
		completed := `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"xs_call-1","name":"x_keyword_search","input":"{}"},{"id":"fc_custom","type":"function_call","call_id":"call_custom","name":"x_keyword_search","arguments":"{}"},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"search X",
			"tools":[
				{"type":"x_search"},
				{"type":"custom","name":"x_keyword_search"}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var foundNormalizedFunction bool
	var foundRawCustom bool
	for _, tool := range gjson.GetBytes(gotBody, "tools").Array() {
		switch tool.Get("type").String() {
		case "function":
			if tool.Get("name").String() == "x_keyword_search" {
				foundNormalizedFunction = true
			}
		case "custom":
			if tool.Get("name").String() == "x_keyword_search" {
				foundRawCustom = true
			}
		}
	}
	if !foundNormalizedFunction {
		t.Fatalf("upstream request missing normalized function tool x_keyword_search; body=%s", gotBody)
	}
	if foundRawCustom {
		t.Fatalf("upstream request still contains client custom tool type; body=%s", gotBody)
	}

	var stream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
		stream.WriteByte('\n')
	}
	streamText := stream.String()
	if strings.Contains(streamText, "xs_call") {
		t.Fatalf("internal X search call_id leaked downstream: %s", streamText)
	}
	if strings.Contains(streamText, "custom_tool_call") {
		t.Fatalf("internal custom_tool_call leaked downstream: %s", streamText)
	}

	var foundClientFunction bool
	var completed gjson.Result
	for _, line := range strings.Split(streamText, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !gjson.Valid(line) {
			continue
		}
		event := gjson.Parse(line)
		if event.Get("type").String() == "response.completed" {
			completed = event
		}
		item := event.Get("item")
		if !item.Exists() {
			continue
		}
		if item.Get("type").String() == "custom_tool_call" {
			t.Fatalf("internal custom_tool_call leaked in stream item: %s", item.Raw)
		}
		if item.Get("type").String() == "function_call" &&
			item.Get("name").String() == "x_keyword_search" &&
			item.Get("call_id").String() == "call_custom" {
			foundClientFunction = true
		}
	}
	if !foundClientFunction {
		t.Fatalf("normalized client custom tool function_call missing from SSE stream: %s", streamText)
	}
	if got := completed.Get("response.output.#").Int(); got != 2 {
		t.Fatalf("completed output length = %d, want 2; completed=%s", got, completed.Raw)
	}
	if completed.Get(`response.output.#(type=="custom_tool_call")`).Exists() {
		t.Fatalf("internal custom_tool_call present in completed output: %s", completed.Raw)
	}
	var completedClientFunction bool
	for _, item := range completed.Get("response.output").Array() {
		if item.Get("type").String() == "function_call" &&
			item.Get("name").String() == "x_keyword_search" &&
			item.Get("call_id").String() == "call_custom" {
			completedClientFunction = true
		}
	}
	if !completedClientFunction {
		t.Fatalf("completed output missing normalized client custom tool function_call: %s", completed.Raw)
	}
}

func TestXAIExecutorComposerSessionIsolation(t *testing.T) {
	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	tests := []struct {
		name          string
		model         string
		payload       []byte
		wantGenerated bool
		wantSession   string
	}{
		{
			name:          "composer_generates_fresh_session",
			model:         "grok-composer-2.5-fast",
			payload:       []byte(`{"model":"grok-composer-2.5-fast","input":"hello"}`),
			wantGenerated: true,
		},
		{
			name:    "grok_build_stays_stateless_without_session",
			model:   "grok-build-0.1",
			payload: []byte(`{"model":"grok-build-0.1","input":"hello"}`),
		},
		{
			name:        "explicit_prompt_cache_key_is_preserved",
			model:       "grok-composer-2.5-fast",
			payload:     []byte(`{"model":"grok-composer-2.5-fast","prompt_cache_key":"client-session","input":"hello"}`),
			wantSession: "client-session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
				Model:   tt.model,
				Payload: tt.payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Stream:       true,
			}, true)
			if err != nil {
				t.Fatalf("prepareResponsesRequest() error = %v", err)
			}

			gotSession := prepared.sessionID
			gotPromptCacheKey := gjson.GetBytes(prepared.body, "prompt_cache_key").String()
			httpReq, errRequest := http.NewRequest(http.MethodPost, "https://example.test/responses", bytes.NewReader(prepared.body))
			if errRequest != nil {
				t.Fatalf("NewRequest() error = %v", errRequest)
			}
			applyXAIHeaders(httpReq, auth, "xai-token", true, gotSession)
			gotGrokConvID := httpReq.Header.Get("x-grok-conv-id")

			if tt.wantGenerated {
				if _, errParse := uuid.Parse(gotSession); errParse != nil {
					t.Fatalf("generated sessionID = %q, want UUID; body=%s", gotSession, string(prepared.body))
				}
				if gotPromptCacheKey != gotSession {
					t.Fatalf("prompt_cache_key = %q, want sessionID %q; body=%s", gotPromptCacheKey, gotSession, string(prepared.body))
				}
				if gotGrokConvID != gotSession {
					t.Fatalf("x-grok-conv-id = %q, want sessionID %q", gotGrokConvID, gotSession)
				}
				return
			}

			if tt.wantSession != "" {
				if gotSession != tt.wantSession {
					t.Fatalf("sessionID = %q, want %q", gotSession, tt.wantSession)
				}
				if gotPromptCacheKey != tt.wantSession {
					t.Fatalf("prompt_cache_key = %q, want %q; body=%s", gotPromptCacheKey, tt.wantSession, string(prepared.body))
				}
				if gotGrokConvID != tt.wantSession {
					t.Fatalf("x-grok-conv-id = %q, want %q", gotGrokConvID, tt.wantSession)
				}
				return
			}

			if gotSession != "" {
				t.Fatalf("sessionID = %q, want empty", gotSession)
			}
			if gotPromptCacheKey != "" {
				t.Fatalf("prompt_cache_key = %q, want empty; body=%s", gotPromptCacheKey, string(prepared.body))
			}
			if gotGrokConvID != "" {
				t.Fatalf("x-grok-conv-id = %q, want empty", gotGrokConvID)
			}
		})
	}
}

func TestXAIExecutorCompactUsesCompactEndpoint(t *testing.T) {
	validEncryptedContent := testValidGrokEncryptedContent()
	var gotPath string
	var gotAuth string
	var gotAccept string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque-out"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "xai-token",
		},
	}

	payload := []byte(`{"model":"grok-4.3","stream":true,"input":[{"type":"compaction","encrypted_content":""},{"role":"user","content":"hello"}]}`)
	payload, _ = sjson.SetBytes(payload, "input.0.encrypted_content", validEncryptedContent)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", gotPath)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q, want Bearer xai-token", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", gotAccept)
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream exists in compact body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.encrypted_content").String(); got != validEncryptedContent {
		t.Fatalf("input.0.encrypted_content = %q, want valid sample; body=%s", got, string(gotBody))
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque-out"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestXAIExecutorCompactOAuthUsesOfficialAPIHeadersNotCLIProxy(t *testing.T) {
	var gotPath string
	var gotHost string
	var gotTokenAuth string
	var gotClientVersion string
	var gotUserAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		gotClientVersion = r.Header.Get(xaiClientVersionHeader)
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque-out"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			// Custom base is honored for both chat and compact; this asserts that
			// OAuth compact uses standard API headers, not CLI chat-proxy identity.
			"base_url": server.URL,
			"api_key":  "oauth-token",
		},
	}
	if compactBase := xaiCompactBaseURL(auth); compactBase != server.URL {
		t.Fatalf("xaiCompactBaseURL() = %q, want %q", compactBase, server.URL)
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", gotPath)
	}
	wantHost := strings.TrimPrefix(strings.TrimPrefix(server.URL, "https://"), "http://")
	if gotHost != wantHost {
		t.Fatalf("host = %q, want %q", gotHost, wantHost)
	}
	if gotTokenAuth != "" {
		t.Fatalf("%s = %q, want empty on compact (not CLI proxy)", xaiTokenAuthHeader, gotTokenAuth)
	}
	if gotClientVersion != "" {
		t.Fatalf("%s = %q, want empty on compact", xaiClientVersionHeader, gotClientVersion)
	}
	if strings.Contains(gotUserAgent, "xai-grok-workspace/") {
		t.Fatalf("User-Agent = %q, want no CLI workspace UA on compact", gotUserAgent)
	}
}

func TestXAIExecutorCompactClearsReplayBeforePostCompactTurn(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque-out"}]}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "xai-token",
		},
	}
	ctx := testContextWithAPIKey("xai-compact-caller")
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Alt:          "responses/compact",
		Stream:       false,
	}
	compactEncryptedContent := testValidGrokEncryptedContentForSeed(41)
	compactPayload := []byte(`{"model":"grok-4.3","prompt_cache_key":"compact-session","input":[{"type":"compaction","encrypted_content":""},{"type":"message","role":"user","content":[{"type":"input_text","text":"compact"}]}]}`)
	compactPayload, _ = sjson.SetBytes(compactPayload, "input.0.encrypted_content", compactEncryptedContent)
	compactReq := cliproxyexecutor.Request{Model: "grok-4.3", Payload: compactPayload}
	scope := xaiReasoningReplayScopeFromRequest(ctx, sdktranslator.FormatOpenAIResponse, compactReq, opts, compactPayload)
	if !scope.valid() {
		t.Fatal("compact replay scope must be valid")
	}
	reasoning := []byte(`{"type":"reasoning","summary":[],"encrypted_content":""}`)
	reasoning, _ = sjson.SetBytes(reasoning, "encrypted_content", testValidGrokEncryptedContentForSeed(42))
	if !internalcache.CacheXAIReasoningReplayItems(scope.modelName, scope.sessionKey, [][]byte{
		reasoning,
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pre-compact answer"}]}`),
	}) {
		t.Fatal("failed to seed xAI replay cache")
	}

	if _, err := exec.Execute(ctx, auth, compactReq, opts); err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	if _, ok := internalcache.GetXAIReasoningReplayItems(scope.modelName, scope.sessionKey); ok {
		t.Fatal("successful compact must clear the pre-compact replay batch")
	}

	postCompactPayload := []byte(`{"model":"grok-4.3","prompt_cache_key":"compact-session","input":[{"type":"compaction","encrypted_content":""},{"type":"message","role":"user","content":[{"type":"input_text","text":"after compact"}]}]}`)
	postCompactPayload, _ = sjson.SetBytes(postCompactPayload, "input.0.encrypted_content", compactEncryptedContent)
	prepared, errPrepare := exec.prepareResponsesRequest(ctx, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: postCompactPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if errPrepare != nil {
		t.Fatalf("prepare post-compact request: %v", errPrepare)
	}
	input := gjson.GetBytes(prepared.body, "input").Array()
	if len(input) != 2 || input[0].Get("type").String() != "compaction" || input[1].Get("role").String() != "user" {
		t.Fatalf("post-compact input contains stale replay state: %s", prepared.body)
	}
}

func TestXAIExecutorCompactFailureRetainsReplay(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"compact failed"}}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "xai-token",
		},
	}
	ctx := testContextWithAPIKey("xai-compact-failure-caller")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"}
	payload := []byte(`{"model":"grok-4.3","prompt_cache_key":"compact-failure-session","input":[{"type":"message","role":"user","content":"compact"}]}`)
	req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: payload}
	scope := xaiReasoningReplayScopeFromRequest(ctx, sdktranslator.FormatOpenAIResponse, req, opts, payload)
	reasoning := []byte(`{"type":"reasoning","summary":[],"encrypted_content":""}`)
	reasoning, _ = sjson.SetBytes(reasoning, "encrypted_content", testValidGrokEncryptedContentForSeed(43))
	if !internalcache.CacheXAIReasoningReplayItems(scope.modelName, scope.sessionKey, [][]byte{reasoning}) {
		t.Fatal("failed to seed xAI replay cache")
	}

	if _, err := exec.Execute(ctx, auth, req, opts); err == nil {
		t.Fatal("Execute compact error = nil, want upstream failure")
	}
	if _, ok := internalcache.GetXAIReasoningReplayItems(scope.modelName, scope.sessionKey); !ok {
		t.Fatal("failed compact must retain the previous replay batch")
	}
}

func TestXAIExecutorExecuteStreamCompactionTriggerUsesCompactEndpoint(t *testing.T) {
	var gotPath string
	var gotAccept string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_xai_1","model":"grok-4.3","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "xai-token",
		},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","stream":true,"input":[{"role":"user","content":"hello"},{"type":"compaction_trigger"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream compaction trigger error: %v", err)
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", gotPath)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", gotAccept)
	}
	if xaiInputHasItemType(gotBody, "compaction_trigger") {
		t.Fatalf("compaction_trigger reached xai compact body: %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream exists in compact body: %s", string(gotBody))
	}

	var streamed bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	output := streamed.String()
	for _, eventName := range []string{"response.created", "response.in_progress", "response.output_item.added", "response.output_item.done", "response.completed"} {
		if !strings.Contains(output, "event: "+eventName+"\n") {
			t.Fatalf("missing %s event in stream: %s", eventName, output)
		}
	}
	if !strings.Contains(output, `"type":"compaction"`) || !strings.Contains(output, `"encrypted_content":"opaque"`) {
		t.Fatalf("compaction output missing from stream: %s", output)
	}
	if !strings.Contains(output, `"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}`) {
		t.Fatalf("usage missing from completed stream: %s", output)
	}
}

func TestXAIExecutorOmitsUnsupportedReasoningEffort(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4",
		Payload: []byte(`{"model":"grok-4","input":"hello","reasoning":{"effort":"high"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gjson.GetBytes(gotBody, "reasoning").Exists() {
		t.Fatalf("unsupported xAI model must omit reasoning key: %s", string(gotBody))
	}
}

func TestXAISupportsReasoningEffortUsesModelRegistry(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "grok-4.5", model: "grok-4.5", want: true},
		{name: "grok-4.5 with suffix", model: "grok-4.5(high)", want: true},
		{name: "grok-4.3", model: "grok-4.3", want: true},
		{name: "grok-3-mini", model: "grok-3-mini", want: true},
		{name: "grok-3-mini-fast", model: "grok-3-mini-fast", want: true},
		{name: "grok-4.20-multi-agent", model: "grok-4.20-multi-agent-0309", want: true},
		{name: "provider-prefixed grok-4.5", model: "xai/grok-4.5", want: true},
		{name: "legacy grok-4", model: "grok-4", want: false},
		{name: "composer without thinking metadata", model: "grok-composer-2.5-fast", want: false},
		{name: "non-reasoning 4.20", model: "grok-4.20-0309-non-reasoning", want: false},
		{name: "unknown model", model: "unknown-xai-model", want: false},
		{name: "empty model", model: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xaiSupportsReasoningEffort(tt.model); got != tt.want {
				t.Fatalf("xaiSupportsReasoningEffort(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestXAIExecutorKeepsReasoningEffortForGrok45(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hello","reasoning":{"effort":"high"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(gotBody, "model").String(); got != "grok-4.5" {
		t.Fatalf("model = %q, want grok-4.5; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", got, string(gotBody))
	}
}

func TestXAIExecutorKeepsPayloadOverrideReasoningEffortForGrok45(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "grok-4.5"}},
					Params: map[string]any{"reasoning.effort": "high"},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high from payload.override; body=%s", got, string(gotBody))
	}
}

func TestXAIExecutorAppliesThinkingSuffix(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3(low)",
		Payload: []byte(`{"model":"grok-4.3","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(gotBody, "model").String(); got != "grok-4.3" {
		t.Fatalf("model = %q, want grok-4.3; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "low" {
		t.Fatalf("reasoning.effort = %q, want low; body=%s", got, string(gotBody))
	}
}

func TestXAIExecutorExecuteStreamFiltersToolSearchTool(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"test"}],"content":null,"encrypted_content":null},{"type":"reasoning","summary":[{"type":"summary_text","text":"second"}]},{"role":"user","content":"hello"},{"type":"reasoning","summary":[{"type":"summary_text","text":"separate"}]}],"tools":[{"type":"tool_search"},{"type":"image_generation"},{"type":"custom","name":"apply_patch"},{"type":"custom","name":"custom_lookup"},{"type":"function","name":"lookup"},{"type":"web_search","external_web_access":true,"search_content_types":["text","image"]},{"type":"namespace","name":"codex_app","description":"Tools in the codex_app namespace.","tools":[{"type":"function","name":"automation_update"},{"type":"custom","name":"namespace_custom"},{"type":"tool_search"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	tools := gjson.GetBytes(gotBody, "tools").Array()
	if len(tools) != 6 {
		t.Fatalf("tools length = %d, want 6; body=%s", len(tools), string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.0.content").Exists() {
		t.Fatalf("input.0.content exists, want removed; body=%s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.0.encrypted_content").Exists() {
		t.Fatalf("input.0.encrypted_content exists, want removed; body=%s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.summary.0.text").String(); got != "test" {
		t.Fatalf("input.0.summary.0.text = %q, want test; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.summary.1.text").String(); got != "second" {
		t.Fatalf("input.0.summary.1.text = %q, want second; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.2.summary.0.text").String(); got != "separate" {
		t.Fatalf("input.2.summary.0.text = %q, want separate; body=%s", got, string(gotBody))
	}
	foundAutomationUpdate := false
	foundNamespaceCustom := false
	foundXSearch := false
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		if toolType == "image_generation" {
			t.Fatalf("tools.%d.type = image_generation, want removed; body=%s", i, string(gotBody))
		}
		if toolType != "function" && toolType != "web_search" && toolType != "x_search" {
			t.Fatalf("tools.%d.type = %q, want function, web_search, or x_search; body=%s", i, toolType, string(gotBody))
		}
		if toolType == "function" && !tool.Get("parameters").Exists() {
			t.Fatalf("tools.%d.parameters missing for xAI function tool; body=%s", i, string(gotBody))
		}
		if got := tool.Get("name").String(); got == "apply_patch" {
			t.Fatalf("tools.%d.name = apply_patch, want removed; body=%s", i, string(gotBody))
		}
		switch tool.Get("name").String() {
		case "codex_app__automation_update":
			foundAutomationUpdate = true
		case "codex_app__namespace_custom":
			foundNamespaceCustom = true
		}
		if toolType == "x_search" {
			foundXSearch = true
		}
		if toolType == "web_search" {
			if tool.Get("external_web_access").Exists() {
				t.Fatalf("tools.%d.external_web_access exists, want removed; body=%s", i, string(gotBody))
			}
			if got := tool.Get("search_content_types.1").String(); got != "image" {
				t.Fatalf("tools.%d.search_content_types missing image entry; body=%s", i, string(gotBody))
			}
		}
	}
	if !foundAutomationUpdate {
		t.Fatalf("namespace function tool was not moved to top-level tools; body=%s", string(gotBody))
	}
	if !foundNamespaceCustom {
		t.Fatalf("namespace custom tool was not moved to top-level tools; body=%s", string(gotBody))
	}
	if !foundXSearch {
		t.Fatalf("native x_search tool was not injected; body=%s", string(gotBody))
	}
}

func TestXAIExecutorExecuteStreamNormalizesReasoningTextEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"summary\":[]}}\n\n"))
		_, _ = w.Write([]byte("event: response.content_part.added\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.content_part.added\",\"sequence_number\":2,\"item_id\":\"rs_1\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"reasoning_text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.reasoning_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.delta\",\"sequence_number\":3,\"item_id\":\"rs_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"thinking\"}\n\n"))
		_, _ = w.Write([]byte("event: response.reasoning_text.done\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.done\",\"sequence_number\":4,\"item_id\":\"rs_1\",\"output_index\":0,\"content_index\":0,\"text\":\"thinking\"}\n\n"))
		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"sequence_number\":5,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[],\"content\":[{\"type\":\"reasoning_text\",\"text\":\"thinking\"}]}}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatCodex,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var streamed bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	output := streamed.String()
	if strings.Contains(output, "reasoning_text") {
		t.Fatalf("stream contains xAI reasoning_text shape: %s", output)
	}
	for _, want := range []string{
		"event: response.reasoning_summary_part.added",
		"event: response.reasoning_summary_text.delta",
		"event: response.reasoning_summary_text.done",
		"event: response.reasoning_summary_part.done",
		`"type":"response.reasoning_summary_part.added"`,
		`"type":"response.reasoning_summary_text.delta"`,
		`"type":"response.reasoning_summary_text.done"`,
		`"type":"response.reasoning_summary_part.done"`,
		`"part":{"type":"summary_text","text":"thinking"}`,
		`"summary_index":0`,
		`"summary":[{"type":"summary_text","text":"thinking"}]`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stream missing %q: %s", want, output)
		}
	}
	textDoneIndex := strings.Index(output, `"type":"response.reasoning_summary_text.done"`)
	partDoneIndex := strings.Index(output, `"type":"response.reasoning_summary_part.done"`)
	if textDoneIndex < 0 || partDoneIndex < 0 || textDoneIndex > partDoneIndex {
		t.Fatalf("reasoning done events are out of order: %s", output)
	}
}

func TestXAIExecutorExecuteNormalizesReasoningOutputForNonStreamTranslation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[],\"content\":[{\"type\":\"reasoning_text\",\"text\":\"thinking\"}]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatCodex,
		Stream:         false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if strings.Contains(string(resp.Payload), "reasoning_text") {
		t.Fatalf("payload contains xAI reasoning_text shape: %s", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "response.output.0.summary.0.type").String(); got != "summary_text" {
		t.Fatalf("response.output.0.summary.0.type = %q, want summary_text; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "response.output.0.summary.0.text").String(); got != "thinking" {
		t.Fatalf("response.output.0.summary.0.text = %q, want thinking; payload=%s", got, string(resp.Payload))
	}
	if gjson.GetBytes(resp.Payload, "response.output.0.content").Exists() {
		t.Fatalf("reasoning output content exists, want summary only: %s", string(resp.Payload))
	}
}

func TestXAIExecutorExecuteImagesUsesImagesEndpointAndPublishesUsage(t *testing.T) {
	const requestedModel = "grok-imagine-image-quality"

	var gotPath string
	var gotAuth string
	var gotAccept string
	var gotTokenAuth string
	var gotClientVersion string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		gotClientVersion = r.Header.Get(xaiClientVersionHeader)
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}],"usage":{"cost_in_usd_ticks":250000}}`))
	}))
	defer server.Close()
	plugin := &captureXAIUsagePlugin{
		model:   requestedModel,
		records: make(chan usage.Record, 2),
	}
	usage.RegisterPlugin(plugin)

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "image-model-alias",
		Payload: []byte(`{"model":"grok-imagine-image-quality","prompt":"draw"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotPath != "/images/generations" {
		t.Fatalf("path = %q, want /images/generations", gotPath)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q, want Bearer xai-token", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", gotAccept)
	}
	if gotTokenAuth != "" {
		t.Fatalf("%s = %q, want empty on media path", xaiTokenAuthHeader, gotTokenAuth)
	}
	if gotClientVersion != "" {
		t.Fatalf("%s = %q, want empty on media path", xaiClientVersionHeader, gotClientVersion)
	}
	if string(gotBody) != `{"model":"grok-imagine-image-quality","prompt":"draw"}` {
		t.Fatalf("body = %s", string(gotBody))
	}
	if gjson.GetBytes(resp.Payload, "data.0.b64_json").String() != "AA==" {
		t.Fatalf("payload = %s", string(resp.Payload))
	}

	record := waitForXAIUsageRecord(t, plugin.records)
	if record.Model != requestedModel {
		t.Fatalf("model = %q, want %q", record.Model, requestedModel)
	}
	if record.Failed {
		t.Fatalf("failed = true, want false; failure=%+v", record.Fail)
	}
	if record.Detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want zero token usage", record.Detail)
	}
	if record.TTFT <= 0 {
		t.Fatalf("ttft = %v, want positive duration", record.TTFT)
	}
	assertNoAdditionalXAIUsageRecord(t, plugin.records)
}

func TestXAIExecutorExecuteImagesPublishesFailureUsage(t *testing.T) {
	const requestedModel = "grok-imagine-image-quality"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	plugin := &captureXAIUsagePlugin{
		model:   requestedModel,
		records: make(chan usage.Record, 2),
	}
	usage.RegisterPlugin(plugin)

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "image-model-alias",
		Payload: []byte(`{"model":"grok-imagine-image-quality","prompt":"draw"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}

	record := waitForXAIUsageRecord(t, plugin.records)
	if record.Model != requestedModel {
		t.Fatalf("model = %q, want %q", record.Model, requestedModel)
	}
	if !record.Failed {
		t.Fatal("failed = false, want true")
	}
	if record.Fail.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("failure status = %d, want %d", record.Fail.StatusCode, http.StatusTooManyRequests)
	}
	assertNoAdditionalXAIUsageRecord(t, plugin.records)
}

func TestXAIExecutorExecuteImagesPublishesRequestBuildFailureUsage(t *testing.T) {
	const requestedModel = "grok-imagine-image-fallback"

	plugin := &captureXAIUsagePlugin{
		model:   requestedModel,
		records: make(chan usage.Record, 2),
	}
	usage.RegisterPlugin(plugin)

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": "://invalid"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   requestedModel,
		Payload: []byte(`{"prompt":"draw"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}

	record := waitForXAIUsageRecord(t, plugin.records)
	if record.Model != requestedModel {
		t.Fatalf("model = %q, want %q", record.Model, requestedModel)
	}
	if !record.Failed {
		t.Fatal("failed = false, want true")
	}
	assertNoAdditionalXAIUsageRecord(t, plugin.records)
}

type captureXAIUsagePlugin struct {
	model   string
	records chan usage.Record
}

func (p *captureXAIUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.Provider != "xai" || record.Model != p.model {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitForXAIUsageRecord(t *testing.T, records <-chan usage.Record) usage.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for xAI usage record")
		return usage.Record{}
	}
}

func assertNoAdditionalXAIUsageRecord(t *testing.T, records <-chan usage.Record) {
	t.Helper()
	select {
	case record := <-records:
		t.Fatalf("received additional xAI usage record: %+v", record)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestXAIExecutorExecuteImagesUsesEditsEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"url":"https://x.ai/image.png"}]}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-imagine-image",
		Payload: []byte(`{"model":"grok-imagine-image","prompt":"edit","image":{"type":"image_url","url":"https://example.com/a.png"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/edits",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotPath != "/images/edits" {
		t.Fatalf("path = %q, want /images/edits", gotPath)
	}
}

func TestNormalizeXAIImageRefsRewritesImageURLField(t *testing.T) {
	t.Parallel()

	in := []byte(`{
		"model":"grok-imagine-image",
		"prompt":"edit",
		"image":{"type":"image_url","image_url":"https://example.com/a.png"},
		"images":[{"image_url":{"url":"https://example.com/b.png"}},{"url":"https://example.com/c.png","image_url":"https://example.com/ignored.png"}],
		"reference_images":[{"image_url":"https://example.com/d.png"}],
		"nested":{"image":{"image_url":"https://example.com/e.png"}},
		"content":[{"type":"image_url","image_url":{"url":"https://example.com/keep.png"}}]
	}`)
	out := normalizeXAIImageRefs(in)

	if got := gjson.GetBytes(out, "image.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("image.url = %q, want https://example.com/a.png; body=%s", got, out)
	}
	if gjson.GetBytes(out, "image.image_url").Exists() {
		t.Fatalf("image.image_url should be removed; body=%s", out)
	}
	if got := gjson.GetBytes(out, "image.type").String(); got != "image_url" {
		t.Fatalf("image.type = %q, want image_url; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "images.0.url").String(); got != "https://example.com/b.png" {
		t.Fatalf("images.0.url = %q, want https://example.com/b.png; body=%s", got, out)
	}
	if gjson.GetBytes(out, "images.0.image_url").Exists() {
		t.Fatalf("images.0.image_url should be removed; body=%s", out)
	}
	if got := gjson.GetBytes(out, "images.1.url").String(); got != "https://example.com/c.png" {
		t.Fatalf("images.1.url = %q, want existing url kept; body=%s", got, out)
	}
	if gjson.GetBytes(out, "images.1.image_url").Exists() {
		t.Fatalf("images.1.image_url should be removed when url already set; body=%s", out)
	}
	if got := gjson.GetBytes(out, "reference_images.0.url").String(); got != "https://example.com/d.png" {
		t.Fatalf("reference_images.0.url = %q, want https://example.com/d.png; body=%s", got, out)
	}
	if gjson.GetBytes(out, "reference_images.0.image_url").Exists() {
		t.Fatalf("reference_images.0.image_url should be removed; body=%s", out)
	}
	if got := gjson.GetBytes(out, "nested.image.url").String(); got != "https://example.com/e.png" {
		t.Fatalf("nested.image.url = %q, want https://example.com/e.png; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.image_url.url").String(); got != "https://example.com/keep.png" {
		t.Fatalf("chat content image_url.url should be preserved, got %q; body=%s", got, out)
	}
	if gjson.GetBytes(out, "content.0.url").Exists() {
		t.Fatalf("chat content parts must not be rewritten to url; body=%s", out)
	}
}

func TestNormalizeXAIImageRefsSupportsSpecialJSONKeys(t *testing.T) {
	t.Parallel()

	in := []byte(`{
		"metadata.with.dot":{"image":{"image_url":"https://example.com/dot.png"}},
		"back\\slash":{"image":{"image_url":"https://example.com/backslash.png"}},
		"":{"image":{"image_url":"https://example.com/empty-key.png"}}
	}`)
	out := normalizeXAIImageRefs(in)

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(out, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal normalized payload: %v", errUnmarshal)
	}
	for key, wantURL := range map[string]string{
		"metadata.with.dot": "https://example.com/dot.png",
		"back\\slash":       "https://example.com/backslash.png",
		"":                  "https://example.com/empty-key.png",
	} {
		nested, ok := payload[key].(map[string]any)
		if !ok {
			t.Fatalf("payload[%q] = %#v, want object", key, payload[key])
		}
		image, ok := nested["image"].(map[string]any)
		if !ok {
			t.Fatalf("payload[%q].image = %#v, want object", key, nested["image"])
		}
		if gotURL, _ := image["url"].(string); gotURL != wantURL {
			t.Fatalf("payload[%q].image.url = %q, want %q", key, gotURL, wantURL)
		}
		if _, exists := image["image_url"]; exists {
			t.Fatalf("payload[%q].image_url should be removed", key)
		}
	}
}

func TestXAIExecutorExecuteImagesRewritesImageURLToURL(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"url":"https://x.ai/image.png"}]}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-imagine-image",
		Payload: []byte(`{"model":"grok-imagine-image","prompt":"edit","image":{"image_url":"https://example.com/a.png"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/edits",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(gotBody, "image.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("upstream image.url = %q, want https://example.com/a.png; body=%s", got, gotBody)
	}
	if gjson.GetBytes(gotBody, "image.image_url").Exists() {
		t.Fatalf("upstream body still has image.image_url: %s", gotBody)
	}
}

func TestXAIExecutorExecuteVideosCreate(t *testing.T) {
	var gotPath string
	var gotMethod string
	var gotAuth string
	var gotIdempotencyKey string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotIdempotencyKey = r.Header.Get("x-idempotency-key")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"vid_123"}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-imagine-video",
		Payload: []byte(`{"model":"grok-imagine-video","prompt":"animate","duration":4}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Metadata: map[string]any{
			"idempotency_key": "idem-123",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/videos/generations" {
		t.Fatalf("path = %q, want /videos/generations", gotPath)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q, want Bearer xai-token", gotAuth)
	}
	if gotIdempotencyKey != "idem-123" {
		t.Fatalf("x-idempotency-key = %q, want idem-123", gotIdempotencyKey)
	}
	if string(gotBody) != `{"model":"grok-imagine-video","prompt":"animate","duration":4}` {
		t.Fatalf("body = %s", string(gotBody))
	}
	if gjson.GetBytes(resp.Payload, "request_id").String() != "vid_123" {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestXAIExecutorExecuteVideosRetrieve(t *testing.T) {
	var gotPath string
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/video.mp4","duration":6},"model":"grok-imagine-video","progress":100}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-imagine-video",
		Payload: []byte(`{"request_id":"vid_123"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/videos/vid_123" {
		t.Fatalf("path = %q, want /videos/vid_123", gotPath)
	}
	if gjson.GetBytes(resp.Payload, "video.url").String() != "https://vidgen.x.ai/video.mp4" {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestXAIExecutorExecuteVideosUsesNativeEndpointFromRequestPath(t *testing.T) {
	tests := []struct {
		name        string
		requestPath string
		wantPath    string
	}{
		{
			name:        "generations",
			requestPath: "/v1/videos/generations",
			wantPath:    "/videos/generations",
		},
		{
			name:        "edits",
			requestPath: "/v1/videos/edits",
			wantPath:    "/videos/edits",
		},
		{
			name:        "extensions",
			requestPath: "/v1/videos/extensions",
			wantPath:    "/videos/extensions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotMethod string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"request_id":"vid_123"}`))
			}))
			defer server.Close()

			exec := NewXAIExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider:   "xai",
				Attributes: map[string]string{"base_url": server.URL},
				Metadata:   map[string]any{"access_token": "xai-token"},
			}

			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "grok-imagine-video",
				Payload: []byte(`{"model":"grok-imagine-video","prompt":"animate"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-video"),
				Metadata: map[string]any{
					cliproxyexecutor.RequestPathMetadataKey: tt.requestPath,
				},
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			if gotMethod != http.MethodPost {
				t.Fatalf("method = %q, want POST", gotMethod)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %s", gotPath, tt.wantPath)
			}
		})
	}
}

func TestNormalizeXAITools_SimplifiesCodexAppAutomationUpdateSchema(t *testing.T) {
	// Large oneOf+$ref schema mimicking Codex Desktop codex_app.automation_update.
	params := `{"type":"object","oneOf":[{"properties":{"mode":{"type":"string"}}}],"$defs":{"a":{"type":"string"}},"x":"` + strings.Repeat("y", 1600) + `"}`
	body := []byte(`{"model":"grok-4.5","tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"automation_update","description":"sched","strict":true,"parameters":` + params + `}]},{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`)
	out := normalizeXAITools(body)

	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() {
		t.Fatalf("tools missing: %s", string(out))
	}
	foundAuto := false
	foundExec := false
	for _, tool := range tools.Array() {
		switch tool.Get("name").String() {
		case "codex_app__automation_update":
			foundAuto = true
			paramsRaw := tool.Get("parameters").Raw
			if strings.Contains(paramsRaw, `"oneOf"`) || strings.Contains(paramsRaw, `"$defs"`) {
				t.Fatalf("automation_update parameters were not simplified: %s", paramsRaw)
			}
			if tool.Get("parameters.type").String() != "object" {
				t.Fatalf("automation_update parameters.type = %q, want object", tool.Get("parameters.type").String())
			}
			if tool.Get("parameters.additionalProperties").Type != gjson.True {
				t.Fatalf("automation_update parameters should allow additionalProperties: %s", paramsRaw)
			}
			if tool.Get("strict").Type != gjson.False {
				t.Fatalf("automation_update strict = %s, want false", tool.Get("strict").Raw)
			}
		case "exec_command":
			foundExec = true
			if got := tool.Get("parameters.properties.cmd.type").String(); got != "string" {
				t.Fatalf("exec_command schema should be preserved, got %q in %s", got, tool.Raw)
			}
		}
	}
	if !foundAuto {
		t.Fatalf("automation_update tool missing after normalize: %s", string(out))
	}
	if !foundExec {
		t.Fatalf("exec_command tool missing after normalize: %s", string(out))
	}
}

func TestNormalizeXAITools_SimplifiesFlattenedAndInvalidRootSchemas(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"codex_app__automation_update","strict":true,"parameters":{"oneOf":[{"type":"object","properties":{"action":{"type":"string"}},"required":["action"]},{"type":"null"}]}},{"type":"function","name":"nullable_lookup","strict":true,"parameters":{"anyOf":[{"type":"object","properties":{"query":{"type":"string"}}},{"type":["object","null"]}]}},{"type":"custom","name":"nullable_custom","strict":true,"parameters":{"oneOf":[{"type":"object"},{"type":"null"}]}},{"type":"function","name":"mixed_nullable","strict":true,"parameters":{"type":"object","oneOf":[{"required":["query"]},{"type":"null"}],"properties":{"query":{"type":"string"}}}},{"type":"function","name":"array_root_union","strict":true,"parameters":{"type":["object"],"anyOf":[{"required":["query"]},{"required":["id"]}],"properties":{"query":{"type":"string"},"id":{"type":"integer"}}}},{"type":"function","name":"echo_tool","strict":true,"parameters":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}}]}`)
	out := normalizeXAITools(body)

	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 6 {
		t.Fatalf("tools length = %d, want 6; body=%s", len(tools), string(out))
	}
	for index, wantName := range []string{"codex_app__automation_update", "nullable_lookup", "nullable_custom", "mixed_nullable", "array_root_union"} {
		tool := tools[index]
		if got := tool.Get("name").String(); got != wantName {
			t.Fatalf("tools.%d.name = %q, want %q; body=%s", index, got, wantName, string(out))
		}
		if got := tool.Get("type").String(); got != xaiFunctionToolType {
			t.Fatalf("tools.%d type = %q, want function; body=%s", index, got, string(out))
		}
		if got := tool.Get("parameters.type").String(); got != "object" {
			t.Fatalf("tools.%d parameters.type = %q, want object; body=%s", index, got, string(out))
		}
		if tool.Get("parameters.additionalProperties").Type != gjson.True {
			t.Fatalf("tools.%d parameters should allow additionalProperties: %s", index, string(out))
		}
		if tool.Get("strict").Type != gjson.False {
			t.Fatalf("tools.%d strict = %s, want false; body=%s", index, tool.Get("strict").Raw, string(out))
		}
	}

	echoTool := tools[5]
	if got := echoTool.Get("parameters.properties.message.type").String(); got != "string" {
		t.Fatalf("echo_tool schema changed, message type = %q; body=%s", got, string(out))
	}
	if echoTool.Get("strict").Type != gjson.True {
		t.Fatalf("echo_tool strict changed: %s", string(out))
	}
	if echoTool.Get("parameters.additionalProperties").Type != gjson.False {
		t.Fatalf("echo_tool additionalProperties changed: %s", string(out))
	}
}

func TestNormalizeXAITools_AddsObjectTypeToRootUnionBranches(t *testing.T) {
	body := []byte(`{
		"tools":[
			{
				"type":"function",
				"name":"crop_around_point",
				"strict":true,
				"parameters":{
					"type":"object",
					"additionalProperties":false,
					"required":["imagePath","point"],
					"oneOf":[
						{"required":["radius"],"not":{"required":["size"]}},
						{"required":["size"],"not":{"required":["radius"]}}
					],
					"properties":{
						"imagePath":{"type":"string"},
						"point":{"type":"array"},
						"radius":{"type":"number"},
						"size":{"type":"object"},
						"nested":{"oneOf":[{"required":["value"]},{}]}
					}
				}
			},
			{
				"type":"function",
				"name":"lookup",
				"strict":true,
				"parameters":{
					"type":"object",
					"anyOf":[{"required":["query"]},{"required":["id"]}],
					"properties":{"query":{"type":"string"},"id":{"type":"integer"}}
				}
			},
			{
				"type":"custom",
				"name":"custom_lookup",
				"strict":true,
				"parameters":{
					"type":"object",
					"oneOf":[{"required":["query"]},{"required":["id"]}],
					"properties":{"query":{"type":"string"},"id":{"type":"integer"}}
				}
			}
		]
	}`)
	out := normalizeXAITools(body)

	for toolIndex, unionName := range []string{"oneOf", "anyOf"} {
		tool := gjson.GetBytes(out, fmt.Sprintf("tools.%d", toolIndex))
		branches := tool.Get("parameters." + unionName).Array()
		if len(branches) != 2 {
			t.Fatalf("tools.%d %s branch count = %d, want 2; body=%s", toolIndex, unionName, len(branches), string(out))
		}
		for branchIndex, branch := range branches {
			if got := branch.Get("type").String(); got != "object" {
				t.Fatalf("tools.%d parameters.%s.%d.type = %q, want object; body=%s", toolIndex, unionName, branchIndex, got, string(out))
			}
		}
		if tool.Get("strict").Type != gjson.True {
			t.Fatalf("tools.%d strict changed: %s", toolIndex, string(out))
		}
	}

	cropParameters := gjson.GetBytes(out, "tools.0.parameters")
	if cropParameters.Get("additionalProperties").Type != gjson.False {
		t.Fatalf("crop additionalProperties changed: %s", cropParameters.Raw)
	}
	if got := cropParameters.Get("required.#").Int(); got != 2 {
		t.Fatalf("crop required length = %d, want 2; parameters=%s", got, cropParameters.Raw)
	}
	if !cropParameters.Get("oneOf.0.not.required").Exists() || !cropParameters.Get("oneOf.1.not.required").Exists() {
		t.Fatalf("crop oneOf constraints changed: %s", cropParameters.Raw)
	}
	if cropParameters.Get("properties.nested.oneOf.0.type").Exists() {
		t.Fatalf("nested union branch must not be changed: %s", cropParameters.Raw)
	}

	customTool := gjson.GetBytes(out, "tools.2")
	if got := customTool.Get("type").String(); got != xaiFunctionToolType {
		t.Fatalf("custom tool type = %q, want function; body=%s", got, string(out))
	}
	for branchIndex, branch := range customTool.Get("parameters.oneOf").Array() {
		if got := branch.Get("type").String(); got != "object" {
			t.Fatalf("custom tool oneOf.%d.type = %q, want object; body=%s", branchIndex, got, string(out))
		}
	}
	if customTool.Get("strict").Type != gjson.True {
		t.Fatalf("custom tool strict changed: %s", string(out))
	}
}

func TestNormalizeXAITools_QualifiesSameNamedNamespaceTools(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"search","parameters":{"type":"object"}}]},
			{"type":"namespace","name":"mcp__docs","tools":[{"type":"function","name":"search","parameters":{"type":"object"}}]}
		]
	}`)
	out := normalizeXAITools(body)

	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2; body=%s", len(tools), string(out))
	}
	if got := tools[0].Get("name").String(); got != "mcp__exa__search" {
		t.Fatalf("tools.0.name = %q, want mcp__exa__search; body=%s", got, string(out))
	}
	if got := tools[1].Get("name").String(); got != "mcp__docs__search" {
		t.Fatalf("tools.1.name = %q, want mcp__docs__search; body=%s", got, string(out))
	}
}

func TestPromoteXAIAdditionalTools(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"search","parameters":{"type":"object"}}]}]},
			{"role":"user","content":"hello"},
			{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"custom_lookup"}]}
		]
	}`)
	out := promoteXAIAdditionalTools(normalizeXAITools(body))

	input := gjson.GetBytes(out, "input").Array()
	if len(input) != 1 || input[0].Get("role").String() != "user" {
		t.Fatalf("input should contain only the user message: %s", string(out))
	}
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("tools length = %d, want 3; body=%s", len(tools), string(out))
	}
	if got := tools[0].Get("name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup; body=%s", got, string(out))
	}
	if got := tools[1].Get("name").String(); got != "mcp__exa__search" {
		t.Fatalf("tools.1.name = %q, want mcp__exa__search; body=%s", got, string(out))
	}
	if got := tools[2].Get("name").String(); got != "custom_lookup" {
		t.Fatalf("tools.2.name = %q, want custom_lookup; body=%s", got, string(out))
	}
	if got := tools[2].Get("type").String(); got != "function" {
		t.Fatalf("tools.2.type = %q, want function; body=%s", got, string(out))
	}
	if !tools[2].Get("parameters").Exists() {
		t.Fatalf("tools.2.parameters missing: %s", string(out))
	}
}

func TestNormalizeXAINamespaceToolChoice(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"search","parameters":{"type":"object"}}]}],
		"tool_choice":{"type":"function","name":"search","namespace":"mcp__exa"}
	}`)
	out := normalizeXAITools(body)
	out = normalizeXAINamespaceToolChoice(out)

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "mcp__exa__search" {
		t.Fatalf("tools.0.name = %q, want mcp__exa__search; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "mcp__exa__search" {
		t.Fatalf("tool_choice.name = %q, want mcp__exa__search; body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice.namespace").Exists() {
		t.Fatalf("tool_choice.namespace should be removed for xAI upstream: %s", string(out))
	}
}

func TestNormalizeXAINamespaceToolChoiceAllowedTools(t *testing.T) {
	body := []byte(`{
		"tool_choice":{
			"type":"allowed_tools",
			"tools":[
				{"type":"function","name":"search","namespace":"mcp__exa"},
				{"type":"function","name":"collaboration__send_message","namespace":"collaboration"},
				{"type":"function","name":"lookup"},
				{"type":"web_search","namespace":"ignored"}
			]
		}
	}`)
	out := normalizeXAINamespaceToolChoice(body)

	if got := gjson.GetBytes(out, "tool_choice.tools.0.name").String(); got != "mcp__exa__search" {
		t.Fatalf("tool_choice.tools.0.name = %q, want mcp__exa__search; body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice.tools.0.namespace").Exists() {
		t.Fatalf("tool_choice.tools.0.namespace should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.tools.1.name").String(); got != "collaboration__send_message" {
		t.Fatalf("tool_choice.tools.1.name = %q, want collaboration__send_message; body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice.tools.1.namespace").Exists() {
		t.Fatalf("tool_choice.tools.1.namespace should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.tools.2.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.tools.2.name = %q, want lookup; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.tools.3.namespace").String(); got != "ignored" {
		t.Fatalf("non-function namespace = %q, want ignored; body=%s", got, string(out))
	}
}

func TestNormalizeXAINamespaceToolChoice_PreservesOtherChoices(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "automatic choice", body: []byte(`{"tool_choice":"auto"}`)},
		{name: "top-level function", body: []byte(`{"tool_choice":{"type":"function","name":"search"}}`)},
		{name: "non-function choice", body: []byte(`{"tool_choice":{"type":"web_search","name":"search","namespace":"mcp__exa"}}`)},
		{name: "malformed payload", body: []byte(`{"tool_choice":{"type":"function"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeXAINamespaceToolChoice(tt.body); !bytes.Equal(got, tt.body) {
				t.Fatalf("payload changed: got=%q want=%q", got, tt.body)
			}
		})
	}
}

func TestQualifyXAINamespaceToolNamePreservesQualifiedNames(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		tool      string
		want      string
	}{
		{name: "plain child", namespace: "mcp__exa", tool: "search", want: "mcp__exa__search"},
		{name: "prequalified MCP child", namespace: "mcp__exa", tool: "mcp__exa__search", want: "mcp__exa__search"},
		{name: "prequalified generic child", namespace: "collaboration", tool: "collaboration__send_message", want: "collaboration__send_message"},
		{name: "namespace with separator", namespace: "collaboration__", tool: "send_message", want: "collaboration__send_message"},
		{name: "partial prefix is not qualified", namespace: "exa", tool: "example_tool", want: "exa__example_tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qualifyXAINamespaceToolName(tt.namespace, tt.tool); got != tt.want {
				t.Fatalf("qualifyXAINamespaceToolName(%q, %q) = %q, want %q", tt.namespace, tt.tool, got, tt.want)
			}
		})
	}
}

func TestNormalizeXAITools_PreservesUnrelatedSchemas(t *testing.T) {
	largeParams := `{"oneOf":[{"type":"object","properties":{"mode":{"type":"string"}}}],"$defs":{"a":{"type":"string"}},"x":"` + strings.Repeat("y", 1600) + `"}`
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "top-level automation_update",
			body: []byte(`{"tools":[{"type":"function","name":"automation_update","strict":true,"parameters":{"type":"object","properties":{"cron":{"type":"string"}},"required":["cron"],"additionalProperties":false}}]}`),
		},
		{
			name: "automation_update in another namespace",
			body: []byte(`{"tools":[{"type":"namespace","name":"calendar","tools":[{"type":"function","name":"automation_update","strict":true,"parameters":{"type":"object","properties":{"cron":{"type":"string"}},"required":["cron"],"additionalProperties":false}}]}]}`),
		},
		{
			name: "custom automation_update in codex_app",
			body: []byte(`{"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"custom","name":"automation_update","strict":true,"parameters":{"type":"object","properties":{"cron":{"type":"string"}},"required":["cron"],"additionalProperties":false}}]}]}`),
		},
		{
			name: "large schema on another codex_app function",
			body: []byte(`{"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"exec_command","strict":true,"parameters":` + largeParams + `}]}]}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := normalizeXAITools(tt.body)
			tool := gjson.GetBytes(out, "tools.0")
			if tool.Get("strict").Type != gjson.True {
				t.Fatalf("strict changed for unrelated tool: %s", string(out))
			}
			params := tool.Get("parameters")
			if tt.name == "large schema on another codex_app function" {
				if !params.Get("oneOf").Exists() || !params.Get("$defs").Exists() {
					t.Fatalf("large schema was simplified: %s", string(out))
				}
				return
			}
			if got := params.Get("properties.cron.type").String(); got != "string" {
				t.Fatalf("schema was simplified, cron type = %q: %s", got, string(out))
			}
			if params.Get("additionalProperties").Type != gjson.False {
				t.Fatalf("additionalProperties changed: %s", string(out))
			}
		})
	}
}

func TestXAIFunctionParametersNeedSimplification(t *testing.T) {
	auto := gjson.Parse(`{"type":"function","name":"automation_update","parameters":{"type":"object"}}`)
	if !xaiFunctionParametersNeedSimplification(auto, "codex_app") {
		t.Fatal("codex_app.automation_update should need simplification")
	}
	if xaiFunctionParametersNeedSimplification(auto, "calendar") {
		t.Fatal("automation_update outside codex_app should not need simplification")
	}
	if xaiFunctionParametersNeedSimplification(auto, "") {
		t.Fatal("top-level automation_update should not need simplification")
	}
	flattened := gjson.Parse(`{"type":"function","name":"codex_app__automation_update","parameters":{"type":"object"}}`)
	if !xaiFunctionParametersNeedSimplification(flattened, "") {
		t.Fatal("flattened codex_app__automation_update should need simplification")
	}
	custom := gjson.Parse(`{"type":"custom","name":"automation_update","parameters":{"type":"object"}}`)
	if xaiFunctionParametersNeedSimplification(custom, "codex_app") {
		t.Fatal("custom codex_app.automation_update with an object schema should not need simplification")
	}
	invalidCustom := gjson.Parse(`{"type":"custom","name":"nullable_lookup","parameters":{"oneOf":[{"type":"object"},{"type":"null"}]}}`)
	if !xaiFunctionParametersNeedSimplification(invalidCustom, "") {
		t.Fatal("custom tool normalized to a function should simplify an invalid root union")
	}
	invalidOneOf := gjson.Parse(`{"type":"function","name":"nullable_lookup","parameters":{"oneOf":[{"type":"object"},{"type":"null"}]}}`)
	if !xaiFunctionParametersNeedSimplification(invalidOneOf, "") {
		t.Fatal("root oneOf with a non-object branch should need simplification")
	}
	invalidAnyOf := gjson.Parse(`{"type":"function","name":"nullable_lookup","parameters":{"anyOf":[{"type":"object"},{"type":["object","null"]}]}}`)
	if !xaiFunctionParametersNeedSimplification(invalidAnyOf, "") {
		t.Fatal("root anyOf with a non-object type should need simplification")
	}
	untypedBranch := gjson.Parse(`{"type":"function","name":"nullable_lookup","parameters":{"oneOf":[{"type":"object"},{"const":null}]}}`)
	if !xaiFunctionParametersNeedSimplification(untypedBranch, "") {
		t.Fatal("root union with an untyped branch should need simplification")
	}
	objectUnion := gjson.Parse(`{"type":"function","name":"lookup","parameters":{"oneOf":[{"type":"object"},{"type":"object"}]}}`)
	if xaiFunctionParametersNeedSimplification(objectUnion, "") {
		t.Fatal("root union containing only object branches should not need simplification")
	}
	nestedUnion := gjson.Parse(`{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"null"}]}}}}`)
	if xaiFunctionParametersNeedSimplification(nestedUnion, "") {
		t.Fatal("nested union should not need root schema simplification")
	}
	safe := gjson.Parse(`{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}`)
	if xaiFunctionParametersNeedSimplification(safe, "codex_app") {
		t.Fatal("unrelated codex_app function should not need simplification")
	}
}

func TestNormalizeXAIInputNamespaceToolCalls(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","name":"web_search_exa","namespace":"mcp__exa","call_id":"call_1","arguments":"{}"},{"type":"function_call","name":"plain_tool","call_id":"call_2","arguments":"{}"}]}`)
	out := normalizeXAIInputNamespaceToolCalls(body)

	if got := gjson.GetBytes(out, "input.0.name").String(); got != "mcp__exa__web_search_exa" {
		t.Fatalf("input.0.name = %q, want qualified namespace name; body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.namespace").Exists() {
		t.Fatalf("input.0.namespace should be removed for xAI upstream: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "plain_tool" {
		t.Fatalf("plain function call name changed to %q", got)
	}
}

func TestRestoreXAINamespaceToolCalls(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"web_search_exa","parameters":{"type":"object"}}]}]}`)
	refs := collectXAINamespaceToolRefs(request)

	event := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"mcp__exa__web_search_exa","call_id":"call_1","arguments":"{}"}}`)
	restoredEvent := restoreXAINamespaceToolCalls(event, refs)
	if got := gjson.GetBytes(restoredEvent, "item.name").String(); got != "web_search_exa" {
		t.Fatalf("item.name = %q, want child name; event=%s", got, string(restoredEvent))
	}
	if got := gjson.GetBytes(restoredEvent, "item.namespace").String(); got != "mcp__exa" {
		t.Fatalf("item.namespace = %q, want mcp__exa; event=%s", got, string(restoredEvent))
	}

	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","name":"mcp__exa__web_search_exa","call_id":"call_1","arguments":"{}"}]}}`)
	restoredCompleted := restoreXAINamespaceToolCalls(completed, refs)
	if got := gjson.GetBytes(restoredCompleted, "response.output.0.name").String(); got != "web_search_exa" {
		t.Fatalf("response.output.0.name = %q, want child name; event=%s", got, string(restoredCompleted))
	}
	if got := gjson.GetBytes(restoredCompleted, "response.output.0.namespace").String(); got != "mcp__exa" {
		t.Fatalf("response.output.0.namespace = %q, want mcp__exa; event=%s", got, string(restoredCompleted))
	}
}

func TestRestoreXAINamespaceToolCallsPreservesMalformedPayload(t *testing.T) {
	data := []byte(`{"item":{"type":"function_call","name":"mcp__exa__web_search_exa"`)
	refs := map[string]xaiNamespaceToolRef{
		"mcp__exa__web_search_exa": {namespace: "mcp__exa", name: "web_search_exa"},
	}

	if got := restoreXAINamespaceToolCalls(data, refs); !bytes.Equal(got, data) {
		t.Fatalf("malformed payload changed: got=%q want=%q", got, data)
	}
}

func TestNormalizeXAIToolChoiceForTools_DropsWhenToolsEmpty(t *testing.T) {
	body := []byte(`{"model":"grok-4","tools":[],"tool_choice":"auto","parallel_tool_calls":true,"input":"hi"}`)
	out := normalizeXAIToolChoiceForTools(body)

	if gjson.GetBytes(out, "tools").Exists() {
		t.Fatalf("empty tools should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be removed when tools empty: %s", string(out))
	}
	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools empty: %s", string(out))
	}
}

func TestNormalizeXAIToolChoiceForTools_DropsWhenToolsMissing(t *testing.T) {
	body := []byte(`{"model":"grok-4","tool_choice":"auto","input":"hi"}`)
	out := normalizeXAIToolChoiceForTools(body)

	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be removed when tools missing: %s", string(out))
	}
}

func TestNormalizeXAIToolChoiceForTools_DropsOrphanedParallelToolCalls(t *testing.T) {
	body := []byte(`{"model":"grok-4","parallel_tool_calls":true,"input":"hi"}`)
	out := normalizeXAIToolChoiceForTools(body)

	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools missing even without tool_choice: %s", string(out))
	}
}

func TestNormalizeXAIToolChoiceForTools_KeepsWhenToolsPresent(t *testing.T) {
	body := []byte(`{"model":"grok-4","tools":[{"type":"function","name":"Bash"}],"tool_choice":"auto","input":"hi"}`)
	out := normalizeXAIToolChoiceForTools(body)

	if !gjson.GetBytes(out, "tools").Exists() {
		t.Fatalf("tools should be kept: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto: %s", got, string(out))
	}
}

func TestNormalizeXAIToolChoiceForTools_KeepsWhenAdditionalToolsPresent(t *testing.T) {
	body := []byte(`{"model":"grok-4","input":[{"type":"additional_tools","tools":[{"type":"function","name":"Bash"}]}],"tool_choice":"auto","parallel_tool_calls":true}`)
	out := normalizeXAIToolChoiceForTools(body)

	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls should be kept: %s", string(out))
	}
}

func TestNormalizeXAIToolChoiceForTools_NoOpWhenBothAbsent(t *testing.T) {
	body := []byte(`{"model":"grok-4","input":"hi"}`)
	out := normalizeXAIToolChoiceForTools(body)

	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice should not appear: %s", string(out))
	}
}

func TestXAIExecutorComposerReusesClaudeCodeSession(t *testing.T) {
	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	payload := []byte(`{"model":"grok-composer-2.5-fast","metadata":{"user_id":"{\"session_id\":\"cache-session-1\"}"},"input":"hello"}`)
	req := cliproxyexecutor.Request{Model: "grok-composer-2.5-fast", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true}

	first, err := exec.prepareResponsesRequest(context.Background(), req, opts, true)
	if err != nil {
		t.Fatalf("prepareResponsesRequest first error: %v", err)
	}
	second, err := exec.prepareResponsesRequest(context.Background(), req, opts, true)
	if err != nil {
		t.Fatalf("prepareResponsesRequest second error: %v", err)
	}

	firstKey := gjson.GetBytes(first.body, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(second.body, "prompt_cache_key").String()
	if firstKey == "" {
		t.Fatalf("first prompt_cache_key is empty; body=%s", string(first.body))
	}
	if secondKey != firstKey {
		t.Fatalf("same Claude Code session produced different prompt_cache_key: first=%q second=%q", firstKey, secondKey)
	}

	httpReq, errRequest := http.NewRequest(http.MethodPost, "https://example.test/responses", bytes.NewReader(first.body))
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}
	applyXAIHeaders(httpReq, auth, "xai-token", true, first.sessionID)
	if got := httpReq.Header.Get("x-grok-conv-id"); got != firstKey {
		t.Fatalf("x-grok-conv-id = %q, want %q", got, firstKey)
	}
}

func TestSanitizeXAIInputEncryptedContent_DropsInvalidReasoningBlob(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","input":[{"type":"reasoning","summary":[],"encrypted_content":"bad"},{"type":"reasoning","summary":[],"encrypted_content":"gAAAAABinvalid-gpt-shape"},{"role":"user","content":"hi"}]}`)
	got := sanitizeXAIInputEncryptedContent(body)
	if gjson.GetBytes(got, "input.0.encrypted_content").Exists() || gjson.GetBytes(got, "input.1.encrypted_content").Exists() {
		t.Fatalf("invalid encrypted_content should be removed: %s", string(got))
	}
}

func TestSanitizeXAIInputEncryptedContent_PreservesValidBlob(t *testing.T) {
	sample := testValidGrokEncryptedContent()
	body := []byte(`{"model":"grok-4.3","input":[{"type":"reasoning","summary":[],"encrypted_content":""}]}`)
	body, _ = sjson.SetBytes(body, "input.0.encrypted_content", sample)
	got := sanitizeXAIInputEncryptedContent(body)
	if gotEnc := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotEnc != sample {
		t.Fatalf("valid encrypted_content should be preserved, got %q", gotEnc)
	}
}

func TestXAIExecutorReMergesReasoningAfterDroppingInvalidEncryptedContent(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":[` +
			`{"type":"reasoning","summary":[{"type":"summary_text","text":"first"}]},` +
			`{"type":"reasoning","summary":[{"type":"summary_text","text":"second"}],"encrypted_content":"gAAAAABforeign-codex-replay"},` +
			`{"role":"user","content":"hi"}` +
			`]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(gotBody, "input.0.summary.0.text").String(); got != "first" {
		t.Fatalf("input.0.summary.0.text = %q, want first; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.summary.1.text").String(); got != "second" {
		t.Fatalf("input.0.summary.1.text = %q, want second; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.2").Exists() {
		t.Fatalf("input.2 exists, want invalid reasoning blob removed and summaries re-merged; body=%s", string(gotBody))
	}
}

func TestXAIExecutorDropsInvalidCompactionItem(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":[{"type":"compaction","encrypted_content":"gAAAAABforeign-codex-replay"},{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if xaiInputHasItemType(gotBody, "compaction") {
		t.Fatalf("invalid compaction item reached upstream body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user after dropping invalid compaction; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.1").Exists() {
		t.Fatalf("input.1 exists, want only user item after dropping invalid compaction; body=%s", string(gotBody))
	}
}

func TestXAIExecutorReasoningReplayCacheStoresFinalDoneAndInjectsNextClaudeRequest(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	addedEncryptedContent := testValidGrokEncryptedContentForSeed(1)
	doneEncryptedContent := testValidGrokEncryptedContentForSeed(2)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"rs_added","type":"reasoning","status":"in_progress","summary":[],"encrypted_content":"` + addedEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_done","type":"reasoning","summary":[],"encrypted_content":"` + doneEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"grok-4.3","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-replay-1",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"access_token": "xai-token",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       false,
	}
	ctx := testContextWithAPIKey("xai-replay-caller")

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"xai-session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"xai-session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "reasoning" {
		t.Fatalf("input.0.type = %q, want reasoning; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.0.encrypted_content").String(); got != doneEncryptedContent {
		t.Fatalf("injected encrypted_content = %q, want final done %q; body=%s", got, doneEncryptedContent, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user; body=%s", got, string(secondBody))
	}
}

func TestXAIExecutorResponsesSSEReplaysEncryptedReasoningAndAssistantMessage(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	encryptedContent := testValidGrokEncryptedContentForSeed(9)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"` + encryptedContent + `"},"output_index":0}` + "\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first answer"}]},"output_index":1}` + "\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"grok-4.5","output":[]}}` + "\n\n"))
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"grok-4.5","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-responses-sse-replay",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	}
	firstPayload := []byte(`{"model":"grok-4.5","stream":true,"store":false,"prompt_cache_key":"codex-sse-session","include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}]}`)
	secondPayload := []byte(`{"model":"grok-4.5","stream":true,"store":false,"prompt_cache_key":"codex-sse-session","include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}]}`)

	streamedResponses := make([][]byte, 0, 2)
	ctx := testContextWithAPIKey("codex-sse-api-key")
	for _, payload := range [][]byte{firstPayload, secondPayload} {
		result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4.5", Payload: payload}, opts)
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		var streamed bytes.Buffer
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error: %v", chunk.Err)
			}
			streamed.Write(chunk.Payload)
		}
		streamedResponses = append(streamedResponses, bytes.Clone(streamed.Bytes()))
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	if includes := gjson.GetBytes(bodies[0], "include").Array(); len(includes) != 1 || includes[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("first request include was not preserved: %s", bodies[0])
	}
	var downstreamEncryptedContent string
	for _, line := range bytes.Split(streamedResponses[0], []byte("\n")) {
		if !bytes.HasPrefix(line, xaiDataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[len(xaiDataTag):])
		if gjson.GetBytes(eventData, "type").String() != "response.output_item.done" ||
			gjson.GetBytes(eventData, "item.type").String() != "reasoning" {
			continue
		}
		downstreamEncryptedContent = gjson.GetBytes(eventData, "item.encrypted_content").String()
		break
	}
	if downstreamEncryptedContent != encryptedContent {
		t.Fatalf("downstream encrypted_content = %q, want upstream Grok blob; stream=%s", downstreamEncryptedContent, streamedResponses[0])
	}
	if got := gjson.GetBytes(bodies[1], "input.0.type").String(); got != "reasoning" {
		t.Fatalf("second input.0.type = %q, want reasoning; body=%s", got, bodies[1])
	}
	if got := gjson.GetBytes(bodies[1], "input.0.encrypted_content").String(); got != encryptedContent {
		t.Fatalf("replayed encrypted_content = %q, want cached Grok blob; body=%s", got, bodies[1])
	}
	if got := gjson.GetBytes(bodies[1], "input.1.type").String(); got != "message" {
		t.Fatalf("second input.1.type = %q, want assistant message; body=%s", got, bodies[1])
	}
	if got := gjson.GetBytes(bodies[1], "input.1.content.0.text").String(); got != "first answer" {
		t.Fatalf("replayed assistant text = %q, want first answer; body=%s", got, bodies[1])
	}
	if got := gjson.GetBytes(bodies[1], "input.2.content.0.text").String(); got != "second" {
		t.Fatalf("new user text = %q, want second; body=%s", got, bodies[1])
	}
}

func TestFilterXAIReasoningReplayItemsSkipsMatchingCachedTurn(t *testing.T) {
	encryptedContent := testValidGrokEncryptedContentForSeed(10)
	body := []byte(`{"input":[{"type":"reasoning","summary":[],"encrypted_content":""},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}]}`)
	body, _ = sjson.SetBytes(body, "input.0.encrypted_content", encryptedContent)
	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}`),
	}
	items[0], _ = sjson.SetBytes(items[0], "encrypted_content", encryptedContent)

	filtered := filterXAIReasoningReplayItemsForInput(body, items)
	if len(filtered) != 0 {
		t.Fatalf("filtered replay items = %q, want none for client-provided history", filtered)
	}
}

func TestFilterXAIReasoningReplayItemsSkipsAmbiguousCachedTurnWhenInputHasOlderReasoning(t *testing.T) {
	oldEncryptedContent := testValidGrokEncryptedContentForSeed(10)
	newEncryptedContent := testValidGrokEncryptedContentForSeed(12)
	body := []byte(`{"input":[{"type":"reasoning","summary":[],"encrypted_content":""},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"older answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)
	body, _ = sjson.SetBytes(body, "input.0.encrypted_content", oldEncryptedContent)
	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"new answer"}]}`),
	}
	items[0], _ = sjson.SetBytes(items[0], "encrypted_content", newEncryptedContent)

	filtered := filterXAIReasoningReplayItemsForInput(body, items)
	if len(filtered) != 0 {
		t.Fatalf("filtered replay items = %q, want none when cached assistant does not match history", filtered)
	}
}

func TestFilterXAIReasoningReplayItemsSkipsDuplicateAssistantMessage(t *testing.T) {
	encryptedContent := testValidGrokEncryptedContentForSeed(11)
	body := []byte(`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}]}`)
	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}`),
	}
	items[0], _ = sjson.SetBytes(items[0], "encrypted_content", encryptedContent)

	filtered := filterXAIReasoningReplayItemsForInput(body, items)
	if len(filtered) != 1 || gjson.GetBytes(filtered[0], "type").String() != "reasoning" {
		t.Fatalf("filtered replay items = %q, want reasoning only", filtered)
	}
}

func TestFilterXAIReasoningReplayItemsRecognizesRoleOnlyAssistantMessage(t *testing.T) {
	encryptedContent := testValidGrokEncryptedContentForSeed(31)
	body := []byte(`{"input":[{"role":"assistant","content":"first answer"},{"role":"user","content":"second"}]}`)
	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}`),
	}
	items[0], _ = sjson.SetBytes(items[0], "encrypted_content", encryptedContent)

	filtered := filterXAIReasoningReplayItemsForInput(body, items)
	if len(filtered) != 1 || gjson.GetBytes(filtered[0], "type").String() != "reasoning" {
		t.Fatalf("filtered replay items = %q, want reasoning only", filtered)
	}
	updated, ok := insertCodexReasoningReplayItems(body, filtered)
	if !ok {
		t.Fatal("insertCodexReasoningReplayItems failed")
	}
	input := gjson.GetBytes(updated, "input").Array()
	if len(input) != 3 || input[0].Get("type").String() != "reasoning" || input[1].Get("role").String() != "assistant" {
		t.Fatalf("unexpected role-only replay order: %s", updated)
	}
	assistantCount := 0
	for _, item := range input {
		if strings.EqualFold(item.Get("role").String(), "assistant") {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("assistant messages after replay = %d, want 1; body=%s", assistantCount, updated)
	}
}

func TestFilterXAIReasoningReplayItemsDoesNotMatchOlderAssistantMessage(t *testing.T) {
	encryptedContent := testValidGrokEncryptedContentForSeed(13)
	body := []byte(`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"different answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)
	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}`),
	}
	items[0], _ = sjson.SetBytes(items[0], "encrypted_content", encryptedContent)

	filtered := filterXAIReasoningReplayItemsForInput(body, items)
	if len(filtered) != 0 {
		t.Fatalf("filtered replay items = %q, want none when the last assistant differs from the cached turn", filtered)
	}
}

// Scenario #3: client already has a last assistant whose text drifts from the
// cached message. The cache cannot safely determine whether this is a trimmed
// older turn or a modified latest turn, so skip the entire cached batch.
func TestFilterXAIReasoningReplayItemsSkipsAmbiguousTurnWhenLastAssistantTextDrifts(t *testing.T) {
	encryptedContent := testValidGrokEncryptedContentForSeed(20)
	body := []byte(`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}]}`)
	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}`),
	}
	items[0], _ = sjson.SetBytes(items[0], "encrypted_content", encryptedContent)

	filtered := filterXAIReasoningReplayItemsForInput(body, items)
	if len(filtered) != 0 {
		t.Fatalf("filtered = %q, want no replay for ambiguous drifted assistant", filtered)
	}
}

// Scenario #2: Claude multi-turn where the client resends older thinking signature
// but drops the latest turn's signature. Cache holds the latest R(+M); upstream
// must receive the latest encrypted blob, not only the older client-provided one.
func TestXAIExecutorClaudeInjectsLatestCachedReasoningWhenHistoryHasOnlyOlderSignature(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	oldEncrypted := testValidGrokEncryptedContentForSeed(21)
	latestEncrypted := testValidGrokEncryptedContentForSeed(22)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_latest","type":"reasoning","summary":[],"encrypted_content":"` + latestEncrypted + `"},"output_index":0}` + "\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"latest answer"}]},"output_index":1}` + "\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"grok-4.5","output":[]}}` + "\n\n"))
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"grok-4.5","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "xai-auth-claude-missing-latest-sig",
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: false}
	ctx := testContextWithAPIKey("claude-missing-sig-key")

	// Turn 1: user only -> cache latest R+M
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","metadata":{"user_id":"{\"session_id\":\"claude-missing-latest\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	// Turn 2 (actual failure shape): client keeps an OLDER thinking signature and the
	// assistant text, but does not resend the latest encrypted/signature blob.
	secondPayload := []byte(`{
		"model":"grok-4.5",
		"metadata":{"user_id":"{\"session_id\":\"claude-missing-latest\"}"},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"older summary","signature":""},
				{"type":"text","text":"latest answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)
	secondPayload, _ = sjson.SetBytes(secondPayload, "messages.1.content.0.signature", oldEncrypted)

	_, err = executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: secondPayload,
	}, opts)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(bodies))
	}

	// Upstream must include BOTH older client signature (as reasoning) and latest cached blob.
	// At minimum the latest cached encrypted_content must be present for continuity.
	second := bodies[1]
	foundLatest := false
	foundOld := false
	assistantCount := 0
	for _, item := range gjson.GetBytes(second, "input").Array() {
		switch item.Get("type").String() {
		case "reasoning":
			enc := item.Get("encrypted_content").String()
			if enc == latestEncrypted {
				foundLatest = true
			}
			if enc == oldEncrypted {
				foundOld = true
			}
		case "message":
			if item.Get("role").String() == "assistant" {
				assistantCount++
			}
		}
	}
	if !foundLatest {
		t.Fatalf("latest cached encrypted_content missing from upstream body (broken Claude missing-signature scenario): %s", second)
	}
	if !foundOld {
		t.Fatalf("older client signature/reasoning missing after translate: %s", second)
	}
	if assistantCount != 1 {
		t.Fatalf("assistant messages = %d, want 1 (no partial double-message inject); body=%s", assistantCount, second)
	}
}

func TestCacheXAIReasoningReplayFromCompletedClearsPreviousEntryWhenNoReplayableState(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	modelName := "grok-4.5"
	sessionKey := "prompt-cache:clear-previous"
	encryptedContent := testValidGrokEncryptedContentForSeed(14)
	previousItems := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":""}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"previous answer"}]}`),
	}
	previousItems[0], _ = sjson.SetBytes(previousItems[0], "encrypted_content", encryptedContent)
	if !internalcache.CacheXAIReasoningReplayItems(modelName, sessionKey, previousItems) {
		t.Fatal("failed to seed xAI reasoning replay cache")
	}

	cacheXAIReasoningReplayFromCompleted(context.Background(), xaiReasoningReplayScope{
		modelName:  modelName,
		sessionKey: sessionKey,
	}, []byte(`{"response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"message without reasoning"}]}]}}`))

	if _, ok := internalcache.GetXAIReasoningReplayItems(modelName, sessionKey); ok {
		t.Fatal("expected previous replay entry to be cleared after non-replayable completed output")
	}
}

func TestXAIReasoningReplayScopeIsolatesOpenAIResponsePromptCacheKeyByAPIKey(t *testing.T) {
	payload := []byte(`{"model":"grok-4.5","prompt_cache_key":"shared-session","input":[]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	req := cliproxyexecutor.Request{Model: "grok-4.5", Payload: payload}

	scopeA := xaiReasoningReplayScopeFromRequest(testContextWithAPIKey("api-key-a"), sdktranslator.FormatOpenAIResponse, req, opts, payload)
	scopeB := xaiReasoningReplayScopeFromRequest(testContextWithAPIKey("api-key-b"), sdktranslator.FormatOpenAIResponse, req, opts, payload)
	if !scopeA.valid() || !scopeB.valid() {
		t.Fatalf("scopes must be valid with caller api keys: A=%+v B=%+v", scopeA, scopeB)
	}
	if scopeA.sessionKey == scopeB.sessionKey {
		t.Fatalf("session keys must differ across callers, both %q", scopeA.sessionKey)
	}
	if !strings.HasPrefix(scopeA.sessionKey, "caller:") || !strings.Contains(scopeA.sessionKey, "prompt-cache:shared-session") {
		t.Fatalf("session key A = %q, want caller-isolated prompt-cache key", scopeA.sessionKey)
	}

	scopeNoKey := xaiReasoningReplayScopeFromRequest(context.Background(), sdktranslator.FormatOpenAIResponse, req, opts, payload)
	if scopeNoKey.valid() {
		t.Fatalf("OpenAI Responses without caller API key must disable replay: %+v", scopeNoKey)
	}
}

func TestXAIReasoningReplayScopeDisablesClaudeWithoutAPIKey(t *testing.T) {
	payload := []byte(`{"model":"grok-4.3","metadata":{"user_id":"{\"session_id\":\"shared-session\"}"},"messages":[{"role":"user","content":"hello"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: payload}

	scopeNoKey := xaiReasoningReplayScopeFromRequest(context.Background(), sdktranslator.FormatClaude, req, opts, payload)
	if scopeNoKey.valid() {
		t.Fatalf("Claude without caller API key must disable replay: %+v", scopeNoKey)
	}

	scopeWithKey := xaiReasoningReplayScopeFromRequest(testContextWithAPIKey("api-key-a"), sdktranslator.FormatClaude, req, opts, payload)
	if !scopeWithKey.valid() {
		t.Fatal("Claude with caller API key must enable replay")
	}
	if !strings.HasPrefix(scopeWithKey.sessionKey, "caller:") || !strings.Contains(scopeWithKey.sessionKey, "claude:shared-session") {
		t.Fatalf("session key = %q, want caller-isolated Claude session key", scopeWithKey.sessionKey)
	}
}

func TestXAIReasoningReplayScopeAllowsTrustedExecutionSessionWithoutAPIKey(t *testing.T) {
	payload := []byte(`{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}]}`)
	scope := xaiReasoningReplayScopeFromRequest(context.Background(), sdktranslator.FormatClaude, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "trusted-session",
		},
	}, payload)
	if !scope.valid() {
		t.Fatal("trusted execution session must remain replayable without caller API key")
	}
	if scope.sessionKey != "execution:trusted-session" {
		t.Fatalf("session key = %q, want execution:trusted-session", scope.sessionKey)
	}
}

func TestXAIReasoningReplayScopeSkipsIncrementalWebsocketPreviousResponse(t *testing.T) {
	scope := xaiReasoningReplayScopeFromRequest(
		cliproxyexecutor.WithDownstreamWebsocket(context.Background()),
		sdktranslator.FormatOpenAIResponse,
		cliproxyexecutor.Request{
			Model:   "grok-4.5",
			Payload: []byte(`{"model":"grok-4.5","previous_response_id":"resp_1","prompt_cache_key":"codex-ws-session","input":[]}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse},
		[]byte(`{"model":"grok-4.5","prompt_cache_key":"codex-ws-session","input":[]}`),
	)
	if scope.valid() {
		t.Fatalf("incremental websocket request must not enable cache replay: %+v", scope)
	}
}

func TestApplyXAIReasoningReplayCacheFallsBackWhenReadFails(t *testing.T) {
	previous := getXAIReasoningReplayItemsRequired
	getXAIReasoningReplayItemsRequired = func(context.Context, string, string) ([][]byte, bool, error) {
		return nil, false, errors.New("cache unavailable")
	}
	t.Cleanup(func() {
		getXAIReasoningReplayItemsRequired = previous
	})

	body := []byte(`{"model":"grok-4.3","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	updated, scope, err := applyXAIReasoningReplayCacheRequired(context.Background(), sdktranslator.FormatClaude, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "xai-read-error",
		},
	}, body)
	if err != nil {
		t.Fatalf("applyXAIReasoningReplayCacheRequired() error = %v", err)
	}
	if !scope.valid() {
		t.Fatalf("replay scope should remain valid")
	}
	if string(updated) != string(body) {
		t.Fatalf("body changed on cache read error: %s", string(updated))
	}
}

func TestXAIReasoningReplayCacheReplaysFunctionCallWithoutReasoning(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	const executionSessionID = "xai-tool-call-only"
	cacheXAIReasoningReplayFromCompleted(context.Background(), xaiReasoningReplayScope{
		modelName:  "grok-4.3",
		sessionKey: "execution:" + executionSessionID,
	}, []byte(`{"response":{"output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}"}]}}`))

	body := []byte(`{"model":"grok-4.3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"call lookup"}]},{"type":"function_call_output","call_id":"call_1","output":"sunny"}]}`)
	updated, scope, errReplay := applyXAIReasoningReplayCacheRequired(context.Background(), sdktranslator.FormatClaude, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: executionSessionID,
		},
	}, body)
	if errReplay != nil {
		t.Fatalf("applyXAIReasoningReplayCacheRequired() error = %v", errReplay)
	}
	if !scope.valid() {
		t.Fatal("tool-call-only replay scope must remain valid")
	}
	input := gjson.GetBytes(updated, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3; body=%s", len(input), updated)
	}
	wantTypes := []string{"message", "function_call", "function_call_output"}
	for i, wantType := range wantTypes {
		if got := input[i].Get("type").String(); got != wantType {
			t.Fatalf("input.%d.type = %q, want %q; body=%s", i, got, wantType, updated)
		}
	}
	if got := input[1].Get("call_id").String(); got != "call_1" {
		t.Fatalf("replayed call_id = %q, want call_1; body=%s", got, updated)
	}
}

func TestXAIExecutorReasoningReplayCacheReplaysFunctionCallForClaudeToolResult(t *testing.T) {
	internalcache.ClearXAIReasoningReplayCache()
	t.Cleanup(internalcache.ClearXAIReasoningReplayCache)

	reasoningEncryptedContent := testValidGrokEncryptedContentForSeed(3)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"` + reasoningEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}","status":"in_progress"},"output_index":1}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}","status":"completed"},"output_index":1}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"grok-4.3","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-replay-tool",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"access_token": "xai-token",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       false,
	}
	ctx := testContextWithAPIKey("xai-tool-replay-caller")

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "grok-4.3",
		Payload: []byte(`{
			"model":"grok-4.3",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"xai-session-tool\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"call lookup"}]}],
			"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
		}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "grok-4.3",
		Payload: []byte(`{
			"model":"grok-4.3",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"xai-session-tool\"}"},
			"messages":[
				{"role":"user","content":[{"type":"text","text":"call lookup"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]}
			],
			"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
		}`),
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want initial user message; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.1.type").String(); got != "reasoning" {
		t.Fatalf("input.1.type = %q, want cached reasoning; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.2.type").String(); got != "function_call" {
		t.Fatalf("input.2.type = %q, want cached function_call; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.2.call_id").String(); got != "call_1" {
		t.Fatalf("input.2.call_id = %q, want call_1; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.3.type").String(); got != "function_call_output" {
		t.Fatalf("input.3.type = %q, want function_call_output after cached call; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.3.call_id").String(); got != "call_1" {
		t.Fatalf("input.3.call_id = %q, want call_1; body=%s", got, string(secondBody))
	}
}

func TestXAIBaseURLSource(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "default api", baseURL: xaiauth.DefaultAPIBaseURL, want: "DefaultAPIBaseURL"},
		{name: "default api trailing slash", baseURL: xaiauth.DefaultAPIBaseURL + "/", want: "DefaultAPIBaseURL"},
		{name: "cli chat proxy", baseURL: xaiauth.CLIChatProxyBaseURL, want: "CLIChatProxyBaseURL"},
		{name: "cli chat proxy trailing slash", baseURL: xaiauth.CLIChatProxyBaseURL + "/", want: "CLIChatProxyBaseURL"},
		{name: "custom", baseURL: "https://gateway.example.com/v1", want: "custom"},
		{name: "empty treated as custom", baseURL: "", want: "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xaiBaseURLSource(tt.baseURL); got != tt.want {
				t.Fatalf("xaiBaseURLSource(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestXAIChatBaseURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth defaults to official api",
			auth: nil,
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "empty base url defaults to official api without using_api",
			auth: &cliproxyauth.Auth{Provider: "xai"},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "official default stays official without using_api",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"base_url": xaiauth.DefaultAPIBaseURL},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "OAuth credentials default to chat proxy without using_api",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"base_url":  xaiauth.DefaultAPIBaseURL,
				},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "metadata-only OAuth credentials default to chat proxy without using_api",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"auth_kind": "oauth",
					"base_url":  xaiauth.DefaultAPIBaseURL,
				},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "using_api false empty base url rewrites to chat proxy",
			auth: &cliproxyauth.Auth{
				Provider:   "xai",
				Attributes: map[string]string{xaiUsingAPIAttr: "false"},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "using_api false official default rewrites to chat proxy",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":      xaiauth.DefaultAPIBaseURL,
					xaiUsingAPIAttr: "false",
				},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "using_api false official default with trailing slash rewrites to chat proxy",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":      xaiauth.DefaultAPIBaseURL + "/",
					xaiUsingAPIAttr: "false",
				},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "metadata using_api false official default rewrites to chat proxy",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"base_url":      xaiauth.DefaultAPIBaseURL,
					xaiUsingAPIAttr: false,
				},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "using_api false custom base url is honored",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":      "https://gateway.example.com/v1",
					xaiUsingAPIAttr: "false",
				},
			},
			want: "https://gateway.example.com/v1",
		},
		{
			name: "custom base url is honored without using_api",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"base_url": "https://gateway.example.com/v1"},
			},
			want: "https://gateway.example.com/v1",
		},
		{
			name: "using_api false explicit chat proxy base url is preserved",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":      xaiauth.CLIChatProxyBaseURL,
					xaiUsingAPIAttr: "false",
				},
			},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "using_api true keeps official api",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":      xaiauth.DefaultAPIBaseURL,
					xaiUsingAPIAttr: "true",
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "OAuth using_api true keeps official api",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"auth_kind":     "oauth",
					"base_url":      xaiauth.DefaultAPIBaseURL,
					xaiUsingAPIAttr: "true",
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xaiChatBaseURL(tt.auth); got != tt.want {
				t.Fatalf("xaiChatBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestXAICompactBaseURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "empty base url defaults to official api",
			auth: &cliproxyauth.Auth{Provider: "xai"},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "OAuth official default stays on official api for compact",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"base_url":  xaiauth.DefaultAPIBaseURL,
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "metadata OAuth official default stays on official api for compact",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"auth_kind": "oauth",
					"base_url":  xaiauth.DefaultAPIBaseURL,
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "using_api false official default stays on official api for compact",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":      xaiauth.DefaultAPIBaseURL,
					xaiUsingAPIAttr: "false",
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "explicit CLI chat proxy is rewritten to official api for compact",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"base_url":  xaiauth.CLIChatProxyBaseURL,
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "explicit CLI chat proxy trailing slash is rewritten",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url": xaiauth.CLIChatProxyBaseURL + "/",
				},
			},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "custom gateway is honored for compact",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"base_url":  "https://gateway.example.com/v1",
				},
			},
			want: "https://gateway.example.com/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := xaiCompactBaseURL(tt.auth)
			if got != tt.want {
				t.Fatalf("xaiCompactBaseURL() = %q, want %q", got, tt.want)
			}
			// Chat may still rewrite OAuth defaults to CLI proxy; compact must not.
			chat := xaiChatBaseURL(tt.auth)
			if xaiIsCLIChatProxyBaseURL(chat) && xaiIsCLIChatProxyBaseURL(got) {
				t.Fatalf("compact base unexpectedly pinned to CLI chat proxy: chat=%q compact=%q", chat, got)
			}
		})
	}
}

func TestApplyXAIChatHeaders(t *testing.T) {
	t.Run("non OAuth defaults to official API headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://example.invalid/responses", nil)
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{"base_url": xaiauth.DefaultAPIBaseURL},
		}
		applyXAIChatHeaders(req, auth, "xai-token", true, "conv-1")

		if got := req.Header.Get("Authorization"); got != "Bearer xai-token" {
			t.Fatalf("Authorization = %q, want Bearer xai-token", got)
		}
		if got := req.Header.Get("x-grok-conv-id"); got != "conv-1" {
			t.Fatalf("x-grok-conv-id = %q, want conv-1", got)
		}
		if got := req.Header.Get(xaiTokenAuthHeader); got != "" {
			t.Fatalf("%s = %q, want empty for official API", xaiTokenAuthHeader, got)
		}
		if got := req.Header.Get(xaiClientVersionHeader); got != "" {
			t.Fatalf("%s = %q, want empty for official API", xaiClientVersionHeader, got)
		}
		if got := req.Header.Get("User-Agent"); got != "" {
			t.Fatalf("User-Agent = %q, want empty for official API", got)
		}
	})

	t.Run("OAuth defaults to cli chat proxy headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://example.invalid/responses", nil)
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  xaiauth.DefaultAPIBaseURL,
			},
		}
		applyXAIChatHeaders(req, auth, "xai-token", true, "conv-1")

		if got := req.Header.Get("Authorization"); got != "Bearer xai-token" {
			t.Fatalf("Authorization = %q, want Bearer xai-token", got)
		}
		if got := req.Header.Get("x-grok-conv-id"); got != "conv-1" {
			t.Fatalf("x-grok-conv-id = %q, want conv-1", got)
		}
		if got := req.Header.Get(xaiTokenAuthHeader); got != xaiTokenAuthValue {
			t.Fatalf("%s = %q, want %q", xaiTokenAuthHeader, got, xaiTokenAuthValue)
		}
		if got := req.Header.Get(xaiClientVersionHeader); got != xaiClientVersionValue {
			t.Fatalf("%s = %q, want %q", xaiClientVersionHeader, got, xaiClientVersionValue)
		}
		if got := req.Header.Get("User-Agent"); got != "xai-grok-workspace/"+xaiClientVersionValue {
			t.Fatalf("User-Agent = %q, want xai-grok-workspace/%s", got, xaiClientVersionValue)
		}
	})

	t.Run("no cli headers on custom gateway with using_api false", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://gateway.example.com/responses", nil)
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"base_url":      "https://gateway.example.com/v1",
				xaiUsingAPIAttr: "false",
			},
		}
		applyXAIChatHeaders(req, auth, "xai-token", false, "")

		if got := req.Header.Get(xaiTokenAuthHeader); got != "" {
			t.Fatalf("%s = %q, want empty for custom gateway", xaiTokenAuthHeader, got)
		}
		if got := req.Header.Get(xaiClientVersionHeader); got != "" {
			t.Fatalf("%s = %q, want empty for custom gateway", xaiClientVersionHeader, got)
		}
		if got := req.Header.Get("User-Agent"); got != "" {
			t.Fatalf("User-Agent = %q, want empty for custom gateway", got)
		}
	})

	t.Run("custom headers override cli chat proxy defaults", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, xaiauth.CLIChatProxyBaseURL+"/responses", nil)
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"base_url":                         xaiauth.CLIChatProxyBaseURL,
				xaiUsingAPIAttr:                    "false",
				"header:" + xaiTokenAuthHeader:     "custom-token-auth",
				"header:" + xaiClientVersionHeader: "custom-client-version",
			},
		}
		applyXAIChatHeaders(req, auth, "xai-token", true, "")

		if got := req.Header.Get(xaiTokenAuthHeader); got != "custom-token-auth" {
			t.Fatalf("%s = %q, want custom-token-auth", xaiTokenAuthHeader, got)
		}
		if got := req.Header.Get(xaiClientVersionHeader); got != "custom-client-version" {
			t.Fatalf("%s = %q, want custom-client-version", xaiClientVersionHeader, got)
		}
	})

	t.Run("cli headers on explicit chat proxy base with using_api false", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, xaiauth.CLIChatProxyBaseURL+"/responses", nil)
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"base_url":      xaiauth.CLIChatProxyBaseURL + "/",
				xaiUsingAPIAttr: "false",
			},
		}
		applyXAIChatHeaders(req, auth, "xai-token", true, "")

		if got := req.Header.Get(xaiTokenAuthHeader); got != xaiTokenAuthValue {
			t.Fatalf("%s = %q, want %q", xaiTokenAuthHeader, got, xaiTokenAuthValue)
		}
		if got := req.Header.Get(xaiClientVersionHeader); got != xaiClientVersionValue {
			t.Fatalf("%s = %q, want %q", xaiClientVersionHeader, got, xaiClientVersionValue)
		}
	})
}

func TestXAIExecutorExecuteChatUsesProxyHeadersOnlyForChatProxy(t *testing.T) {
	var gotTokenAuth string
	var gotClientVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		gotClientVersion = r.Header.Get(xaiClientVersionHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":      server.URL,
			xaiUsingAPIAttr: "false",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotTokenAuth != "" {
		t.Fatalf("%s = %q, want empty for custom chat gateway", xaiTokenAuthHeader, gotTokenAuth)
	}
	if gotClientVersion != "" {
		t.Fatalf("%s = %q, want empty for custom chat gateway", xaiClientVersionHeader, gotClientVersion)
	}
}

func testValidGrokEncryptedContentForSeed(seed byte) string {
	buf := make([]byte, 0, 256)
	for i := 0; len(buf) < 256; i++ {
		sum := sha256.Sum256([]byte{seed, byte(i), byte(i >> 8), byte(i >> 16)})
		buf = append(buf, sum[:]...)
	}
	return base64.RawStdEncoding.EncodeToString(buf[:256])
}

func testValidGrokEncryptedContent() string {
	buf := make([]byte, 0, 256)
	for i := 0; len(buf) < 256; i++ {
		sum := sha256.Sum256([]byte{byte(i), byte(i >> 8), byte(i >> 16)})
		buf = append(buf, sum[:]...)
	}
	return base64.RawStdEncoding.EncodeToString(buf[:256])
}
