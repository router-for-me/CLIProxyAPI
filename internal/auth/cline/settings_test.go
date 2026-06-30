package cline

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestProviderAuthSupportsSiblingAuthShape(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2"
				},
				"auth": {"accessToken": "sibling-token"}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	provider, ok := ProviderAuth(settings, ProviderClinePass)
	if !ok {
		t.Fatal("expected sibling auth to be accepted")
	}
	if got := provider.Auth.AccessToken; got != "sibling-token" {
		t.Fatalf("access token = %q, want sibling-token", got)
	}
}

func TestProviderAuthCanUseClineAccountForClinePass(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2"
				}
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {"accessToken": "account-token"}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	if _, ok := FindProvider(settings, ProviderClinePass); !ok {
		t.Fatal("expected cline-pass provider to be detected")
	}
	provider, ok := ProviderAuth(settings, ProviderCline, ProviderClinePass)
	if !ok {
		t.Fatal("expected cline account auth to be accepted for Cline Pass")
	}
	if got := provider.Provider; got != ProviderCline {
		t.Fatalf("provider = %q, want %q", got, ProviderCline)
	}
	if got := provider.Auth.AccessToken; got != "account-token" {
		t.Fatalf("access token = %q, want account-token", got)
	}
}

func TestProviderAuthPrefersClineAccountOverLegacyClinePassAuth(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2",
					"auth": {"accessToken": "legacy-token"}
				}
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {"accessToken": "account-token"}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	provider, ok := ProviderAuth(settings, ProviderCline, ProviderClinePass)
	if !ok {
		t.Fatal("expected provider auth")
	}
	if got := provider.Auth.AccessToken; got != "account-token" {
		t.Fatalf("access token = %q, want account-token", got)
	}
}

func TestProviderAuthFallsBackWhenPreferredProviderHasNoToken(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"cline": {
				"settings": {
					"provider": "cline"
				}
			},
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2",
					"auth": {"accessToken": "legacy-token"}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	provider, ok := ProviderAuth(settings, ProviderCline, ProviderClinePass)
	if !ok {
		t.Fatal("expected provider auth")
	}
	if got := provider.Provider; got != ProviderClinePass {
		t.Fatalf("provider = %q, want %q", got, ProviderClinePass)
	}
	if got := provider.Auth.AccessToken; got != "legacy-token" {
		t.Fatalf("access token = %q, want legacy-token", got)
	}
}

func TestFindProviderPrefersExactKeyBeforeAlias(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"alias": {
				"settings": {
					"provider": "cline",
					"auth": {"accessToken": "alias-token"}
				}
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {"accessToken": "exact-token"}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	provider, ok := ProviderAuth(settings, ProviderCline)
	if !ok {
		t.Fatal("expected provider auth")
	}
	if got := provider.Auth.AccessToken; got != "exact-token" {
		t.Fatalf("access token = %q, want exact-token", got)
	}
}

func TestFindProviderSupportsFlatAliasEntry(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"account": {
				"provider": "cline",
				"model": "some-model",
				"auth": {"accessToken": "flat-token"}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	provider, ok := ProviderAuth(settings, ProviderCline)
	if !ok {
		t.Fatal("expected flat provider auth")
	}
	if got := provider.Provider; got != ProviderCline {
		t.Fatalf("provider = %q, want %q", got, ProviderCline)
	}
	if got := provider.Model; got != "some-model" {
		t.Fatalf("model = %q, want some-model", got)
	}
	if got := provider.Auth.AccessToken; got != "flat-token" {
		t.Fatalf("access token = %q, want flat-token", got)
	}
}

func TestProviderAuthIgnoresNonClineProviders(t *testing.T) {
	settings, err := ParseProviderSettings([]byte(`{
		"providers": {
			"openai": {
				"settings": {
					"provider": "openai",
					"auth": {"accessToken": "openai-token"}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseProviderSettings error: %v", err)
	}
	if _, ok := ProviderAuth(settings, ProviderCline, ProviderClinePass); ok {
		t.Fatal("expected non-Cline provider to be ignored")
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

func TestReadProviderAccessTokenFallsBackToClineAccountForClinePass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	raw := []byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2"
				}
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {"accessToken": "account-token"}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("failed to write provider settings: %v", err)
	}
	token, err := ReadProviderAccessToken(path, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken error: %v", err)
	}
	if token != "account-token" {
		t.Fatalf("token = %q, want account-token", token)
	}
}

func TestReadProviderAccessTokenRejectsExpiredClineAccountWithoutWriteBack(t *testing.T) {
	resetProviderAccessTokenCache(t)

	targetPath := filepath.Join(t.TempDir(), "providers.json")
	raw := []byte(`{
		"lastUsedProvider": "cline",
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2"
				},
				"tokenSource": "manual"
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"model": "some-model",
					"auth": {
						"accessToken": "workos:old-access-token",
						"refreshToken": "old-refresh-token",
						"expiresAt": 1700000000000,
						"accountId": "acct_old"
					}
				},
				"tokenSource": "oauth"
			}
		}
	}`)
	if err := os.WriteFile(targetPath, raw, 0600); err != nil {
		t.Fatalf("failed to write providers.json: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "cline-providers.json")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("failed to create providers symlink: %v", err)
	}

	token, err := ReadProviderAccessToken(linkPath, ProviderClinePass)
	if err == nil {
		t.Fatalf("expected expired token error, got token %q", token)
	}
	if !strings.Contains(err.Error(), "access token expired") {
		t.Fatalf("error = %v, want expired token error", err)
	}
	if info, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("failed to stat symlink: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s was replaced instead of preserving symlink", linkPath)
	}
	updated, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read providers.json: %v", err)
	}
	if string(updated) != string(raw) {
		t.Fatalf("providers.json changed during read:\n%s", string(updated))
	}
}

func TestReadProviderAccessTokenReturnsFutureExpiringToken(t *testing.T) {
	resetProviderAccessTokenCache(t)

	path := filepath.Join(t.TempDir(), "providers.json")
	writeProvidersJSON(t, path, "workos:fresh-access-token", futureProviderExpiryMillis())
	token, err := ReadProviderAccessToken(path, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken error: %v", err)
	}
	if token != "workos:fresh-access-token" {
		t.Fatalf("token = %q, want fresh token", token)
	}
}

func TestReadProviderAccessTokenCachesUntilModTimeChanges(t *testing.T) {
	resetProviderAccessTokenCache(t)

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

func resetProviderAccessTokenCache(t *testing.T) {
	t.Helper()
	providerAccessTokenCache.Lock()
	providerAccessTokenCache.entries = make(map[string]providerAccessTokenCacheEntry)
	providerAccessTokenCache.Unlock()
}

func futureProviderExpiryMillis() int64 {
	return time.Now().Add(24 * time.Hour).UnixMilli()
}

func writeProvidersJSON(t *testing.T, path string, accessToken string, expiresAt int64) {
	t.Helper()
	raw := []byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2"
				}
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {
						"accessToken": "` + accessToken + `",
						"expiresAt": ` + strconv.FormatInt(expiresAt, 10) + `
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("failed to write providers.json: %v", err)
	}
}
