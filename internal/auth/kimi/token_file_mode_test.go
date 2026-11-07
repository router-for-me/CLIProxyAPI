package kimi

import (
	"os"
	"path/filepath"
	"testing"
)

// A new credential file gets the mode os.Create gave it before the atomic write.
func TestSaveTokenToFileNewFileModeMatchesCreate(t *testing.T) {
	dir := t.TempDir()
	reference, errCreate := os.Create(filepath.Join(dir, "reference"))
	if errCreate != nil {
		t.Fatalf("create reference: %v", errCreate)
	}
	if errClose := reference.Close(); errClose != nil {
		t.Fatalf("close reference: %v", errClose)
	}
	want, _ := os.Stat(filepath.Join(dir, "reference"))

	path := filepath.Join(dir, "auth.json")
	if err := (&KimiTokenStorage{}).SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}
	got, _ := os.Stat(path)
	if got.Mode().Perm() != want.Mode().Perm() {
		t.Fatalf("mode = %v, want %v (os.Create)", got.Mode().Perm(), want.Mode().Perm())
	}
}
