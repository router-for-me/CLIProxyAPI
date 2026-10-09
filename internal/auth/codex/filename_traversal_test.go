package codex

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// TestCredentialFileNameRejectsPathTraversal proves that an untrusted email claim
// cannot inject a path separator into the credential file name. The email is
// attacker-controlled: it is read from the JWT "email" claim of the Codex
// auth.json being imported (internal/cmd/codex_import.go).
func TestCredentialFileNameRejectsPathTraversal(t *testing.T) {
	hostile := []string{
		"../../../../tmp/pwned",
		"..\\..\\..\\tmp\\pwned",
		"a/../../b@example.com",
		"/etc/cron.d/pwned",
		"user@example.com/../../../evil",
		"..",
		"....//....//etc/passwd",
		"evil\x00name@example.com",
		"line\nbreak@example.com",
		"victim:stream@example.com",
	}

	for _, email := range hostile {
		t.Run(email, func(t *testing.T) {
			got := CredentialFileName(email, "plus", "abc12345", true)

			if strings.ContainsRune(got, '/') || strings.ContainsRune(got, '\\') || strings.ContainsRune(got, ':') {
				t.Fatalf("CredentialFileName(%q) = %q, contains a path or stream separator", email, got)
			}
			if strings.IndexFunc(got, unicode.IsControl) >= 0 {
				t.Fatalf("CredentialFileName(%q) = %q, contains a control character", email, got)
			}
			if got != filepath.Base(got) {
				t.Fatalf("CredentialFileName(%q) = %q, is not a bare file name", email, got)
			}
			if !strings.HasPrefix(got, "codex-") || !strings.HasSuffix(got, ".json") {
				t.Fatalf("CredentialFileName(%q) = %q, lost its expected prefix/suffix", email, got)
			}

			// The decisive property: joining the result onto the credentials
			// directory must never leave that directory.
			dir := filepath.Clean("/var/lib/cliproxyapi/auths")
			joined := filepath.Clean(filepath.Join(dir, got))
			if !strings.HasPrefix(joined, dir+string(filepath.Separator)) {
				t.Fatalf("CredentialFileName(%q) = %q escapes %q (joined %q)", email, got, dir, joined)
			}
		})
	}
}

// TestCredentialFileNameRejectsHostileAccountHash covers the second untrusted
// component: the account hash is a caller-supplied string on several code paths.
func TestCredentialFileNameRejectsHostileAccountHash(t *testing.T) {
	got := CredentialFileName("user@example.com", "plus", "../../etc", true)
	if strings.ContainsRune(got, '/') || strings.ContainsRune(got, '\\') || got != filepath.Base(got) {
		t.Fatalf("CredentialFileName with hostile hash = %q, not a bare file name", got)
	}
}

// TestCredentialFileNameKeepsLegitimateEmails guards existing credentials: an email
// without a path separator, colon or control character must keep the file name it
// had before the sanitizer existed, or a new login would no longer find the saved file.
func TestCredentialFileNameKeepsLegitimateEmails(t *testing.T) {
	emails := []string{
		"user@example.com",
		"first.last+tag@example.com",
		"jos\xc3\xa9@example.com",
		"o'brien@example.com",
		"a%b@example.com",
		"two words@example.com",
		"user@example.com.",
	}
	for _, email := range emails {
		t.Run(email, func(t *testing.T) {
			got := CredentialFileName(email, "plus", "abc12345", true)
			if want := "codex-abc12345-" + email + "-plus.json"; got != want {
				t.Fatalf("CredentialFileName(%q) = %q, want %q", email, got, want)
			}
		})
	}
}

// TestCredentialFileNamePreservesEmptyEmail guards the pre-existing behaviour:
// an email that is genuinely empty must keep producing the legacy layout.
func TestCredentialFileNamePreservesEmptyEmail(t *testing.T) {
	got := CredentialFileName("", "plus", "abc12345", true)
	if want := "codex-abc12345--plus.json"; got != want {
		t.Fatalf("CredentialFileName(empty email) = %q, want %q", got, want)
	}
}
