package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileRefreshClaimRequiresParentSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is unavailable on Windows")
	}
	dir := filepath.Join(t.TempDir(), "auths")
	if errMkdir := os.Mkdir(dir, 0o300); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
			t.Error(errChmod)
		}
	})
	if handle, errOpen := os.Open(dir); errOpen == nil {
		if errClose := handle.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		t.Skip("test requires directory read permissions to be enforced")
	}
	journal := &FileRefreshJournal{Dir: dir}
	claimed, errClaim := journal.RefreshClaim(context.Background(), strings.Repeat("e", 64))
	if claimed || errClaim == nil {
		t.Fatalf("claim allowed OAuth without synchronizing parent directory: claimed=%v, error=%v", claimed, errClaim)
	}
}

func TestFileRefreshClaimCreatesDurableAncestorDirectories(t *testing.T) {
	journal := &FileRefreshJournal{Dir: filepath.Join(t.TempDir(), "new", "nested", "auths")}
	key := strings.Repeat("f", 64)
	claimed, errClaim := journal.RefreshClaim(context.Background(), key)
	if !claimed || errClaim != nil {
		t.Fatalf("create journal hierarchy: %v, %v", claimed, errClaim)
	}
	claimed, errClaim = journal.RefreshClaim(context.Background(), key)
	if claimed || errClaim != nil {
		t.Fatalf("claim was recreated: %v, %v", claimed, errClaim)
	}
}
