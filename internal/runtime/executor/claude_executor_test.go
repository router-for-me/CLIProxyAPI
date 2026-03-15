package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestNormalizeCacheControlTTL_PreservesOriginalBytesWhenNoChange(t *testing.T) {
	// Payload where no TTL normalization is needed (all blocks use 1h with no
	// preceding 5m block). The text intentionally contains HTML chars (<, >, &)
	// that json.Marshal would escape to \u003c etc., altering byte identity.
	payload := []byte(`{"tools":[{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","text":"<system-reminder>foo & bar</system-reminder>","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	out := normalizeCacheControlTTL(payload)

	if !bytes.Equal(out, payload) {
		t.Fatalf("normalizeCacheControlTTL altered bytes when no change was needed.\noriginal: %s\ngot:      %s", payload, out)
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

func TestClaudeExecutor_CountTokens_UsesConfiguredClaudeVersion(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			Version: "2.2.0",
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	if len(seenBody) == 0 {
		t.Fatal("expected count_tokens request body to be captured")
	}
	if got := gjson.GetBytes(seenBody, "system.0.text").String(); !strings.Contains(got, "cc_version=2.2.0.") {
		t.Fatalf("count_tokens billing header should use configured Claude version, got %q", got)
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

func TestClaudeExecutor_Execute_InvalidGzipErrorBodyReturnsDecodeMessage(t *testing.T) {
	testClaudeExecutorInvalidCompressedErrorBody(t, func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error {
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		return err
	})
}

func TestClaudeExecutor_ExecuteStream_InvalidGzipErrorBodyReturnsDecodeMessage(t *testing.T) {
	testClaudeExecutorInvalidCompressedErrorBody(t, func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error {
		_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		return err
	})
}

func TestClaudeExecutor_CountTokens_InvalidGzipErrorBodyReturnsDecodeMessage(t *testing.T) {
	testClaudeExecutorInvalidCompressedErrorBody(t, func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error {
		_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		return err
	})
}

func testClaudeExecutorInvalidCompressedErrorBody(
	t *testing.T,
	invoke func(executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) error,
) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("not-a-valid-gzip-stream"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	err := invoke(executor, auth, payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decode error response body") {
		t.Fatalf("expected decode failure message, got: %v", err)
	}
	if statusProvider, ok := err.(interface{ StatusCode() int }); !ok || statusProvider.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected status code 400, got: %v", err)
	}
}

// TestClaudeExecutor_ExecuteStream_SetsIdentityAcceptEncoding verifies that streaming
// requests use Accept-Encoding: identity so the upstream cannot respond with a
// compressed SSE body that would silently break the line scanner.
func TestClaudeExecutor_ExecuteStream_SetsIdentityAcceptEncoding(t *testing.T) {
	var gotEncoding, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	if gotEncoding != "identity" {
		t.Errorf("Accept-Encoding = %q, want %q", gotEncoding, "identity")
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want %q", gotAccept, "text/event-stream")
	}
}

// TestClaudeExecutor_Execute_SetsCompressedAcceptEncoding verifies that non-streaming
// requests keep the full accept-encoding to allow response compression (which
// decodeResponseBody handles correctly).
func TestClaudeExecutor_Execute_SetsCompressedAcceptEncoding(t *testing.T) {
	var gotEncoding, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotEncoding != "gzip, deflate, br, zstd" {
		t.Errorf("Accept-Encoding = %q, want %q", gotEncoding, "gzip, deflate, br, zstd")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want %q", gotAccept, "application/json")
	}
}

// TestClaudeExecutor_ExecuteStream_GzipSuccessBodyDecoded verifies that a streaming
// HTTP 200 response with Content-Encoding: gzip is correctly decompressed before
// the line scanner runs, so SSE chunks are not silently dropped.
func TestClaudeExecutor_ExecuteStream_GzipSuccessBodyDecoded(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("data: {\"type\":\"message_stop\"}\n"))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}

	if combined.Len() == 0 {
		t.Fatal("expected at least one chunk from gzip-encoded SSE body, got none (body was not decompressed)")
	}
	if !strings.Contains(combined.String(), "message_stop") {
		t.Errorf("expected SSE content in chunks, got: %q", combined.String())
	}
}

// TestDecodeResponseBody_MagicByteGzipNoHeader verifies that decodeResponseBody
// detects gzip-compressed content via magic bytes even when Content-Encoding is absent.
func TestDecodeResponseBody_MagicByteGzipNoHeader(t *testing.T) {
	const plaintext = "data: {\"type\":\"message_stop\"}\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(plaintext))
	_ = gz.Close()

	rc := io.NopCloser(&buf)
	decoded, err := decodeResponseBody(rc, "")
	if err != nil {
		t.Fatalf("decodeResponseBody error: %v", err)
	}
	defer decoded.Close()

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("decoded = %q, want %q", got, plaintext)
	}
}

// TestDecodeResponseBody_PlainTextNoHeader verifies that decodeResponseBody returns
// plain text untouched when Content-Encoding is absent and no magic bytes match.
func TestDecodeResponseBody_PlainTextNoHeader(t *testing.T) {
	const plaintext = "data: {\"type\":\"message_stop\"}\n"
	rc := io.NopCloser(strings.NewReader(plaintext))
	decoded, err := decodeResponseBody(rc, "")
	if err != nil {
		t.Fatalf("decodeResponseBody error: %v", err)
	}
	defer decoded.Close()

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("decoded = %q, want %q", got, plaintext)
	}
}

// TestClaudeExecutor_ExecuteStream_GzipNoContentEncodingHeader verifies the full
// pipeline: when the upstream returns a gzip-compressed SSE body WITHOUT setting
// Content-Encoding (a misbehaving upstream), the magic-byte sniff in
// decodeResponseBody still decompresses it, so chunks reach the caller.
func TestClaudeExecutor_ExecuteStream_GzipNoContentEncodingHeader(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("data: {\"type\":\"message_stop\"}\n"))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Intentionally omit Content-Encoding to simulate misbehaving upstream.
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}

	if combined.Len() == 0 {
		t.Fatal("expected chunks from gzip body without Content-Encoding header, got none (magic-byte sniff failed)")
	}
	if !strings.Contains(combined.String(), "message_stop") {
		t.Errorf("unexpected chunk content: %q", combined.String())
	}
}

// TestClaudeExecutor_ExecuteStream_AcceptEncodingOverrideCannotBypassIdentity verifies
// that injecting Accept-Encoding via auth.Attributes cannot override the stream
// path's enforced identity encoding.
func TestClaudeExecutor_ExecuteStream_AcceptEncodingOverrideCannotBypassIdentity(t *testing.T) {
	var gotEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	// Inject Accept-Encoding via the custom header attribute mechanism.
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                "key-123",
		"base_url":               server.URL,
		"header:Accept-Encoding": "gzip, deflate, br, zstd",
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	if gotEncoding != "identity" {
		t.Errorf("Accept-Encoding = %q; stream path must enforce identity regardless of auth.Attributes override", gotEncoding)
	}
}

// TestDecodeResponseBody_MagicByteZstdNoHeader verifies that decodeResponseBody
// detects zstd-compressed content via magic bytes (28 b5 2f fd) even when
// Content-Encoding is absent.
func TestDecodeResponseBody_MagicByteZstdNoHeader(t *testing.T) {
	const plaintext = "data: {\"type\":\"message_stop\"}\n"

	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	_, _ = enc.Write([]byte(plaintext))
	_ = enc.Close()

	rc := io.NopCloser(&buf)
	decoded, err := decodeResponseBody(rc, "")
	if err != nil {
		t.Fatalf("decodeResponseBody error: %v", err)
	}
	defer decoded.Close()

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("decoded = %q, want %q", got, plaintext)
	}
}

// TestClaudeExecutor_Execute_GzipErrorBodyNoContentEncodingHeader verifies that the
// error path (4xx) correctly decompresses a gzip body even when the upstream omits
// the Content-Encoding header.  This closes the gap left by PR #1771, which only
// fixed header-declared compression on the error path.
func TestClaudeExecutor_Execute_GzipErrorBodyNoContentEncodingHeader(t *testing.T) {
	const errJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"test error"}}`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(errJSON))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Intentionally omit Content-Encoding to simulate misbehaving upstream.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err == nil {
		t.Fatal("expected an error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "test error") {
		t.Errorf("error message should contain decompressed JSON, got: %q", err.Error())
	}
}

// TestClaudeExecutor_ExecuteStream_GzipErrorBodyNoContentEncodingHeader verifies
// the same for the streaming executor: 4xx gzip body without Content-Encoding is
// decoded and the error message is readable.
func TestClaudeExecutor_ExecuteStream_GzipErrorBodyNoContentEncodingHeader(t *testing.T) {
	const errJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"stream test error"}}`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(errJSON))
	_ = gz.Close()
	compressedBody := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Intentionally omit Content-Encoding to simulate misbehaving upstream.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(compressedBody)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err == nil {
		t.Fatal("expected an error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "stream test error") {
		t.Errorf("error message should contain decompressed JSON, got: %q", err.Error())
	}
}

// Test case 1: String system prompt is preserved and converted to a content block
func TestCheckSystemInstructionsWithMode_StringSystemPreserved(t *testing.T) {
	payload := []byte(`{"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false, nil)

	system := gjson.GetBytes(out, "system")
	if !system.IsArray() {
		t.Fatalf("system should be an array, got %s", system.Type)
	}

	blocks := system.Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}

	if !strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") {
		t.Fatalf("blocks[0] should be billing header, got %q", blocks[0].Get("text").String())
	}
	if !strings.Contains(blocks[0].Get("text").String(), "cc_version=2.1.63.") {
		t.Fatalf("blocks[0] should use default Claude version, got %q", blocks[0].Get("text").String())
	}
	if blocks[1].Get("text").String() != "You are a Claude agent, built on Anthropic's Claude Agent SDK." {
		t.Fatalf("blocks[1] should be agent block, got %q", blocks[1].Get("text").String())
	}
	if blocks[2].Get("text").String() != "You are a helpful assistant." {
		t.Fatalf("blocks[2] should be user system prompt, got %q", blocks[2].Get("text").String())
	}
	if blocks[2].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("blocks[2] should have cache_control.type=ephemeral")
	}
}

// Test case 2: Strict mode drops the string system prompt
func TestCheckSystemInstructionsWithMode_StringSystemStrict(t *testing.T) {
	payload := []byte(`{"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, true, nil)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("strict mode should produce 2 blocks, got %d", len(blocks))
	}
}

// Test case 3: Empty string system prompt does not produce a spurious block
func TestCheckSystemInstructionsWithMode_EmptyStringSystemIgnored(t *testing.T) {
	payload := []byte(`{"system":"","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false, nil)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("empty string system should produce 2 blocks, got %d", len(blocks))
	}
}

// Test case 4: Array system prompt is unaffected by the string handling
func TestCheckSystemInstructionsWithMode_ArraySystemStillWorks(t *testing.T) {
	payload := []byte(`{"system":[{"type":"text","text":"Be concise."}],"messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false, nil)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}
	if blocks[2].Get("text").String() != "Be concise." {
		t.Fatalf("blocks[2] should be user system prompt, got %q", blocks[2].Get("text").String())
	}
}

// Test case 5: Special characters in string system prompt survive conversion
func TestCheckSystemInstructionsWithMode_StringWithSpecialChars(t *testing.T) {
	payload := []byte(`{"system":"Use <xml> tags & \"quotes\" in output.","messages":[{"role":"user","content":"hi"}]}`)

	out := checkSystemInstructionsWithMode(payload, false, nil)

	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}
	if blocks[2].Get("text").String() != `Use <xml> tags & "quotes" in output.` {
		t.Fatalf("blocks[2] text mangled, got %q", blocks[2].Get("text").String())
	}
}

func TestCheckSystemInstructionsWithMode_UsesConfiguredClaudeVersion(t *testing.T) {
	payload := []byte(`{"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`)
	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			Version: "2.2.0",
		},
	}

	out := checkSystemInstructionsWithMode(payload, false, cfg)

	billingHeader := gjson.GetBytes(out, "system.0.text").String()
	if !strings.Contains(billingHeader, "cc_version=2.2.0.") {
		t.Fatalf("billing header should use configured Claude version, got %q", billingHeader)
	}
}

func TestApplyClaudeHeaders_UsesConfiguredClaudeVersionForFallbackUserAgent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			Version: "2.2.0",
		},
	}

	applyClaudeHeaders(req, nil, "key-123", false, nil, cfg)

	if got := req.Header.Get("User-Agent"); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("User-Agent = %q, want %q", got, "claude-cli/2.2.0 (external, cli)")
	}
}

func TestApplyClaudeHeaders_UsesConfiguredUserAgent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
	cfg := &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			Version:   "2.3.4",
			UserAgent: "claude-cli/custom-build (external, cli)",
		},
	}

	applyClaudeHeaders(req, nil, "key-123", false, nil, cfg)

	if got := req.Header.Get("User-Agent"); got != "claude-cli/custom-build (external, cli)" {
		t.Fatalf("User-Agent = %q, want %q", got, "claude-cli/custom-build (external, cli)")
	}
}
