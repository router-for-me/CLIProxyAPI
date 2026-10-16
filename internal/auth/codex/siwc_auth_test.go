package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"golang.org/x/sync/singleflight"
)

func TestGenerateSIWCAuthURL(t *testing.T) {
	auth := NewCodexAuth(&config.Config{})
	pkce := &PKCECodes{
		CodeVerifier:  "test-verifier",
		CodeChallenge: "test-challenge",
	}
	deviceID := "E61BBE28-07EF-466D-8E5D-A344F94AB305"
	rawURL, err := auth.GenerateSIWCAuthURL("test-state", "test-nonce", deviceID, "", pkce)
	if err != nil {
		t.Fatalf("GenerateSIWCAuthURL error: %v", err)
	}
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		t.Fatalf("url.Parse error: %v", errParse)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != SIWCAuthorizeURL {
		t.Fatalf("url = %s, want prefix %s", rawURL, SIWCAuthorizeURL)
	}
	q := parsed.Query()
	if got := q.Get("client_id"); got != SIWCDynamicClientID {
		t.Fatalf("client_id = %q, want %q", got, SIWCDynamicClientID)
	}
	if got := q.Get("agent_name_hint"); got != SIWCAgentNameHint {
		t.Fatalf("agent_name_hint = %q, want %q", got, SIWCAgentNameHint)
	}
	wantHostID := "urn:uuid:" + strings.ToLower(deviceID)
	if got := q.Get("ext_agent_host_id"); got != wantHostID {
		t.Fatalf("ext_agent_host_id = %q, want %q", got, wantHostID)
	}
	if got := q.Get("resource"); got != SIWCResource {
		t.Fatalf("resource = %q, want %q", got, SIWCResource)
	}
	if got := q.Get("scope"); got != SIWCScope {
		t.Fatalf("scope = %q, want %q", got, SIWCScope)
	}
	if got := q.Get("redirect_uri"); got != SIWCRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, SIWCRedirectURI)
	}
	if got := q.Get("code_challenge"); got != "test-challenge" {
		t.Fatalf("code_challenge = %q, want test-challenge", got)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	if got := q.Get("state"); got != "test-state" {
		t.Fatalf("state = %q, want test-state", got)
	}
	if got := q.Get("nonce"); got != "test-nonce" {
		t.Fatalf("nonce = %q, want test-nonce", got)
	}

	if _, errBadDevice := auth.GenerateSIWCAuthURL("test-state", "test-nonce", "not-a-uuid", "", pkce); errBadDevice == nil {
		t.Fatal("expected error for invalid device UUID")
	}
	if SIWCAgentNameHint != "CLIProxyAPI" {
		t.Fatalf("SIWCAgentNameHint = %q, want CLIProxyAPI", SIWCAgentNameHint)
	}

	customCfg := &config.Config{}
	customCfg.Codex.SignInWithChatGPTIdentifier = "CustomAgent"
	customAuth := NewCodexAuth(customCfg)
	customURL, errCustom := customAuth.GenerateSIWCAuthURL("test-state", "test-nonce", deviceID, "", pkce)
	if errCustom != nil {
		t.Fatalf("GenerateSIWCAuthURL (custom) error: %v", errCustom)
	}
	parsedCustom, errParseCustom := url.Parse(customURL)
	if errParseCustom != nil {
		t.Fatalf("url.Parse (custom) error: %v", errParseCustom)
	}
	if got := parsedCustom.Query().Get("agent_name_hint"); got != "CustomAgent" {
		t.Fatalf("custom agent_name_hint = %q, want CustomAgent", got)
	}
}

func TestParseOAuthCallbackClientID(t *testing.T) {
	cbURL := "http://127.0.0.1:1455/auth/callback?code=auth-code-123&state=state-abc&client_id=oaiapp_issued_99"
	parsed, err := misc.ParseOAuthCallback(cbURL)
	if err != nil {
		t.Fatalf("ParseOAuthCallback error: %v", err)
	}
	if parsed == nil {
		t.Fatal("expected non-nil callback")
	}
	if parsed.Code != "auth-code-123" || parsed.State != "state-abc" || parsed.ClientID != "oaiapp_issued_99" {
		t.Fatalf("parsed = %+v, want code=auth-code-123 state=state-abc client_id=oaiapp_issued_99", parsed)
	}

	srv := NewOAuthServer(0)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state=s1&client_id=oaiapp_srv_1", nil)
	rec := httptest.NewRecorder()
	srv.handleCallback(rec, req)
	res, errWait := srv.WaitForCallback(time.Second)
	if errWait != nil {
		t.Fatalf("WaitForCallback error: %v", errWait)
	}
	if res.Code != "c1" || res.State != "s1" || res.ClientID != "oaiapp_srv_1" {
		t.Fatalf("OAuthServer result = %+v", res)
	}
}

func TestExchangeAndRefreshSIWCTokens(t *testing.T) {
	siwcRefreshGroup = singleflight.Group{}
	var lastForm url.Values
	var responseBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseBody)
	}))
	defer ts.Close()

	origTokenURL := siwcTokenEndpointURL
	siwcTokenEndpointURL = ts.URL
	defer func() { siwcTokenEndpointURL = origTokenURL }()

	auth := NewCodexAuth(&config.Config{})
	pkce := &PKCECodes{CodeVerifier: "verifier-1", CodeChallenge: "challenge-1"}
	deviceID := "e61bbe28-07ef-466d-8e5d-a344f94ab305"

	responseBody = map[string]any{
		"access_token":  "siwc-access-1",
		"refresh_token": "siwc-refresh-1",
		"id_token":      "siwc-id-1",
		"expires_in":    3600,
		"scope":         SIWCScope,
	}

	before := time.Now()
	tokenData, err := auth.ExchangeSIWCCodeForTokens(context.Background(), "code-xyz", "oaiapp_client1", SIWCRedirectURI, deviceID, pkce)
	if err != nil {
		t.Fatalf("ExchangeSIWCCodeForTokens error: %v", err)
	}
	if lastForm.Get("grant_type") != "authorization_code" ||
		lastForm.Get("client_id") != "oaiapp_client1" ||
		lastForm.Get("code") != "code-xyz" ||
		lastForm.Get("code_verifier") != "verifier-1" ||
		lastForm.Get("redirect_uri") != SIWCRedirectURI ||
		lastForm.Get("resource") != SIWCResource {
		t.Fatalf("unexpected exchange form: %v", lastForm)
	}
	if tokenData.AccessToken != "siwc-access-1" || tokenData.RefreshToken != "siwc-refresh-1" || tokenData.ClientID != "oaiapp_client1" || tokenData.DeviceID != deviceID {
		t.Fatalf("unexpected tokenData: %+v", tokenData)
	}
	expTime, errExp := time.Parse(time.RFC3339, tokenData.Expire)
	if errExp != nil {
		t.Fatalf("parse expire error: %v", errExp)
	}
	// 3600s minus 3m margin = 3420s
	if expTime.Before(before.Add(3415*time.Second)) || expTime.After(time.Now().Add(3425*time.Second)) {
		t.Fatalf("unexpected expire time with 3m margin: %v (before=%v)", expTime, before)
	}

	// Missing direct token scope must fail
	responseBody = map[string]any{
		"access_token":  "siwc-access-1",
		"refresh_token": "siwc-refresh-1",
		"id_token":      "siwc-id-1",
		"expires_in":    3600,
		"scope":         "openid profile email offline_access resource.invoke",
	}
	if _, errScope := auth.ExchangeSIWCCodeForTokens(context.Background(), "code-xyz", "oaiapp_client1", SIWCRedirectURI, deviceID, pkce); errScope == nil {
		t.Fatal("expected error when chatgpt.tokens.use.direct scope is missing")
	}

	// Refresh SIWC tokens
	responseBody = map[string]any{
		"access_token":  "siwc-access-2",
		"refresh_token": "siwc-refresh-2",
		"id_token":      "siwc-id-2",
		"expires_in":    3600,
		"scope":         SIWCScope,
	}
	refreshed, errRefresh := auth.RefreshSIWCTokensWithRetry(context.Background(), "siwc-refresh-1", "oaiapp_client1", deviceID, 1)
	if errRefresh != nil {
		t.Fatalf("RefreshSIWCTokensWithRetry error: %v", errRefresh)
	}
	if lastForm.Get("grant_type") != "refresh_token" ||
		lastForm.Get("client_id") != "oaiapp_client1" ||
		lastForm.Get("refresh_token") != "siwc-refresh-1" ||
		lastForm.Get("resource") != SIWCResource {
		t.Fatalf("unexpected refresh form: %v", lastForm)
	}
	if refreshed.AccessToken != "siwc-access-2" || refreshed.RefreshToken != "siwc-refresh-2" || refreshed.ClientID != "oaiapp_client1" || refreshed.DeviceID != deviceID {
		t.Fatalf("unexpected refreshed data: %+v", refreshed)
	}
}

func TestSaveTokenToFileReadModifyWriteUnderLock(t *testing.T) {
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "codex-test.json")

	useSIWC := true
	initial := &CodexTokenStorage{
		IDToken:      "outer-id-1",
		AccessToken:  "outer-access-1",
		RefreshToken: "outer-refresh-1",
		AccountID:    "acct-1",
		Email:        "user@example.com",
		Type:         "codex",
		Expire:       "2026-10-20T00:00:00Z",
		PlanType:     "plus",
		UseSIWC:      &useSIWC,
		SIWC: map[string]any{
			"access_token":  "siwc-access-1",
			"refresh_token": "siwc-refresh-1",
			"client_id":     "oaiapp_1",
			"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
			"expired":       "2026-10-12T09:00:00Z",
		},
	}
	if err := initial.SaveTokenToFile(authPath); err != nil {
		t.Fatalf("initial SaveTokenToFile: %v", err)
	}

	before := tokenStorageTestMap(t, initial)

	// Concurrent outer-only update and siwc-only update must serialize under lock and preserve all fields
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		outerOnly := &CodexTokenStorage{
			IDToken:      "outer-id-2",
			AccessToken:  "outer-access-2",
			RefreshToken: "outer-refresh-2",
			AccountID:    "acct-1",
			Email:        "user@example.com",
			Type:         "codex",
			Expire:       "2026-10-21T00:00:00Z",
			PlanType:     "plus",
		}
		outerOnly.RefreshPatch = NewCredentialRefreshPatch(before, tokenStorageTestMap(t, outerOnly), true, false)
		if err := outerOnly.SaveTokenToFile(authPath); err != nil {
			t.Errorf("outerOnly SaveTokenToFile: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		siwcOnly := &CodexTokenStorage{
			Type: "codex",
			SIWC: map[string]any{
				"access_token":  "siwc-access-2",
				"refresh_token": "siwc-refresh-2",
				"expired":       "2026-10-12T10:00:00Z",
			},
		}
		siwcOnly.RefreshPatch = NewCredentialRefreshPatch(before, tokenStorageTestMap(t, siwcOnly), false, true)
		if err := siwcOnly.SaveTokenToFile(authPath); err != nil {
			t.Errorf("siwcOnly SaveTokenToFile: %v", err)
		}
	}()
	wg.Wait()

	raw, errRead := os.ReadFile(authPath)
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal: %v", errUnmarshal)
	}
	if saved["access_token"] != "outer-access-2" || saved["refresh_token"] != "outer-refresh-2" {
		t.Fatalf("outer tokens lost or not updated: %+v", saved)
	}
	if saved["use_siwc"] != true {
		t.Fatalf("use_siwc lost: %+v", saved["use_siwc"])
	}
	siwcMap, ok := saved["siwc"].(map[string]any)
	if !ok {
		t.Fatalf("siwc map lost: %+v", saved["siwc"])
	}
	if siwcMap["access_token"] != "siwc-access-2" || siwcMap["refresh_token"] != "siwc-refresh-2" {
		t.Fatalf("siwc tokens not updated: %+v", siwcMap)
	}
	if siwcMap["client_id"] != "oaiapp_1" || siwcMap["device_id"] != "e61bbe28-07ef-466d-8e5d-a344f94ab305" {
		t.Fatalf("siwc client_id or device_id lost: %+v", siwcMap)
	}
}

func TestSaveTokenToFileDoesNotOverwriteNewerDiskSIWC(t *testing.T) {
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "codex-newer-disk.json")

	// Disk has a newly bound SIWC credential (last_refresh 12:00:00Z)
	diskJSON := `{"type":"codex","access_token":"outer-old","refresh_token":"outer-ref-old","last_refresh":"2026-10-12T10:00:00Z","use_siwc":true,"siwc":{"access_token":"siwc-newer-on-disk","refresh_token":"siwc-ref-newer","client_id":"oaiapp_new","device_id":"e61bbe28-07ef-466d-8e5d-a344f94ab305","last_refresh":"2026-10-12T12:00:00Z","expired":"2026-10-12T12:57:00Z"}}`
	if err := os.WriteFile(authPath, []byte(diskJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Server process finishes refreshing outer token (last_refresh 12:01:00Z), but its in-memory SIWC snapshot is stale (last_refresh 10:00:00Z)
	useSIWCFalse := false
	staleSnapshot := &CodexTokenStorage{
		AccessToken:  "outer-refreshed",
		RefreshToken: "outer-ref-refreshed",
		LastRefresh:  "2026-10-12T12:01:00Z",
		Expire:       "2026-10-20T12:01:00Z",
		Type:         "codex",
		UseSIWC:      &useSIWCFalse,
		SIWC: map[string]any{
			"access_token":  "siwc-stale-in-memory",
			"refresh_token": "siwc-ref-stale",
			"client_id":     "oaiapp_old",
			"last_refresh":  "2026-10-12T10:00:00Z",
			"expired":       "2026-10-12T10:57:00Z",
		},
	}
	var before map[string]any
	if errUnmarshal := json.Unmarshal([]byte(diskJSON), &before); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	staleSnapshot.RefreshPatch = NewCredentialRefreshPatch(before, tokenStorageTestMap(t, staleSnapshot), true, false)
	if err := staleSnapshot.SaveTokenToFile(authPath); err != nil {
		t.Fatalf("SaveTokenToFile: %v", err)
	}

	raw, errRead := os.ReadFile(authPath)
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal: %v", errUnmarshal)
	}
	if saved["access_token"] != "outer-refreshed" {
		t.Fatalf("outer access_token = %v, want outer-refreshed", saved["access_token"])
	}
	siwcMap, _ := saved["siwc"].(map[string]any)
	if siwcMap["access_token"] != "siwc-newer-on-disk" || siwcMap["client_id"] != "oaiapp_new" {
		t.Fatalf("newer on-disk SIWC was overwritten by stale in-memory snapshot: %#v", siwcMap)
	}
	if saved["use_siwc"] != true {
		t.Fatalf("use_siwc from newer on-disk SIWC bind should remain true, got %v", saved["use_siwc"])
	}
}

func tokenStorageTestMap(t *testing.T, storage *CodexTokenStorage) map[string]any {
	t.Helper()
	raw, errMarshal := json.Marshal(storage)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return metadata
}
