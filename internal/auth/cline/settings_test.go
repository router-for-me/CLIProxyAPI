package cline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseProviderSettingsNormalizesProviderKeys(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			" CLINE-PASS ": {
				"settings": {
					"provider": "cline-pass",
					"auth": {"accessToken": "token"}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	if _, ok := ProviderAuth(settings, ProviderClinePass); !ok {
		t.Fatal("expected normalized cline-pass provider auth")
	}
}

func TestParseProviderSettingsRejectsDuplicateNormalizedProviderKeys(t *testing.T) {
	_, err := ParseProviderSettings([]byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"auth": {"accessToken": "token-a"}
				}
			},
			" CLINE-PASS ": {
				"settings": {
					"provider": "cline-pass",
					"auth": {"accessToken": "token-b"}
				}
			}
		}
	}`))
	if err == nil {
		t.Fatal("expected duplicate normalized provider key error")
	}
	if !strings.Contains(err.Error(), "duplicate provider key") {
		t.Fatalf("expected duplicate provider key error, got: %v", err)
	}
}

func TestReadProviderAccessTokenCachesUntilModTimeChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	fixedTime := time.Unix(1700000000, 0)
	writeSettings := func(token string, modTime time.Time) {
		t.Helper()
		raw := []byte(`{"providers":{"cline-pass":{"settings":{"provider":"cline-pass","auth":{"accessToken":"` + token + `"}}}}}`)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatalf("failed to write provider settings: %v", err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("failed to set provider settings mtime: %v", err)
		}
	}

	writeSettings("token-a", fixedTime)
	first, err := ReadProviderAccessToken(path, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken first error: %v", err)
	}
	if first != "token-a" {
		t.Fatalf("first token = %q, want token-a", first)
	}

	writeSettings("token-b", fixedTime)
	second, err := ReadProviderAccessToken(path, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken second error: %v", err)
	}
	if second != "token-a" {
		t.Fatalf("second token = %q, want cached token-a", second)
	}

	writeSettings("token-b", fixedTime.Add(time.Second))
	third, err := ReadProviderAccessToken(path, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken third error: %v", err)
	}
	if third != "token-b" {
		t.Fatalf("third token = %q, want refreshed token-b", third)
	}
}
