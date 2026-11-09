package payloadcompress

import (
	"strings"
	"testing"
)

// TestQuantumLockCategories checks every ported category moves its volatile
// token out of the stable prefix and into the value tail.
func TestQuantumLockCategories(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		secret string
	}{
		{"uuid", "id 123e4567-e89b-12d3-a456-426614174000 done", "123e4567-e89b-12d3-a456-426614174000"},
		{"unix_ts", "started 1700000000 finished", "1700000000"},
		{"long_hex", "digest d41d8cd98f00b204e9800998ecf8427e here", "d41d8cd98f00b204e9800998ecf8427e"},
		{"jwt", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLTEyMw end", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlLTEyMw"},
		{"api_key", "key sk_abcdefghijklmnop1234 end", "sk_abcdefghijklmnop1234"},
		{"bearer", "auth Bearer abcdefghijklmnop1234 end", "Bearer abcdefghijklmnop1234"},
		{"request_id", "req-abcdef123456 traced", "req-abcdef123456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locked := lockQuantumSpans(tc.text)
			head, tail, ok := strings.Cut(locked, quantumTailDelim)
			if !ok {
				t.Fatalf("missing tail delimiter in %q", locked)
			}
			if strings.Contains(head, tc.secret) {
				t.Fatalf("stable prefix still contains the secret: %q", head)
			}
			if !strings.Contains(tail, tc.secret) {
				t.Fatalf("value tail lost the secret: %q", tail)
			}
			if n := strings.Count(head, quantumPlaceholder(0)); n != 1 {
				t.Fatalf("expected 1 placeholder, got %d in %q", n, head)
			}
		})
	}
}

// TestQuantumLockAdjacentAndOverlappingSpans covers multiple spans in one text
// plus an overlap where a UUID's inner hex must not claim its own placeholder.
func TestQuantumLockAdjacentAndOverlappingSpans(t *testing.T) {
	text := "first 123e4567-e89b-12d3-a456-426614174000 second 1700000000 third req-abcdef123456"
	locked, _, _ := strings.Cut(lockQuantumSpans(text), quantumTailDelim)
	if n := strings.Count(locked, "⟦Q"); n != 3 {
		t.Fatalf("expected 3 placeholders, got %d in %q", n, locked)
	}

	// The UUID is 36 chars and its hex segments also match long_hex; only the
	// widest, highest-priority span may survive.
	overlap := "id=123e4567-e89b-12d3-a456-426614174000"
	lockedOverlap, _, _ := strings.Cut(lockQuantumSpans(overlap), quantumTailDelim)
	if n := strings.Count(lockedOverlap, "⟦Q"); n != 1 {
		t.Fatalf("overlapping span produced %d placeholders, want 1: %q", n, lockedOverlap)
	}
}

// TestQuantumLockIsIdempotent checks the tail sentinel prevents double masking.
func TestQuantumLockIsIdempotent(t *testing.T) {
	text := "id 123e4567-e89b-12d3-a456-426614174000 at 1700000000"
	once := lockQuantumSpans(text)
	if twice := lockQuantumSpans(once); twice != once {
		t.Fatalf("quantum lock is not idempotent: %q vs %q", once, twice)
	}
}
