package management

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

func TestAuthIDForPath_UsesSharedFileAuthNormalization(t *testing.T) {
	authDir := filepath.Join(`C:\`, "Auths")
	path := filepath.Join(authDir, "Nested", "Foo.json")
	h := &Handler{cfg: &config.Config{AuthDir: authDir}}

	if got, want := h.authIDForPath(path), sdkAuth.NormalizeFileAuthID(path, authDir); got != want {
		t.Fatalf("auth ID = %q, want %q", got, want)
	}
}
