package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestClaudeExecutor_SubagentPreserves1hTTLAndExtendedCacheBetaWhenRequested verifies
// that when a Claude Code subagent explicitly requests 1h cache TTL (via subagentPromptCacheTtl: 1h),
// the executor preserves both the 1h TTL on cache_control blocks and the extended-cache-ttl beta header.
// Regression test for issue #5629.
func TestClaudeExecutor_SubagentPreserves1hTTLAndExtendedCacheBetaWhenRequested(t *testing.T) {
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-ant-oat-subagent-preserve-test",
		}},
	}
	auth := &cliproxyauth.Auth{
		ID:       "auth-subagent-preserve-test",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-subagent-preserve-test",
			"base_url": server.URL,
		},
	}

	executor := NewClaudeExecutor(cfg)
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "subagent work", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
			]
		}]
	}`)
	incomingHeaders := http.Header{}
	incomingHeaders.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	incomingHeaders.Set("X-Claude-Code-Agent-Id", "agent-sub-123")
	incomingHeaders.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,extended-cache-ttl-2025-04-11")

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incomingHeaders,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	// 1. Verify body cache_control preserves ttl: "1h"
	rawBody := string(seenBody)
	has1h := strings.Contains(rawBody, `"ttl":"1h"`) || strings.Contains(rawBody, `"ttl": "1h"`)
	if !has1h {
		t.Fatalf("subagent body must preserve ttl: 1h when requested by client, got: %s", rawBody)
	}
	for _, msg := range gjson.GetBytes(seenBody, "messages").Array() {
		for _, content := range msg.Get("content").Array() {
			if cc := content.Get("cache_control"); cc.Exists() {
				if gotTTL := cc.Get("ttl").String(); gotTTL != "1h" {
					t.Fatalf("expected cache_control.ttl = '1h', got: %q in block: %s", gotTTL, content.Raw)
				}
			}
		}
	}

	// 2. Verify extended-cache-ttl beta is preserved in Anthropic-Beta header
	betas := seenHeaders.Get("Anthropic-Beta")
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("subagent Anthropic-Beta must preserve extended-cache-ttl-2025-04-11 when requested, got: %s", betas)
	}
}

// TestClaudeExecutor_SubagentPreserves1hTTLAndExtendedCacheBetaWhenRequested_Stream verifies
// the streaming path preserves 1h TTL and the beta header for subagent requests.
func TestClaudeExecutor_SubagentPreserves1hTTLAndExtendedCacheBetaWhenRequested_Stream(t *testing.T) {
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-ant-oat-subagent-preserve-stream-test",
		}},
	}
	auth := &cliproxyauth.Auth{
		ID:       "auth-subagent-preserve-stream-test",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-subagent-preserve-stream-test",
			"base_url": server.URL,
		},
	}

	executor := NewClaudeExecutor(cfg)
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"stream": true,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "subagent streaming task", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
			]
		}]
	}`)
	incomingHeaders := http.Header{}
	incomingHeaders.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	incomingHeaders.Set("X-Claude-Code-Agent-Id", "agent-sub-stream-456")
	incomingHeaders.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,extended-cache-ttl-2025-04-11")

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incomingHeaders,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}
	if result != nil && result.Chunks != nil {
		for range result.Chunks {
		}
	}

	// 1. Verify body cache_control preserves ttl: "1h"
	rawBody := string(seenBody)
	if !strings.Contains(rawBody, `"ttl":"1h"`) && !strings.Contains(rawBody, `"ttl": "1h"`) {
		t.Fatalf("subagent stream body must preserve ttl: 1h when requested by client, got: %s", rawBody)
	}

	// 2. Verify extended-cache-ttl beta is preserved in Anthropic-Beta header
	betas := seenHeaders.Get("Anthropic-Beta")
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("subagent Anthropic-Beta must preserve extended-cache-ttl-2025-04-11 on stream when requested, got: %s", betas)
	}
}

// TestClaudeExecutor_SubagentWithout1hKeepsDefault5m verifies that when a subagent
// does NOT request 1h TTL, the executor keeps 5m (does not upgrade or inject extended-cache-ttl beta).
func TestClaudeExecutor_SubagentWithout1hKeepsDefault5m(t *testing.T) {
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-ant-oat-subagent-5m-test",
		}},
	}
	auth := &cliproxyauth.Auth{
		ID:       "auth-subagent-5m-test",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-subagent-5m-test",
			"base_url": server.URL,
		},
	}

	executor := NewClaudeExecutor(cfg)
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "subagent work without 1h", "cache_control": {"type": "ephemeral"}}
			]
		}]
	}`)
	incomingHeaders := http.Header{}
	incomingHeaders.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	incomingHeaders.Set("X-Claude-Code-Agent-Id", "agent-sub-default-789")
	incomingHeaders.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20")

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incomingHeaders,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	// 1. Verify body cache_control does NOT have ttl: "1h"
	rawBody := string(seenBody)
	if strings.Contains(rawBody, `"ttl":"1h"`) || strings.Contains(rawBody, `"ttl": "1h"`) {
		t.Fatalf("subagent default body must not carry ttl: 1h, got: %s", rawBody)
	}

	// 2. Verify extended-cache-ttl beta is NOT present in Anthropic-Beta header
	betas := seenHeaders.Get("Anthropic-Beta")
	if strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("subagent default Anthropic-Beta must not contain extended-cache-ttl, got: %s", betas)
	}
}

// TestClaudeExecutor_APIKeySubagentPreserves1hTTLAndInjectsBeta verifies that on a standard
// API key credential (non-OAuth), when a subagent requests 1h TTL solely in payload cache_control,
// the executor preserves the 1h TTL and ensures extended-cache-ttl-2025-04-11 beta is supplied.
func TestClaudeExecutor_APIKeySubagentPreserves1hTTLAndInjectsBeta(t *testing.T) {
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-ant-api03-subagent-key-test",
		}},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-subagent-key-test",
		Attributes: map[string]string{
			"api_key":  "sk-ant-api03-subagent-key-test",
			"base_url": server.URL,
		},
	}

	executor := NewClaudeExecutor(cfg)
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "apikey subagent work", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
			]
		}]
	}`)
	incomingHeaders := http.Header{}
	incomingHeaders.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	incomingHeaders.Set("X-Claude-Code-Agent-Id", "agent-sub-apikey-101")
	incomingHeaders.Set("Anthropic-Beta", "claude-code-20250219") // Note: NO extended-cache-ttl beta in incoming header

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incomingHeaders,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	// 1. Verify body cache_control preserves ttl: "1h"
	rawBody := string(seenBody)
	if !strings.Contains(rawBody, `"ttl":"1h"`) && !strings.Contains(rawBody, `"ttl": "1h"`) {
		t.Fatalf("API key subagent body must preserve ttl: 1h, got: %s", rawBody)
	}

	// 2. Verify extended-cache-ttl beta is added to Anthropic-Beta header
	betas := seenHeaders.Get("Anthropic-Beta")
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("API key subagent Anthropic-Beta must include extended-cache-ttl-2025-04-11 when 1h is in payload, got: %s", betas)
	}
}

// TestClaudeExecutor_APIKeySubagentPreserves1hTTLAndInjectsBeta_Stream verifies streaming
// on standard API key preserves 1h TTL and supplies extended-cache-ttl beta.
func TestClaudeExecutor_APIKeySubagentPreserves1hTTLAndInjectsBeta_Stream(t *testing.T) {
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-ant-api03-subagent-stream-key-test",
		}},
	}
	auth := &cliproxyauth.Auth{
		ID: "auth-subagent-stream-key-test",
		Attributes: map[string]string{
			"api_key":  "sk-ant-api03-subagent-stream-key-test",
			"base_url": server.URL,
		},
	}

	executor := NewClaudeExecutor(cfg)
	payload := []byte(`{
		"model": "claude-sonnet-5",
		"stream": true,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "apikey streaming subagent work", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
			]
		}]
	}`)
	incomingHeaders := http.Header{}
	incomingHeaders.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	incomingHeaders.Set("X-Claude-Code-Agent-Id", "agent-sub-apikey-stream-202")
	incomingHeaders.Set("Anthropic-Beta", "claude-code-20250219") // Note: NO extended-cache-ttl beta in incoming header

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incomingHeaders,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}
	if result != nil && result.Chunks != nil {
		for range result.Chunks {
		}
	}

	// 1. Verify body cache_control preserves ttl: "1h"
	rawBody := string(seenBody)
	if !strings.Contains(rawBody, `"ttl":"1h"`) && !strings.Contains(rawBody, `"ttl": "1h"`) {
		t.Fatalf("API key subagent stream body must preserve ttl: 1h, got: %s", rawBody)
	}

	// 2. Verify extended-cache-ttl beta is added to Anthropic-Beta header
	betas := seenHeaders.Get("Anthropic-Beta")
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("API key subagent Anthropic-Beta must include extended-cache-ttl-2025-04-11 when 1h is in payload on stream, got: %s", betas)
	}
}
