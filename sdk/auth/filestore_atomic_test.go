package auth

import (
	"context"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMetadataSaveAtomicEvenForIdenticalPayload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	a := &core.Auth{ID: "auth.json", Provider: "codex", Attributes: map[string]string{"path": p}, Metadata: map[string]any{"type": "codex", "refresh_token": "R1"}}
	s := NewFileTokenStore()
	if _, err := s.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	if _, err := s.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if os.SameFile(before, after) {
		t.Fatal("identical payload must not skip durability retry")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0600 {
		t.Fatal("not private")
	}
}
