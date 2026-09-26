package cmd

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

func TestImportCodexAuthNormalizesNativeFile(t *testing.T) {
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(nil) })
	sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())

	root := t.TempDir()
	sourcePath := filepath.Join(root, "auth.json")
	authDir := filepath.Join(root, "imported")
	idToken := testJWT(t, map[string]any{
		"email": "dev@example.com",
		"exp":   1_900_000_000,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-from-token",
			"chatgpt_plan_type":  "pro",
		},
	})
	source := map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": "2026-08-28T08:00:00Z",
		"tokens": map[string]any{
			"id_token":      idToken,
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"account_id":    "account-from-source",
		},
	}
	sourceJSON, errMarshal := json.Marshal(source)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(sourcePath, sourceJSON, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	savedPath, errImport := importCodexAuth(&config.Config{AuthDir: authDir}, sourcePath)
	if errImport != nil {
		t.Fatalf("importCodexAuth() error = %v", errImport)
	}
	if !strings.HasPrefix(savedPath, authDir+string(os.PathSeparator)) {
		t.Fatalf("saved path %q is outside auth dir %q", savedPath, authDir)
	}

	savedJSON, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedJSON, &saved); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for key, want := range map[string]string{
		"type":          "codex",
		"email":         "dev@example.com",
		"account_id":    "account-from-source",
		"access_token":  "access-secret",
		"refresh_token": "refresh-secret",
		"last_refresh":  "2026-08-28T08:00:00Z",
		"expired":       "2030-03-17T17:46:40Z",
	} {
		if got, _ := saved[key].(string); got != want {
			t.Errorf("saved[%q] = %q, want %q", key, got, want)
		}
	}
	afterSource, errReadSource := os.ReadFile(sourcePath)
	if errReadSource != nil {
		t.Fatal(errReadSource)
	}
	if string(afterSource) != string(sourceJSON) {
		t.Fatal("source auth.json was modified")
	}
}

func TestImportCodexAuthRejectsMissingRefreshToken(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "auth.json")
	if errWrite := os.WriteFile(sourcePath, []byte(`{"tokens":{"access_token":"access","id_token":"id"}}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, errImport := importCodexAuth(&config.Config{AuthDir: filepath.Join(root, "auths")}, sourcePath)
	if errImport == nil || !strings.Contains(errImport.Error(), "refresh token missing") {
		t.Fatalf("importCodexAuth() error = %v, want missing refresh token", errImport)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, errMarshal := json.Marshal(claims)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(payload) + ".signature"
}
