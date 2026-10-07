package errorclass

import "testing"

func TestFingerprintCollapsesVariableTokens(t *testing.T) {
	a := Fingerprint(ClassRateLimit, "anthropic", "claude-sonnet-4-5", "rate limit exceeded for request abc-12")
	b := Fingerprint(ClassRateLimit, "anthropic", "claude-sonnet-4-5", "rate limit exceeded for request xyz-98")
	if a != b {
		t.Fatalf("structurally identical messages must share a fingerprint: %q != %q", a, b)
	}
}

func TestFingerprintDistinguishesDifferentCauses(t *testing.T) {
	a := Fingerprint(ClassRateLimit, "anthropic", "m", "rate limit exceeded")
	b := Fingerprint(ClassAuth, "anthropic", "m", "rate limit exceeded")
	if a == b {
		t.Fatal("different classes must not share a fingerprint")
	}
}

func TestFingerprintStableAndBounded(t *testing.T) {
	got := Fingerprint(ClassAuth, "openai", "gpt-4o", "invalid api key")
	if len(got) != 16 {
		t.Fatalf("fingerprint length = %d, want 16", len(got))
	}
	if got != Fingerprint(ClassAuth, "openai", "gpt-4o", "invalid api key") {
		t.Fatal("fingerprint must be deterministic")
	}
}

func TestNormalizeMessage(t *testing.T) {
	if normalizeMessage("Retry 3 of 5") != normalizeMessage("Retry 9 of 12") {
		t.Fatal("digit runs must collapse")
	}
	got := normalizeMessage("rate limit exceeded for request abc-12")
	if got != normalizeMessage("rate limit exceeded for request xyz-98") {
		t.Fatalf("variable ids must collapse: %q != %q", got, normalizeMessage("rate limit exceeded for request xyz-98"))
	}
	if got != "rate limit exceeded for request <id>" {
		t.Fatalf("normalized = %q, want %q", got, "rate limit exceeded for request <id>")
	}
}
