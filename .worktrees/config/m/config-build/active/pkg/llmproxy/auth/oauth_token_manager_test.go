package auth

import (
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
func TestOAuthTokenManagerStoresAndRetrievesToken(t *testing.T) {
	mgr := NewOAuthTokenManager(nil)

	token := &Token{
		AccessToken:  "access_token",
		RefreshToken: "refresh_token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

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
			return "new_access_token_xyz", nil
		},
	}

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
}
