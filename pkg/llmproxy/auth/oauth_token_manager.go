// Package auth provides authentication helpers for CLIProxy.
// oauth_token_manager.go manages OAuth token lifecycle (store/retrieve/auto-refresh).
//
// Ported from thegent OAuth lifecycle management.
package auth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// tokenRefreshLeadTime refreshes a token this long before its recorded expiry.
const tokenRefreshLeadTime = 30 * time.Second

// OAuthTokenManager stores OAuth tokens per provider and automatically refreshes
// expired tokens via the configured OAuthProvider.
//
// Thread-safe: uses RWMutex for concurrent reads and exclusive writes.
type OAuthTokenManager struct {
	store    map[string]*Token
	mu       sync.RWMutex
	provider OAuthProvider
}

// NewOAuthTokenManager returns a new OAuthTokenManager.
// provider may be nil when auto-refresh is not required.
func NewOAuthTokenManager(provider OAuthProvider) *OAuthTokenManager {
	return &OAuthTokenManager{
		store:    make(map[string]*Token),
		provider: provider,
	}
}

// StoreToken stores a token for the given provider key, replacing any existing token.
func (m *OAuthTokenManager) StoreToken(_ context.Context, providerKey string, token *Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[providerKey] = token
	return nil
}

// GetToken retrieves the token for the given provider key.
// If the token is expired and a provider is configured, it is refreshed automatically
// before being returned. The refreshed token is persisted in the store.
func (m *OAuthTokenManager) GetToken(ctx context.Context, providerKey string) (*Token, error) {
	m.mu.RLock()
	token, exists := m.store[providerKey]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("token not found for provider: %s", providerKey)
	}

	// Check expiry with lead time to pre-emptively refresh before clock edge.
	if time.Now().Add(tokenRefreshLeadTime).After(token.ExpiresAt) {
		if m.provider == nil {
			return nil, fmt.Errorf("token expired for provider %s and no OAuthProvider configured for refresh", providerKey)
		}

		newAccessToken, err := m.provider.RefreshToken(ctx, token.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("token refresh failed for provider %s: %w", providerKey, err)
		}

		refreshed := &Token{
			AccessToken:  newAccessToken,
			RefreshToken: token.RefreshToken,
			ExpiresAt:    time.Now().Add(time.Hour),
		}

		m.mu.Lock()
		m.store[providerKey] = refreshed
		m.mu.Unlock()

		return refreshed, nil
	}

	return token, nil
}
