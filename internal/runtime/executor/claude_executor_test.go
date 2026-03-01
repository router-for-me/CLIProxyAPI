package executor

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestApplyClaudeToolPrefix(t *testing.T) {
	input := []byte(`{"tools":[{"name":"alpha"},{"name":"proxy_bravo"}],"tool_choice":{"type":"tool","name":"charlie"},"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"delta","id":"t1","input":{}}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_alpha" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_alpha")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_bravo" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_bravo")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "proxy_charlie" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "proxy_charlie")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_delta" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_delta")
	}
}

func TestApplyClaudeToolPrefix_WithToolReference(t *testing.T) {
	input := []byte(`{"tools":[{"name":"alpha"}],"messages":[{"role":"user","content":[{"type":"tool_reference","tool_name":"beta"},{"type":"tool_reference","tool_name":"proxy_gamma"}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "messages.0.content.0.tool_name").String(); got != "proxy_beta" {
		t.Fatalf("messages.0.content.0.tool_name = %q, want %q", got, "proxy_beta")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.tool_name").String(); got != "proxy_gamma" {
		t.Fatalf("messages.0.content.1.tool_name = %q, want %q", got, "proxy_gamma")
	}
}

func TestApplyClaudeToolPrefix_SkipsBuiltinTools(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"my_custom_tool","input_schema":{"type":"object"}}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("built-in tool name should not be prefixed: tools.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_my_custom_tool" {
		t.Fatalf("custom tool should be prefixed: tools.1.name = %q, want %q", got, "proxy_my_custom_tool")
	}
}

func TestApplyClaudeToolPrefix_BuiltinToolSkipped(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
			{"name": "Read"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "web_search", "id": "ws1", "input": {}},
				{"type": "tool_use", "name": "Read", "id": "r1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("tools.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "web_search" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Read" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Read")
	}
}

func TestApplyClaudeToolPrefix_KnownBuiltinInHistoryOnly(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name": "Read"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "web_search", "id": "ws1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "web_search" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
	}
}

func TestApplyClaudeToolPrefix_CustomToolsPrefixed(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "Read"}, {"name": "Write"}],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "Read", "id": "r1", "input": {}},
				{"type": "tool_use", "name": "Write", "id": "w1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Write" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Write")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_Read" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Write" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Write")
	}
}

func TestApplyClaudeToolPrefix_ToolChoiceBuiltin(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search"},
			{"name": "Read"}
		],
		"tool_choice": {"type": "tool", "name": "web_search"}
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "web_search" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "web_search")
	}
}

func TestStripClaudeToolPrefixFromResponse(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_use","name":"proxy_alpha","id":"t1","input":{}},{"type":"tool_use","name":"bravo","id":"t2","input":{}}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")

	if got := gjson.GetBytes(out, "content.0.name").String(); got != "alpha" {
		t.Fatalf("content.0.name = %q, want %q", got, "alpha")
	}
	if got := gjson.GetBytes(out, "content.1.name").String(); got != "bravo" {
		t.Fatalf("content.1.name = %q, want %q", got, "bravo")
	}
}

func TestStripClaudeToolPrefixFromResponse_WithToolReference(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_reference","tool_name":"proxy_alpha"},{"type":"tool_reference","tool_name":"bravo"}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")

	if got := gjson.GetBytes(out, "content.0.tool_name").String(); got != "alpha" {
		t.Fatalf("content.0.tool_name = %q, want %q", got, "alpha")
	}
	if got := gjson.GetBytes(out, "content.1.tool_name").String(); got != "bravo" {
		t.Fatalf("content.1.tool_name = %q, want %q", got, "bravo")
	}
}

func TestStripClaudeToolPrefixFromStreamLine(t *testing.T) {
	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"proxy_alpha","id":"t1"},"index":0}`)
	out := stripClaudeToolPrefixFromStreamLine(line, "proxy_")

	payload := bytes.TrimSpace(out)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.name").String(); got != "alpha" {
		t.Fatalf("content_block.name = %q, want %q", got, "alpha")
	}
}

func TestStripClaudeToolPrefixFromStreamLine_WithToolReference(t *testing.T) {
	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_reference","tool_name":"proxy_beta"},"index":0}`)
	out := stripClaudeToolPrefixFromStreamLine(line, "proxy_")

	payload := bytes.TrimSpace(out)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.tool_name").String(); got != "beta" {
		t.Fatalf("content_block.tool_name = %q, want %q", got, "beta")
	}
}

func TestApplyClaudeToolPrefix_NestedToolReference(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"mcp__nia__manage_resource"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "proxy_mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want %q", got, "proxy_mcp__nia__manage_resource")
	}
}

func TestClaudeExecutor_ReusesUserIDAcrossModelsWhenCacheEnabled(t *testing.T) {
	resetUserIDCache()

	var userIDs []string
	var requestModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userID := gjson.GetBytes(body, "metadata.user_id").String()
		model := gjson.GetBytes(body, "model").String()
		userIDs = append(userIDs, userID)
		requestModels = append(requestModels, model)
		t.Logf("HTTP Server received request: model=%s, user_id=%s, url=%s", model, userID, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	t.Logf("End-to-end test: Fake HTTP server started at %s", server.URL)

	cacheEnabled := true
	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey:  "key-123",
				BaseURL: server.URL,
				Cloak: &config.CloakConfig{
					CacheUserID: &cacheEnabled,
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	models := []string{"claude-3-5-sonnet", "claude-3-5-haiku"}
	for _, model := range models {
		t.Logf("Sending request for model: %s", model)
		modelPayload, _ := sjson.SetBytes(payload, "model", model)
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: modelPayload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		}); err != nil {
			t.Fatalf("Execute(%s) error: %v", model, err)
		}
	}

	if len(userIDs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(userIDs))
	}
	if userIDs[0] == "" || userIDs[1] == "" {
		t.Fatal("expected user_id to be populated")
	}
	t.Logf("user_id[0] (model=%s): %s", requestModels[0], userIDs[0])
	t.Logf("user_id[1] (model=%s): %s", requestModels[1], userIDs[1])
	if userIDs[0] != userIDs[1] {
		t.Fatalf("expected user_id to be reused across models, got %q and %q", userIDs[0], userIDs[1])
	}
	if !isValidUserID(userIDs[0]) {
		t.Fatalf("user_id %q is not valid", userIDs[0])
	}
	t.Logf("✓ End-to-end test passed: Same user_id (%s) was used for both models", userIDs[0])
}

func TestClaudeExecutor_GeneratesNewUserIDByDefault(t *testing.T) {
	resetUserIDCache()

	var userIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userIDs = append(userIDs, gjson.GetBytes(body, "metadata.user_id").String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	for i := 0; i < 2; i++ {
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		}); err != nil {
			t.Fatalf("Execute call %d error: %v", i, err)
		}
	}

	if len(userIDs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(userIDs))
	}
	if userIDs[0] == "" || userIDs[1] == "" {
		t.Fatal("expected user_id to be populated")
	}
	if userIDs[0] == userIDs[1] {
		t.Fatalf("expected user_id to change when caching is not enabled, got identical values %q", userIDs[0])
	}
	if !isValidUserID(userIDs[0]) || !isValidUserID(userIDs[1]) {
		t.Fatalf("user_ids should be valid, got %q and %q", userIDs[0], userIDs[1])
	}
}

func TestStripClaudeToolPrefixFromResponse_NestedToolReference(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"proxy_mcp__nia__manage_resource"}]}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")
	got := gjson.GetBytes(out, "content.0.content.0.tool_name").String()
	if got != "mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want %q", got, "mcp__nia__manage_resource")
	}
}

func TestApplyClaudeToolPrefix_NestedToolReferenceWithStringContent(t *testing.T) {
	// tool_result.content can be a string - should not be processed
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"plain string result"}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content").String()
	if got != "plain string result" {
		t.Fatalf("string content should remain unchanged = %q", got)
	}
}

func TestApplyClaudeToolPrefix_SkipsBuiltinToolReference(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"web_search"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "web_search" {
		t.Fatalf("built-in tool_reference should not be prefixed, got %q", got)
	}
}

func TestApplyClaudeToolPrefix_PrefixesCustomToolType(t *testing.T) {
	input := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_apply_patch")
	}
}

func TestApplyClaudeToolPrefix_SkipsExplicitNonCustomTypedToolAndReferences(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}},
			{"type":"custom","name":"apply_patch","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"future_one"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"future_one","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"future_one"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"future_one"}]}]},
			{"role":"assistant","content":[{"type":"tool_use","name":"apply_patch","id":"t2","input":{}}]}
		]
	}`)

	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "future_one" {
		t.Fatalf("tools.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "future_one" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "future_one" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_apply_patch")
	}
	if got := gjson.GetBytes(out, "messages.3.content.0.name").String(); got != "proxy_apply_patch" {
		t.Fatalf("messages.3.content.0.name = %q, want %q", got, "proxy_apply_patch")
	}
}

func TestNormalizeClaudeToolsForAnthropic_CustomTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark"}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search_20250305" {
		t.Fatalf("tools.0.type = %q, want %q", got, "web_search_20250305")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "apply_patch" {
		t.Fatalf("tools.1.name = %q, want %q", got, "apply_patch")
	}
	if got := gjson.GetBytes(out, "tools.1.description").String(); got != "Custom tool" {
		t.Fatalf("tools.1.description = %q, want %q", got, "Custom tool")
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.1.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.1.type"); got.Exists() {
		t.Fatalf("tools.1.type should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.format"); got.Exists() {
		t.Fatalf("tools.1.format should be removed, got %s", got.Raw)
	}
}

func TestNormalizeClaudeToolsForAnthropic_PreservesDocumentedCustomMetadata(t *testing.T) {
	input := []byte(`{
		"tools":[
			{
				"type":"custom",
				"name":"apply_patch",
				"cache_control":{"type":"ephemeral","ttl":"1h"},
				"input_examples":[{"input":{"path":"README.md"}}],
				"strict":true,
				"format":{"type":"grammar","syntax":"lark"}
			}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "apply_patch" {
		t.Fatalf("tools.0.name = %q, want %q", got, "apply_patch")
	}
	if got := gjson.GetBytes(out, "tools.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("tools.0.cache_control.ttl = %q, want %q", got, "1h")
	}
	if got := gjson.GetBytes(out, "tools.0.input_examples.#").Int(); got != 1 {
		t.Fatalf("tools.0.input_examples length = %d, want 1", got)
	}
	if !gjson.GetBytes(out, "tools.0.strict").Bool() {
		t.Fatalf("tools.0.strict should be true, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type"); got.Exists() {
		t.Fatalf("tools.0.type should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.format"); got.Exists() {
		t.Fatalf("tools.0.format should be removed, got %s", got.Raw)
	}
}

func TestNormalizeClaudeToolsForAnthropic_FunctionFallbacks(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]}],
		"tools":[
			{"type":"custom","function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if strings.Contains(normalizedName, " ") {
		t.Fatalf("normalized tool name should be sanitized, got %q", normalizedName)
	}
	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "dangerous" {
		t.Fatalf("tools.0.description = %q, want %q", got, "dangerous")
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.0.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_FunctionFallbacksPreserveTopLevelMetadata(t *testing.T) {
	input := []byte(`{
		"tools":[
			{
				"type":"custom",
				"cache_control":{"type":"ephemeral"},
				"input_examples":[{"input":{"command":"run"}}],
				"strict":true,
				"function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}
			}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "dangerous" {
		t.Fatalf("tools.0.description = %q, want %q", got, "dangerous")
	}
	if got := gjson.GetBytes(out, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tools.0.cache_control.type = %q, want %q", got, "ephemeral")
	}
	if got := gjson.GetBytes(out, "tools.0.input_examples.#").Int(); got != 1 {
		t.Fatalf("tools.0.input_examples length = %d, want 1", got)
	}
	if !gjson.GetBytes(out, "tools.0.strict").Bool() {
		t.Fatalf("tools.0.strict should be true, body=%s", string(out))
	}
}

func TestNormalizeClaudeToolsForAnthropic_RenameMapUpdatesAllReferenceSites(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"very bad tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"very bad tool"}]}]}
		],
		"tools":[
			{"type":"custom","function":{"name":"very bad tool","description":"dangerous","parameters":{"type":"object"}}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_PreservesDuplicateOriginalCustomToolNames(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"duplicate tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"duplicate tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"duplicate tool"}]}
		],
		"tools":[
			{"type":"custom","name":"duplicate tool","description":"first","input_schema":{"type":"object"}},
			{"type":"custom","name":"duplicate tool","description":"second","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "duplicate tool" {
		t.Fatalf("tools.0.name = %q, want %q", got, "duplicate tool")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "duplicate tool" {
		t.Fatalf("tools.1.name = %q, want %q", got, "duplicate tool")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "duplicate tool" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "duplicate tool")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "duplicate tool" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "duplicate tool")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "duplicate tool" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "duplicate tool")
	}
}

func TestNormalizeClaudeToolsForAnthropic_PreservesUnsanitizableCustomToolName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"!!!"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"!!!","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"!!!"}]}
		],
		"tools":[
			{"type":"custom","name":"!!!","description":"bad","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "!!!" {
		t.Fatalf("tools.0.name = %q, want %q", got, "!!!")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "!!!" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "!!!")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "!!!" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "!!!")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != "!!!" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "!!!")
	}
}

func TestNormalizeClaudeToolsForAnthropic_ReservesBuiltinNamesForCustomTools(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"web search"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"web search","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"web search"}]}
		],
		"tools":[
			{"type":"custom","name":"web search","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	normalizedName := gjson.GetBytes(out, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	if normalizedName == "web_search" {
		t.Fatalf("custom tool should not normalize to reserved built-in name, got %q", normalizedName)
	}
	if !strings.HasPrefix(normalizedName, "web_search_") {
		t.Fatalf("custom tool should be suffixed off reserved name, got %q", normalizedName)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != normalizedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != normalizedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, normalizedName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != normalizedName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, normalizedName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_AllowsDistinctOriginalNamesThatSanitizeToSameBase(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"very bad tool"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"very bad tool","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"very@bad tool"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"tool_reference","tool_name":"very@bad tool"}]}]}
		],
		"tools":[
			{"type":"custom","name":"very bad tool","description":"first","input_schema":{"type":"object"}},
			{"type":"custom","name":"very@bad tool","description":"second","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	firstName := gjson.GetBytes(out, "tools.0.name").String()
	secondName := gjson.GetBytes(out, "tools.1.name").String()
	if firstName == "" || secondName == "" {
		t.Fatalf("normalized names should not be empty, got first=%q second=%q", firstName, secondName)
	}
	if firstName == secondName {
		t.Fatalf("normalized names should be unique, got %q", firstName)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != firstName {
		t.Fatalf("tool_choice.name = %q, want %q", got, firstName)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != firstName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, firstName)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.tool_name").String(); got != secondName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, secondName)
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.content.0.tool_name").String(); got != secondName {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, secondName)
	}
}

func TestNormalizeClaudeToolsForAnthropic_RejectsNonObjectSchemas(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"custom","name":"array_schema","input_schema":{"type":"array","items":{"type":"string"}}},
			{"type":"custom","name":"bad_properties","input_schema":{"type":"object","properties":[]}}
		]
	}`)
	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.0.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("tools.0.input_schema.properties should be object, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.items"); got.Exists() {
		t.Fatalf("tools.0.input_schema.items should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.1.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("tools.1.input_schema.properties should be object, got %s", got.Raw)
	}
}

func TestNormalizeClaudeToolsForAnthropic_PreservesUnknownExplicitTypedTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}},
			{"type":"custom","name":"apply patch","input_schema":{"type":"object"}}
		]
	}`)

	out, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "future_tool" {
		t.Fatalf("tools.0.type = %q, want %q", got, "future_tool")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "future_one" {
		t.Fatalf("tools.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(out, "tools.0.extra.a").Int(); got != 1 {
		t.Fatalf("tools.0.extra.a = %d, want 1", got)
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got == "apply patch" || got == "" {
		t.Fatalf("tools.1.name = %q, want sanitized non-empty custom name", got)
	}
}

func TestNormalizeCacheControlTTL_DowngradesLaterOneHourBlocks(t *testing.T) {
	payload := []byte(`{
		"tools": [{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],
		"messages": [{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]
	}`)

	out := normalizeCacheControlTTL(payload)

	if got := gjson.GetBytes(out, "tools.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("tools.0.cache_control.ttl = %q, want %q", got, "1h")
	}
	if gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("messages.0.content.0.cache_control.ttl should be removed after a default-5m block")
	}
}

func TestEnforceCacheControlLimit_StripsNonLastToolBeforeMessages(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}}
		],
		"system": [{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral"}}]}
		]
	}`)

	out := enforceCacheControlLimit(payload, 4)

	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want 4", got)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be removed first (non-last tool)")
	}
	if !gjson.GetBytes(out, "tools.1.cache_control").Exists() {
		t.Fatalf("tools.1.cache_control (last tool) should be preserved")
	}
	if !gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists() || !gjson.GetBytes(out, "messages.1.content.0.cache_control").Exists() {
		t.Fatalf("message cache_control blocks should be preserved when non-last tool removal is enough")
	}
}

func TestEnforceCacheControlLimit_ToolOnlyPayloadStillRespectsLimit(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}},
			{"name":"t3","cache_control":{"type":"ephemeral"}},
			{"name":"t4","cache_control":{"type":"ephemeral"}},
			{"name":"t5","cache_control":{"type":"ephemeral"}}
		]
	}`)

	out := enforceCacheControlLimit(payload, 4)

	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want 4", got)
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be removed to satisfy max=4")
	}
	if !gjson.GetBytes(out, "tools.4.cache_control").Exists() {
		t.Fatalf("last tool cache_control should be preserved when possible")
	}
}

func TestClaudeExecutor_CountTokens_AppliesCacheControlGuards(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"tools": [
			{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"name":"t2","cache_control":{"type":"ephemeral"}}
		],
		"system": [
			{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"type":"text","text":"s2","cache_control":{"type":"ephemeral","ttl":"1h"}}
		],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"u1","cache_control":{"type":"ephemeral","ttl":"1h"}}]},
			{"role":"user","content":[{"type":"text","text":"u2","cache_control":{"type":"ephemeral","ttl":"1h"}}]}
		]
	}`)

	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-haiku-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if len(seenBody) == 0 {
		t.Fatal("expected count_tokens request body to be captured")
	}
	if got := countCacheControls(seenBody); got > 4 {
		t.Fatalf("count_tokens body has %d cache_control blocks, want <= 4", got)
	}
	if hasTTLOrderingViolation(seenBody) {
		t.Fatalf("count_tokens body still has ttl ordering violations: %s", string(seenBody))
	}
}

func TestClaudeExecutor_CountTokens_AppliesThinkingFromModelSuffix(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet(2048)",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if got := gjson.GetBytes(seenBody, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "enabled", string(seenBody))
	}
	if got := gjson.GetBytes(seenBody, "thinking.budget_tokens").Int(); got <= 0 {
		t.Fatalf("thinking.budget_tokens = %d, want > 0, body=%s", got, string(seenBody))
	}
}

func TestClaudeExecutor_CountTokens_DisablesThinkingWhenForcedToolChoice(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"tool_choice":{"type":"tool","name":"apply_patch"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)
	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet(2048)",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if gjson.GetBytes(seenBody, "thinking").Exists() {
		t.Fatalf("thinking should be removed for forced tool choice, body=%s", string(seenBody))
	}
}

func TestApplyClaudeHeaders_MergesBetasWithDefaultsAndClaude1M(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginReq := httptest.NewRequest(http.MethodPost, "http://localhost/v1/messages", nil)
	ginReq.Header.Set("Anthropic-Beta", "custom-beta-1,oauth-2025-04-20")
	ginReq.Header.Set("X-CPA-CLAUDE-1M", "1")
	ginCtx.Request = ginReq

	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), "gin", ginCtx))

	applyClaudeHeaders(req, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}, "key-123", false, []string{"beta-from-body", "custom-beta-1"}, &config.Config{})

	got := req.Header.Get("Anthropic-Beta")
	for _, want := range []string{
		"custom-beta-1",
		"oauth-2025-04-20",
		"beta-from-body",
		"context-1m-2025-08-07",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Anthropic-Beta = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "custom-beta-1") != 1 {
		t.Fatalf("Anthropic-Beta should de-duplicate custom-beta-1, got %q", got)
	}
}

func TestClaudeExecutor_Execute_PreservesCustomToolCacheControl(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{
		"tools": [
			{
				"type":"custom",
				"name":"tool_one",
				"cache_control":{"type":"ephemeral","ttl":"1h"},
				"input_schema":{"type":"object"}
			},
			{
				"type":"custom",
				"name":"tool_two",
				"input_schema":{"type":"object"}
			}
		],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := gjson.GetBytes(seenBody, "tools.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("tools.0.cache_control.ttl = %q, want %q, body=%s", got, "1h", string(seenBody))
	}
	if gjson.GetBytes(seenBody, "tools.1.cache_control").Exists() {
		t.Fatalf("tools.1.cache_control should not be injected when caller already provided tool cache_control, body=%s", string(seenBody))
	}
	if got := countCacheControls(seenBody); got != 1 {
		t.Fatalf("cache_control count = %d, want 1, body=%s", got, string(seenBody))
	}
}

func TestNormalizeThenPrefix_KeepsCustomNameConsistentWhenSanitizedNameMatchesBuiltin(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"web search"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"web search","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"web search"}]}
		],
		"tools":[
			{"type":"custom","name":"web search","input_schema":{"type":"object"}}
		]
	}`)

	normalized, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	prefixed := applyClaudeToolPrefix(normalized, "proxy_")

	normalizedName := gjson.GetBytes(normalized, "tools.0.name").String()
	if normalizedName == "" {
		t.Fatal("normalized tool name should not be empty")
	}
	expectedPrefixedName := "proxy_" + normalizedName
	if got := gjson.GetBytes(prefixed, "tools.0.name").String(); got != expectedPrefixedName {
		t.Fatalf("tools.0.name = %q, want %q", got, expectedPrefixedName)
	}
	if got := gjson.GetBytes(prefixed, "tool_choice.name").String(); got != expectedPrefixedName {
		t.Fatalf("tool_choice.name = %q, want %q", got, expectedPrefixedName)
	}
	if got := gjson.GetBytes(prefixed, "messages.0.content.0.name").String(); got != expectedPrefixedName {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, expectedPrefixedName)
	}
	if got := gjson.GetBytes(prefixed, "messages.1.content.0.tool_name").String(); got != expectedPrefixedName {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, expectedPrefixedName)
	}
}

func TestNormalizeThenPrefix_PreservesUnknownExplicitTypedToolName(t *testing.T) {
	input := []byte(`{
		"tool_choice":{"type":"tool","name":"future_one"},
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","name":"future_one","id":"t1","input":{}}]},
			{"role":"user","content":[{"type":"tool_reference","tool_name":"future_one"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"future_one"}]}]}
		],
		"tools":[
			{"type":"future_tool","name":"future_one","extra":{"a":1}}
		]
	}`)

	normalized, err := normalizeClaudeToolsForAnthropic(input)
	if err != nil {
		t.Fatalf("normalizeClaudeToolsForAnthropic error: %v", err)
	}
	prefixed := applyClaudeToolPrefix(normalized, "proxy_")

	if got := gjson.GetBytes(prefixed, "tools.0.name").String(); got != "future_one" {
		t.Fatalf("tools.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "tool_choice.name").String(); got != "future_one" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "messages.0.content.0.name").String(); got != "future_one" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "messages.1.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.1.content.0.tool_name = %q, want %q", got, "future_one")
	}
	if got := gjson.GetBytes(prefixed, "messages.2.content.0.content.0.tool_name").String(); got != "future_one" {
		t.Fatalf("messages.2.content.0.content.0.tool_name = %q, want %q", got, "future_one")
	}
}

func hasTTLOrderingViolation(payload []byte) bool {
	seen5m := false
	violates := false

	checkCC := func(cc gjson.Result) {
		if !cc.Exists() || violates {
			return
		}
		ttl := cc.Get("ttl").String()
		if ttl != "1h" {
			seen5m = true
			return
		}
		if seen5m {
			violates = true
		}
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			checkCC(tool.Get("cache_control"))
			return !violates
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			checkCC(item.Get("cache_control"))
			return !violates
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					checkCC(item.Get("cache_control"))
					return !violates
				})
			}
			return !violates
		})
	}

	return violates
}

func compressBytesForEncoding(t *testing.T, encoding string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch encoding {
	case "gzip":
		writer := gzip.NewWriter(&buf)
		_, _ = writer.Write(payload)
		_ = writer.Close()
	case "deflate":
		writer, errWriter := flate.NewWriter(&buf, flate.DefaultCompression)
		if errWriter != nil {
			t.Fatalf("failed to create deflate writer: %v", errWriter)
		}
		_, _ = writer.Write(payload)
		_ = writer.Close()
	case "br":
		writer := brotli.NewWriter(&buf)
		_, _ = writer.Write(payload)
		_ = writer.Close()
	case "zstd":
		writer, errWriter := zstd.NewWriter(&buf)
		if errWriter != nil {
			t.Fatalf("failed to create zstd writer: %v", errWriter)
		}
		_, _ = writer.Write(payload)
		writer.Close()
	default:
		t.Fatalf("unsupported encoding in test: %s", encoding)
	}
	return buf.Bytes()
}

func TestClaudeExecutor_DecodesCompressedErrorBodies(t *testing.T) {
	encodings := []string{"gzip", "deflate", "br", "zstd"}
	for _, encoding := range encodings {
		t.Run(encoding, func(t *testing.T) {
			errorBody := []byte(`{"error":{"message":"decoded-` + encoding + `"}}`)
			compressed := compressBytesForEncoding(t, encoding, errorBody)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", encoding)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write(compressed)
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}
			payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-3-5-sonnet",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("claude"),
			})
			if err == nil {
				t.Fatal("expected execute error")
			}
			if !strings.Contains(err.Error(), "decoded-"+encoding) {
				t.Fatalf("execute error = %q, want decoded body for %s", err.Error(), encoding)
			}

			_, err = executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-3-5-sonnet",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("claude"),
			})
			if err == nil {
				t.Fatal("expected execute stream error")
			}
			if !strings.Contains(err.Error(), "decoded-"+encoding) {
				t.Fatalf("execute stream error = %q, want decoded body for %s", err.Error(), encoding)
			}

			_, err = executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-3-5-sonnet",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("claude"),
			})
			if err == nil {
				t.Fatal("expected count tokens error")
			}
			if !strings.Contains(err.Error(), "decoded-"+encoding) {
				t.Fatalf("count tokens error = %q, want decoded body for %s", err.Error(), encoding)
			}
		})
	}
}

func TestClaudeExecutor_PropagatesCompressedErrorReadFailure(t *testing.T) {
	errorBody := []byte(`{"error":{"message":"decoded-gzip"}}`)
	compressed := compressBytesForEncoding(t, "gzip", errorBody)
	if len(compressed) < 8 {
		t.Fatalf("compressed gzip payload too small: %d", len(compressed))
	}
	truncated := compressed[:len(compressed)-8]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(truncated)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	checkErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var status statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: expected statusErr, got %T: %v", name, err, err)
		}
		if got := status.StatusCode(); got != http.StatusBadRequest {
			t.Fatalf("%s: status code = %d, want %d", name, got, http.StatusBadRequest)
		}
		errText := strings.ToLower(err.Error())
		if !strings.Contains(errText, "failed to read upstream error body") {
			t.Fatalf("%s: expected read failure context, got %q", name, err.Error())
		}
		if !strings.Contains(errText, "gzip") && !strings.Contains(errText, "eof") && !strings.Contains(errText, "checksum") {
			t.Fatalf("%s: expected gzip read failure, got %q", name, err.Error())
		}
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "Execute", err)

	_, err = executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "ExecuteStream", err)

	_, err = executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "CountTokens", err)
}

func TestClaudeExecutor_PreservesStatusOnCompressedErrorDecodeFailure(t *testing.T) {
	invalidGzip := []byte("not-a-gzip-stream")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(invalidGzip)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	checkErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var status statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: expected statusErr, got %T: %v", name, err, err)
		}
		if got := status.StatusCode(); got != http.StatusBadRequest {
			t.Fatalf("%s: status code = %d, want %d", name, got, http.StatusBadRequest)
		}
		errText := strings.ToLower(err.Error())
		if !strings.Contains(errText, "failed to decode upstream error body") {
			t.Fatalf("%s: expected decode failure context, got %q", name, err.Error())
		}
		if !strings.Contains(errText, "gzip") {
			t.Fatalf("%s: expected gzip decode failure details, got %q", name, err.Error())
		}
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "Execute", err)

	_, err = executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "ExecuteStream", err)

	_, err = executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	checkErr(t, "CountTokens", err)
}
