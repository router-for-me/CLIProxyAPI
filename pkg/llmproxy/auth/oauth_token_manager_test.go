package auth

import (
	"testing"
	"time"
)

type mockOAuthProvider struct {
	refreshFn func(refreshToken string) (string, error)
}

func (m *mockOAuthProvider) RefreshToken(refreshToken string) (string, error) {
	return m.refreshFn(refreshToken)
}

func TestOAuthTokenManagerStoresAndRetrievesToken(t *testing.T) {
	mgr := NewOAuthTokenManager(nil)

	token := &Token{
		AccessToken:  "access_token",
		RefreshToken: "refresh_token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	mgr.StoreToken("provider", token)

	retrieved, err := mgr.GetToken("provider")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retrieved.AccessToken != token.AccessToken {
		t.Errorf("expected %s, got %s", token.AccessToken, retrieved.AccessToken)
	}
}

func TestOAuthTokenManagerRefreshesExpiredToken(t *testing.T) {
	mock := &mockOAuthProvider{
		refreshFn: func(refreshToken string) (string, error) {
			return "new_access_token_xyz", nil
		},
	}

	mgr := NewOAuthTokenManager(mock)

	mgr.StoreToken("provider", &Token{
		AccessToken:  "old_token",
		RefreshToken: "refresh_token",
		ExpiresAt:    time.Now().Add(-time.Hour), // expired
	})

	token, err := mgr.GetToken("provider")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token.AccessToken != "new_access_token_xyz" {
		t.Errorf("expected new_access_token_xyz, got %s", token.AccessToken)
	}
}

func TestOAuthTokenManagerReturnsErrorForUnknownProvider(t *testing.T) {
	mgr := NewOAuthTokenManager(nil)

	_, err := mgr.GetToken("unknown")
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestOAuthTokenManagerReturnsErrorWhenExpiredNoProvider(t *testing.T) {
	mgr := NewOAuthTokenManager(nil)

	mgr.StoreToken("provider", &Token{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	})

	_, err := mgr.GetToken("provider")
	if err == nil {
		t.Error("expected error when token expired and no provider")
	}
}
