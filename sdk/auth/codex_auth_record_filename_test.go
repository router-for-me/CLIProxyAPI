package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func codexLoginRecordFileName(t *testing.T, email, goos string) string {
	t.Helper()
	bundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      makeTestCodexJWT("plus", "acc-12345"),
			AccessToken:  "mock-access-token",
			RefreshToken: "mock-refresh-token",
			Email:        email,
		},
	}
	record, errBuild := NewCodexAuthenticator().buildAuthRecordForOS(codex.NewCodexAuth(nil), bundle, goos)
	if errBuild != nil {
		t.Fatalf("buildAuthRecordForOS error: %v", errBuild)
	}
	if record.ID != record.FileName {
		t.Fatalf("record ID %q != FileName %q", record.ID, record.FileName)
	}
	return record.FileName
}

// TestBuildAuthRecordUsesWindowsSafeNameOnWindows: the maintained CLI login
// must write the same Windows-safe name as the import, so a re-login replaces
// an imported account instead of failing on an invalid Windows path.
func TestBuildAuthRecordUsesWindowsSafeNameOnWindows(t *testing.T) {
	digest := sha256.Sum256([]byte("acc-12345"))
	hash := hex.EncodeToString(digest[:])[:8]
	email := "a?b*c|d@example.com"

	got := codexLoginRecordFileName(t, email, "windows")
	if want := "codex-" + hash + "-a_b_c_d@example.com-plus.json"; got != want {
		t.Fatalf("login file name on windows = %q, want %q", got, want)
	}
	if want := codex.PlatformCredentialFileName(email, "plus", hash, "windows"); got != want {
		t.Fatalf("login file name on windows = %q, want the import's name %q", got, want)
	}
}

// TestBuildAuthRecordKeepsCredentialFileNameOutsideWindows: nothing changes on
// other systems.
func TestBuildAuthRecordKeepsCredentialFileNameOutsideWindows(t *testing.T) {
	digest := sha256.Sum256([]byte("acc-12345"))
	hash := hex.EncodeToString(digest[:])[:8]
	email := "a?b*c|d@example.com"
	for _, goos := range []string{"linux", "darwin"} {
		got := codexLoginRecordFileName(t, email, goos)
		if want := codex.CredentialFileName(email, "plus", hash, true); got != want {
			t.Fatalf("login file name on %s = %q, want %q", goos, got, want)
		}
	}
}
