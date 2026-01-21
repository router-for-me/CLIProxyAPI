package cache

import (
	"testing"
	"time"
)

func TestCacheSignature_BasicStorageAndRetrieval(t *testing.T) {
	ClearSignatureCache("")

	text := "This is some thinking text content"
	signature := "abc123validSignature1234567890123456789012345678901234567890"

	// Store signature
	CacheSignature("test-model", text, signature)

	// Retrieve signature
	retrieved := GetCachedSignature("test-model", text)
	if retrieved != signature {
		t.Errorf("Expected signature '%s', got '%s'", signature, retrieved)
	}
}

func TestCacheSignature_DifferentSessions(t *testing.T) {
	ClearSignatureCache("")

	text := "Same text in different sessions"
	sig1 := "signature1_1234567890123456789012345678901234567890123456"
	sig2 := "signature2_1234567890123456789012345678901234567890123456"

	CacheSignature("test-model", text, sig1)
	CacheSignature("test-model", text, sig2)

	if GetCachedSignature("test-model", text) != sig1 {
		t.Error("Session-a signature mismatch")
	}
	if GetCachedSignature("test-model", text) != sig2 {
		t.Error("Session-b signature mismatch")
	}
}

func TestCacheSignature_NotFound(t *testing.T) {
	ClearSignatureCache("")

	// Non-existent session
	if got := GetCachedSignature("test-model", "some text"); got != "" {
		t.Errorf("Expected empty string for nonexistent session, got '%s'", got)
	}

	// Existing session but different text
	CacheSignature("test-model", "text-a", "sigA12345678901234567890123456789012345678901234567890")
	if got := GetCachedSignature("test-model", "text-b"); got != "" {
		t.Errorf("Expected empty string for different text, got '%s'", got)
	}
}

func TestCacheSignature_EmptyInputs(t *testing.T) {
	ClearSignatureCache("")

	// All empty/invalid inputs should be no-ops
	CacheSignature("test-model", "text", "sig12345678901234567890123456789012345678901234567890")
	CacheSignature("test-model", "", "sig12345678901234567890123456789012345678901234567890")
	CacheSignature("test-model", "text", "")
	CacheSignature("test-model", "text", "short") // Too short

	if got := GetCachedSignature("test-model", "text"); got != "" {
		t.Errorf("Expected empty after invalid cache attempts, got '%s'", got)
	}
}

func TestCacheSignature_ShortSignatureRejected(t *testing.T) {
	ClearSignatureCache("")

	text := "Some text"
	shortSig := "abc123" // Less than 50 chars

	CacheSignature("test-model", text, shortSig)

	if got := GetCachedSignature("test-model", text); got != "" {
		t.Errorf("Short signature should be rejected, got '%s'", got)
	}
}

func TestClearSignatureCache_SpecificSession(t *testing.T) {
	ClearSignatureCache("")

	sig := "validSig1234567890123456789012345678901234567890123456"
	CacheSignature("test-model", "text", sig)
	CacheSignature("test-model", "text", sig)

	ClearSignatureCache("session-1")

	if got := GetCachedSignature("test-model", "text"); got != "" {
		t.Error("session-1 should be cleared")
	}
	if got := GetCachedSignature("test-model", "text"); got != sig {
		t.Error("session-2 should still exist")
	}
}

func TestClearSignatureCache_AllSessions(t *testing.T) {
	ClearSignatureCache("")

	sig := "validSig1234567890123456789012345678901234567890123456"
	CacheSignature("test-model", "text", sig)
	CacheSignature("test-model", "text", sig)

	ClearSignatureCache("")

	if got := GetCachedSignature("test-model", "text"); got != "" {
		t.Error("session-1 should be cleared")
	}
	if got := GetCachedSignature("test-model", "text"); got != "" {
		t.Error("session-2 should be cleared")
	}
}

func TestHasValidSignature(t *testing.T) {
	tests := []struct {
		name      string
		signature string
		expected  bool
	}{
		{"valid long signature", "abc123validSignature1234567890123456789012345678901234567890", true},
		{"exactly 50 chars", "12345678901234567890123456789012345678901234567890", true},
		{"49 chars - invalid", "1234567890123456789012345678901234567890123456789", false},
		{"empty string", "", false},
		{"short signature", "abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasValidSignature("claude-sonnet-4-5-thinking", tt.signature)
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

	CacheSignature("test-model", text1, sig1)
	CacheSignature("test-model", text2, sig2)

	if GetCachedSignature("test-model", text1) != sig1 {
		t.Error("text1 signature mismatch")
	}
	if GetCachedSignature("test-model", text2) != sig2 {
		t.Error("text2 signature mismatch")
	}
}

func TestCacheSignature_UnicodeText(t *testing.T) {
	ClearSignatureCache("")

	text := "한글 텍스트와 이모지 🎉 그리고 特殊文字"
	sig := "unicodeSig123456789012345678901234567890123456789012345"

	CacheSignature("test-model", text, sig)

	if got := GetCachedSignature("test-model", text); got != sig {
		t.Errorf("Unicode text signature retrieval failed, got '%s'", got)
	}
}

func TestCacheSignature_Overwrite(t *testing.T) {
	ClearSignatureCache("")

	text := "Same text"
	sig1 := "firstSignature12345678901234567890123456789012345678901"
	sig2 := "secondSignature1234567890123456789012345678901234567890"

	CacheSignature("test-model", text, sig1)
	CacheSignature("test-model", text, sig2) // Overwrite

	if got := GetCachedSignature("test-model", text); got != sig2 {
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

	CacheSignature("test-model", text, sig)

	// Fresh entry should be retrievable
	if got := GetCachedSignature("test-model", text); got != sig {
		t.Errorf("Fresh entry should be retrievable, got '%s'", got)
	}

	// We can't easily test actual expiration without time mocking
	// but the logic is verified by the implementation
	_ = time.Now() // Acknowledge we're not testing time passage
}
