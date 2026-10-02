package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenSaveAtomicPrivateReplacement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{"refresh_token":"R0"}`), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0666); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	s := &CodexTokenStorage{RefreshToken: "R1"}
	if err := s.SaveTokenToFile(p); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if os.SameFile(before, after) {
		t.Fatal("token save truncated existing inode instead of atomic replacement")
	}
	if after.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", after.Mode().Perm())
	}
}
