package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protowire"
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

func TestSanitizeAntigravityGeminiRequestSignaturesFinalizesParallelCalls(t *testing.T) {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	nativeSignature := base64.StdEncoding.EncodeToString(encoded)

	tests := []struct {
		name               string
		firstSignature     string
		secondSignature    string
		wantFirstSignature string
	}{
		{
			name:               "synthetic",
			wantFirstSignature: "skip_thought_signature_validator",
		},
		{
			name:               "native",
			firstSignature:     nativeSignature,
			secondSignature:    "skip_thought_signature_validator",
			wantFirstSignature: nativeSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"first","args":{}}},{"functionCall":{"name":"second","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"first","response":{"result":"ok"}}},{"functionResponse":{"name":"second","response":{"result":"ok"}}}]}]}}`)
			if tt.firstSignature != "" {
				payload, _ = sjson.SetBytes(payload, "request.contents.0.parts.0.thoughtSignature", tt.firstSignature)
			}
			if tt.secondSignature != "" {
				payload, _ = sjson.SetBytes(payload, "request.contents.0.parts.1.thoughtSignature", tt.secondSignature)
			}

			output := sanitizeAntigravityGeminiRequestSignatures("gemini-3.5-flash", payload)
			if got := gjson.GetBytes(output, "request.contents.0.parts.0.thoughtSignature").String(); got != tt.wantFirstSignature {
				t.Fatalf("first signature = %q, want %q; output=%s", got, tt.wantFirstSignature, output)
			}
			if signature := gjson.GetBytes(output, "request.contents.0.parts.1.thoughtSignature"); signature.Exists() {
				t.Fatalf("second parallel call should remain unsigned; output=%s", output)
			}
			if got := gjson.GetBytes(output, "request.contents.1.role").String(); got != "model" {
				t.Fatalf("functionResponse role = %q, want native Antigravity model role; output=%s", got, output)
			}
		})
	}
}

func TestAntigravityExecutorCountTokensSanitizesGeminiToolHistory(t *testing.T) {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	nativeSignature := base64.StdEncoding.EncodeToString(encoded)

	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravityCountTokensPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, antigravityCountTokensPath)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read countTokens body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":42}`))
	}))
	defer server.Close()

	payload := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"file":"one"},"signature":"` + nativeSignature + `"},{"type":"tool_use","id":"call-2","name":"read","input":{"file":"two"},"signature":"skip_thought_signature_validator"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-2","content":"two"},{"type":"tool_result","tool_use_id":"call-1","content":"one"}]}]}`)
	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	_, errCount := exec.CountTokens(context.Background(), testAntigravityAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(upstreamBody) == 0 {
		t.Fatal("countTokens upstream body was not captured")
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.0.parts.0.thoughtSignature").String(); got != nativeSignature {
		t.Fatalf("first call signature = %q, want native signature; body=%s", got, upstreamBody)
	}
	if signature := gjson.GetBytes(upstreamBody, "request.contents.0.parts.1.thoughtSignature"); signature.Exists() {
		t.Fatalf("second sibling bypass was not removed: %s", upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.1.role").String(); got != "model" {
		t.Fatalf("functionResponse role = %q, want model; body=%s", got, upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.1.parts.0.functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first functionResponse.id = %q, want call-1; body=%s", got, upstreamBody)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(upstreamBody); errPairing != nil {
		t.Fatalf("countTokens tool history is invalid: %v; body=%s", errPairing, upstreamBody)
	}
}

func TestNormalizeAntigravityGeminiFunctionResponseRolesLeavesMixedUserContent(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"run","response":{"result":"ok"}}},{"text":"user follow-up"}]}]}}`)
	output := normalizeAntigravityGeminiFunctionResponseRoles(payload)
	if got := gjson.GetBytes(output, "request.contents.0.role").String(); got != "user" {
		t.Fatalf("mixed functionResponse/user content role = %q, want user; output=%s", got, output)
	}
}

func TestNormalizeAntigravityGeminiFunctionResponseRolesOrdersParallelResponses(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read","args":{"file":"one"}}},{"functionCall":{"id":"call-2","name":"read","args":{"file":"two"}}}]},{"role":" Model ","parts":[{"functionResponse":{"id":"call-2","name":"read","response":{"result":"two"}}},{"functionResponse":{"id":"call-1","name":"read","response":{"result":"one"}}}]}]}}`)
	output := normalizeAntigravityGeminiFunctionResponseRoles(payload)
	if got := gjson.GetBytes(output, "request.contents.1.role").String(); got != "model" {
		t.Fatalf("functionResponse role = %q, want model; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "request.contents.1.parts.0.functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first functionResponse.id = %q, want call-1; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "request.contents.1.parts.1.functionResponse.id").String(); got != "call-2" {
		t.Fatalf("second functionResponse.id = %q, want call-2; output=%s", got, output)
	}
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(output); errValidate != nil {
		t.Fatalf("normalized parallel responses are invalid: %v; output=%s", errValidate, output)
	}
}

func TestNormalizeAntigravityGeminiFunctionResponseRolesDoesNotCrossEmptyContentBoundary(t *testing.T) {
	for _, boundary := range []string{
		`{"role":"user","parts":[]}`,
		`{"role":"user"}`,
		`{"role":"user","parts":null}`,
	} {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read","args":{}}},{"functionCall":{"id":"call-2","name":"read","args":{}}}]},` + boundary + `,{"role":"user","parts":[{"functionResponse":{"id":"call-2","name":"read","response":{"result":"two"}}},{"functionResponse":{"id":"call-1","name":"read","response":{"result":"one"}}}]}]}}`)
		output := normalizeAntigravityGeminiFunctionResponseRoles(payload)
		if got := gjson.GetBytes(output, "request.contents.2.role").String(); got != "model" {
			t.Fatalf("pure functionResponse role = %q, want model; output=%s", got, output)
		}
		if got := gjson.GetBytes(output, "request.contents.2.parts.0.functionResponse.id").String(); got != "call-2" {
			t.Fatalf("response crossed content boundary %s and was reordered: first id=%q; output=%s", boundary, got, output)
		}
		if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(output); errValidate == nil {
			t.Fatalf("responses crossing content boundary %s were accepted: %s", boundary, output)
		}
	}
}

func TestAntigravityExecutor_GeminiTargetPreservesGeminiThinkingCarrier(t *testing.T) {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	validSignature := base64.StdEncoding.EncodeToString(encoded)
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer"},{"type":"thinking","thinking":"","signature":"` + validSignature + `"},{"type":"thinking","thinking":"","signature":"invalid"}]}]}`)

	output, err := validateAntigravityRequestSignatures(context.Background(), "gemini-3.6-flash-high", sdktranslator.FormatClaude, payload)
	if err != nil {
		t.Fatalf("validateAntigravityRequestSignatures() error = %v", err)
	}
	content := gjson.GetBytes(output, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want text plus valid Gemini carrier: %s", len(content), output)
	}
	if got := content[1].Get("signature").String(); got != validSignature {
		t.Fatalf("preserved signature = %q, want Gemini carrier", got)
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
