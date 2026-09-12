package config

import "testing"

func TestParseConfigBytesCodexImageOutputDir(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`codex-image-output-dir: "~/.codex/generated_images/cliproxy"`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.CodexImageOutputDir; got != "~/.codex/generated_images/cliproxy" {
		t.Fatalf("CodexImageOutputDir = %q", got)
	}
}
