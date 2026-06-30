package cline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
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

func TestReadProviderAccessTokenRefreshesExpiredClineAccountAndPreservesSymlink(t *testing.T) {
	resetProviderAccessTokenCache(t)

	var sawRefresh bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/refresh" {
			t.Fatalf("refresh path = %q, want /auth/refresh", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode refresh body: %v", err)
		}
		if body["grantType"] != "refresh_token" {
			t.Fatalf("grantType = %q, want refresh_token", body["grantType"])
		}
		if body["refreshToken"] != "old-refresh-token" {
			t.Fatalf("refreshToken = %q, want old-refresh-token", body["refreshToken"])
		}
		sawRefresh = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"accessToken": "new-access-token",
				"refreshToken": "new-refresh-token",
				"expiresAt": 1782800000000,
				"userInfo": {"clineUserId": "acct_new"}
			}
		}`))
	}))
	defer server.Close()
	oldBaseURL := clineAPIBaseURL
	clineAPIBaseURL = server.URL
	t.Cleanup(func() {
		clineAPIBaseURL = oldBaseURL
	})

	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "providers.json")
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
	authDir := t.TempDir()
	linkPath := filepath.Join(authDir, "cline-providers.json")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("failed to create providers symlink: %v", err)
	}

	token, err := ReadProviderAccessToken(linkPath, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken error: %v", err)
	}
	if !sawRefresh {
		t.Fatal("expected refresh endpoint to be called")
	}
	if token != "workos:new-access-token" {
		t.Fatalf("token = %q, want refreshed account token", token)
	}
	if info, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("failed to stat symlink: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s was replaced instead of preserving symlink", linkPath)
	}

	var updated map[string]any
	updatedData, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read updated providers.json: %v", err)
	}
	if err := json.Unmarshal(updatedData, &updated); err != nil {
		t.Fatalf("failed to parse updated providers.json: %v", err)
	}
	if updated["lastUsedProvider"] != "cline" {
		t.Fatalf("lastUsedProvider = %#v, want cline", updated["lastUsedProvider"])
	}
	providers := updated["providers"].(map[string]any)
	clineEntry := providers["cline"].(map[string]any)
	if clineEntry["tokenSource"] != "oauth" {
		t.Fatalf("cline tokenSource = %#v, want oauth", clineEntry["tokenSource"])
	}
	clineSettings := clineEntry["settings"].(map[string]any)
	auth := clineSettings["auth"].(map[string]any)
	if auth["accessToken"] != "workos:new-access-token" {
		t.Fatalf("stored accessToken = %#v, want refreshed token", auth["accessToken"])
	}
	if auth["refreshToken"] != "new-refresh-token" {
		t.Fatalf("stored refreshToken = %#v, want new-refresh-token", auth["refreshToken"])
	}
	if auth["accountId"] != "acct_new" {
		t.Fatalf("stored accountId = %#v, want acct_new", auth["accountId"])
	}
}

func TestReadProviderAccessTokenUsesCurrentFileWhenRefreshTokenChanged(t *testing.T) {
	resetProviderAccessTokenCache(t)

	targetPath := filepath.Join(t.TempDir(), "providers.json")
	writeProvidersJSON(t, targetPath, "workos:old-access-token", "old-refresh-token", 1700000000000)

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeProvidersJSON(t, targetPath, "external-access-token", "external-refresh-token", 1782800000000)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"accessToken": "stale-refresh-result",
				"refreshToken": "stale-refresh-token",
				"expiresAt": 1782800000000
			}
		}`))
	}))
	defer server.Close()
	oldBaseURL := clineAPIBaseURL
	clineAPIBaseURL = server.URL
	t.Cleanup(func() {
		clineAPIBaseURL = oldBaseURL
	})

	token, err := ReadProviderAccessToken(targetPath, ProviderClinePass)
	if err != nil {
		t.Fatalf("ReadProviderAccessToken error: %v", err)
	}
	if token != "external-access-token" {
		t.Fatalf("token = %q, want current file token", token)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read providers.json: %v", err)
	}
	if strings.Contains(string(data), "stale-refresh-result") {
		t.Fatalf("stale refresh result was written to providers.json: %s", string(data))
	}
}

func TestReadProviderAccessTokenDeduplicatesConcurrentRefresh(t *testing.T) {
	resetProviderAccessTokenCache(t)

	path := filepath.Join(t.TempDir(), "providers.json")
	writeProvidersJSON(t, path, "workos:old-access-token", "shared-refresh-token", 1700000000000)

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"accessToken": "new-access-token",
				"refreshToken": "new-refresh-token",
				"expiresAt": 1782800000000
			}
		}`))
	}))
	defer server.Close()
	oldBaseURL := clineAPIBaseURL
	clineAPIBaseURL = server.URL
	t.Cleanup(func() {
		clineAPIBaseURL = oldBaseURL
	})

	const workers = 8
	begin := make(chan struct{})
	type result struct {
		token string
		err   error
	}
	results := make(chan result, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-begin
			token, err := ReadProviderAccessToken(path, ProviderClinePass)
			results <- result{token: token, err: err}
		}()
	}
	close(begin)
	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)

	for i := 0; i < workers; i++ {
		res := <-results
		if res.err != nil {
			t.Fatalf("ReadProviderAccessToken error: %v", res.err)
		}
		if res.token != "workos:new-access-token" {
			t.Fatalf("token = %q, want refreshed token", res.token)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
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
	providerAccessTokenRefreshGroup = singleflight.Group{}
}

func writeProvidersJSON(t *testing.T, path string, accessToken string, refreshToken string, expiresAt int64) {
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
						"refreshToken": "` + refreshToken + `",
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
