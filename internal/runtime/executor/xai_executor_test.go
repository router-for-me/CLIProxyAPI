package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

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
		Payload: []byte(`{"model":"grok-4.3","input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"test"}],"content":null,"encrypted_content":null},{"type":"reasoning","summary":[{"type":"summary_text","text":"second"}]},{"role":"user","content":"hello"}],"include":["reasoning.encrypted_content"],"reasoning":{"effort":"high"},"tools":[{"type":"tool_search"},{"type":"image_generation"},{"type":"custom","name":"apply_patch"},{"type":"custom","name":"custom_lookup"},{"type":"function","name":"lookup"},{"type":"web_search","external_web_access":true,"search_content_types":["text","image"]},{"type":"namespace","name":"codex_app","description":"Tools in the codex_app namespace.","tools":[{"type":"function","name":"automation_update"},{"type":"custom","name":"namespace_custom"},{"type":"tool_search"}]}]}`),
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
	if len(tools) != 5 {
		t.Fatalf("tools length = %d, want 5; body=%s", len(tools), string(gotBody))
	}
	foundAutomationUpdate := false
	foundNamespaceCustom := false
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		if toolType == "image_generation" {
			t.Fatalf("tools.%d.type = image_generation, want removed; body=%s", i, string(gotBody))
		}
		if toolType != "function" && toolType != "web_search" {
			t.Fatalf("tools.%d.type = %q, want function or web_search; body=%s", i, toolType, string(gotBody))
		}
		if toolType == "function" && !tool.Get("parameters").Exists() {
			t.Fatalf("tools.%d.parameters missing for xAI function tool; body=%s", i, string(gotBody))
		}
		if got := tool.Get("name").String(); got == "apply_patch" {
			t.Fatalf("tools.%d.name = apply_patch, want removed; body=%s", i, string(gotBody))
		}
		switch tool.Get("name").String() {
		case "automation_update":
			foundAutomationUpdate = true
		case "namespace_custom":
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
	for _, include := range gjson.GetBytes(gotBody, "include").Array() {
		if include.String() == "reasoning.encrypted_content" {
			t.Fatalf("xai request must not ask for encrypted reasoning content: %s", string(gotBody))
		}
	}
}

func TestXAIExecutorCompactUsesCompactEndpoint(t *testing.T) {
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

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","stream":true,"input":[{"type":"compaction","encrypted_content":"opaque-in"},{"role":"user","content":"hello"}]}`),
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
	if got := gjson.GetBytes(gotBody, "input.0.encrypted_content").String(); got != "opaque-in" {
		t.Fatalf("input.0.encrypted_content = %q, want opaque-in; body=%s", got, string(gotBody))
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque-out"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
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
	if len(tools) != 5 {
		t.Fatalf("tools length = %d, want 5; body=%s", len(tools), string(gotBody))
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
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		if toolType == "image_generation" {
			t.Fatalf("tools.%d.type = image_generation, want removed; body=%s", i, string(gotBody))
		}
		if toolType != "function" && toolType != "web_search" {
			t.Fatalf("tools.%d.type = %q, want function or web_search; body=%s", i, toolType, string(gotBody))
		}
		if toolType == "function" && !tool.Get("parameters").Exists() {
			t.Fatalf("tools.%d.parameters missing for xAI function tool; body=%s", i, string(gotBody))
		}
		if got := tool.Get("name").String(); got == "apply_patch" {
			t.Fatalf("tools.%d.name = apply_patch, want removed; body=%s", i, string(gotBody))
		}
		switch tool.Get("name").String() {
		case "automation_update":
			foundAutomationUpdate = true
		case "namespace_custom":
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

func TestXAIExecutorExecuteImagesUsesImagesEndpoint(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}]}`))
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

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-imagine-image",
		Payload: []byte(`{"model":"grok-imagine-image","prompt":"draw"}`),
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
	if string(gotBody) != `{"model":"grok-imagine-image","prompt":"draw"}` {
		t.Fatalf("body = %s", string(gotBody))
	}
	if gjson.GetBytes(resp.Payload, "data.0.b64_json").String() != "AA==" {
		t.Fatalf("payload = %s", string(resp.Payload))
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

func TestNormalizeXAIToolChoiceForTools_NoOpWhenBothAbsent(t *testing.T) {
	body := []byte(`{"model":"grok-4","input":"hi"}`)
	out := normalizeXAIToolChoiceForTools(body)

	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice should not appear: %s", string(out))
	}
}
