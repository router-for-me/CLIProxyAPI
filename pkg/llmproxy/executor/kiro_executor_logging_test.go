package executor

import "testing"

func TestKiroModelFingerprint_RedactsRawModel(t *testing.T) {
	raw := "user-custom-model-with-sensitive-suffix"
	got := kiroModelFingerprint(raw)
	if got == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if got == raw {
		t.Fatalf("fingerprint must not equal raw model: %q", got)
	}
}
