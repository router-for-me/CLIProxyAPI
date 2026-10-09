package cmd

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
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

// TestImportCodexAuthKeepsExistingSettings imports the same account twice. The
// second import must keep what the user configured on the saved credential, and
// an ID token without a plan claim must land on the login paths' default plan.
func TestImportCodexAuthKeepsExistingSettings(t *testing.T) {
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
		},
	})
	source := map[string]any{
		"tokens": map[string]any{
			"id_token":      idToken,
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
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
		t.Fatalf("first importCodexAuth() error = %v", errImport)
	}
	if !strings.HasSuffix(savedPath, "-free.json") {
		t.Fatalf("saved path = %q, want the default plan suffix -free.json", savedPath)
	}

	raw, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	saved["disabled"] = true
	saved["proxy_url"] = "http://127.0.0.1:9"
	// Stale identity in the old file: the import must not take it over.
	saved["account_id"] = "stale-account"
	saved["type"] = "other-provider"
	edited, errEdit := json.Marshal(saved)
	if errEdit != nil {
		t.Fatal(errEdit)
	}
	if errWrite := os.WriteFile(savedPath, edited, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	// Rotated tokens, so that a re-import returning the old file unchanged fails.
	source["tokens"].(map[string]any)["access_token"] = "access-rotated"
	source["tokens"].(map[string]any)["refresh_token"] = "refresh-rotated"
	rotatedJSON, errRotate := json.Marshal(source)
	if errRotate != nil {
		t.Fatal(errRotate)
	}
	if errWrite := os.WriteFile(sourcePath, rotatedJSON, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	againPath, errAgain := importCodexAuth(&config.Config{AuthDir: authDir}, sourcePath)
	if errAgain != nil {
		t.Fatalf("second importCodexAuth() error = %v", errAgain)
	}
	if againPath != savedPath {
		t.Fatalf("second import wrote %q, want the same file %q", againPath, savedPath)
	}
	raw, errRead = os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var after map[string]any
	if errUnmarshal := json.Unmarshal(raw, &after); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if disabled, _ := after["disabled"].(bool); !disabled {
		t.Fatalf("disabled = %v after re-import, want true", after["disabled"])
	}
	if got, _ := after["proxy_url"].(string); got != "http://127.0.0.1:9" {
		t.Fatalf("proxy_url = %q after re-import, want it preserved", got)
	}
	for key, want := range map[string]string{
		"access_token":  "access-rotated",
		"refresh_token": "refresh-rotated",
		"account_id":    "account-from-token",
		"type":          "codex",
		"plan_type":     "free",
	} {
		if got, _ := after[key].(string); got != want {
			t.Fatalf("%s = %q after re-import, want %q", key, got, want)
		}
	}
}

// TestReadExistingCredentialIgnoresSymlink checks that a link at the credential
// path is not read through, so nothing outside the auth directory reaches an import.
func TestReadExistingCredentialIgnoresSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.json")
	if errWrite := os.WriteFile(outside, []byte(`{"proxy_url":"http://127.0.0.1:9"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	// Control: the same content is read back from a regular file.
	if got := readExistingCredential(outside); got["proxy_url"] != "http://127.0.0.1:9" {
		t.Fatalf("readExistingCredential(regular file) = %v, want its proxy_url", got)
	}

	link := filepath.Join(root, "codex-link.json")
	if errLink := os.Symlink(outside, link); errLink != nil {
		t.Skipf("symlinks unavailable on this platform: %v", errLink)
	}
	if got := readExistingCredential(link); got != nil {
		t.Fatalf("readExistingCredential(symlink) = %v, want nil", got)
	}
}

// TestImportCodexAuthRejectsSameFileSource checks that an import never rewrites its
// own source: a source that is the destination credential file itself, or a link
// to it, is refused and left byte for byte as it was.
func TestImportCodexAuthRejectsSameFileSource(t *testing.T) {
	aliases := map[string]func(destination, alias string) error{
		"same path": nil,
		"hard link": os.Link,
		"symlink":   os.Symlink,
	}
	for name, link := range aliases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { sdkAuth.RegisterTokenStore(nil) })
			sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())

			root := t.TempDir()
			authDir := filepath.Join(root, "imported")
			native, errMarshal := json.Marshal(map[string]any{
				"tokens": map[string]any{
					"id_token": testJWT(t, map[string]any{
						"email":                       "dev@example.com",
						"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-from-token"},
					}),
					"access_token":  "access-secret",
					"refresh_token": "refresh-secret",
				},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			outside := filepath.Join(root, "auth.json")
			if errWrite := os.WriteFile(outside, native, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			// A first import from outside names the destination file.
			destination, errImport := importCodexAuth(&config.Config{AuthDir: authDir}, outside)
			if errImport != nil {
				t.Fatalf("first importCodexAuth() error = %v", errImport)
			}
			// Put the native file at the destination, then import it through the alias.
			if errWrite := os.WriteFile(destination, native, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			source := destination
			if link != nil {
				source = filepath.Join(root, "alias.json")
				if errLink := link(destination, source); errLink != nil {
					t.Skipf("%s unavailable on this platform: %v", name, errLink)
				}
			}

			_, errAgain := importCodexAuth(&config.Config{AuthDir: authDir}, source)
			if errAgain == nil || !strings.Contains(errAgain.Error(), "same file") {
				t.Fatalf("importCodexAuth() error = %v, want a same-file refusal", errAgain)
			}
			after, errRead := os.ReadFile(destination)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if string(after) != string(native) {
				t.Fatal("the source file was rewritten by its own import")
			}
		})
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
