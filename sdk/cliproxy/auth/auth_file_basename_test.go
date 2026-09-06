package auth

import (
	"path/filepath"
	"testing"
)

func TestAuthFileBasenameUsesBasenameOnly(t *testing.T) {
	auth := &Auth{
		ID:       "nested/account.json",
		FileName: filepath.Join("auth-dir", "subdir", "account.json"),
		Attributes: map[string]string{
			"path": filepath.Join(string(filepath.Separator), "hidden", "auth-dir", "account.json"),
		},
	}
	if got := AuthFileBasename(auth); got != "account.json" {
		t.Fatalf("AuthFileBasename() = %q, want %q", got, "account.json")
	}
	if got := AuthFileBasename(&Auth{ID: "plain-id"}); got != "plain-id" {
		t.Fatalf("AuthFileBasename(id) = %q, want %q", got, "plain-id")
	}
	if got := AuthFileBasename(nil); got != "" {
		t.Fatalf("AuthFileBasename(nil) = %q, want empty", got)
	}
}
