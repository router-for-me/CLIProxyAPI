package config

import "testing"

func TestParseConfigBytesIgnoresRemovedClaudeCodeModelListCloaking(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 9123\nclaude-code:\n  disable-cloaking-model-list: true\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.Port != 9123 {
		t.Fatalf("Port = %d, want 9123", cfg.Port)
	}
}
