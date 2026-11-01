package cmd

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// TestCodexImportFileNameReplacesWindowsForbiddenCharactersOnWindows covers the
// one place where the import differs from the login paths: on Windows a name
// carrying one of these characters cannot be created, so the import writes "_".
func TestCodexImportFileNameReplacesWindowsForbiddenCharactersOnWindows(t *testing.T) {
	for _, forbidden := range `<>"|?*` {
		email := "a" + string(forbidden) + "b@example.com"
		t.Run(email, func(t *testing.T) {
			got := codexImportFileName(email, "plus", "abc12345", "windows")
			if want := "codex-abc12345-a_b@example.com-plus.json"; got != want {
				t.Fatalf("codexImportFileName(%q, windows) = %q, want %q", email, got, want)
			}
		})
	}
}

// TestCodexImportFileNameMatchesLoginOutsideWindows guards the promise that an
// import and a later login of one account resolve to one credential file on
// every other OS.
func TestCodexImportFileNameMatchesLoginOutsideWindows(t *testing.T) {
	emails := []string{
		"user@example.com",
		"a?b@example.com",
		"a*b@example.com",
		"a|b@example.com",
		`a<b>"c@example.com`,
	}
	for _, goos := range []string{"linux", "darwin"} {
		for _, email := range emails {
			got := codexImportFileName(email, "plus", "abc12345", goos)
			want := codex.CredentialFileName(email, "plus", "abc12345", true)
			if got != want {
				t.Fatalf("codexImportFileName(%q, %s) = %q, want CredentialFileName %q", email, goos, got, want)
			}
		}
	}
}

// TestCodexImportFileNameKeepsOrdinaryNamesOnWindows checks that the Windows rule
// touches only the forbidden characters.
func TestCodexImportFileNameKeepsOrdinaryNamesOnWindows(t *testing.T) {
	got := codexImportFileName("user@example.com", "plus", "abc12345", "windows")
	want := codex.CredentialFileName("user@example.com", "plus", "abc12345", true)
	if got != want {
		t.Fatalf("codexImportFileName(windows) = %q, want %q", got, want)
	}
}
