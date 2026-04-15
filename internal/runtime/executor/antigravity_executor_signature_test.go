package executor

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func testGeminiSignaturePayload() string {
	payload := append([]byte{0x0A}, bytes.Repeat([]byte{0x56}, 48)...)
	return base64.StdEncoding.EncodeToString(payload)
}

func invalidClaudeThinkingPayload() []byte {
	return []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + testGeminiSignaturePayload() + `"},
					{"type": "text", "text": "hello"}
				]
			}
		]
	}`)
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

	// Invalid (non-Claude) signatures are stripped before validation, so
	// requests proceed to upstream without error.
	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	result, err := validateAntigravityRequestSignatures(from, payload)
	if err != nil {
		t.Fatalf("non-Claude signatures should be stripped, not rejected: %v", err)
	}
	// The thinking block with invalid signature should have been removed.
	if bytes.Contains(result, []byte(`"thinking"`)) {
		t.Fatal("expected thinking block with invalid signature to be stripped from payload")
	}
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

	_, err := validateAntigravityRequestSignatures(from, payload)
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

	_, err := validateAntigravityRequestSignatures(from, payload)
	if err != nil {
		t.Fatalf("cache mode should skip precheck, got: %v", err)
	}
}
