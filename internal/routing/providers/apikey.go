package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// APIKeyProvider wraps API key configs as routing.Provider.
type APIKeyProvider struct {
	name     string
	provider string // claude, gemini, codex, vertex
	keys     []APIKeyEntry
	mu       sync.RWMutex
	client   HTTPClient
}

// APIKeyEntry represents a single API key configuration.
type APIKeyEntry struct {
	APIKey  string
	BaseURL string
	Models  []config.ClaudeModel // Using ClaudeModel as generic model alias
}

// HTTPClient interface for making HTTP requests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewAPIKeyProvider creates a new API key provider.
func NewAPIKeyProvider(name, provider string, client HTTPClient) *APIKeyProvider {
	return &APIKeyProvider{
		name:     name,
		provider: provider,
		keys:     make([]APIKeyEntry, 0),
		client:   client,
	}
}

// Name returns the provider name.
func (p *APIKeyProvider) Name() string {
	return p.name
}

// Type returns ProviderTypeAPIKey.
func (p *APIKeyProvider) Type() routing.ProviderType {
	return routing.ProviderTypeAPIKey
}

// SupportsModel checks if the model is supported by this provider.
func (p *APIKeyProvider) SupportsModel(model string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, key := range p.keys {
		for _, m := range key.Models {
			if strings.EqualFold(m.Alias, model) || strings.EqualFold(m.Name, model) {
				return true
			}
		}
	}
	return false
}

// Available always returns true for API keys (unless explicitly disabled).
func (p *APIKeyProvider) Available(model string) bool {
	return p.SupportsModel(model)
}

// Priority returns the priority (API key is lower priority than OAuth).
func (p *APIKeyProvider) Priority() int {
	return 20
}

// Execute sends the request using the API key.
func (p *APIKeyProvider) Execute(ctx context.Context, model string, req executor.Request) (executor.Response, error) {
	key := p.selectKey(model)
	if key == nil {
		return executor.Response{}, ErrNoMatchingAPIKey
	}

	// Resolve the actual model name from alias
	actualModel := p.resolveModel(key, model)

	// Execute via HTTP client
	return p.executeHTTP(ctx, key, actualModel, req)
}

// ExecuteStream sends a streaming request.
func (p *APIKeyProvider) ExecuteStream(ctx context.Context, model string, req executor.Request) (
	<-chan executor.StreamChunk, error) {
	key := p.selectKey(model)
	if key == nil {
		return nil, ErrNoMatchingAPIKey
	}

	actualModel := p.resolveModel(key, model)
	return p.executeHTTPStream(ctx, key, actualModel, req)
}

// AddKey adds an API key entry.
func (p *APIKeyProvider) AddKey(entry APIKeyEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, entry)
}

// selectKey selects a key that supports the model.
func (p *APIKeyProvider) selectKey(model string) *APIKeyEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, key := range p.keys {
		for _, m := range key.Models {
			if strings.EqualFold(m.Alias, model) || strings.EqualFold(m.Name, model) {
				return &key
			}
		}
	}
	return nil
}

// resolveModel resolves alias to actual model name.
func (p *APIKeyProvider) resolveModel(key *APIKeyEntry, requested string) string {
	for _, m := range key.Models {
		if strings.EqualFold(m.Alias, requested) {
			return m.Name
		}
	}
	return requested
}

// executeHTTP makes the HTTP request.
func (p *APIKeyProvider) executeHTTP(ctx context.Context, key *APIKeyEntry, model string, req executor.Request) (executor.Response, error) {
	// TODO: implement actual HTTP execution
	// This is a placeholder - actual implementation would build HTTP request
	return executor.Response{}, errors.New("not yet implemented")
}

// executeHTTPStream makes a streaming HTTP request.
func (p *APIKeyProvider) executeHTTPStream(ctx context.Context, key *APIKeyEntry, model string, req executor.Request) (
	<-chan executor.StreamChunk, error) {
	// TODO: implement actual HTTP streaming
	return nil, errors.New("not yet implemented")
}

// Errors
var (
	ErrNoMatchingAPIKey = errors.New("no API key supports the requested model")
)
