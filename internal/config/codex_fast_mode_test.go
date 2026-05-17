package config

import "testing"

func TestParseConfigBytesCodexFastMode(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("codex-fast-mode: true\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if !cfg.CodexFastMode {
		t.Fatal("CodexFastMode = false, want true")
	}
}
