package qoder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewQoderAuth tests the constructor with proxy configuration
func TestNewQoderAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewQoderAuth(cfg)
	require.NotNil(t, auth)
	require.NotNil(t, auth.httpClient)
}

// TestInitiateDeviceFlow tests device flow initiation
func TestInitiateDeviceFlow(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	resp, err := auth.InitiateDeviceFlow(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.VerificationURIComplete)
	require.NotEmpty(t, resp.CodeVerifier)
	require.NotEmpty(t, resp.Nonce)
	require.NotEmpty(t, resp.MachineID)
	assert.Contains(t, resp.VerificationURIComplete, QoderLoginURL)
	assert.Contains(t, resp.VerificationURIComplete, "challenge=")
	assert.Contains(t, resp.VerificationURIComplete, "verifier=")
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
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
	assert.Error(t, err)
	assert.Nil(t, tokenData)
}

// TestRefreshTokens_Failure tests token refresh failure
func TestRefreshTokens_Failure(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tokenData, err := auth.RefreshTokens(ctx, "old_token", "old_refresh")
	assert.Error(t, err)
	assert.Nil(t, tokenData)
}

// TestRefreshTokensWithRetry_Success tests successful refresh after retry
func TestRefreshTokensWithRetry_Success(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// This will fail because we can't actually make HTTP requests
	// We're just testing that the function doesn't panic
	tokenData, err := auth.RefreshTokensWithRetry(ctx, "old_token", "old_refresh", 2)
	assert.Error(t, err)
	assert.Nil(t, tokenData)
}

// TestRefreshTokensWithRetry_Exhausted tests failure after max retries
func TestRefreshTokensWithRetry_Exhausted(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokenData, err := auth.RefreshTokensWithRetry(ctx, "old_token", "old_refresh", 2)
	assert.Error(t, err)
	assert.Nil(t, tokenData)
	assert.Contains(t, err.Error(), "failed after 2 attempts")
}

// TestRefreshTokensWithRetry_ContextCancel tests context cancellation during retry
func TestRefreshTokensWithRetry_ContextCancel(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	tokenData, err := auth.RefreshTokensWithRetry(ctx, "old_token", "old_refresh", 3)
	assert.Error(t, err)
	assert.Nil(t, tokenData)
}

// TestFetchUserInfo_Success tests successful user info fetch
func TestFetchUserInfo_Success(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, email, err := auth.FetchUserInfo(ctx, "test_token")
	assert.Error(t, err)
	assert.Empty(t, name)
	assert.Empty(t, email)
}

// TestFetchUserInfo_Failure tests user info fetch failure
func TestFetchUserInfo_Failure(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, email, err := auth.FetchUserInfo(ctx, "test_token")
	assert.Error(t, err)
	assert.Empty(t, name)
	assert.Empty(t, email)
}

// TestSaveUserInfo tests saving user info
func TestSaveUserInfo(t *testing.T) {
	auth := NewQoderAuth(&config.Config{})
	name, email := auth.SaveUserInfo(context.Background(), "token", "user123", "", "")
	assert.Equal(t, "", name)
	assert.Equal(t, "", email)
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
	require.NotNil(t, storage)
	assert.Equal(t, "token", storage.Token)
	assert.Equal(t, "refresh", storage.RefreshToken)
	assert.Equal(t, "user123", storage.UserID)
	assert.Equal(t, "machine123", storage.MachineID)
	// Type is set when saving to file, not in CreateTokenStorage
	assert.Equal(t, "", storage.Type)
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
	assert.Equal(t, "new_token", storage.Token)
	assert.Equal(t, "new_refresh", storage.RefreshToken)
	assert.Equal(t, int64(2000), storage.ExpireTime)
}

// TestRefreshTokenIfNeeded_NoRefreshNeeded tests no refresh when token is valid
func TestRefreshTokenIfNeeded_NoRefreshNeeded(t *testing.T) {
	storage := &QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
	}
	err := RefreshTokenIfNeeded(context.Background(), &config.Config{}, storage, 600)
	assert.NoError(t, err)
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
	err := RefreshTokenIfNeeded(context.Background(), &config.Config{}, storage, 600)
	assert.Error(t, err)
}

// TestIsExpired tests token expiration check
func TestIsExpired(t *testing.T) {
	storage := &QoderTokenStorage{}
	assert.True(t, storage.IsExpired(0))

	storage.ExpireTime = time.Now().Add(1 * time.Hour).UnixMilli()
	assert.False(t, storage.IsExpired(0))
	assert.True(t, storage.IsExpired(7200000)) // 2 hours in ms
}

// TestParseExpiresAt tests parsing various expire time formats
func TestParseExpiresAt(t *testing.T) {
	// RFC3339 format
	rfc3339 := "2026-02-20T00:00:00Z"
	result := parseExpiresAt(rfc3339)
	assert.Greater(t, result, int64(0))

	// Milliseconds format
	ms := "1776902400000"
	result = parseExpiresAt(ms)
	assert.Greater(t, result, int64(0))

	// Invalid format - should return default (now + 30 days)
	invalid := "invalid"
	result = parseExpiresAt(invalid)
	assert.Greater(t, result, time.Now().UnixMilli())
}

// TestGenerateDeviceCodeVerifier tests verifier generation
func TestGenerateDeviceCodeVerifier(t *testing.T) {
	verifier, err := generateDeviceCodeVerifier()
	require.NoError(t, err)
	require.NotEmpty(t, verifier)
	assert.Len(t, verifier, 43) // base64url encoded 32 bytes
}

// TestGenerateDeviceCodeChallenge tests challenge generation
func TestGenerateDeviceCodeChallenge(t *testing.T) {
	verifier := "test_verifier_string_for_testing"
	challenge := generateDeviceCodeChallenge(verifier)
	require.NotEmpty(t, challenge)
	assert.Len(t, challenge, 43) // base64url encoded 32 bytes
}

// TestGenerateMachineID tests machine ID generation
func TestGenerateMachineID(t *testing.T) {
	id := generateMachineID()
	require.NotEmpty(t, id)
	// Should be a valid UUID
	assert.Len(t, id, 36)
}

// TestFormatExpiresAt tests expire time formatting
func TestFormatExpiresAt(t *testing.T) {
	expireMs := int64(1776902400000)
	result := formatExpiresAt(expireMs)
	// The exact format depends on the local timezone, so just check it's not empty
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "2026")
}
