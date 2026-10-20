package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type geminiAppAuthTransport func(*http.Request) (*http.Response, error)

func (f geminiAppAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGeminiAppImportPersistsAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.json")
	if err := os.WriteFile(path, []byte(`{"__Secure-1PSID":"private-session-cookie","SID":"unrelated-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	authenticator := NewGeminiAppAuthenticator()
	authenticator.client = &http.Client{Transport: geminiAppAuthTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://gemini.google.com/app" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"private-csrf"}`))}, nil
	})}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := NewManager(store, authenticator)
	opts := &LoginOptions{Metadata: map[string]string{"cookie-file": path, "account": "personal"}}
	record, saved, err := manager.Login(context.Background(), "gemini-app", &config.Config{AuthDir: dir}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if record.Provider != "gemini-app" || record.AuthKind() != "oauth" || strings.Contains(record.FileName, "private") || authenticator.RefreshLead() != nil {
		t.Fatalf("unexpected auth: %s", record.FileName)
	}
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "unrelated-secret") || strings.Contains(string(data), "private-csrf") {
		t.Fatal("persisted unrelated credentials or transient page token")
	}
	var metadata map[string]any
	if err = json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["type"] != "gemini-app" || metadata["cookies"].(map[string]any)["__Secure-1PSID"] != "private-session-cookie" {
		t.Fatal("session did not survive persistence")
	}
	if metadata["auth_kind"] != "oauth" || metadata["label"] != "Gemini App" {
		t.Fatal("credential classification or label was lost")
	}
	info, err := os.Stat(saved)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("auth file is not private: %v", info.Mode())
	}
	if err := os.WriteFile(path, []byte(`{"__Secure-1PSID":"replacement-session-cookie"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, replacedPath, err := manager.Login(context.Background(), "gemini-app", &config.Config{AuthDir: dir}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if replacedPath != saved {
		t.Fatal("re-import created a second credential for the same account")
	}
	data, err = os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-session-cookie") || !strings.Contains(string(data), "replacement-session-cookie") {
		t.Fatal("expired credential was not replaced")
	}
	opts.Metadata["account"] = "work"
	_, otherPath, err := manager.Login(context.Background(), "gemini-app", &config.Config{AuthDir: dir}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if otherPath == saved {
		t.Fatal("different accounts overwrite each other")
	}
}

func TestGeminiAppImportRequiresStableAccount(t *testing.T) {
	authenticator := NewGeminiAppAuthenticator()
	_, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{Metadata: map[string]string{"cookie-file": "cookies.json", "account": "   "}})
	if err == nil || !strings.Contains(err.Error(), "stable account name") {
		t.Fatalf("expected account-name validation before reading credentials, got %v", err)
	}
}
