package executor

import "testing"

func TestAntigravityModelFingerprint_RedactsRawModel(t *testing.T) {
	raw := "my-sensitive-model-name"
	got := antigravityModelFingerprint(raw)
	if got == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if got == raw {
		t.Fatalf("fingerprint must not equal raw model: %q", got)
	}
}
