package codex

import "testing"

// TestPlatformCredentialFileNameReplacesWindowsForbiddenCharactersOnWindows: on
// Windows a name carrying one of these characters cannot be created, so the
// import and the maintained login write "_" instead.
func TestPlatformCredentialFileNameReplacesWindowsForbiddenCharactersOnWindows(t *testing.T) {
	for _, forbidden := range `<>"|?*` {
		email := "a" + string(forbidden) + "b@example.com"
		t.Run(email, func(t *testing.T) {
			got := PlatformCredentialFileName(email, "plus", "abc12345", "windows")
			if want := "codex-abc12345-a_b@example.com-plus.json"; got != want {
				t.Fatalf("PlatformCredentialFileName(%q, windows) = %q, want %q", email, got, want)
			}
		})
	}
}

// TestPlatformCredentialFileNameMatchesCredentialFileNameOutsideWindows guards
// that nothing changes on any other OS.
func TestPlatformCredentialFileNameMatchesCredentialFileNameOutsideWindows(t *testing.T) {
	emails := []string{
		"user@example.com",
		"a?b@example.com",
		"a*b@example.com",
		"a|b@example.com",
		`a<b>"c@example.com`,
	}
	for _, goos := range []string{"linux", "darwin"} {
		for _, email := range emails {
			got := PlatformCredentialFileName(email, "plus", "abc12345", goos)
			want := CredentialFileName(email, "plus", "abc12345", true)
			if got != want {
				t.Fatalf("PlatformCredentialFileName(%q, %s) = %q, want CredentialFileName %q", email, goos, got, want)
			}
		}
	}
}

// TestPlatformCredentialFileNameKeepsOrdinaryNamesOnWindows checks that the
// Windows rule touches only the forbidden characters.
func TestPlatformCredentialFileNameKeepsOrdinaryNamesOnWindows(t *testing.T) {
	got := PlatformCredentialFileName("user@example.com", "plus", "abc12345", "windows")
	want := CredentialFileName("user@example.com", "plus", "abc12345", true)
	if got != want {
		t.Fatalf("PlatformCredentialFileName(windows) = %q, want %q", got, want)
	}
}

// TestCredentialFileNameStaysPlainForDeprecatedRoute: the deprecated
// /v0/management codex-auth-url flow names its file with CredentialFileName,
// which has no OS input and must keep these characters on every OS.
func TestCredentialFileNameStaysPlainForDeprecatedRoute(t *testing.T) {
	got := CredentialFileName("a?b*c|d@example.com", "plus", "abc12345", true)
	if want := "codex-abc12345-a?b*c|d@example.com-plus.json"; got != want {
		t.Fatalf("CredentialFileName = %q, want %q", got, want)
	}
}
