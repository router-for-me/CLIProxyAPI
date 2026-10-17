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
	record, saved, err := manager.Login(context.Background(), "gemini-app", &config.Config{AuthDir: dir}, &LoginOptions{Metadata: map[string]string{"cookie-file": path}})
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
}
