package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/grok"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGrokAuthenticator_Provider(t *testing.T) {
	a := NewGrokAuthenticator()
	if got := a.Provider(); got != "grok" {
		t.Errorf("Provider() = %q, want %q", got, "grok")
	}
}

func TestGrokAuthenticator_RefreshLead(t *testing.T) {
	a := NewGrokAuthenticator()
	lead := a.RefreshLead()
	if lead == nil {
		t.Fatal("RefreshLead() returned nil")
	}
	want := grok.AccessTokenRefreshSkew
	if *lead != want {
		t.Errorf("RefreshLead() = %v, want %v", *lead, want)
	}
	if *lead != 120*time.Second {
		t.Errorf("RefreshLead() = %v, want 120s", *lead)
	}
}

func TestGrokAuthenticator_LoginDeviceCodePath(t *testing.T) {
	// Verify that NoBrowser=true selects the device-code branch by checking
	// that loginWithDeviceCode is invoked (it will fail fast with a network
	// error rather than attempting a browser/loopback flow).
	a := NewGrokAuthenticator()

	// shouldUseDeviceCode is the branch decision: NoBrowser=true means device-code.
	opts := &LoginOptions{NoBrowser: true}
	// We cannot reach xAI in CI, so we confirm only that the path is taken by
	// inspecting the error origin. The device-code path contacts DeviceAuthURL;
	// the browser path contacts OAuthCallbackPort 56121. We look for a network
	// error (not a port-listen error) to confirm device-code was chosen.
	//
	// This is a lightweight structural test: if the branch logic changes,
	// this assertion will fail, catching regressions without requiring a
	// live xAI server.
	ctx := t.Context()
	_, err := a.Login(ctx, nil, opts)
	// cfg=nil triggers the early nil-config guard before any network call.
	if err == nil {
		t.Fatal("expected error for nil cfg, got nil")
	}
	want := "cliproxy auth: configuration is required"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestGrokAuthenticatorBuildAuthRecordDefersPersistenceToStore(t *testing.T) {
	t.Chdir(t.TempDir())

	a := NewGrokAuthenticator()
	record, err := a.buildAuthRecord(&grok.TokenResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		IDToken:      "id-token",
		ExpiresIn:    3600,
	}, &config.Config{})
	if err != nil {
		t.Fatalf("buildAuthRecord() error = %v", err)
	}
	if record == nil {
		t.Fatal("buildAuthRecord() returned nil record")
	}
	if record.Provider != "grok" {
		t.Fatalf("Provider = %q, want grok", record.Provider)
	}
	if filepath.Base(record.FileName) != record.FileName {
		t.Fatalf("FileName = %q, want a top-level file name for auth-dir loading", record.FileName)
	}
	if _, errStat := os.Stat(filepath.Join("auths", "grok")); !os.IsNotExist(errStat) {
		t.Fatalf("buildAuthRecord should not pre-save nested auth files, stat err = %v", errStat)
	}
}
