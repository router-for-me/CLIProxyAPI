package config

import "testing"

func TestParseConfigBytesPreservesManagementWebDirectory(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("remote-management:\n  web-directory: ./ui/admin\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes: %v", errParse)
	}
	if cfg.RemoteManagement.WebDirectory != "./ui/admin" {
		t.Fatalf("web directory = %q, want %q", cfg.RemoteManagement.WebDirectory, "./ui/admin")
	}
}
