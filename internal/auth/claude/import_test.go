package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseImportedCredentialWrappedPayload(t *testing.T) {
	raw := []byte(`{
      "data": {
        "access_token": "sk-ant-oat01-test-access",
        "refresh_token": "sk-ant-ort01-test-refresh",
        "expires_at": 1793037600,
        "scope": "org:create_api_key user:profile user:inference",
        "last_refresh": "2026-10-26T10:00:00Z",
        "email": "account@example.com",
        "account_uuid": "account-uuid",
        "organization_uuid": "organization-uuid",
        "organization_name": "Example Organization",
        "subscription_type": "max",
        "organization_type": "claude_max",
        "rate_limit_tier": "default_claude_max_20x",
        "custom_attribute": "preserve-me"
      }
    }`)

	credential, errParse := ParseImportedCredential(raw)
	if errParse != nil {
		t.Fatalf("ParseImportedCredential() error = %v", errParse)
	}
	if credential.AccessToken != "sk-ant-oat01-test-access" || credential.RefreshToken != "sk-ant-ort01-test-refresh" {
		t.Fatalf("tokens = %q/%q", credential.AccessToken, credential.RefreshToken)
	}
	if credential.ExpiresAt != 1793037600 {
		t.Fatalf("ExpiresAt = %d, want 1793037600", credential.ExpiresAt)
	}

	auth, errBuild := BuildImportedAuth(credential)
	if errBuild != nil {
		t.Fatalf("BuildImportedAuth() error = %v", errBuild)
	}
	wantExpired := time.Unix(1793037600, 0).UTC().Format(time.RFC3339)
	if auth.Provider != "claude" || auth.FileName != CredentialFileName("account@example.com", "organization-uuid", "account-uuid") {
		t.Fatalf("auth identity = %#v", auth)
	}
	if got := auth.Metadata["expired"]; got != wantExpired {
		t.Fatalf("expired metadata = %#v, want %q", got, wantExpired)
	}
	if got := auth.Metadata["scopes"]; got != credential.Scope {
		t.Fatalf("scopes metadata = %#v, want %q", got, credential.Scope)
	}
	if got := auth.Metadata["custom_attribute"]; got != "preserve-me" {
		t.Fatalf("custom metadata = %#v, want preserve-me", got)
	}
	storage, ok := auth.Storage.(*ClaudeTokenStorage)
	if !ok {
		t.Fatalf("storage type = %T, want *ClaudeTokenStorage", auth.Storage)
	}
	if storage.Expire != wantExpired || storage.OrganizationName != "Example Organization" {
		t.Fatalf("storage = %#v", storage)
	}

	storage.SetMetadata(auth.Metadata)
	path := filepath.Join(t.TempDir(), auth.FileName)
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read saved credential: %v", errRead)
	}
	var savedMetadata map[string]any
	if errUnmarshal := json.Unmarshal(saved, &savedMetadata); errUnmarshal != nil {
		t.Fatalf("unmarshal saved credential: %v", errUnmarshal)
	}
	if savedMetadata["access_token"] != credential.AccessToken || savedMetadata["subscription_type"] != "max" {
		t.Fatalf("saved credential metadata = %#v", savedMetadata)
	}
}

func TestParseImportedCredentialAcceptsDirectPayloadAndStringExpiry(t *testing.T) {
	credential, errParse := ParseImportedCredential([]byte(`{
      "access_token": "sk-ant-oat01-direct",
      "expires_at": "1793037600",
      "email": "direct@example.com"
    }`))
	if errParse != nil {
		t.Fatalf("ParseImportedCredential() error = %v", errParse)
	}
	if credential.ExpiresAt != 1793037600 {
		t.Fatalf("ExpiresAt = %d, want 1793037600", credential.ExpiresAt)
	}
}

func TestParseImportedCredentialRequiresAccessTokenAndEmail(t *testing.T) {
	for name, raw := range map[string]string{
		"missing access token": `{"email":"user@example.com"}`,
		"missing email":        `{"access_token":"sk-ant-oat01-test"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, errParse := ParseImportedCredential([]byte(raw)); errParse == nil {
				t.Fatal("ParseImportedCredential() succeeded, want validation error")
			}
		})
	}
}

func TestParseImportedCredentialRejectsInvalidExpiry(t *testing.T) {
	_, errParse := ParseImportedCredential([]byte(`{
      "access_token":"sk-ant-oat01-test",
      "email":"user@example.com",
      "expires_at":"tomorrow"
    }`))
	if errParse == nil {
		t.Fatal("ParseImportedCredential() succeeded, want invalid expiry error")
	}
}
