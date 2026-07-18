package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

func testGeminiSignaturePayload() string {
	payload := append([]byte{0x0A}, bytes.Repeat([]byte{0x56}, 48)...)
	return base64.StdEncoding.EncodeToString(payload)
}

// testFakeClaudeSignature returns a base64 string starting with 'E' that passes
// the lightweight hasValidClaudeSignature check but has invalid protobuf content
// (first decoded byte 0x12 is correct, but no valid protobuf field 2 follows),
// so it fails deep validation in strict mode.
func testFakeClaudeSignature() string {
	return base64.StdEncoding.EncodeToString([]byte{0x12, 0xFF, 0xFE, 0xFD})
}

func testAntigravityAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": baseURL,
		},
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
}

func invalidClaudeThinkingPayload() []byte {
	return []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + testFakeClaudeSignature() + `"},
					{"type": "text", "text": "hello"}
				]
			}
		]
	}`)
}

func newSignatureDebugHook(t *testing.T) *test.Hook {
	t.Helper()

	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	hook := test.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})
	return hook
}

func assertSignatureDebugDoesNotLeak(t *testing.T, hook *test.Hook, forbidden string) {
	t.Helper()

	if forbidden == "" {
		return
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, forbidden) {
			t.Fatalf("debug log leaked signature in message: %q", entry.Message)
		}
		for key, value := range entry.Data {
			if strings.Contains(fmt.Sprint(value), forbidden) {
				t.Fatalf("debug log leaked signature in field %q: %v", key, value)
			}
		}
	}
}

func TestAntigravityExecutor_StrictBypassStripsInvalidSignature(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	previousStrict := cache.SignatureBypassStrictMode()
	cache.SetSignatureCacheEnabled(false)
	cache.SetSignatureBypassStrictMode(true)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})

	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	output, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload)
	if err != nil {
		t.Fatalf("strict bypass should strip invalid signatures instead of rejecting request: %v", err)
	}
	parts := gjson.GetBytes(output, "messages.0.content").Array()
	if len(parts) != 1 {
		t.Fatalf("content length = %d, want 1 after invalid thinking strip: %s", len(parts), output)
	}
	if got := parts[0].Get("type").String(); got != "text" {
		t.Fatalf("remaining part type = %q, want text: %s", got, output)
	}
}

func TestAntigravityExecutor_StrictBypassLogsStrippedInvalidSignature(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	previousStrict := cache.SignatureBypassStrictMode()
	cache.SetSignatureCacheEnabled(false)
	cache.SetSignatureBypassStrictMode(true)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})

	hook := newSignatureDebugHook(t)
	rawSignature := testFakeClaudeSignature()
	payload := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + rawSignature + `"},
					{"type": "text", "text": "hello"}
				]
			}
		]
	}`)
	from := sdktranslator.FromString("claude")

	if _, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload); err != nil {
		t.Fatalf("strict bypass should strip invalid signatures instead of rejecting request: %v", err)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.DebugLevel {
			continue
		}
		if entry.Data["component"] != "signature_sanitizer" ||
			entry.Data["executor"] != "antigravity" ||
			entry.Data["action"] != "drop_thinking_blocks" ||
			entry.Data["stage"] != "strict_bypass" {
			continue
		}
		if entry.Data["count"] != 1 {
			t.Fatalf("debug drop count = %v, want 1", entry.Data["count"])
		}
		found = true
	}
	if !found {
		t.Fatal("expected debug log for stripped Antigravity Claude thinking signature")
	}
	assertSignatureDebugDoesNotLeak(t, hook, rawSignature)
}

func TestClaudeExecutor_LogsSanitizedClaudeUpstreamSignatures(t *testing.T) {
	hook := newSignatureDebugHook(t)
	rawSignature := "skip_thought_signature_validator"
	body := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + rawSignature + `"},
					{"type": "text", "text": "hello"},
					{"type": "tool_use", "id": "call_123", "name": "get_weather", "input": {}, "signature": "` + rawSignature + `"}
				]
			}
		]
	}`)

	output := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(context.Background(), body, "claude-sonnet-4-5")
	parts := gjson.GetBytes(output, "messages.0.content").Array()
	if len(parts) != 2 {
		t.Fatalf("content length = %d, want 2 after invalid thinking strip: %s", len(parts), output)
	}
	if parts[1].Get("signature").Exists() {
		t.Fatalf("tool_use signature should be removed before Claude upstream: %s", output)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.DebugLevel {
			continue
		}
		if entry.Data["component"] != "signature_sanitizer" ||
			entry.Data["executor"] != "claude" ||
			entry.Data["action"] != "sanitize_claude_messages" {
			continue
		}
		if entry.Data["dropped_blocks"] != 1 {
			t.Fatalf("dropped_blocks = %v, want 1", entry.Data["dropped_blocks"])
		}
		if entry.Data["dropped_signatures"] != 1 {
			t.Fatalf("dropped_signatures = %v, want 1", entry.Data["dropped_signatures"])
		}
		found = true
	}
	if !found {
		t.Fatal("expected debug log for Claude upstream signature sanitization")
	}
	assertSignatureDebugDoesNotLeak(t, hook, rawSignature)
}

func TestAntigravityExecutor_NonStrictBypassSkipsPrecheck(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	previousStrict := cache.SignatureBypassStrictMode()
	cache.SetSignatureCacheEnabled(false)
	cache.SetSignatureBypassStrictMode(false)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})

	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	_, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload)
	if err != nil {
		t.Fatalf("non-strict bypass should skip precheck, got: %v", err)
	}
}

func TestAntigravityExecutor_CacheModeSkipsPrecheck(t *testing.T) {
	previous := cache.SignatureCacheEnabled()
	cache.SetSignatureCacheEnabled(true)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previous)
	})

	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	_, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload)
	if err != nil {
		t.Fatalf("cache mode should skip precheck, got: %v", err)
	}
}
