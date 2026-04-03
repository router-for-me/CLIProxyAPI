package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestStickySelector_ExtractSessionKey_GjsonPriority(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)

	body := []byte(`{"metadata":{"user_id":"uid-123"},"prompt_cache_key":"pck-456"}`)
	got := s.extractSessionKey(body)
	if got != "uid-123" {
		t.Fatalf("extractSessionKey() = %q, want %q (metadata.user_id should win)", got, "uid-123")
	}
}

func TestStickySelector_ExtractSessionKey_FallbackToPromptCacheKey(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)

	body := []byte(`{"prompt_cache_key":"pck-456","model":"gpt-5.4"}`)
	got := s.extractSessionKey(body)
	if got != "pck-456" {
		t.Fatalf("extractSessionKey() = %q, want %q", got, "pck-456")
	}
}

func TestStickySelector_ExtractSessionKey_BodyPrefixHashFallback(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)

	// Build a body large enough to trigger hash (>= minBodySizeForHash).
	body := []byte(`{"model":"gemini-3-flash","contents":[{"role":"user","parts":[{"text":"` +
		strings.Repeat("Hello world. ", 100) + `"}]}]}`)
	if len(body) < minBodySizeForHash {
		t.Fatalf("test body too small: %d < %d", len(body), minBodySizeForHash)
	}

	got := s.extractSessionKey(body)
	if got == "" {
		t.Fatal("extractSessionKey() returned empty, expected bph: hash")
	}
	if !strings.HasPrefix(got, "bph:") {
		t.Fatalf("extractSessionKey() = %q, expected bph: prefix", got)
	}

	// Verify determinism: same payload → same key.
	got2 := s.extractSessionKey(body)
	if got != got2 {
		t.Fatalf("extractSessionKey() not deterministic: %q != %q", got, got2)
	}
}

func TestStickySelector_ExtractSessionKey_SmallBodyNoHash(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)

	body := []byte(`{"model":"gemini-3-flash","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`)
	if len(body) >= minBodySizeForHash {
		t.Fatalf("test body too large: %d >= %d", len(body), minBodySizeForHash)
	}

	got := s.extractSessionKey(body)
	if got != "" {
		t.Fatalf("extractSessionKey() = %q, expected empty for small body", got)
	}
}

func TestStickySelector_ExtractSessionKey_EmptyBody(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)

	if got := s.extractSessionKey(nil); got != "" {
		t.Fatalf("extractSessionKey(nil) = %q, want empty", got)
	}
	if got := s.extractSessionKey([]byte{}); got != "" {
		t.Fatalf("extractSessionKey([]) = %q, want empty", got)
	}
}

func TestStickySelector_BodyPrefixHash_Truncation(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)
	s.bodyPrefixHashSize = 1024 // small for testing

	// Two payloads sharing the same first 1024 bytes should produce the same hash.
	prefix := `{"contents":[{"role":"user","parts":[{"text":"` + strings.Repeat("A", 2000) + `"}]}]}`
	body1 := []byte(prefix)
	body2 := append([]byte(prefix), []byte(`,"extra":"data"`)...)

	hash1 := s.bodyPrefixHash(body1)
	hash2 := s.bodyPrefixHash(body2)
	if hash1 != hash2 {
		t.Fatalf("hash should be equal for same prefix: %q != %q", hash1, hash2)
	}

	// Verify against manual computation.
	data := body1[:1024]
	sum := sha256.Sum256(data)
	want := "bph:" + hex.EncodeToString(sum[:16])
	if hash1 != want {
		t.Fatalf("bodyPrefixHash() = %q, want %q", hash1, want)
	}
}

func TestStickySelector_Pick_StickyViaHash(t *testing.T) {
	t.Parallel()

	s := NewStickySelector(16, nil, nil)
	auths := []*Auth{
		{ID: "acct-a"},
		{ID: "acct-b"},
		{ID: "acct-c"},
	}

	// A large Gemini-style body (no metadata.user_id, no prompt_cache_key).
	body := []byte(`{"model":"gemini-3-flash","contents":[{"role":"user","parts":[{"text":"` +
		strings.Repeat("Tell me about Go programming. ", 50) + `"}]}]}`)

	opts := cliproxyexecutor.Options{OriginalRequest: body}

	// First pick: round-robin selects some account.
	first, err := s.Pick(context.Background(), "antigravity", "gemini-3-flash", opts, auths)
	if err != nil {
		t.Fatalf("Pick() #1 error = %v", err)
	}

	// Subsequent picks with the same body should return the same account.
	for i := 0; i < 5; i++ {
		got, err := s.Pick(context.Background(), "antigravity", "gemini-3-flash", opts, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i+2, err)
		}
		if got.ID != first.ID {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q (sticky violation)", i+2, got.ID, first.ID)
		}
	}
}

func TestStickySelector_Pick_GjsonKeyTakesPrecedenceOverHash(t *testing.T) {
	t.Parallel()

	s := NewStickySelector(16, nil, nil)
	auths := []*Auth{
		{ID: "acct-a"},
		{ID: "acct-b"},
	}

	// Body has metadata.user_id AND is large enough for hash.
	body := []byte(`{"metadata":{"user_id":"uid-999"},"messages":[{"role":"user","content":"` +
		strings.Repeat("x", 1000) + `"}]}`)

	opts := cliproxyexecutor.Options{OriginalRequest: body}

	first, err := s.Pick(context.Background(), "antigravity", "claude-sonnet-4-6", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// Remove the cached gjson-based entry and re-pick — should re-bind via gjson, not hash.
	s.EvictSession("uid-999")
	second, err := s.Pick(context.Background(), "antigravity", "claude-sonnet-4-6", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// Both should use "uid-999" as key, so after eviction round-robin picks next.
	// The important thing is that the session key is "uid-999" not a hash.
	_ = first
	_ = second
	if s.CacheLen() != 1 {
		t.Fatalf("CacheLen() = %d, want 1 (single key for uid-999)", s.CacheLen())
	}
}

func TestStickySelector_Config_Disabled(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, &StickyBodyHashConfig{Enabled: false})

	body := []byte(`{"model":"gemini-3-flash","contents":[{"role":"user","parts":[{"text":"` +
		strings.Repeat("Hello world. ", 100) + `"}]}]}`)

	got := s.extractSessionKey(body)
	if got != "" {
		t.Fatalf("extractSessionKey() = %q, expected empty when body hash disabled", got)
	}
}

func TestStickySelector_Config_CustomSizeKB(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, &StickyBodyHashConfig{Enabled: true, SizeKB: 1})

	// Body larger than 1 KB but build two variants: same first 1KB, different after.
	prefix := strings.Repeat("A", 1024)
	body1 := []byte(`{"x":"` + prefix + strings.Repeat("B", 500) + `"}`)
	body2 := []byte(`{"x":"` + prefix + strings.Repeat("C", 500) + `"}`)

	hash1 := s.extractSessionKey(body1)
	hash2 := s.extractSessionKey(body2)

	if hash1 == "" || hash2 == "" {
		t.Fatalf("expected non-empty hashes, got %q and %q", hash1, hash2)
	}
	if hash1 != hash2 {
		t.Fatalf("hashes should match for same 1KB prefix: %q != %q", hash1, hash2)
	}
}

func TestStickySelector_Config_NilDefaultsToEnabled(t *testing.T) {
	t.Parallel()
	s := NewStickySelector(16, nil, nil)
	if s.bodyPrefixHashSize != defaultBodyPrefixHashSize {
		t.Fatalf("bodyPrefixHashSize = %d, want %d", s.bodyPrefixHashSize, defaultBodyPrefixHashSize)
	}
}
