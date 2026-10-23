package auth

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authfile"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func enableTestAuthEncryption(t *testing.T) {
	t.Helper()
	codec, errCodec := authfile.NewAESGCMCodec(bytes.Repeat([]byte{7}, authfile.KeySize))
	if errCodec != nil {
		t.Fatalf("NewAESGCMCodec() error = %v", errCodec)
	}
	authfile.Configure(codec, true)
	t.Cleanup(func() { authfile.Configure(nil, false) })
}

func requireSealedFile(t *testing.T, path string, secret string) {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}
	if !authfile.IsSealed(raw) {
		t.Fatalf("%s is not encrypted: %s", filepath.Base(path), raw)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("%s leaks %q on disk", filepath.Base(path), secret)
	}
}

func TestFileTokenStoreEncryptsPlainFileOnUnchangedSave(t *testing.T) {
	enableTestAuthEncryption(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-a.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"a@example.com","access_token":"tok-plain","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	auths, errList := store.List(context.Background())
	if errList != nil || len(auths) != 1 {
		t.Fatalf("List() = %d auths, %v; want 1 plain auth", len(auths), errList)
	}
	if _, errSave := store.Save(context.Background(), auths[0]); errSave != nil {
		t.Fatalf("Save() error = %v", errSave)
	}
	requireSealedFile(t, path, "tok-plain")

	auths, errList = store.List(context.Background())
	if errList != nil || len(auths) != 1 || auths[0].Metadata["access_token"] != "tok-plain" {
		t.Fatalf("List() after encrypt = %+v, %v", auths, errList)
	}
}

func TestFileTokenStoreTokenStoragesWriteEncrypted(t *testing.T) {
	enableTestAuthEncryption(t)
	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	claudePath := filepath.Join(dir, "claude-b.json")
	if _, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:         "claude-b.json",
		Provider:   "claude",
		FileName:   "claude-b.json",
		Storage:    &claude.ClaudeTokenStorage{AccessToken: "tok-claude", RefreshToken: "ref-claude", Email: "b@example.com"},
		Metadata:   map[string]any{"type": "claude"},
		Attributes: map[string]string{cliproxyauth.AttributePath: claudePath},
	}); errSave != nil {
		t.Fatalf("Save(claude) error = %v", errSave)
	}
	requireSealedFile(t, claudePath, "tok-claude")

	codexPath := filepath.Join(dir, "codex-c.json")
	if _, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:         "codex-c.json",
		Provider:   "codex",
		FileName:   "codex-c.json",
		Storage:    &codex.CodexTokenStorage{AccessToken: "tok-codex", RefreshToken: "ref-codex", Email: "c@example.com"},
		Metadata:   map[string]any{"type": "codex"},
		Attributes: map[string]string{cliproxyauth.AttributePath: codexPath},
	}); errSave != nil {
		t.Fatalf("Save(codex) error = %v", errSave)
	}
	requireSealedFile(t, codexPath, "tok-codex")

	auths, errList := store.List(context.Background())
	if errList != nil || len(auths) != 2 {
		t.Fatalf("List() = %d auths, %v; want 2", len(auths), errList)
	}
	tokens := map[string]bool{}
	for _, auth := range auths {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			tokens[token] = true
		}
	}
	if !tokens["tok-claude"] || !tokens["tok-codex"] {
		t.Fatalf("List() tokens = %v, want both decrypted", tokens)
	}
}

func TestFileTokenStoreReloginReplacesUnreadableFile(t *testing.T) {
	enableTestAuthEncryption(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-a.json")
	if errWrite := authfile.WriteFile(path, []byte(`{"type":"claude","access_token":"tok-old"}`), 0o600); errWrite != nil {
		t.Fatalf("write sealed file: %v", errWrite)
	}
	// Encryption is turned off: the sealed file cannot be read anymore.
	authfile.Configure(nil, false)
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	if auths, _ := store.List(context.Background()); len(auths) != 0 {
		t.Fatalf("List() = %d auths, want the unreadable file skipped", len(auths))
	}

	// Logging in again writes a fresh plain credential over it.
	if _, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:         "claude-a.json",
		Provider:   "claude",
		FileName:   "claude-a.json",
		Metadata:   map[string]any{"type": "claude", "access_token": "tok-new"},
		Attributes: map[string]string{cliproxyauth.AttributePath: path},
	}); errSave != nil {
		t.Fatalf("Save() over unreadable file error = %v", errSave)
	}
	raw, _ := os.ReadFile(path)
	if authfile.IsSealed(raw) || !bytes.Contains(raw, []byte("tok-new")) {
		t.Fatalf("file after relogin = %s, want plain new credential", raw)
	}
}

func TestFileTokenStoreCodexReloginReplacesUnreadableFile(t *testing.T) {
	enableTestAuthEncryption(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-a.json")
	if errWrite := authfile.WriteFile(path, []byte(`{"type":"codex","access_token":"tok-old","siwc":{"access_token":"siwc-old"}}`), 0o600); errWrite != nil {
		t.Fatalf("write sealed file: %v", errWrite)
	}
	authfile.Configure(nil, false)
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	if _, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:         "codex-a.json",
		Provider:   "codex",
		FileName:   "codex-a.json",
		Storage:    &codex.CodexTokenStorage{AccessToken: "tok-new", RefreshToken: "ref-new", Email: "a@example.com"},
		Metadata:   map[string]any{"type": "codex"},
		Attributes: map[string]string{cliproxyauth.AttributePath: path},
	}); errSave != nil {
		t.Fatalf("Save(codex) over unreadable file error = %v", errSave)
	}
	raw, _ := os.ReadFile(path)
	if authfile.IsSealed(raw) || !bytes.Contains(raw, []byte("tok-new")) {
		t.Fatalf("file after relogin = %s, want plain new credential", raw)
	}
}
