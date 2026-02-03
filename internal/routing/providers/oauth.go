package providers

import (
	"context"
	"errors"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// OAuthProvider wraps OAuth-based auths as routing.Provider.
type OAuthProvider struct {
	name      string
	auths     []*coreauth.Auth
	mu        sync.RWMutex
	executor  coreauth.ProviderExecutor
}

// NewOAuthProvider creates a new OAuth provider.
func NewOAuthProvider(name string, exec coreauth.ProviderExecutor) *OAuthProvider {
	return &OAuthProvider{
		name:     name,
		auths:    make([]*coreauth.Auth, 0),
		executor: exec,
	}
}

// Name returns the provider name.
func (p *OAuthProvider) Name() string {
	return p.name
}

// Type returns ProviderTypeOAuth.
func (p *OAuthProvider) Type() routing.ProviderType {
	return routing.ProviderTypeOAuth
}

// SupportsModel checks if any auth supports the model.
func (p *OAuthProvider) SupportsModel(model string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// OAuth providers typically support models via oauth-model-alias
	// The actual model support is determined at execution time
	return true
}

// Available checks if there's an available auth for the model.
func (p *OAuthProvider) Available(model string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, auth := range p.auths {
		if p.isAuthAvailable(auth, model) {
			return true
		}
	}
	return false
}

// Priority returns the priority (OAuth is preferred over API key).
func (p *OAuthProvider) Priority() int {
	return 10
}

// Execute sends the request using an available OAuth auth.
func (p *OAuthProvider) Execute(ctx context.Context, model string, req executor.Request) (executor.Response, error) {
	auth := p.selectAuth(model)
	if auth == nil {
		return executor.Response{}, ErrNoAvailableAuth
	}

	return p.executor.Execute(ctx, auth, req, executor.Options{})
}

// ExecuteStream sends a streaming request.
func (p *OAuthProvider) ExecuteStream(ctx context.Context, model string, req executor.Request) (<-chan executor.StreamChunk, error) {
	auth := p.selectAuth(model)
	if auth == nil {
		return nil, ErrNoAvailableAuth
	}

	return p.executor.ExecuteStream(ctx, auth, req, executor.Options{})
}

// AddAuth adds an auth to this provider.
func (p *OAuthProvider) AddAuth(auth *coreauth.Auth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.auths = append(p.auths, auth)
}

// RemoveAuth removes an auth from this provider.
func (p *OAuthProvider) RemoveAuth(authID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	filtered := make([]*coreauth.Auth, 0, len(p.auths))
	for _, auth := range p.auths {
		if auth.ID != authID {
			filtered = append(filtered, auth)
		}
	}
	p.auths = filtered
}

// isAuthAvailable checks if an auth is available for the model.
func (p *OAuthProvider) isAuthAvailable(auth *coreauth.Auth, model string) bool {
	// TODO: integrate with model_registry for quota checking
	// For now, just check if auth exists
	return auth != nil
}

// selectAuth selects an available auth for the model.
func (p *OAuthProvider) selectAuth(model string) *coreauth.Auth {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, auth := range p.auths {
		if p.isAuthAvailable(auth, model) {
			return auth
		}
	}
	return nil
}

// Errors
var (
	ErrNoAvailableAuth = errors.New("no available OAuth auth for model")
)
