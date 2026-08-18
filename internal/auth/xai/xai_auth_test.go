package xai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func resetXAIRefreshGroupForTest() {
	xaiRefreshGroup = singleflight.Group{}
}

func TestValidateOAuthEndpointRejectsNonXAIOrigin(t *testing.T) {
	if _, err := ValidateOAuthEndpoint("https://auth.x.ai/oauth2/token", "token_endpoint"); err != nil {
		t.Fatalf("ValidateOAuthEndpoint(xai) error = %v", err)
	}
	if _, err := ValidateOAuthEndpoint("http://auth.x.ai/oauth2/token", "token_endpoint"); err == nil {
		t.Fatal("expected non-HTTPS endpoint to be rejected")
	}
	if _, err := ValidateOAuthEndpoint("https://evil.example/oauth/token", "token_endpoint"); err == nil {
		t.Fatal("expected non-xAI endpoint to be rejected")
	}
}

func TestRequestDeviceCodePostsClientIDAndScope(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Fatalf("Content-Type = %q, want form", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-abc",
			"user_code":                 "ABCD-1234",
			"verification_uri":          "https://accounts.x.ai/oauth2/device",
			"verification_uri_complete": "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234",
			"expires_in":                1800,
			"interval":                  5,
		})
	}))
	defer server.Close()

	auth := NewXAIAuth(nil)
	deviceCode, err := auth.RequestDeviceCode(context.Background(), server.URL, "https://auth.x.ai/oauth2/token")
	if err != nil {
		t.Fatalf("RequestDeviceCode() error = %v", err)
	}
	if deviceCode.DeviceCode != "device-abc" {
		t.Fatalf("device_code = %q, want device-abc", deviceCode.DeviceCode)
	}
	if deviceCode.UserCode != "ABCD-1234" {
		t.Fatalf("user_code = %q, want ABCD-1234", deviceCode.UserCode)
	}
	if deviceCode.TokenEndpoint != "https://auth.x.ai/oauth2/token" {
		t.Fatalf("TokenEndpoint = %q", deviceCode.TokenEndpoint)
	}
	if gotForm.Get("client_id") != ClientID {
		t.Fatalf("client_id = %q, want %q", gotForm.Get("client_id"), ClientID)
	}
	if gotForm.Get("scope") != Scope {
		t.Fatalf("scope = %q, want %q", gotForm.Get("scope"), Scope)
	}
}

func TestPollForTokenExchangesDeviceCode(t *testing.T) {
	var pollCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != DeviceCodeGrantType {
			t.Fatalf("grant_type = %q, want %q", got, DeviceCodeGrantType)
		}
		if got := r.PostForm.Get("device_code"); got != "device-abc" {
			t.Fatalf("device_code = %q, want device-abc", got)
		}
		if got := r.PostForm.Get("client_id"); got != ClientID {
			t.Fatalf("client_id = %q, want %q", got, ClientID)
		}

		count := atomic.AddInt32(&pollCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":             "authorization_pending",
				"error_description": "User has not yet authorized",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      fakeJWTWithEmail("user@x.ai", "sub-1"),
		})
	}))
	defer server.Close()

	auth := NewXAIAuth(nil)
	tokenData, err := auth.PollForToken(context.Background(), &DeviceCodeResponse{
		DeviceCode:    "device-abc",
		UserCode:      "ABCD-1234",
		ExpiresIn:     60,
		Interval:      1,
		TokenEndpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("PollForToken() error = %v", err)
	}
	if tokenData.AccessToken != "access-1" {
		t.Fatalf("access token = %q, want access-1", tokenData.AccessToken)
	}
	if tokenData.RefreshToken != "refresh-1" {
		t.Fatalf("refresh token = %q, want refresh-1", tokenData.RefreshToken)
	}
	if tokenData.Email != "user@x.ai" {
		t.Fatalf("email = %q, want user@x.ai", tokenData.Email)
	}
	if tokenData.Subject != "sub-1" {
		t.Fatalf("subject = %q, want sub-1", tokenData.Subject)
	}
	if got := atomic.LoadInt32(&pollCount); got != 2 {
		t.Fatalf("poll count = %d, want 2", got)
	}
}

func TestPollForTokenAccessDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "access_denied",
			"error_description": "The user rejected the request",
		})
	}))
	defer server.Close()

	auth := NewXAIAuth(nil)
	_, err := auth.PollForToken(context.Background(), &DeviceCodeResponse{
		DeviceCode:    "device-abc",
		UserCode:      "ABCD-1234",
		ExpiresIn:     60,
		Interval:      1,
		TokenEndpoint: server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("PollForToken() error = %v, want authorization denied", err)
	}
}

func TestPollForTokenSlowDownContinuesPolling(t *testing.T) {
	var pollCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&pollCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-slow",
			"refresh_token": "refresh-slow",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	auth := NewXAIAuth(nil)
	tokenData, err := auth.PollForToken(context.Background(), &DeviceCodeResponse{
		DeviceCode:    "device-abc",
		UserCode:      "ABCD-1234",
		ExpiresIn:     60,
		Interval:      5,
		TokenEndpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("PollForToken() error = %v", err)
	}
	if tokenData.AccessToken != "access-slow" {
		t.Fatalf("access token = %q, want access-slow", tokenData.AccessToken)
	}
	if got := atomic.LoadInt32(&pollCount); got != 2 {
		t.Fatalf("poll count = %d, want 2", got)
	}
}

func TestBuildTokenDataOmitsExpireWhenExpiresInZero(t *testing.T) {
	tokenData := buildTokenData("access", "refresh", "", "Bearer", 0, "user@x.ai", "sub-1")
	if tokenData.Expire != "" {
		t.Fatalf("Expire = %q, want empty", tokenData.Expire)
	}
	tokenData = buildTokenData("access", "refresh", "", "Bearer", 60, "user@x.ai", "sub-1")
	if tokenData.Expire == "" {
		t.Fatal("Expire empty, want RFC3339 timestamp")
	}
}

func TestRefreshTokensPostsClientIDAndRefreshToken(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Fatalf("Content-Type = %q, want form", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	auth := NewXAIAuth(nil)
	tokenData, err := auth.RefreshTokens(context.Background(), "old-refresh", server.URL)
	if err != nil {
		t.Fatalf("RefreshTokens() error = %v", err)
	}
	if tokenData.AccessToken != "new-access" {
		t.Fatalf("access token = %q, want new-access", tokenData.AccessToken)
	}
	if gotForm.Get("grant_type") != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", gotForm.Get("grant_type"))
	}
	if gotForm.Get("client_id") != ClientID {
		t.Fatalf("client_id = %q, want %q", gotForm.Get("client_id"), ClientID)
	}
	if gotForm.Get("refresh_token") != "old-refresh" {
		t.Fatalf("refresh_token = %q, want old-refresh", gotForm.Get("refresh_token"))
	}
}

func TestRefreshTokens_DeduplicatesConcurrentRefresh(t *testing.T) {
	resetXAIRefreshGroupForTest()
	t.Cleanup(resetXAIRefreshGroupForTest)

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	authA := NewXAIAuth(nil)
	authB := NewXAIAuth(nil)
	results := make(chan *TokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func(auth *XAIAuth, launched chan<- struct{}) {
		if launched != nil {
			close(launched)
		}
		tokenData, errRefresh := auth.RefreshTokens(context.Background(), "shared-refresh-token", server.URL)
		results <- tokenData
		errs <- errRefresh
	}

	go runRefresh(authA, nil)
	<-started

	secondLaunched := make(chan struct{})
	go runRefresh(authB, secondLaunched)
	<-secondLaunched
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if errRefresh := <-errs; errRefresh != nil {
			t.Fatalf("expected refresh to succeed, got %v", errRefresh)
		}
		tokenData := <-results
		if tokenData == nil || tokenData.AccessToken != "new-access" || tokenData.RefreshToken != "new-refresh" {
			t.Fatalf("unexpected token data: %#v", tokenData)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected both refresh callers to share a single upstream call, got %d", got)
	}
}

func fakeJWTWithEmail(email, subject string) string {
	header := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(`{"email":"` + email + `","sub":"` + subject + `"}`))
	return header + "." + payload + ".sig"
}
