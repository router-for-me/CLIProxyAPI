//go:build unix

package pluginhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestHostSaveAuthFileSecuresExistingFile(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "auth.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"old@example.com"}`), 0o664); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errChmod := os.Chmod(path, 0o664); errChmod != nil {
		t.Fatalf("Chmod() error = %v", errChmod)
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	replacement := []byte(`{"type":"demo","email":"new@example.com"}`)
	savedPath, errSave := host.saveAuthFile(context.Background(), "auth.json", replacement)
	if errSave != nil {
		t.Fatalf("saveAuthFile() error = %v", errSave)
	}
	if savedPath != path {
		t.Errorf("saved path = %q, want %q", savedPath, path)
	}

	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() error = %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if got, want := string(persisted), string(replacement); got != want {
		t.Errorf("saved auth file = %q, want %q", got, want)
	}
	if auth, ok := host.currentAuthManager().GetByID("auth.json"); !ok || auth == nil {
		t.Fatal("saved auth record was not registered")
	}
}
