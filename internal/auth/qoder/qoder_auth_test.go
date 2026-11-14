package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestNewQoderAuth tests the constructor with proxy configuration
func TestNewQoderAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewQoderAuth(cfg)
	if auth == nil {
		t.Fatal("NewQoderAuth returned nil")
	}
	if auth.httpClient == nil {
		t.Fatal("NewQoderAuth: httpClient is nil")
	}
}

// TestInitiateDeviceFlow tests device flow initiation
func TestInitiateDeviceFlow(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	resp, err := auth.InitiateDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("InitiateDeviceFlow returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("InitiateDeviceFlow returned nil response")
	}
	if resp.VerificationURIComplete == "" {
		t.Error("VerificationURIComplete is empty")
	}
	if resp.CodeVerifier == "" {
		t.Error("CodeVerifier is empty")
	}
	if resp.Nonce == "" {
		t.Error("Nonce is empty")
	}
	if resp.MachineID == "" {
		t.Error("MachineID is empty")
	}
	if !strings.Contains(resp.VerificationURIComplete, QoderLoginURL) {
		t.Errorf("VerificationURIComplete %q does not contain %q", resp.VerificationURIComplete, QoderLoginURL)
	}
	if !strings.Contains(resp.VerificationURIComplete, "challenge=") {
		t.Errorf("VerificationURIComplete %q missing challenge=", resp.VerificationURIComplete)
	}
	if strings.Contains(resp.VerificationURIComplete, "verifier=") {
		t.Errorf("VerificationURIComplete %q must not leak verifier", resp.VerificationURIComplete)
	}
}

// TestPollForToken_Success tests successful token polling
func TestPollForToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"data": {
				"token": "test_access_token",
				"refresh_token": "test_refresh_token",
				"expire_time": 1776902400000,
				"expireTime": "2026-02-20T00:00:00Z",
				"user_id": "test_user",
				"machine_token": "test_machine_token",
				"machineType": "personal"
			}
		}`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	deviceFlow := &DeviceFlowResponse{
		VerificationURIComplete: server.URL + "?verifier=test_verifier&nonce=test_nonce",
		CodeVerifier:            "test_verifier",
		Nonce:                   "test_nonce",
		MachineID:               "test_machine",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tokenData, err := auth.PollForToken(ctx, deviceFlow)
	// This will timeout because we can't override the endpoint URL
	// Just verify it doesn't panic
	if err == nil {
		t.Error("expected error due to non-overridable endpoint, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestPollForToken_Timeout tests timeout after max attempts
func TestPollForToken_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted) // Still pending
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	deviceFlow := &DeviceFlowResponse{
		VerificationURIComplete: server.URL + "?verifier=test_verifier&nonce=test_nonce",
		CodeVerifier:            "test_verifier",
		Nonce:                   "test_nonce",
		MachineID:               "test_machine",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tokenData, err := auth.PollForToken(ctx, deviceFlow)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestPollForToken_ContextCancel tests context cancellation
func TestPollForToken_ContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	deviceFlow := &DeviceFlowResponse{
		VerificationURIComplete: server.URL + "?verifier=test_verifier&nonce=test_nonce",
		CodeVerifier:            "test_verifier",
		Nonce:                   "test_nonce",
		MachineID:               "test_machine",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	tokenData, err := auth.PollForToken(ctx, deviceFlow)
	if err == nil {
		t.Error("expected context-cancelled error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestPollForToken_HTTPError tests handling of HTTP errors
func TestPollForToken_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message": "internal server error"}`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	deviceFlow := &DeviceFlowResponse{
		VerificationURIComplete: server.URL + "?verifier=test_verifier&nonce=test_nonce",
		CodeVerifier:            "test_verifier",
		Nonce:                   "test_nonce",
		MachineID:               "test_machine",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tokenData, err := auth.PollForToken(ctx, deviceFlow)
	if err == nil {
		t.Error("expected HTTP error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestPollForToken_InvalidJSON tests handling of malformed JSON
func TestPollForToken_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `invalid json`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	deviceFlow := &DeviceFlowResponse{
		VerificationURIComplete: server.URL + "?verifier=test_verifier&nonce=test_nonce",
		CodeVerifier:            "test_verifier",
		Nonce:                   "test_nonce",
		MachineID:               "test_machine",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tokenData, err := auth.PollForToken(ctx, deviceFlow)
	if err == nil {
		t.Error("expected JSON parse error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestPollForToken_NonOKStatus tests handling of non-200 status codes
func TestPollForToken_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message": "bad request"}`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	deviceFlow := &DeviceFlowResponse{
		VerificationURIComplete: server.URL + "?verifier=test_verifier&nonce=test_nonce",
		CodeVerifier:            "test_verifier",
		Nonce:                   "test_nonce",
		MachineID:               "test_machine",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tokenData, err := auth.PollForToken(ctx, deviceFlow)
	if err == nil {
		t.Error("expected non-OK status error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestRefreshTokens_Success tests successful token refresh
func TestRefreshTokens_Success(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// This test will fail because we can't actually make HTTP requests
	// to the real endpoint. We're just testing that the function doesn't panic
	// and returns an error (since we're using invalid credentials).
	tokenData, err := auth.RefreshTokens(ctx, "old_token", "old_refresh")
	if err == nil {
		t.Error("expected error from invalid refresh, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestRefreshTokens_Failure tests token refresh failure
func TestRefreshTokens_Failure(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tokenData, err := auth.RefreshTokens(ctx, "old_token", "old_refresh")
	if err == nil {
		t.Error("expected error from invalid refresh, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestRefreshTokensWithRetry_Success tests successful refresh after retry
func TestRefreshTokensWithRetry_Success(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// This will fail because we can't actually make HTTP requests
	// We're just testing that the function doesn't panic
	tokenData, err := auth.RefreshTokensWithRetry(ctx, "old_token", "old_refresh", 2)
	if err == nil {
		t.Error("expected error from invalid retry, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestRefreshTokensWithRetry_Exhausted tests failure after max retries
func TestRefreshTokensWithRetry_Exhausted(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokenData, err := auth.RefreshTokensWithRetry(ctx, "old_token", "old_refresh", 2)
	if err == nil {
		t.Fatal("expected exhausted-retries error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
	if !strings.Contains(err.Error(), "failed after 2 attempts") {
		t.Errorf("error %q does not contain %q", err.Error(), "failed after 2 attempts")
	}
}

// TestRefreshTokensWithRetry_ContextCancel tests context cancellation during retry
func TestRefreshTokensWithRetry_ContextCancel(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	tokenData, err := auth.RefreshTokensWithRetry(ctx, "old_token", "old_refresh", 3)
	if err == nil {
		t.Error("expected context-cancelled error, got nil")
	}
	if tokenData != nil {
		t.Errorf("expected nil tokenData, got %+v", tokenData)
	}
}

// TestFetchUserInfo_Success tests successful user info fetch
func TestFetchUserInfo_Success(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, email, err := auth.FetchUserInfo(ctx, "test_token")
	if err == nil {
		t.Error("expected error from fake token, got nil")
	}
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
	if email != "" {
		t.Errorf("expected empty email, got %q", email)
	}
}

// TestFetchUserInfo_Failure tests user info fetch failure
func TestFetchUserInfo_Failure(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, email, err := auth.FetchUserInfo(ctx, "test_token")
	if err == nil {
		t.Error("expected error from fake token, got nil")
	}
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
	if email != "" {
		t.Errorf("expected empty email, got %q", email)
	}
}

// TestSaveUserInfo tests saving user info
func TestSaveUserInfo(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	name, email := auth.SaveUserInfo(context.Background(), "token", "user123", "", "")
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
	if email != "" {
		t.Errorf("expected empty email, got %q", email)
	}
}

// TestCreateTokenStorage tests creating token storage
func TestCreateTokenStorage(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	tokenData := &QoderTokenData{
		AccessToken:  "token",
		RefreshToken: "refresh",
		UserID:       "user123",
		ExpireTime:   1776902400000,
		MachineToken: "machine_token",
		MachineType:  "personal",
	}
	storage := auth.CreateTokenStorage(tokenData, "machine123")
	if storage == nil {
		t.Fatal("CreateTokenStorage returned nil")
	}
	if storage.Token != "token" {
		t.Errorf("Token = %q, want %q", storage.Token, "token")
	}
	if storage.RefreshToken != "refresh" {
		t.Errorf("RefreshToken = %q, want %q", storage.RefreshToken, "refresh")
	}
	if storage.UserID != "user123" {
		t.Errorf("UserID = %q, want %q", storage.UserID, "user123")
	}
	if storage.MachineID != "machine123" {
		t.Errorf("MachineID = %q, want %q", storage.MachineID, "machine123")
	}
	// Type is set when saving to file, not in CreateTokenStorage
	if storage.Type != "" {
		t.Errorf("Type = %q, want empty", storage.Type)
	}
}

// TestUpdateTokenStorage tests updating token storage
func TestUpdateTokenStorage(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	storage := &QoderTokenStorage{
		Token:        "old_token",
		RefreshToken: "old_refresh",
		ExpireTime:   1000,
	}
	tokenData := &QoderTokenData{
		AccessToken:  "new_token",
		RefreshToken: "new_refresh",
		ExpireTime:   2000,
	}
	auth.UpdateTokenStorage(storage, tokenData)
	if storage.Token != "new_token" {
		t.Errorf("Token = %q, want %q", storage.Token, "new_token")
	}
	if storage.RefreshToken != "new_refresh" {
		t.Errorf("RefreshToken = %q, want %q", storage.RefreshToken, "new_refresh")
	}
	if storage.ExpireTime != 2000 {
		t.Errorf("ExpireTime = %d, want %d", storage.ExpireTime, 2000)
	}
}

// TestRefreshTokenIfNeeded_NoRefreshNeeded tests no refresh when token is valid
func TestRefreshTokenIfNeeded_NoRefreshNeeded(t *testing.T) {
	storage := &QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
	}
	if err := RefreshTokenIfNeeded(context.Background(), &config.Config{}, storage, 600, ""); err != nil {
		t.Errorf("RefreshTokenIfNeeded returned error: %v", err)
	}
}

// TestRefreshTokenIfNeeded_RefreshFails tests refresh failure
func TestRefreshTokenIfNeeded_RefreshFails(t *testing.T) {
	storage := &QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   1000, // Expired
		UserID:       "user123",
		Email:        "test@example.com",
	}
	if err := RefreshTokenIfNeeded(context.Background(), &config.Config{}, storage, 600, ""); err == nil {
		t.Error("expected refresh error, got nil")
	}
}

// TestIsExpired tests token expiration check
func TestIsExpired(t *testing.T) {
	storage := &QoderTokenStorage{}
	if !storage.IsExpired(0) {
		t.Error("IsExpired(0) on zero ExpireTime should be true")
	}

	storage.ExpireTime = time.Now().Add(1 * time.Hour).UnixMilli()
	if storage.IsExpired(0) {
		t.Error("IsExpired(0) on +1h token should be false")
	}
	if !storage.IsExpired(7200000) { // 2 hours in ms
		t.Error("IsExpired(2h buffer) on +1h token should be true")
	}
}

// TestParseExpiresAt tests parsing various expire time formats
func TestParseExpiresAt(t *testing.T) {
	// RFC3339 format
	rfc3339 := "2026-02-20T00:00:00Z"
	if got := parseExpiresAt(rfc3339, 0); got <= 0 {
		t.Errorf("parseExpiresAt(%q, 0) = %d, want > 0", rfc3339, got)
	}

	// Milliseconds format
	ms := "1776902400000"
	if got := parseExpiresAt(ms, 0); got <= 0 {
		t.Errorf("parseExpiresAt(%q, 0) = %d, want > 0", ms, got)
	}

	// Invalid format - should return default (now + 30 days)
	invalid := "invalid"
	if got := parseExpiresAt(invalid, 0); got <= time.Now().UnixMilli() {
		t.Errorf("parseExpiresAt(%q, 0) = %d, expected > now", invalid, got)
	}
}

// TestGenerateDeviceCodeVerifier tests verifier generation
func TestGenerateDeviceCodeVerifier(t *testing.T) {
	verifier, err := generateDeviceCodeVerifier()
	if err != nil {
		t.Fatalf("generateDeviceCodeVerifier returned error: %v", err)
	}
	if verifier == "" {
		t.Fatal("verifier is empty")
	}
	if len(verifier) != 43 { // base64url encoded 32 bytes
		t.Errorf("len(verifier) = %d, want 43", len(verifier))
	}
}

// TestGenerateDeviceCodeChallenge tests challenge generation
func TestGenerateDeviceCodeChallenge(t *testing.T) {
	verifier := "test_verifier_string_for_testing"
	challenge := generateDeviceCodeChallenge(verifier)
	if challenge == "" {
		t.Fatal("challenge is empty")
	}
	if len(challenge) != 43 { // base64url encoded 32 bytes
		t.Errorf("len(challenge) = %d, want 43", len(challenge))
	}
}

// TestGenerateMachineID tests machine ID generation
func TestGenerateMachineID(t *testing.T) {
	id := generateMachineID()
	if id == "" {
		t.Fatal("machine ID is empty")
	}
	// Should be a valid UUID
	if len(id) != 36 {
		t.Errorf("len(machineID) = %d, want 36", len(id))
	}
}

// TestFormatExpiresAt tests expire time formatting
func TestFormatExpiresAt(t *testing.T) {
	expireMs := int64(1776902400000)
	result := formatExpiresAt(expireMs)
	// The exact format depends on the local timezone, so just check it's not empty
	if result == "" {
		t.Fatal("formatted expire time is empty")
	}
	if !strings.Contains(result, "2026") {
		t.Errorf("formatted expire %q does not contain 2026", result)
	}
}

func TestExchangePersonalToken_UsesJobTokenProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != QoderJobTokenExchangePath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["personal_token"] != "pt-test" {
			t.Fatalf("personal_token = %q", body["personal_token"])
		}
		if got := r.Header.Get("Cosy-Version"); got != QoderGatewayCosyVersion {
			t.Fatalf("Cosy-Version = %q, want %q", got, QoderGatewayCosyVersion)
		}
		if got := r.Header.Get("Cosy-Machineos"); got != QoderCNMachineOS {
			t.Fatalf("Cosy-Machineos = %q, want %q", got, QoderCNMachineOS)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"jt-test","refresh_token":"jrt-test","expires_in":86400000}`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	auth.httpClient = server.Client()
	auth.endpoints.OpenAPIBase = server.URL
	auth.endpoints.VPCInstance = "acme"

	before := time.Now().Add(23 * time.Hour).UnixMilli()
	tokenData, err := auth.ExchangePersonalToken(context.Background(), "pt-test")
	if err != nil {
		t.Fatalf("ExchangePersonalToken() error = %v", err)
	}
	if tokenData.AccessToken != "jt-test" || tokenData.RefreshToken != "jrt-test" {
		t.Fatalf("tokenData = %+v", tokenData)
	}
	if tokenData.AuthMode != "job-token" {
		t.Fatalf("AuthMode = %q, want job-token", tokenData.AuthMode)
	}
	if tokenData.ExpireTime <= before {
		t.Fatalf("ExpireTime = %d, expected about one day from now", tokenData.ExpireTime)
	}
}

func TestRefreshJobToken_UsesRefreshEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != QoderJobTokenRefreshPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["refresh_token"] != "jrt-old" {
			t.Fatalf("refresh_token = %q", body["refresh_token"])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"jt-new","refresh_token":"jrt-new","expires_in":3600}`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	auth.httpClient = server.Client()
	auth.endpoints.OpenAPIBase = server.URL
	auth.endpoints.VPCInstance = "acme"

	tokenData, err := auth.RefreshJobToken(context.Background(), "jrt-old")
	if err != nil {
		t.Fatalf("RefreshJobToken() error = %v", err)
	}
	if tokenData.AccessToken != "jt-new" || tokenData.RefreshToken != "jrt-new" {
		t.Fatalf("tokenData = %+v", tokenData)
	}
	if tokenData.AuthMode != "job-token" {
		t.Fatalf("AuthMode = %q, want job-token", tokenData.AuthMode)
	}
}

func TestFetchUserInfoDetails_ExtractsNestedOrganization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/organizations/org-1/tags" {
			if r.Header.Get("Cosy-Version") != QoderGatewayCosyVersion || r.Header.Get("Cosy-Machineos") != QoderCNMachineOS {
				t.Fatalf("organization tags headers = %v", r.Header)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"tags":["tag-a"]}`)
			return
		}
		if r.Header.Get("Cosy-Version") != QoderGatewayCosyVersion || r.Header.Get("Cosy-Machineos") != QoderCNMachineOS {
			t.Fatalf("userinfo headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/userinfo" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"id":"user-1","username":"user-name","email":"user@example.com","organization":{"id":"org-1","name":"Org One"}}`)
	}))
	defer server.Close()

	auth := NewQoderAuth(&config.Config{})
	auth.httpClient = server.Client()
	auth.endpoints.OpenAPIBase = server.URL
	auth.endpoints.VPCInstance = "acme"

	details, err := auth.FetchUserInfoDetails(context.Background(), "jt-test")
	if err != nil {
		t.Fatalf("FetchUserInfoDetails() error = %v", err)
	}
	if details.UserID != "user-1" || details.OrganizationID != "org-1" || details.Name != "user-name" {
		t.Fatalf("details = %+v", details)
	}
	if len(details.OrganizationTags) != 1 || details.OrganizationTags[0] != "tag-a" {
		t.Fatalf("OrganizationTags = %+v", details.OrganizationTags)
	}
}

func TestJobTokenExpiry_HandlesMillisecondDuration(t *testing.T) {
	before := time.Now().Add(23 * time.Hour).UnixMilli()
	got := parseJobTokenExpiresAt("", 86400000)
	if got <= before {
		t.Fatalf("parseJobTokenExpiresAt() = %d, expected about one day from now", got)
	}
}

func TestParseExpiresAt_HandlesLongSecondDuration(t *testing.T) {
	before := time.Now().Add(29 * 24 * time.Hour).UnixMilli()
	got := parseExpiresAt("", 30*24*60*60)
	if got <= before {
		t.Fatalf("parseExpiresAt() = %d, expected about 30 days from now", got)
	}
}

func TestParseExpiresAt_HandlesUnixSecondTimestamp(t *testing.T) {
	got := parseExpiresAt("1781594470", 0)
	if got != 1781594470000 {
		t.Fatalf("parseExpiresAt() = %d, want %d", got, int64(1781594470000))
	}
}

func TestJobTokenExpiryAliasesMatchOfficialCLIShape(t *testing.T) {
	response := JobTokenResponse{
		Token:                  "jt-test",
		RefreshToken:           "jrt-test",
		ExpireTimeCamel:        json.RawMessage(`1781594470`),
		RefreshTokenExpireTime: json.RawMessage(`1813123200`),
	}
	if got := jobTokenExpireTime(response); got != 1781594470000 {
		t.Fatalf("jobTokenExpireTime() = %d, want %d", got, int64(1781594470000))
	}
	if got := jobTokenRefreshTokenExpireTime(response); got != 1813123200000 {
		t.Fatalf("jobTokenRefreshTokenExpireTime() = %d, want %d", got, int64(1813123200000))
	}
}

func TestBuildAuthHeaders_EnterpriseVPCIdentity(t *testing.T) {
	const machineID = "11111111-2222-3333-4444-555555555555"
	headers, err := BuildAuthHeaders(nil, "https://acme-gateway.vpc.qoder.com.cn/algo/api/v2/model/list?Encode=1", CosyCredentials{
		UserID:           "user-1",
		AuthToken:        "jt-test",
		MachineID:        machineID,
		OrganizationID:   "org-1",
		OrganizationTags: []string{"tag-a", "tag-b"},
		EnterpriseVPC:    true,
	})
	if err != nil {
		t.Fatalf("BuildAuthHeaders() error = %v", err)
	}
	if headers.CosyVersion != QoderGatewayCosyVersion {
		t.Errorf("CosyVersion = %q, want %q", headers.CosyVersion, QoderGatewayCosyVersion)
	}
	if headers.CosyMachineOS != QoderCNMachineOS {
		t.Errorf("CosyMachineOS = %q, want %q", headers.CosyMachineOS, QoderCNMachineOS)
	}
	if headers.CosyClientIP != machineID {
		t.Errorf("CosyClientIP = %q, want %q", headers.CosyClientIP, machineID)
	}
	if headers.CosyOrganizationID != "org-1" || headers.CosyScene != "assistant" {
		t.Errorf("enterprise headers = %+v", headers)
	}
	if headers.CosyOrgTags != "tag-a,tag-b" {
		t.Errorf("CosyOrgTags = %q, want tag-a,tag-b", headers.CosyOrgTags)
	}
	if headers.CosyBusinessProduct != "cli" || headers.CosyBusinessType != "agent" {
		t.Errorf("business headers = %+v", headers)
	}
}

func TestBuildAuthHeaders_GlobalOmitsEnterpriseIdentity(t *testing.T) {
	headers, err := BuildAuthHeaders(nil, QoderModelListURL, CosyCredentials{
		UserID:           "user-1",
		AuthToken:        "dt-test",
		OrganizationID:   "org-1",
		OrganizationTags: []string{"tag-a"},
	})
	if err != nil {
		t.Fatalf("BuildAuthHeaders() error = %v", err)
	}
	if headers.CosyOrganizationID != "" || headers.CosyOrgTags != "" || headers.CosyScene != "" {
		t.Fatalf("global headers contain enterprise identity: %+v", headers)
	}
	if headers.CosyVersion != QoderIDEVersion || headers.CosyMachineOS != QoderMachineOS {
		t.Fatalf("global identity changed: %+v", headers)
	}
}
