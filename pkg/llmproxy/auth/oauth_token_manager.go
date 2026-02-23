// Package auth — OAuth token manager for automatic token refresh.
package auth

import (
	"fmt"
	"sync"
	"time"
)

// OAuthTokenManager stores and auto-refreshes OAuth tokens per provider.
type OAuthTokenManager struct {
	store    map[string]*Token
	mu       sync.RWMutex
	provider OAuthProvider
}

// NewOAuthTokenManager returns a new OAuthTokenManager.
func NewOAuthTokenManager(provider OAuthProvider) *OAuthTokenManager {
	return &OAuthTokenManager{
		store:    make(map[string]*Token),
		provider: provider,
	}
}

// StoreToken stores a token for the given provider.
func (m *OAuthTokenManager) StoreToken(provider string, token *Token) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[provider] = token
}

// GetToken retrieves a token for the given provider, auto-refreshing if expired.
func (m *OAuthTokenManager) GetToken(provider string) (*Token, error) {
	m.mu.RLock()
	token, exists := m.store[provider]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("token not found for provider: %s", provider)
	}

	if time.Now().After(token.ExpiresAt) {
		if m.provider == nil {
			return nil, fmt.Errorf("token expired and no provider available to refresh")
		}

		newAccessToken, err := m.provider.RefreshToken(token.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("token refresh failed: %w", err)
		}

		m.mu.Lock()
		token.AccessToken = newAccessToken
		token.ExpiresAt = time.Now().Add(time.Hour)
		m.store[provider] = token
		m.mu.Unlock()
	}

	return token, nil
}
