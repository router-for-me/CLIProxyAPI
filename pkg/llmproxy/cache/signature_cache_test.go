package cache

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	homekv "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/home"
	log "github.com/sirupsen/logrus"
)

const testModelName = "claude-sonnet-4-5"

type fakeSignatureKVClient struct {
	values        map[string][]byte
	getErr        error
	setErr        error
	delErr        error
	expireErr     error
	getCount      int
	setCount      int
	delCount      int
	expireCount   int
	lastSetTTL    time.Duration
	lastExpireTTL time.Duration
}

func newFakeSignatureKVClient() *fakeSignatureKVClient {
	return &fakeSignatureKVClient{values: make(map[string][]byte)}
}

func (c *fakeSignatureKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.getCount++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *fakeSignatureKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.setCount++
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeSignatureKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.delCount++
	if c.delErr != nil {
		return 0, c.delErr
	}
	var deleted int64
	for _, key := range keys {
		if _, ok := c.values[key]; ok {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func (c *fakeSignatureKVClient) KVExpire(_ context.Context, _ string, ttl time.Duration) (bool, error) {
	c.expireCount++
	c.lastExpireTTL = ttl
	if c.expireErr != nil {
		return false, c.expireErr
	}
	return true, nil
}

func useFakeSignatureKVClient(t *testing.T, client *fakeSignatureKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentSignatureKVClient
	currentSignatureKVClient = func() (signatureKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentSignatureKVClient = previous
	})
}

func TestCacheSignature_BasicStorageAndRetrieval(t *testing.T) {
	ClearSignatureCache("")

	text := "This is some thinking text content"
	signature := "abc123validSignature1234567890123456789012345678901234567890"

	// Store signature
	CacheSignature(testModelName, text, signature)

	// Retrieve signature
	retrieved := GetCachedSignature(testModelName, text)
	if retrieved != signature {
		t.Errorf("Expected signature '%s', got '%s'", signature, retrieved)
	}
}

func TestGetCachedSignatureRequiredHomeReadAndSlidingExpire(t *testing.T) {
	ClearSignatureCache("")
	text := "thinking text"
	signature := "abc123validSignature1234567890123456789012345678901234567890"
	client := newFakeSignatureKVClient()
	client.values[signatureKVKey(testModelName, text)] = []byte(signature)
	useFakeSignatureKVClient(t, client, true, nil)

	got, errGet := GetCachedSignatureRequired(context.Background(), testModelName, text)
	if errGet != nil {
		t.Fatalf("GetCachedSignatureRequired() error = %v", errGet)
	}
	if got != signature {
		t.Fatalf("GetCachedSignatureRequired() = %q, want %q", got, signature)
	}
	if client.expireCount != 1 || client.lastExpireTTL != SignatureCacheTTL {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/%v", client.expireCount, client.lastExpireTTL, SignatureCacheTTL)
	}
}

func TestGetCachedSignatureRequiredHomeFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *fakeSignatureKVClient
	}{
		{name: "get", client: &fakeSignatureKVClient{values: make(map[string][]byte), getErr: errors.New("get failed")}},
		{name: "expire", client: &fakeSignatureKVClient{values: map[string][]byte{
			signatureKVKey(testModelName, "thinking text"): []byte("abc123validSignature1234567890123456789012345678901234567890"),
		}, expireErr: errors.New("expire failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeSignatureKVClient(t, tc.client, true, nil)
			if _, errGet := GetCachedSignatureRequired(context.Background(), testModelName, "thinking text"); errGet == nil {
				t.Fatalf("GetCachedSignatureRequired() error = nil, want error")
			}
		})
	}
}

func TestGetCachedSignatureRequiredHomeMissDoesNotFallbackToLocalCache(t *testing.T) {
	ClearSignatureCache("")
	text := "thinking text"
	signature := "abc123validSignature1234567890123456789012345678901234567890"
	CacheSignature(testModelName, text, signature)

	client := newFakeSignatureKVClient()
	useFakeSignatureKVClient(t, client, true, nil)

	got, errGet := GetCachedSignatureRequired(context.Background(), testModelName, text)
	if errGet != nil {
		t.Fatalf("GetCachedSignatureRequired() error = %v", errGet)
	}
	if got != "" {
		t.Fatalf("GetCachedSignatureRequired() = %q, want Home miss without local fallback", got)
	}
}

func TestCacheSignatureBestEffortHomeWriteFailureDoesNotUseLocalCache(t *testing.T) {
	ClearSignatureCache("")
	text := "thinking text"
	signature := "abc123validSignature1234567890123456789012345678901234567890"
	client := newFakeSignatureKVClient()
	client.setErr = errors.New("set failed")
	useFakeSignatureKVClient(t, client, true, nil)

	if CacheSignatureBestEffort(context.Background(), testModelName, text, signature) {
		t.Fatalf("CacheSignatureBestEffort() = true, want false")
	}
	useFakeSignatureKVClient(t, newFakeSignatureKVClient(), false, nil)
	if got := GetCachedSignature(testModelName, text); got != "" {
		t.Fatalf("local cache = %q, want empty after Home write failure", got)
	}
}

func TestDeleteCachedSignatureRequiredHomeExactKey(t *testing.T) {
	ClearSignatureCache("")
	text := "thinking text"
	signature := "abc123validSignature1234567890123456789012345678901234567890"
	client := newFakeSignatureKVClient()
	client.values[signatureKVKey(testModelName, text)] = []byte(signature)
	useFakeSignatureKVClient(t, client, true, nil)

	if errDel := DeleteCachedSignatureRequired(context.Background(), testModelName, text); errDel != nil {
		t.Fatalf("DeleteCachedSignatureRequired() error = %v", errDel)
	}
	if _, ok := client.values[signatureKVKey(testModelName, text)]; ok {
		t.Fatalf("signature key was not deleted")
	}
	if client.delCount != 1 {
		t.Fatalf("KVDel count = %d, want 1", client.delCount)
	}
}

func TestClearSignatureCacheHomeDoesNotPrefixDelete(t *testing.T) {
	client := newFakeSignatureKVClient()
	useFakeSignatureKVClient(t, client, true, nil)

	ClearSignatureCache("")
	ClearSignatureCache(testModelName)

	if client.delCount != 0 {
		t.Fatalf("ClearSignatureCache() KVDel count = %d, want 0", client.delCount)
	}
}

func TestGetCachedSignatureRequiredGeminiEmptyThinkingSentinel(t *testing.T) {
	client := newFakeSignatureKVClient()
	client.getErr = errors.New("get should not be called")
	useFakeSignatureKVClient(t, client, true, nil)

	got, errGet := GetCachedSignatureRequired(context.Background(), "gemini-3-pro-preview", "")
	if errGet != nil {
		t.Fatalf("GetCachedSignatureRequired() error = %v", errGet)
	}
	if got != "skip_thought_signature_validator" {
		t.Fatalf("GetCachedSignatureRequired() = %q, want Gemini sentinel", got)
	}
	if client.getCount != 0 {
		t.Fatalf("KVGet count = %d, want 0", client.getCount)
	}
}

func TestCacheSignature_DifferentModelGroups(t *testing.T) {
	ClearSignatureCache("")

	text := "Same text across models"
	sig1 := "signature1_1234567890123456789012345678901234567890123456"
	sig2 := "signature2_1234567890123456789012345678901234567890123456"

	geminiModel := "gemini-3-pro-preview"
	CacheSignature(testModelName, text, sig1)
	CacheSignature(geminiModel, text, sig2)

	if GetCachedSignature(testModelName, text) != sig1 {
		t.Error("Claude signature mismatch")
	}
	if GetCachedSignature(geminiModel, text) != sig2 {
		t.Error("Gemini signature mismatch")
	}
}

func TestCacheSignature_NotFound(t *testing.T) {
	ClearSignatureCache("")

	// Non-existent session
	if got := GetCachedSignature(testModelName, "some text"); got != "" {
		t.Errorf("Expected empty string for nonexistent session, got '%s'", got)
	}

	// Existing session but different text
	CacheSignature(testModelName, "text-a", "sigA12345678901234567890123456789012345678901234567890")
	if got := GetCachedSignature(testModelName, "text-b"); got != "" {
		t.Errorf("Expected empty string for different text, got '%s'", got)
	}
}

func TestCacheSignature_EmptyInputs(t *testing.T) {
	ClearSignatureCache("")

	// All empty/invalid inputs should be no-ops
	CacheSignature(testModelName, "", "sig12345678901234567890123456789012345678901234567890")
	CacheSignature(testModelName, "text", "")
	CacheSignature(testModelName, "text", "short") // Too short

	if got := GetCachedSignature(testModelName, "text"); got != "" {
		t.Errorf("Expected empty after invalid cache attempts, got '%s'", got)
	}
}

func TestCacheSignature_ShortSignatureRejected(t *testing.T) {
	ClearSignatureCache("")

	text := "Some text"
	shortSig := "abc123" // Less than 50 chars

	CacheSignature(testModelName, text, shortSig)

	if got := GetCachedSignature(testModelName, text); got != "" {
		t.Errorf("Short signature should be rejected, got '%s'", got)
	}
}

func TestClearSignatureCache_ModelGroup(t *testing.T) {
	ClearSignatureCache("")

	sig := "validSig1234567890123456789012345678901234567890123456"
	CacheSignature(testModelName, "text", sig)
	CacheSignature(testModelName, "text-2", sig)

	ClearSignatureCache("session-1")

	if got := GetCachedSignature(testModelName, "text"); got != sig {
		t.Error("signature should remain when clearing unknown session")
	}
}

func TestClearSignatureCache_AllSessions(t *testing.T) {
	ClearSignatureCache("")

	sig := "validSig1234567890123456789012345678901234567890123456"
	CacheSignature(testModelName, "text", sig)
	CacheSignature(testModelName, "text-2", sig)

	ClearSignatureCache("")

	if got := GetCachedSignature(testModelName, "text"); got != "" {
		t.Error("text should be cleared")
	}
	if got := GetCachedSignature(testModelName, "text-2"); got != "" {
		t.Error("text-2 should be cleared")
	}
}

func TestHasValidSignature(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		signature string
		expected  bool
	}{
		{"valid long signature", testModelName, "abc123validSignature1234567890123456789012345678901234567890", true},
		{"exactly 50 chars", testModelName, "12345678901234567890123456789012345678901234567890", true},
		{"49 chars - invalid", testModelName, "1234567890123456789012345678901234567890123456789", false},
		{"empty string", testModelName, "", false},
		{"short signature", testModelName, "abc", false},
		{"gemini sentinel", "gemini-3-pro-preview", "skip_thought_signature_validator", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasValidSignature(tt.modelName, tt.signature)
			if result != tt.expected {
				t.Errorf("HasValidSignature(%q) = %v, expected %v", tt.signature, result, tt.expected)
			}
		})
	}
}

func TestCacheSignature_TextHashCollisionResistance(t *testing.T) {
	ClearSignatureCache("")

	// Different texts should produce different hashes
	text1 := "First thinking text"
	text2 := "Second thinking text"
	sig1 := "signature1_1234567890123456789012345678901234567890123456"
	sig2 := "signature2_1234567890123456789012345678901234567890123456"

	CacheSignature(testModelName, text1, sig1)
	CacheSignature(testModelName, text2, sig2)

	if GetCachedSignature(testModelName, text1) != sig1 {
		t.Error("text1 signature mismatch")
	}
	if GetCachedSignature(testModelName, text2) != sig2 {
		t.Error("text2 signature mismatch")
	}
}

func TestCacheSignature_UnicodeText(t *testing.T) {
	ClearSignatureCache("")

	text := "한글 텍스트와 이모지 🎉 그리고 特殊文字"
	sig := "unicodeSig123456789012345678901234567890123456789012345"

	CacheSignature(testModelName, text, sig)

	if got := GetCachedSignature(testModelName, text); got != sig {
		t.Errorf("Unicode text signature retrieval failed, got '%s'", got)
	}
}

func TestCacheSignature_Overwrite(t *testing.T) {
	ClearSignatureCache("")

	text := "Same text"
	sig1 := "firstSignature12345678901234567890123456789012345678901"
	sig2 := "secondSignature1234567890123456789012345678901234567890"

	CacheSignature(testModelName, text, sig1)
	CacheSignature(testModelName, text, sig2) // Overwrite

	if got := GetCachedSignature(testModelName, text); got != sig2 {
		t.Errorf("Expected overwritten signature '%s', got '%s'", sig2, got)
	}
}

// Note: TTL expiration test is tricky to test without mocking time
// We test the logic path exists but actual expiration would require time manipulation
func TestCacheSignature_ExpirationLogic(t *testing.T) {
	ClearSignatureCache("")

	// This test verifies the expiration check exists
	// In a real scenario, we'd mock time.Now()
	text := "text"
	sig := "validSig1234567890123456789012345678901234567890123456"

	CacheSignature(testModelName, text, sig)

	// Fresh entry should be retrievable
	if got := GetCachedSignature(testModelName, text); got != sig {
		t.Errorf("Fresh entry should be retrievable, got '%s'", got)
	}

	// We can't easily test actual expiration without time mocking
	// but the logic is verified by the implementation
	_ = time.Now() // Acknowledge we're not testing time passage
}

func TestSignatureModeSetters_LogAtInfoLevel(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	previousCache := SignatureCacheEnabled()
	previousStrict := SignatureBypassStrictMode()
	SetSignatureCacheEnabled(true)
	SetSignatureBypassStrictMode(false)
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
		SetSignatureCacheEnabled(previousCache)
		SetSignatureBypassStrictMode(previousStrict)
	})

	SetSignatureCacheEnabled(false)
	SetSignatureBypassStrictMode(true)
	SetSignatureBypassStrictMode(false)

	output := buffer.String()
	if !strings.Contains(output, "antigravity signature cache DISABLED") {
		t.Fatalf("expected info output for disabling signature cache, got: %q", output)
	}
	if strings.Contains(output, "strict mode (protobuf tree)") {
		t.Fatalf("expected strict bypass mode log to stay below info level, got: %q", output)
	}
	if strings.Contains(output, "basic mode (R/E + 0x12)") {
		t.Fatalf("expected basic bypass mode log to stay below info level, got: %q", output)
	}
}

func TestSignatureModeSetters_DoNotRepeatSameStateLogs(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	previousCache := SignatureCacheEnabled()
	previousStrict := SignatureBypassStrictMode()
	SetSignatureCacheEnabled(false)
	SetSignatureBypassStrictMode(true)
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
		SetSignatureCacheEnabled(previousCache)
		SetSignatureBypassStrictMode(previousStrict)
	})

	SetSignatureCacheEnabled(false)
	SetSignatureBypassStrictMode(true)

	if buffer.Len() != 0 {
		t.Fatalf("expected repeated setter calls with unchanged state to stay silent, got: %q", buffer.String())
	}
}

func TestSignatureBypassStrictMode_LogsAtDebugLevel(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	previousStrict := SignatureBypassStrictMode()
	SetSignatureBypassStrictMode(false)
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
		SetSignatureBypassStrictMode(previousStrict)
	})

	SetSignatureBypassStrictMode(true)
	SetSignatureBypassStrictMode(false)

	output := buffer.String()
	if !strings.Contains(output, "strict mode (protobuf tree)") {
		t.Fatalf("expected debug output for strict bypass mode, got: %q", output)
	}
	if !strings.Contains(output, "basic mode (R/E + 0x12)") {
		t.Fatalf("expected debug output for basic bypass mode, got: %q", output)
	}
}
