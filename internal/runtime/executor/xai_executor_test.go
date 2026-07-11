package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
	if gotTokenAuth != "" {
		t.Fatalf("%s = %q, want empty on media path", xaiTokenAuthHeader, gotTokenAuth)
	}
	if gotClientVersion != "" {
		t.Fatalf("%s = %q, want empty on media path", xaiClientVersionHeader, gotClientVersion)
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

func TestNormalizeXAITools_SimplifiesCodexAppAutomationUpdateSchema(t *testing.T) {
	// Large oneOf+$ref schema mimicking Codex Desktop codex_app.automation_update.
	params := `{"oneOf":[{"type":"object","properties":{"mode":{"type":"string"}}}],"$defs":{"a":{"type":"string"}},"x":"` + strings.Repeat("y", 1600) + `"}`
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
		case "automation_update":
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
	custom := gjson.Parse(`{"type":"custom","name":"automation_update","parameters":{"type":"object"}}`)
	if xaiFunctionParametersNeedSimplification(custom, "codex_app") {
		t.Fatal("custom codex_app.automation_update should not need simplification")
	}
	safe := gjson.Parse(`{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}`)
	if xaiFunctionParametersNeedSimplification(safe, "codex_app") {
		t.Fatal("unrelated codex_app function should not need simplification")
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

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"xai-session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
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

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
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

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
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

func TestNormalizeXAIInputItems_PromotesCodexAdditionalTools(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.5",
		"tool_choice":"auto",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"custom","name":"exec","description":"run","format":{"type":"grammar","syntax":"lark","definition":"start: SOURCE"}},
				{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}},
				{"type":"namespace","name":"collaboration","tools":[
					{"type":"function","name":"spawn_agent","parameters":{"type":"object","properties":{"prompt":{"type":"string"}}}}
				]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		]
	}`)

	// Mirror prepareResponsesRequest ordering for the xAI path.
	out := normalizeXAIToolChoiceForTools(normalizeXAITools(normalizeXAIInputItems(body)))

	if gjson.GetBytes(out, "input.#").Int() != 1 {
		t.Fatalf("input should drop additional_tools item: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want message: %s", got, string(out))
	}

	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() {
		t.Fatalf("tools missing after promote: %s", string(out))
	}
	names := map[string]bool{}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			t.Fatalf("tool type = %q, want function: %s", tool.Get("type").String(), tool.Raw)
		}
		if tool.Get("format").Exists() {
			t.Fatalf("custom format should be stripped: %s", tool.Raw)
		}
		if !tool.Get("parameters").Exists() {
			t.Fatalf("function parameters missing: %s", tool.Raw)
		}
		names[tool.Get("name").String()] = true
		if tool.Get("name").String() == "exec" {
			if got := tool.Get("parameters.properties.input.type").String(); got != "string" {
				t.Fatalf("exec freeform input type = %q, want string: %s", got, tool.Raw)
			}
		}
	}
	for _, want := range []string{"exec", "wait", "spawn_agent"} {
		if !names[want] {
			t.Fatalf("tool %q missing after promote/normalize: %s", want, string(out))
		}
	}
	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto with promoted tools: %s", got, string(out))
	}
	// original custom names must be collectable before rewrite
	customNames := collectXAIOriginalCustomToolNames(body)
	if _, ok := customNames["exec"]; !ok {
		t.Fatalf("collectXAIOriginalCustomToolNames missing exec: %#v", customNames)
	}
}

func TestRemapXAICustomToolCallsInPayload_OnlyCustomNames(t *testing.T) {
	custom := map[string]struct{}{"exec": {}}
	payload := []byte(`{
		"type":"response.completed",
		"response":{"output":[
			{"type":"function_call","name":"exec","call_id":"c1","arguments":"{\"input\":\"console.log(1)\"}"},
			{"type":"function_call","name":"wait","call_id":"c2","arguments":"{\"seconds\":1}"}
		]}
	}`)
	out := remapXAICustomToolCallsInPayload(payload, custom)
	if got := gjson.GetBytes(out, "response.output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("exec type = %q, want custom_tool_call: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "response.output.0.input").String(); got != "console.log(1)" {
		t.Fatalf("exec input = %q, want source: %s", got, string(out))
	}
	if gjson.GetBytes(out, "response.output.0.arguments").Exists() {
		t.Fatalf("exec arguments should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "response.output.1.type").String(); got != "function_call" {
		t.Fatalf("wait type should stay function_call, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "response.output.1.arguments").String(); got != `{"seconds":1}` {
		t.Fatalf("wait arguments mutated: %s", string(out))
	}
}

func TestRemapXAICustomToolCallsInPayload_NoopWithoutCustomNames(t *testing.T) {
	payload := []byte(`{"item":{"type":"function_call","name":"exec","arguments":"{\"input\":\"x\"}"}}`)
	out := remapXAICustomToolCallsInPayload(payload, nil)
	if string(out) != string(payload) {
		t.Fatalf("expected no-op without custom names, got %s", string(out))
	}
}

func TestNormalizeXAIInputItems_DropsItemReference(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[
		{"role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"item_reference","id":"rs_abc"},
		{"type":"item_reference","id":"fc_abc_0"},
		{"type":"function_call_output","call_id":"call-1","output":"done"},
		{"role":"user","content":[{"type":"input_text","text":"continue"}]}
	]}`)
	out := normalizeXAIInputItems(body)

	// normalize alone only strips item_reference; orphan outputs are handled by
	// pruneXAIOrphanToolOutputs after reasoning-replay in prepareResponsesRequestTo.
	if gjson.GetBytes(out, "input.#").Int() != 3 {
		t.Fatalf("item_reference items should be dropped: %s", string(out))
	}
	for _, item := range gjson.GetBytes(out, "input").Array() {
		if item.Get("type").String() == "item_reference" {
			t.Fatalf("item_reference remained: %s", string(out))
		}
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_call_output" {
		t.Fatalf("function_call_output should remain after normalize-only: %s", string(out))
	}
}

func TestPruneXAIOrphanToolOutputs_DropsUnmatchedOutputs(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[
		{"role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"function_call","call_id":"call-keep","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","call_id":"call-keep","output":"ok"},
		{"type":"function_call_output","call_id":"call-orphan","output":"gone"},
		{"type":"custom_tool_call_output","call_id":"c-orphan","output":"gone2"},
		{"role":"user","content":[{"type":"input_text","text":"continue"}]}
	]}`)
	out := pruneXAIOrphanToolOutputs(body)
	if gjson.GetBytes(out, "input.#").Int() != 4 {
		t.Fatalf("want 4 items after prune, got %d: %s", gjson.GetBytes(out, "input.#").Int(), string(out))
	}
	for _, item := range gjson.GetBytes(out, "input").Array() {
		typeName := item.Get("type").String()
		if typeName != "function_call_output" && typeName != "custom_tool_call_output" {
			continue
		}
		if item.Get("call_id").String() != "call-keep" {
			t.Fatalf("unexpected output remained: %s", item.Raw)
		}
	}
}

func TestPruneXAIOrphanToolOutputs_AfterItemReferenceDrop(t *testing.T) {
	// Simulate prepare order: normalize (drop refs) then prune (drop orphans).
	body := []byte(`{"model":"grok-4.5","input":[
		{"role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"item_reference","id":"fc_abc_0"},
		{"type":"function_call_output","call_id":"call-1","output":"done"},
		{"role":"user","content":[{"type":"input_text","text":"continue"}]}
	]}`)
	out := pruneXAIOrphanToolOutputs(normalizeXAIInputItems(body))
	if gjson.GetBytes(out, "input.#").Int() != 2 {
		t.Fatalf("want only user messages, got %s", string(out))
	}
	for _, item := range gjson.GetBytes(out, "input").Array() {
		if strings.Contains(item.Get("type").String(), "function_call") {
			t.Fatalf("tool items should be gone: %s", string(out))
		}
	}
}

func TestPruneXAIOrphanToolOutputs_KeepsWhenCallPresent(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[
		{"type":"function_call","call_id":"c1","name":"x","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"y"}
	]}`)
	out := pruneXAIOrphanToolOutputs(body)
	if string(out) != string(body) && gjson.GetBytes(out, "input.#").Int() != 2 {
		t.Fatalf("should keep matched pair: %s", string(out))
	}
}

func TestNormalizeXAIInputItems_ConvertsCustomToolCalls(t *testing.T) {
	body := []byte(`{"model":"grok-4","input":[
		{"type":"custom_tool_call","call_id":"c0","name":"ApplyPatch","input":"*** Begin Patch"},
		{"type":"custom_tool_call_output","call_id":"c0","output":"done"},
		{"type":"function_call","call_id":"f0","name":"lookup","arguments":"{}"}
	]}`)
	out := normalizeXAIInputItems(body)

	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.input").Exists() {
		t.Fatalf("custom input field should be removed: %s", string(out))
	}
	arguments := gjson.GetBytes(out, "input.0.arguments").String()
	if got := gjson.Get(arguments, "input").String(); got != "*** Begin Patch" {
		t.Fatalf("arguments.input = %q, want patch text; arguments=%s body=%s", got, arguments, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_call_output" {
		t.Fatalf("input.1.type = %q, want function_call_output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.type").String(); got != "function_call" {
		t.Fatalf("existing function_call should be preserved: %s", string(out))
	}
}

func TestNormalizeXAIInputItems_FlattensFunctionCallOutputArray(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[
		{"type":"function_call_output","call_id":"call-1","output":[
			{"type":"input_text","text":"hello "},
			{"type":"input_text","text":"world"}
		]}
	]}`)
	out := normalizeXAIInputItems(body)

	if got := gjson.GetBytes(out, "input.0.output").String(); got != "hello world" {
		t.Fatalf("output = %q, want flattened string: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.output").IsArray() {
		t.Fatalf("output should be string, not array: %s", string(out))
	}
}

func TestNormalizeXAIInputItems_MergesAdditionalToolsWithExistingTools(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.5",
		"tools":[{"type":"function","name":"Bash","parameters":{"type":"object","properties":{}}}],
		"input":[
			{"type":"additional_tools","tools":[{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}}]},
			{"type":"message","role":"user","content":"hi"}
		]
	}`)
	out := normalizeXAIInputItems(body)
	names := map[string]bool{}
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		names[tool.Get("name").String()] = true
	}
	if !names["Bash"] || !names["wait"] {
		t.Fatalf("expected both existing and promoted tools, got %v body=%s", names, string(out))
	}
}

func TestInjectXAIBuildCacheTools_DisabledByDefault(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}}],"input":"hi"}`)
	out := injectXAIBuildCacheTools(&config.Config{}, body)
	if gjson.GetBytes(out, "tools.#").Int() != 1 {
		t.Fatalf("tools count = %d, want 1; body=%s", gjson.GetBytes(out, "tools.#").Int(), out)
	}
	if gjson.GetBytes(out, "tools.0.type").String() != "function" {
		t.Fatalf("tools.0.type = %q, want function", gjson.GetBytes(out, "tools.0.type").String())
	}
}

func TestInjectXAIBuildCacheTools_PrependsMissingSearchTools(t *testing.T) {
	cfg := &config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}
	body := []byte(`{"model":"grok-4.5","tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}}],"input":"hi"}`)
	out := injectXAIBuildCacheTools(cfg, body)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 3 {
		t.Fatalf("tools count = %d, want 3; body=%s", got, out)
	}
	if gjson.GetBytes(out, "tools.0.type").String() != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; body=%s", gjson.GetBytes(out, "tools.0.type").String(), out)
	}
	if gjson.GetBytes(out, "tools.1.type").String() != "x_search" {
		t.Fatalf("tools.1.type = %q, want x_search; body=%s", gjson.GetBytes(out, "tools.1.type").String(), out)
	}
	if gjson.GetBytes(out, "tools.2.name").String() != "exec" {
		t.Fatalf("tools.2.name = %q, want exec; body=%s", gjson.GetBytes(out, "tools.2.name").String(), out)
	}
}

func TestInjectXAIBuildCacheTools_NoDuplicateWhenPresent(t *testing.T) {
	cfg := &config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}
	body := []byte(`{"tools":[{"type":"web_search"},{"type":"x_search"},{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}}]}`)
	out := injectXAIBuildCacheTools(cfg, body)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 3 {
		t.Fatalf("tools count = %d, want 3; body=%s", got, out)
	}
}

func TestInjectXAIBuildCacheTools_CreatesToolsWhenMissing(t *testing.T) {
	cfg := &config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}
	body := []byte(`{"model":"grok-4.5","input":"hi"}`)
	out := injectXAIBuildCacheTools(cfg, body)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 2 {
		t.Fatalf("tools count = %d, want 2; body=%s", got, out)
	}
}

func TestFilterXAIInjectedServerToolPayload_DropsSearchCalls(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"output":[{"type":"reasoning"},{"type":"web_search_call"},{"type":"message","role":"assistant"}]}}`)
	out := filterXAIInjectedServerToolPayload(data)
	arr := gjson.GetBytes(out, "response.output")
	if !arr.IsArray() || len(arr.Array()) != 2 {
		t.Fatalf("output = %s, want 2 items", arr.Raw)
	}
	if arr.Array()[0].Get("type").String() != "reasoning" || arr.Array()[1].Get("type").String() != "message" {
		t.Fatalf("output types unexpected: %s", arr.Raw)
	}
}

func TestFilterXAIInjectedServerToolPayload_DropsOutputItemEvent(t *testing.T) {
	data := []byte(`{"type":"response.output_item.done","item":{"type":"web_search_call"}}`)
	out := filterXAIInjectedServerToolPayload(data)
	if out != nil {
		t.Fatalf("want nil dropped event, got %s", out)
	}
}

func TestInjectXAIBuildCacheTools_StripsFunctionNamedWebSearch(t *testing.T) {
	cfg := &config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}
	body := []byte(`{"tools":[{"type":"function","name":"web_search","parameters":{"type":"object","properties":{}}},{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}}]}`)
	out := injectXAIBuildCacheTools(cfg, body)
	// native web, native x, exec — function web_search stripped (xAI name uniqueness)
	if gjson.GetBytes(out, "tools.#").Int() != 3 {
		t.Fatalf("tools.# = %d, want 3; body=%s", gjson.GetBytes(out, "tools.#").Int(), out)
	}
	if gjson.GetBytes(out, "tools.0.type").String() != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; body=%s", gjson.GetBytes(out, "tools.0.type").String(), out)
	}
	if gjson.GetBytes(out, "tools.1.type").String() != "x_search" {
		t.Fatalf("tools.1.type = %q, want x_search; body=%s", gjson.GetBytes(out, "tools.1.type").String(), out)
	}
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		if tool.Get("type").String() == "function" && tool.Get("name").String() == "web_search" {
			t.Fatalf("function web_search still present: %s", out)
		}
	}
}

func TestInjectXAIBuildCacheTools_StripsCustomNamedXSearch(t *testing.T) {
	cfg := &config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}
	body := []byte(`{"tools":[{"type":"custom","name":"x_search"},{"type":"web_search"},{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}}]}`)
	out := injectXAIBuildCacheTools(cfg, body)
	// web native kept, custom x_search stripped, native x injected, exec kept => 3
	if gjson.GetBytes(out, "tools.#").Int() != 3 {
		t.Fatalf("tools.# = %d, want 3; body=%s", gjson.GetBytes(out, "tools.#").Int(), out)
	}
	types := map[string]int{}
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		types[tool.Get("type").String()+"|"+tool.Get("name").String()]++
	}
	if types["web_search|"] != 1 || types["x_search|"] != 1 || types["function|exec"] != 1 {
		t.Fatalf("unexpected tools: %v body=%s", types, out)
	}
	if types["custom|x_search"] != 0 {
		t.Fatalf("custom x_search should be stripped: %s", out)
	}
}

func TestXAIShouldHideInjectedSearchResults(t *testing.T) {
	if xaiShouldHideInjectedSearchResults(nil) {
		t.Fatal("nil cfg")
	}
	if xaiShouldHideInjectedSearchResults(&config.Config{}) {
		t.Fatal("defaults must not hide")
	}
	if xaiShouldHideInjectedSearchResults(&config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}) {
		t.Fatal("inject alone must not hide (cache + real search coexistence)")
	}
	if !xaiShouldHideInjectedSearchResults(&config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true, HideInjectedSearchResults: true}}) {
		t.Fatal("inject+hide must hide")
	}
	if xaiShouldHideInjectedSearchResults(&config.Config{XAI: config.XAIConfig{HideInjectedSearchResults: true}}) {
		t.Fatal("hide without inject must not hide")
	}
}

func TestInjectXAIBuildCacheTools_DedupesNativeDuplicates(t *testing.T) {
	cfg := &config.Config{XAI: config.XAIConfig{InjectBuildSearchTools: true}}
	body := []byte(`{"tools":[{"type":"web_search"},{"type":"web_search"},{"type":"x_search"}]}`)
	out := injectXAIBuildCacheTools(cfg, body)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 2 {
		t.Fatalf("tools count = %d, want 2; body=%s", got, out)
	}
}

func TestXAICustomToolCallArguments_WrapsJSONLookingString(t *testing.T) {
	// Freeform custom input that happens to be a JSON object must stay a string
	// under "input", not be promoted to top-level function arguments.
	raw := "\"{\\\"cmd\\\":\\\"pwd\\\"}\""
	input := gjson.Parse(raw)
	got := xaiCustomToolCallArguments(input)
	if !gjson.Get(got, "input").Exists() {
		t.Fatalf("missing input field: %s", got)
	}
	if gjson.Get(got, "cmd").Exists() {
		t.Fatalf("arguments leaked cmd to top level: %s", got)
	}
	if gotInput := gjson.Get(got, "input").String(); gotInput != `{"cmd":"pwd"}` {
		t.Fatalf("input = %q, want {\"cmd\":\"pwd\"}", gotInput)
	}

	// Plain freeform text still wraps.
	plain := xaiCustomToolCallArguments(gjson.Parse(`"*** Begin Patch"`))
	if gjson.Get(plain, "input").String() != "*** Begin Patch" {
		t.Fatalf("plain wrap = %s", plain)
	}
}

func TestXAICompactOutputIndex_SubtractsDroppedBelow(t *testing.T) {
	dropped := map[int64]struct{}{0: {}}
	in := []byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message"}}`)
	out := xaiCompactOutputIndex(in, dropped)
	if got := gjson.GetBytes(out, "output_index").Int(); got != 0 {
		t.Fatalf("output_index = %d, want 0", got)
	}
	// No drop below -> unchanged
	out2 := xaiCompactOutputIndex(in, map[int64]struct{}{2: {}})
	if got := gjson.GetBytes(out2, "output_index").Int(); got != 1 {
		t.Fatalf("output_index = %d, want 1", got)
	}
}

func TestXAIRecordDroppedOutputIndex(t *testing.T) {
	dropped := map[int64]struct{}{}
	xaiRecordDroppedOutputIndex([]byte(`{"output_index":3,"item":{"type":"web_search_call"}}`), dropped)
	if _, ok := dropped[3]; !ok {
		t.Fatalf("expected index 3 recorded, got %#v", dropped)
	}
}

func TestTranslateXAICustomToolCallInputEvents_DeltaAndDone(t *testing.T) {
	custom := map[int64]struct{}{1: {}}

	// Partial / full argument deltas for custom tools are suppressed: xAI streams
	// JSON wrapper fragments that would corrupt freeform custom input.
	deltaIn := []byte(`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"input\":\"pwd\"}"}`)
	if out := translateXAICustomToolCallInputEvents(deltaIn, custom); out != nil {
		t.Fatalf("custom argument delta should be dropped, got %s", string(out))
	}
	partial := []byte(`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"in"}`)
	if out := translateXAICustomToolCallInputEvents(partial, custom); out != nil {
		t.Fatalf("partial custom argument delta should be dropped, got %s", string(out))
	}

	doneIn := []byte(`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"input\":\"pwd\"}"}`)
	doneOut := translateXAICustomToolCallInputEvents(doneIn, custom)
	if got := gjson.GetBytes(doneOut, "type").String(); got != "response.custom_tool_call_input.done" {
		t.Fatalf("done type = %q", got)
	}
	if gjson.GetBytes(doneOut, "arguments").Exists() {
		t.Fatalf("arguments should be removed: %s", string(doneOut))
	}
	if got := gjson.GetBytes(doneOut, "input").String(); got != "pwd" {
		t.Fatalf("done input = %q", got)
	}

	// Non-custom index: unchanged (including deltas).
	other := []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`)
	if string(translateXAICustomToolCallInputEvents(other, custom)) != string(other) {
		t.Fatalf("non-custom index should be noop")
	}
}

func TestXAITrackCustomOutputIndex(t *testing.T) {
	custom := map[int64]struct{}{}
	xaiTrackCustomOutputIndex([]byte(`{"type":"response.output_item.added","output_index":2,"item":{"type":"custom_tool_call","name":"exec"}}`), custom)
	if _, ok := custom[2]; !ok {
		t.Fatalf("expected index 2 tracked")
	}
	xaiTrackCustomOutputIndex([]byte(`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","name":"lookup"}}`), custom)
	if _, ok := custom[3]; ok {
		t.Fatalf("function_call must not be tracked")
	}
}

func TestXAIOutputIndexIsDropped(t *testing.T) {
	dropped := map[int64]struct{}{0: {}}
	if !xaiOutputIndexIsDropped([]byte(`{"output_index":0,"delta":"x"}`), dropped) {
		t.Fatal("index 0 should be dropped")
	}
	if xaiOutputIndexIsDropped([]byte(`{"output_index":1,"delta":"x"}`), dropped) {
		t.Fatal("index 1 should not be dropped")
	}
}

func TestCollectXAIOriginalCustomToolNames_FromHistory(t *testing.T) {
	body := []byte(`{"model":"grok-4","tools":[{"type":"function","name":"lookup"}],"input":[
		{"type":"custom_tool_call","call_id":"c0","name":"ApplyPatch","input":"x"}
	]}`)
	names := collectXAIOriginalCustomToolNames(body)
	if _, ok := names["ApplyPatch"]; !ok {
		t.Fatalf("ApplyPatch from history missing: %#v", names)
	}
	if _, ok := names["lookup"]; ok {
		t.Fatalf("lookup is function, must not be custom: %#v", names)
	}
}
