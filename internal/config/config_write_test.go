package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfigFileKeepsInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8001\n"), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	if err = WriteConfigFile(path, []byte("port: 8002\n"), 0o600); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("WriteConfigFile replaced the config file")
	}
	data, err := ReadConfigFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(data) != "port: 8002\n" {
		t.Fatalf("config = %q, want the new content", data)
	}
}
