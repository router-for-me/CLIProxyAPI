package minimax

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResolveRegionAndBaseURL(t *testing.T) {
	if got := NormalizeRegion("CN"); got != RegionCN {
		t.Fatalf("NormalizeRegion(CN) = %q", got)
	}
	if got := NormalizeRegion("global"); got != RegionGlobal {
		t.Fatalf("NormalizeRegion(global) = %q", got)
	}
	if got := NormalizeRegion(""); got != RegionGlobal {
		t.Fatalf("NormalizeRegion(empty) = %q", got)
	}
	if got := ResolveOAuthHost(RegionCN); got != OAuthHostCN {
		t.Fatalf("CN oauth host = %q", got)
	}
	if got := ResolveOAuthHost(RegionGlobal); got != OAuthHostGlobal {
		t.Fatalf("global oauth host = %q", got)
	}
	// The Claude request builder appends /v1/messages, so the Claude base
	// must end with the Anthropic prefix and never with a trailing slash.
	if got := ResolveClaudeBaseURL(RegionGlobal); got != "https://api.minimax.io/anthropic" {
		t.Fatalf("global claude base = %q", got)
	}
	if got := ResolveClaudeBaseURL(RegionCN); got != "https://api.minimax.cn/anthropic" {
		t.Fatalf("cn claude base = %q", got)
	}
}

func TestResolveClaudeUpstreamURLHonorsResourceURL(t *testing.T) {
	// A resource_url that already carries the Anthropic suffix must not be doubled.
	auth := &cliproxyauthAuthStub{metadata: map[string]any{
		"resource_url": "https://api.minimax.io/anthropic",
	}}
	if got := resolveClaudeURL(auth); got != "https://api.minimax.io/anthropic" {
		t.Fatalf("resource_url base = %q", got)
	}
	// A plain API base gains the suffix.
	auth2 := &cliproxyauthAuthStub{metadata: map[string]any{
		"base_url": "https://api.minimax.cn",
	}}
	if got := resolveClaudeURL(auth2); got != "https://api.minimax.cn/anthropic" {
		t.Fatalf("base_url = %q", got)
	}
	// With nothing configured, the region default applies.
	auth3 := &cliproxyauthAuthStub{provider: ProviderCN}
	if got := resolveClaudeURL(auth3); got != "https://api.minimax.cn/anthropic" {
		t.Fatalf("region default = %q", got)
	}
}

func TestGeneratePKCECodes(t *testing.T) {
	codes, err := GeneratePKCECodes()
	if err != nil {
		t.Fatalf("GeneratePKCECodes: %v", err)
	}
	if codes.Verifier == "" || codes.Challenge == "" {
		t.Fatal("PKCE codes must be populated")
	}
	if codes.Verifier == codes.Challenge {
		t.Fatal("verifier and challenge must differ")
	}
	// base64url alphabet only: no '+', '/', or padding.
	if strings.ContainsAny(codes.Verifier, "+/=") || strings.ContainsAny(codes.Challenge, "+/=") {
		t.Fatalf("PKCE values must be base64url: verifier=%q challenge=%q", codes.Verifier, codes.Challenge)
	}
	other, err := GeneratePKCECodes()
	if err != nil {
		t.Fatalf("GeneratePKCECodes second: %v", err)
	}
	if other.Verifier == codes.Verifier {
		t.Fatal("verifiers must be unique per flow")
	}
}

// TestDeviceCodeExpiryIsAbsoluteTimestamp locks in MiniMax's deviation from
// RFC 8628: expired_in is an absolute epoch-ms deadline, not a duration. A
// duration-based reading would compute an ~55 year timeout.
func TestDeviceCodeExpiryIsAbsoluteTimestamp(t *testing.T) {
	future := &DeviceCodeResponse{ExpiredIn: time.Now().Add(5 * time.Minute).UnixMilli()}
	if d := future.expiryDeadline(); d > 6*time.Minute || d < 4*time.Minute {
		t.Fatalf("future expiry deadline = %v, want ~5m", d)
	}
	past := &DeviceCodeResponse{ExpiredIn: time.Now().Add(-time.Minute).UnixMilli()}
	if d := past.expiryDeadline(); d != time.Millisecond {
		t.Fatalf("past expiry deadline = %v, want a positive minimum", d)
	}
	if d := (&DeviceCodeResponse{}).expiryDeadline(); d != MaxPollDuration {
		t.Fatalf("missing expiry deadline = %v, want %v", d, MaxPollDuration)
	}
}

func TestPollIntervalFloor(t *testing.T) {
	if got := (&DeviceCodeResponse{Interval: 0}).pollInterval(); got != defaultPollInterval {
		t.Fatalf("zero interval = %v, want %v", got, defaultPollInterval)
	}
	if got := (&DeviceCodeResponse{Interval: 3000}).pollInterval(); got != 3*time.Second {
		t.Fatalf("3000ms interval = %v", got)
	}
	// An interval below the floor is raised to avoid hammering the endpoint.
	if got := (&DeviceCodeResponse{Interval: 100}).pollInterval(); got != defaultPollInterval {
		t.Fatalf("100ms interval = %v, want floor", got)
	}
}

func TestRequestDeviceCode(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_code":        "ABCD-EFGH",
			"verification_uri": "https://platform.minimax.io/oauth-authorize?user_code=ABCD-EFGH",
			"expired_in":       time.Now().Add(5 * time.Minute).UnixMilli(),
			"interval":         3000,
			"state":            r.PostForm.Get("state"),
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, pkce, err := client.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if resp.UserCode != "ABCD-EFGH" {
		t.Fatalf("user code = %q", resp.UserCode)
	}
	if pkce == nil || pkce.Verifier == "" || pkce.Challenge == "" {
		t.Fatal("PKCE codes must be returned")
	}
	// The endpoint is not configurable per test, so assert on the captured form.
	if gotForm.Get("client_id") != ClientID {
		t.Fatalf("client_id = %q", gotForm.Get("client_id"))
	}
	if gotForm.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q", gotForm.Get("code_challenge_method"))
	}
	if gotForm.Get("code_challenge") != pkce.Challenge {
		t.Fatalf("code_challenge mismatch")
	}
	if gotForm.Get("scope") != "openid profile coding_plan" {
		t.Fatalf("scope = %q", gotForm.Get("scope"))
	}
	if gotForm.Get("state") == "" {
		t.Fatal("state must be sent")
	}
	if resp.State != gotForm.Get("state") {
		t.Fatal("state echo mismatch")
	}
}

func TestRequestDeviceCodeRejectsStateMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_code":        "ABCD-EFGH",
			"verification_uri": "https://platform.minimax.io/oauth-authorize",
			"state":            "attacker-state",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	if _, _, err := client.RequestDeviceCode(context.Background()); err == nil {
		t.Fatal("expected an error when the server does not echo our state")
	}
}

func TestRequestDeviceCodeRejectsMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"verification_uri": "https://platform.minimax.io/oauth-authorize",
			"state":            r.PostForm.Get("state"),
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	if _, _, err := client.RequestDeviceCode(context.Background()); err == nil {
		t.Fatal("expected an error when user_code is missing")
	}
}

// TestPostTokenPendingIsNotAnError pins the second MiniMax deviation: a pending
// poll is HTTP 200 with {"status":"pending"}, not an RFC 8628 error code.
func TestPostTokenPendingIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	token, pending, err := client.postToken(context.Background(), url.Values{}, "device code")
	if err != nil {
		t.Fatalf("pending must not be an error: %v", err)
	}
	if !pending {
		t.Fatal("pending flag must be set")
	}
	if token != nil {
		t.Fatal("pending poll must not return a token")
	}
}

func TestPostTokenSuccess(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "success",
			"access_token":  "access-123",
			"refresh_token": "refresh-456",
			"token_type":    "Bearer",
			"expired_in":    expiry,
			"scope":         "openid profile coding_plan",
			"resource_url":  "https://api.minimax.io",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	token, pending, err := client.postToken(context.Background(), url.Values{}, "device code")
	if err != nil {
		t.Fatalf("postToken: %v", err)
	}
	if pending {
		t.Fatal("success must not report pending")
	}
	if token.AccessToken != "access-123" || token.RefreshToken != "refresh-456" {
		t.Fatalf("token = %+v", token)
	}
	if token.ResourceURL != "https://api.minimax.io" {
		t.Fatalf("resource_url = %q", token.ResourceURL)
	}
	if token.ExpiresAt.IsZero() {
		t.Fatal("expires_at must be derived from the absolute expired_in")
	}
}

func TestPostTokenErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		body      map[string]any
		wantErr   string
		wantPends bool
	}{
		{"access_denied", map[string]any{"status": "failed", "error": "access_denied"}, "access denied", false},
		{"expired_token", map[string]any{"status": "failed", "error": "expired_token"}, "expired", false},
		{"authorization_pending", map[string]any{"error": "authorization_pending"}, "", true},
		{"slow_down", map[string]any{"error": "slow_down"}, "", true},
		{"other", map[string]any{"error": "server_error", "error_description": "boom"}, "server_error", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			defer srv.Close()

			client := newTestClient(srv)
			_, pending, err := client.postToken(context.Background(), url.Values{}, "device code")
			if tc.wantPends {
				if err != nil {
					t.Fatalf("expected pending continuation, got error: %v", err)
				}
				if !pending {
					t.Fatal("expected pending flag")
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if pending {
				t.Fatal("error must not report pending")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestPostTokenHTTPErrorSurfacesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "invalid or expired code",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	if _, _, err := client.postToken(context.Background(), url.Values{}, "device code"); err == nil {
		t.Fatal("expected an error for a 400 response")
	} else if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error should include the OAuth code: %v", err)
	}
}

func TestCreateTokenStorage(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	auth := NewMinimaxAuth(&config.Config{}, RegionCN)
	storage := auth.CreateTokenStorage(&AuthBundle{
		Region: RegionCN,
		TokenData: &TokenData{
			AccessToken:  "access-1",
			RefreshToken: "refresh-1",
			TokenType:    "Bearer",
			Scope:        "openid",
			ExpiresAt:    expiry,
			ResourceURL:  "https://api.minimax.cn",
		},
	})
	if storage == nil {
		t.Fatal("storage must be created")
	}
	if storage.Type != ProviderCN {
		t.Fatalf("type = %q, want %q", storage.Type, ProviderCN)
	}
	if storage.Region != RegionCN {
		t.Fatalf("region = %q", storage.Region)
	}
	// A resource_url from the authorization server takes precedence.
	if storage.BaseURL != "https://api.minimax.cn" {
		t.Fatalf("base_url = %q", storage.BaseURL)
	}
	if storage.Expired != expiry.Format(time.RFC3339) {
		t.Fatalf("expired = %q", storage.Expired)
	}

	// Without a resource_url the region default is used.
	auth2 := NewMinimaxAuth(&config.Config{}, RegionGlobal)
	storage2 := auth2.CreateTokenStorage(&AuthBundle{Region: RegionGlobal, TokenData: &TokenData{AccessToken: "a"}})
	if storage2.BaseURL != APIBaseURLGlobal {
		t.Fatalf("default base_url = %q", storage2.BaseURL)
	}
	if storage2.Type != "minimax" {
		t.Fatalf("global type = %q", storage2.Type)
	}
}

func TestSaveTokenToFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/minimax-1.json"
	storage := &MinimaxTokenStorage{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		Region:       RegionCN,
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	storage.SetMetadata(map[string]any{"extra": "value"})
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile: %v", err)
	}

	data, err := readJSONFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if data["type"] != ProviderCN {
		t.Fatalf("type = %v, want %q", data["type"], ProviderCN)
	}
	if data["access_token"] != "access-1" || data["refresh_token"] != "refresh-1" {
		t.Fatalf("tokens not persisted: %+v", data)
	}
	if data["region"] != RegionCN {
		t.Fatalf("region = %v", data["region"])
	}
	if data["base_url"] != APIBaseURLCN {
		t.Fatalf("base_url = %v", data["base_url"])
	}
	if data["extra"] != "value" {
		t.Fatalf("metadata must be flattened into the file: %+v", data)
	}
}

func TestNeedsRefresh(t *testing.T) {
	past := &MinimaxTokenStorage{
		RefreshToken: "r",
		Expired:      time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}
	if !past.NeedsRefresh() {
		t.Fatal("expired token needs refresh")
	}
	future := &MinimaxTokenStorage{
		RefreshToken: "r",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if future.NeedsRefresh() {
		t.Fatal("valid token must not need refresh")
	}
	// Without a refresh token there is nothing to do.
	noRefresh := &MinimaxTokenStorage{Expired: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	if noRefresh.NeedsRefresh() {
		t.Fatal("token without a refresh token must not report NeedsRefresh")
	}
	// An unparseable expiry is treated as expired.
	broken := &MinimaxTokenStorage{RefreshToken: "r", Expired: "not-a-time"}
	if !broken.NeedsRefresh() {
		t.Fatal("unparseable expiry must be treated as expired")
	}
}

// TestProviderForRegion locks in that the two regions get separate provider
// keys, matching the kimi.com / kimi.ai split so credentials never share a
// provider across regions.
func TestProviderForRegion(t *testing.T) {
	if got := ProviderForRegion(RegionGlobal); got != "minimax" {
		t.Fatalf("global provider = %q", got)
	}
	if got := ProviderForRegion(RegionCN); got != ProviderCN {
		t.Fatalf("cn provider = %q", got)
	}
	if !IsCNProvider(ProviderCN) {
		t.Fatal("ProviderCN must be recognised as the China provider")
	}
	if IsCNProvider("minimax") {
		t.Fatal("the global provider must not be treated as China")
	}
}

// TestResolveRegionFromAuthPrefersProviderKey ensures a China credential is
// never resolved as global, even if its stored base URL is ambiguous.
func TestResolveRegionFromAuthPrefersProviderKey(t *testing.T) {
	cn := &cliproxyauth.Auth{
		Provider:   ProviderCN,
		Metadata:   map[string]any{"region": "global", "base_url": "https://api.minimax.io"},
		Attributes: map[string]string{"base_url": "https://api.minimax.io"},
	}
	if got := ResolveRegionFromAuth(cn); got != RegionCN {
		t.Fatalf("CN provider resolved to %q; the provider key must win", got)
	}

	global := &cliproxyauth.Auth{Provider: "minimax", Metadata: map[string]any{"base_url": "https://api.minimax.cn"}}
	if got := ResolveRegionFromAuth(global); got != RegionGlobal {
		t.Fatalf("global provider resolved to %q; the provider key must win", got)
	}
}
