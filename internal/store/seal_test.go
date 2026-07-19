package store

import (
	"strings"
	"testing"
)

func TestSealerNilPassthrough(t *testing.T) {
	s, err := NewSealer(nil)
	if err != nil {
		t.Fatalf("NewSealer(nil): %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil sealer; got %+v", s)
	}
	got, err := s.Seal("sk-secret-12345678")
	if err != nil || got != "sk-secret-12345678" {
		t.Fatalf("nil Seal = (%q, %v); want passthrough", got, err)
	}
	got, err = s.Open("sk-secret-12345678")
	if err != nil || got != "sk-secret-12345678" {
		t.Fatalf("nil Open = (%q, %v); want passthrough", got, err)
	}
}

func TestSealerEmptyInput(t *testing.T) {
	s, _ := NewSealer([]byte("any"))
	got, err := s.Seal("")
	if err != nil || got != "" {
		t.Fatalf("Seal(\"\") = (%q, %v); want \"\"", got, err)
	}
	got, err = s.Open("")
	if err != nil || got != "" {
		t.Fatalf("Open(\"\") = (%q, %v); want \"\"", got, err)
	}
}

func TestSealerRoundTrip(t *testing.T) {
	s, err := NewSealer([]byte("hunter2"))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if !s.Enabled() {
		t.Fatal("expected sealer enabled")
	}
	plaintext := "sk-abcdef0123456789"
	sealed, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed == plaintext {
		t.Fatal("sealed payload equals plaintext")
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("payload missing v1 prefix: %q", sealed)
	}
	// Two Seal calls with the same input must differ (random nonce).
	other, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal(2): %v", err)
	}
	if other == sealed {
		t.Fatal("expected non-deterministic ciphertext due to random nonce")
	}
	got, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plaintext {
		t.Fatalf("Open = %q; want %q", got, plaintext)
	}
}

func TestSealerLegacyPlaintextPassesThrough(t *testing.T) {
	s, _ := NewSealer([]byte("k"))
	got, err := s.Open("sk-legacy-plaintext-row")
	if err != nil {
		t.Fatalf("Open(legacy): %v", err)
	}
	if got != "sk-legacy-plaintext-row" {
		t.Fatalf("Open(legacy) = %q; want passthrough", got)
	}
}

func TestSealerTamperDetection(t *testing.T) {
	s, _ := NewSealer([]byte("k"))
	sealed, err := s.Seal("sk-secret-1234567")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Flip the last character to break GCM integrity.
	tampered := sealed[:len(sealed)-1]
	switch sealed[len(sealed)-1] {
	case 'A':
		tampered += "B"
	default:
		tampered += "A"
	}
	if _, err := s.Open(tampered); err == nil {
		t.Fatal("expected error on tampered payload; got nil")
	}
}

func TestSealerWrongPassphraseFails(t *testing.T) {
	a, _ := NewSealer([]byte("alpha"))
	b, _ := NewSealer([]byte("beta"))
	sealed, err := a.Seal("sk-secret-12345678")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := b.Open(sealed); err == nil {
		t.Fatal("expected error opening with wrong passphrase; got nil")
	}
}
