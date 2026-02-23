package executor

import "testing"

func TestCodexLogFingerprint_RedactsRawValue(t *testing.T) {
	raw := "wss://example.openai.com/v1/realtime?token=secret"
	got := codexLogFingerprint(raw)
	if got == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if got == raw {
		t.Fatalf("fingerprint must not equal raw input: %q", got)
	}
}

func TestCodexLogFingerprint_TrimmedEmpty(t *testing.T) {
	if got := codexLogFingerprint("   \t\n"); got != "" {
		t.Fatalf("expected empty fingerprint for blank input, got %q", got)
	}
}
