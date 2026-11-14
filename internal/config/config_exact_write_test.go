package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfigAtomicExactPreservesModeAndBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	candidate := []byte("# retain comment\nport: 8318\n")
	if err := WriteConfigAtomicExact(path, candidate); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, candidate) {
		t.Fatalf("read published configuration: %v; got %q", err, data)
	}
	oldInfo, err := previous.Stat()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || os.SameFile(oldInfo, info) {
		t.Fatal("publication changed the mode or mutated the existing file in place")
	}
}
