/**
 * @file Unit tests for Cline authentication
 * @description Tests the core authentication functionality including token refresh,
 * storage creation, and error handling. These tests verify the contract with Cline API
 * and ensure proper token lifecycle management.
 */

package cline

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewClineAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewClineAuth(cfg)

	if auth == nil {
		t.Fatal("NewClineAuth returned nil")
	}

	if auth.httpClient == nil {
		t.Error("httpClient should not be nil")
	}

	if auth.apiBaseURL != ClineAPIBaseURL {
		t.Errorf("Expected apiBaseURL to be %s, got %s", ClineAPIBaseURL, auth.apiBaseURL)
	}
}

func TestRefreshTokensValidation(t *testing.T) {
	cfg := &config.Config{}
	auth := NewClineAuth(cfg)
	ctx := context.Background()

	// Test with empty refresh token
	_, err := auth.RefreshTokens(ctx, "")
	if err == nil {
		t.Error("Expected error for empty refresh token")
	}
}

func TestCreateTokenStorage(t *testing.T) {
	cfg := &config.Config{}
	auth := NewClineAuth(cfg)

	tokenData := &ClineTokenData{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		Email:        "test@example.com",
		Expire:       time.Now().Add(10 * time.Minute).Format(time.RFC3339),
	}

	storage := auth.CreateTokenStorage(tokenData)

	if storage == nil {
		t.Fatal("CreateTokenStorage returned nil")
	}

	if storage.AccessToken != tokenData.AccessToken {
		t.Errorf("Expected AccessToken %s, got %s", tokenData.AccessToken, storage.AccessToken)
	}

	if storage.RefreshToken != tokenData.RefreshToken {
		t.Errorf("Expected RefreshToken %s, got %s", tokenData.RefreshToken, storage.RefreshToken)
	}

	if storage.Email != tokenData.Email {
		t.Errorf("Expected Email %s, got %s", tokenData.Email, storage.Email)
	}

	if storage.Expire != tokenData.Expire {
		t.Errorf("Expected Expire %s, got %s", tokenData.Expire, storage.Expire)
	}

	if storage.LastRefresh == "" {
		t.Error("LastRefresh should not be empty")
	}
}

func TestUpdateTokenStorage(t *testing.T) {
	cfg := &config.Config{}
	auth := NewClineAuth(cfg)

	storage := &ClineTokenStorage{
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		Email:        "old@example.com",
		Expire:       time.Now().Format(time.RFC3339),
		LastRefresh:  time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}

	newTokenData := &ClineTokenData{
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		Email:        "new@example.com",
		Expire:       time.Now().Add(10 * time.Minute).Format(time.RFC3339),
	}

	auth.UpdateTokenStorage(storage, newTokenData)

	if storage.AccessToken != newTokenData.AccessToken {
		t.Errorf("Expected AccessToken %s, got %s", newTokenData.AccessToken, storage.AccessToken)
	}

	if storage.RefreshToken != newTokenData.RefreshToken {
		t.Errorf("Expected RefreshToken %s, got %s", newTokenData.RefreshToken, storage.RefreshToken)
	}

	if storage.Email != newTokenData.Email {
		t.Errorf("Expected Email %s, got %s", newTokenData.Email, storage.Email)
	}

	if storage.Expire != newTokenData.Expire {
		t.Errorf("Expected Expire %s, got %s", newTokenData.Expire, storage.Expire)
	}
}
