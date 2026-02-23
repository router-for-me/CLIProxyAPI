package auth

import (
<<<<<<< HEAD
	"testing"
	"time"
)

type mockOAuthProvider struct {
	refreshFn func(refreshToken string) (string, error)
}

func (m *mockOAuthProvider) RefreshToken(refreshToken string) (string, error) {
	return m.refreshFn(refreshToken)
}

=======
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockOAuthProvider is a test double for OAuthProvider.
type MockOAuthProvider struct {
	RefreshTokenFn func(ctx context.Context, refreshToken string) (string, error)
}

func (m *MockOAuthProvider) RefreshToken(ctx context.Context, refreshToken string) (string, error) {
	return m.RefreshTokenFn(ctx, refreshToken)
}

// TestOAuthTokenManagerStoresAndRetrievesToken verifies basic store/retrieve round-trip.
// @trace FR-AUTH-001
>>>>>>> ci-compile-fix
func TestOAuthTokenManagerStoresAndRetrievesToken(t *testing.T) {
	mgr := NewOAuthTokenManager(nil)

	token := &Token{
		AccessToken:  "access_token",
		RefreshToken: "refresh_token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

<<<<<<< HEAD
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
=======
	err := mgr.StoreToken(context.Background(), "provider", token)
	require.NoError(t, err)

	retrieved, err := mgr.GetToken(context.Background(), "provider")
	require.NoError(t, err)
	assert.Equal(t, token.AccessToken, retrieved.AccessToken)
}

// TestOAuthTokenManagerRefreshesExpiredToken verifies that an expired token triggers
// auto-refresh via the configured OAuthProvider.
// @trace FR-AUTH-001 FR-AUTH-002
func TestOAuthTokenManagerRefreshesExpiredToken(t *testing.T) {
	mockProvider := &MockOAuthProvider{
		RefreshTokenFn: func(_ context.Context, _ string) (string, error) {
>>>>>>> ci-compile-fix
			return "new_access_token_xyz", nil
		},
	}

<<<<<<< HEAD
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
=======
	mgr := NewOAuthTokenManager(mockProvider)

	err := mgr.StoreToken(context.Background(), "provider", &Token{
		AccessToken:  "old_token",
		RefreshToken: "refresh_token",
		ExpiresAt:    time.Now().Add(-time.Hour), // Already expired.
	})
	require.NoError(t, err)

	token, err := mgr.GetToken(context.Background(), "provider")
	require.NoError(t, err)
	assert.Equal(t, "new_access_token_xyz", token.AccessToken)
}

// TestOAuthTokenManagerReturnsErrorForMissingProvider verifies error on unknown provider key.
// @trace FR-AUTH-001
func TestOAuthTokenManagerReturnsErrorForMissingProvider(t *testing.T) {
	mgr := NewOAuthTokenManager(nil)

	_, err := mgr.GetToken(context.Background(), "nonexistent")
	assert.ErrorContains(t, err, "token not found")
}

// TestOAuthTokenManagerErrorsWhenExpiredWithNoProvider verifies that GetToken fails
// loudly when a token is expired and no provider is configured to refresh it.
// @trace FR-AUTH-002
func TestOAuthTokenManagerErrorsWhenExpiredWithNoProvider(t *testing.T) {
	mgr := NewOAuthTokenManager(nil) // No provider.

	err := mgr.StoreToken(context.Background(), "provider", &Token{
		AccessToken:  "old_token",
		RefreshToken: "refresh_token",
		ExpiresAt:    time.Now().Add(-time.Hour), // Expired.
	})
	require.NoError(t, err)

	_, err = mgr.GetToken(context.Background(), "provider")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "no OAuthProvider configured")
>>>>>>> ci-compile-fix
}
