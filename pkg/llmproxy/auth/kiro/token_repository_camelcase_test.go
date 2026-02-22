package kiro

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTokenFile_AcceptsCamelCaseFields(t *testing.T) {
	baseDir := t.TempDir()
	tokenPath := filepath.Join(baseDir, "kiro-enterprise.json")
	content := `{
  "type": "kiro",
  "authMethod": "idc",
  "accessToken": "at",
  "refreshToken": "rt",
  "clientId": "cid",
  "clientSecret": "csecret",
  "startUrl": "https://view.awsapps.com/start",
  "region": "us-east-1",
  "expiresAt": "2099-01-01T00:00:00Z"
}`
	if err := os.WriteFile(tokenPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	repo := NewFileTokenRepository(baseDir)
	token, err := repo.readTokenFile(tokenPath)
	if err != nil {
		t.Fatalf("readTokenFile() error = %v", err)
	}
	if token == nil {
		t.Fatal("readTokenFile() returned nil token")
	}
	if token.AuthMethod != "idc" {
		t.Fatalf("AuthMethod = %q, want %q", token.AuthMethod, "idc")
	}
	if token.ClientID != "cid" {
		t.Fatalf("ClientID = %q, want %q", token.ClientID, "cid")
	}
	if token.ClientSecret != "csecret" {
		t.Fatalf("ClientSecret = %q, want %q", token.ClientSecret, "csecret")
	}
	if token.StartURL != "https://view.awsapps.com/start" {
		t.Fatalf("StartURL = %q, want expected start URL", token.StartURL)
	}
}
