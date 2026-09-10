package auth

import (
	"path/filepath"
	"strings"
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

func TestAuthFileBasenamePrecedence(t *testing.T) {
	auth := &Auth{
		ID:       "from-id.json",
		FileName: "from-filename.json",
		Attributes: map[string]string{
			"path": filepath.Join(string(filepath.Separator), "hidden", "from-path.json"),
		},
	}
	if got := AuthFileBasename(auth); got != "from-filename.json" {
		t.Fatalf("FileName precedence: got %q, want from-filename.json", got)
	}
	auth.FileName = ""
	if got := AuthFileBasename(auth); got != "from-path.json" {
		t.Fatalf("Attributes[path] precedence: got %q, want from-path.json", got)
	}
	auth.Attributes = nil
	if got := AuthFileBasename(auth); got != "from-id.json" {
		t.Fatalf("ID fallback: got %q, want from-id.json", got)
	}
}

func TestAuthFileBasenameWindowsSeparators(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`auth-dir\account.json`, "account.json"},
		{`C:\auth-dir\account.json`, "account.json"},
		{`C:\auth-dir\subdir\win-account.json`, "win-account.json"},
	}
	for _, tc := range cases {
		got := AuthFileBasename(&Auth{FileName: tc.raw})
		if got != tc.want {
			t.Fatalf("AuthFileBasename(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestAuthFileBasenameRejectsControlCharacters(t *testing.T) {
	got := AuthFileBasename(&Auth{
		FileName: "evil\nname.json",
		ID:       "safe-fallback.json",
	})
	if got != "safe-fallback.json" {
		t.Fatalf("control-char FileName should fall through: got %q", got)
	}
	got = AuthFileBasename(&Auth{FileName: "evil\x00name.json"})
	if got != "" {
		t.Fatalf("NUL basename should be rejected: got %q", got)
	}
	long := strings.Repeat("a", 300) + ".json"
	got = AuthFileBasename(&Auth{FileName: long})
	if len(got) > 255 {
		t.Fatalf("basename length %d exceeds 255", len(got))
	}
	if got == "" {
		t.Fatal("long but otherwise valid basename should truncate, not empty")
	}
}
