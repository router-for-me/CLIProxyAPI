package management

import (
	"testing"
	"time"
)

func TestFormatSubstitutionMessage(t *testing.T) {
	got := formatSubstitutionMessage("claude-opus-5", "claude-haiku-4-5", "anthropic", 7)
	want := `anthropic served "claude-haiku-4-5" for 7 request(s) that asked for "claude-opus-5".`
	if got != want {
		t.Fatalf("formatSubstitutionMessage() = %q, want %q", got, want)
	}
}

func TestSubstitutionFingerprintIsStablePerProviderModelPair(t *testing.T) {
	a := substitutionFingerprint("anthropic", "claude-opus-5", "claude-haiku-4-5")
	b := substitutionFingerprint("anthropic", "claude-opus-5", "claude-haiku-4-5")
	if a != b {
		t.Fatalf("fingerprint not stable: %q vs %q", a, b)
	}
	c := substitutionFingerprint("anthropic", "claude-opus-5", "claude-sonnet-5")
	if a == c {
		t.Fatalf("fingerprint must differ when the served model differs")
	}
	d := substitutionFingerprint("openai", "claude-opus-5", "claude-haiku-4-5")
	if a == d {
		t.Fatalf("fingerprint must differ when the provider differs")
	}
	// The key is the requested→served pair, so a different requested model
	// with the same provider and served model must also raise a new alert.
	e := substitutionFingerprint("anthropic", "claude-opus-4-5", "claude-haiku-4-5")
	if a == e {
		t.Fatalf("fingerprint must differ when the requested model differs")
	}
}

func TestSubstitutionDetectorIntervalIsPositive(t *testing.T) {
	if alertSubstitutionLookback <= 0 || alertSubstitutionLookback > time.Hour {
		t.Fatalf("alertSubstitutionLookback = %v, want a positive window at most one hour", alertSubstitutionLookback)
	}
}
