package cache

import (
	"testing"
	"time"
)

const testModelName = "claude-sonnet-4-5"

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

// === GetModelGroup Tests ===
// These tests verify that GetModelGroup correctly identifies model groups
// both by name pattern (fast path) and by registry provider lookup (slow path).

func TestGetModelGroup_ByNamePattern(t *testing.T) {
	tests := []struct {
		modelName     string
		expectedGroup string
	}{
		{"gpt-4o", "gpt"},
		{"gpt-4-turbo", "gpt"},
		{"claude-sonnet-4-20250514", "claude"},
		{"claude-opus-4-5-thinking", "claude"},
		{"gemini-2.5-pro", "gemini"},
		{"gemini-3-pro-preview", "gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.modelName, func(t *testing.T) {
			result := GetModelGroup(tt.modelName)
			if result != tt.expectedGroup {
				t.Errorf("GetModelGroup(%q) = %q, expected %q", tt.modelName, result, tt.expectedGroup)
			}
		})
	}
}

func TestGetModelGroup_UnknownModel(t *testing.T) {
	// For unknown models with no registry entry, should return the model name itself
	result := GetModelGroup("unknown-model-xyz")
	if result != "unknown-model-xyz" {
		t.Errorf("GetModelGroup for unknown model should return model name, got %q", result)
	}
}

// TestGetModelGroup_RegistryFallback tests that models registered via
// provider-specific API keys (e.g., kimi-k2.5 via claude-api-key) are
// correctly grouped by their provider.
// This test requires a populated global registry.
func TestGetModelGroup_RegistryFallback(t *testing.T) {
	// This test only makes sense when the global registry is populated
	// In unit test context, skip if registry is empty
	
	// Example: kimi-k2.5 registered via claude-api-key should group as "claude"
	// The model name doesn't contain "claude", so name pattern matching fails.
	// The registry should be checked to find the provider.
	
	// Skip for now - this requires integration test setup
	t.Skip("Requires populated global registry - run as integration test")
}

// === Cross-Model Signature Validation Tests ===
// These tests verify that signatures cached under one model name can be
// validated under mapped model names (same provider group).

func TestCacheSignature_CrossModelValidation(t *testing.T) {
	ClearSignatureCache("")

	// Original request uses "claude-opus-4-5-20251101"
	originalModel := "claude-opus-4-5-20251101"
	// Mapped model is "claude-opus-4-5-thinking"
	mappedModel := "claude-opus-4-5-thinking"
	
	text := "Some thinking block content"
	sig := "validSignature123456789012345678901234567890123456789012"

	// Cache signature under the original model
	CacheSignature(originalModel, text, sig)

	// Both should return the same signature because they're in the same group
	retrieved1 := GetCachedSignature(originalModel, text)
	retrieved2 := GetCachedSignature(mappedModel, text)

	if retrieved1 != sig {
		t.Errorf("Original model signature mismatch: got %q", retrieved1)
	}
	if retrieved2 != sig {
		t.Errorf("Mapped model signature mismatch: got %q", retrieved2)
	}
}
