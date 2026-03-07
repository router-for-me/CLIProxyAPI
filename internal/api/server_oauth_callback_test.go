package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	managementHandlers "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type oauthCallbackMirrorStore struct {
	authDir string
}

func (s *oauthCallbackMirrorStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *oauthCallbackMirrorStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}
func (s *oauthCallbackMirrorStore) Delete(context.Context, string) error { return nil }
func (s *oauthCallbackMirrorStore) AuthDir() string                      { return s.authDir }

func TestWritePendingOAuthCallbackFileUsesEffectiveAuthDir(t *testing.T) {
	configuredAuthDir := t.TempDir()
	mirroredAuthDir := t.TempDir()
	previousStore := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(&oauthCallbackMirrorStore{authDir: mirroredAuthDir})
	t.Cleanup(func() {
		sdkAuth.RegisterTokenStore(previousStore)
	})

	state := "anthropic-state-1"
	managementHandlers.RegisterOAuthSession(state, "anthropic")

	writePendingOAuthCallbackFile(configuredAuthDir, "anthropic", state, "test-code", "")

	callbackPath := filepath.Join(mirroredAuthDir, ".oauth-anthropic-"+state+".oauth")
	if _, err := os.Stat(callbackPath); err != nil {
		t.Fatalf("expected callback file in mirrored auth dir: %v", err)
	}
	configuredPath := filepath.Join(configuredAuthDir, ".oauth-anthropic-"+state+".oauth")
	if _, err := os.Stat(configuredPath); !os.IsNotExist(err) {
		t.Fatalf("expected configured auth dir to remain untouched, stat err=%v", err)
	}
}
