package quota

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ErrUnknownProvider is returned when a provider is not recognized.
var ErrUnknownProvider = errors.New("unknown provider")

// unsupportedProviders lists providers that don't have quota APIs.
// These are "known" providers but don't support quota fetching.
var unsupportedProviders = map[string]bool{
	"claude":   true,
	"gemini":   true, // Gemini CLI doesn't have a public quota API
	"vertex":   true,
	"iflow":    true,
	"qwen":     true,
	"aistudio": true,
}

// Manager orchestrates quota fetching for all providers.
type Manager struct {
	mu         sync.RWMutex
	fetchers   []Fetcher
	cache      *QuotaCache
	authStore  coreauth.Store
	httpClient *http.Client
	worker     *Worker
}

// NewManager creates a new quota manager with the given auth store.
func NewManager(authStore coreauth.Store, httpClient *http.Client) *Manager {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	m := &Manager{
		fetchers:   make([]Fetcher, 0),
		cache:      NewQuotaCache(DefaultCacheTTL),
		authStore:  authStore,
		httpClient: httpClient,
	}

	// Register default fetchers
	m.RegisterFetcher(NewAntigravityFetcher(httpClient))
	m.RegisterFetcher(NewCodexFetcher(httpClient))

	return m
}

// RegisterFetcher adds a quota fetcher to the manager.
func (m *Manager) RegisterFetcher(fetcher Fetcher) {
	if fetcher == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchers = append(m.fetchers, fetcher)
}

// SetCacheTTL updates the cache TTL.
func (m *Manager) SetCacheTTL(ttl time.Duration) {
	m.cache.SetTTL(ttl)
}

// SetAuthStore updates the auth store.
func (m *Manager) SetAuthStore(store coreauth.Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authStore = store
}

// SetRefreshInterval configures the background refresh interval.
// If interval is > 0, creates a new worker that will be started when StartWorker is called.
// If interval is <= 0, stops any existing worker and disables background refresh.
func (m *Manager) SetRefreshInterval(interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop existing worker if running
	if m.worker != nil {
		m.worker.Stop()
		m.worker = nil
	}

	// Create new worker if interval is positive
	if interval > 0 {
		m.worker = NewWorker(m, interval)
	}
}

// StartWorker starts the background quota refresh worker if configured.
// This should be called after the server is ready to handle requests.
func (m *Manager) StartWorker(ctx context.Context) {
	m.mu.RLock()
	worker := m.worker
	m.mu.RUnlock()

	if worker != nil {
		worker.Start(ctx)
	}
}

// StopWorker stops the background quota refresh worker.
// This should be called during server shutdown.
func (m *Manager) StopWorker() {
	m.mu.RLock()
	worker := m.worker
	m.mu.RUnlock()

	if worker != nil {
		worker.Stop()
	}
}

// WorkerStatus returns information about the background worker.
// Returns nil if no worker is configured.
func (m *Manager) WorkerStatus() *WorkerStatus {
	m.mu.RLock()
	worker := m.worker
	m.mu.RUnlock()

	if worker == nil {
		return nil
	}

	return &WorkerStatus{
		Running:  worker.IsRunning(),
		Interval: worker.Interval(),
	}
}

// WorkerStatus contains information about the quota refresh worker.
type WorkerStatus struct {
	Running  bool          `json:"running"`
	Interval time.Duration `json:"interval"`
}

// getFetcherForProvider returns the fetcher that can handle the given provider.
func (m *Manager) getFetcherForProvider(provider string) Fetcher {
	m.mu.RLock()
	defer m.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, fetcher := range m.fetchers {
		if fetcher.CanFetch(provider) {
			return fetcher
		}
	}
	return nil
}

// isUnsupportedProvider returns true if the provider doesn't support quota fetching.
func isUnsupportedProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return unsupportedProviders[provider]
}

// GetKnownProviders returns a list of all known provider names.
// This includes both supported providers (with fetchers) and unsupported providers.
func (m *Manager) GetKnownProviders() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	providers := make(map[string]bool)

	// Add providers from fetchers
	for _, fetcher := range m.fetchers {
		for _, p := range fetcher.SupportedProviders() {
			providers[strings.ToLower(p)] = true
		}
	}

	// Add unsupported providers (they're still "known")
	for p := range unsupportedProviders {
		providers[p] = true
	}

	result := make([]string, 0, len(providers))
	for p := range providers {
		result = append(result, p)
	}
	return result
}

// IsKnownProvider returns true if the provider is recognized.
func (m *Manager) IsKnownProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))

	// Check if it's an unsupported but known provider
	if unsupportedProviders[provider] {
		return true
	}

	// Check if any fetcher supports this provider
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, fetcher := range m.fetchers {
		if fetcher.CanFetch(provider) {
			return true
		}
	}

	return false
}

// FetchAllQuotas fetches quota for all connected accounts.
func (m *Manager) FetchAllQuotas(ctx context.Context) (*QuotaResponse, error) {
	m.mu.RLock()
	authStore := m.authStore
	m.mu.RUnlock()

	if authStore == nil {
		return &QuotaResponse{
			Quotas:      make(map[string]map[string]*ProviderQuotaData),
			LastUpdated: time.Now(),
		}, nil
	}

	// List all auth records
	auths, err := authStore.List(ctx)
	if err != nil {
		return nil, err
	}

	response := &QuotaResponse{
		Quotas:      make(map[string]map[string]*ProviderQuotaData),
		LastUpdated: time.Now(),
	}

	// Group auths by provider
	for _, auth := range auths {
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		accountID := m.getAccountID(auth)

		if response.Quotas[provider] == nil {
			response.Quotas[provider] = make(map[string]*ProviderQuotaData)
		}

		quotaData := m.fetchQuotaForAuth(ctx, auth, false)
		response.Quotas[provider][accountID] = quotaData
	}

	return response, nil
}

// FetchProviderQuotas fetches quota for all accounts of a specific provider.
func (m *Manager) FetchProviderQuotas(ctx context.Context, provider string) (*ProviderQuotaResponse, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))

	// Validate provider is known
	if !m.IsKnownProvider(provider) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, provider)
	}

	m.mu.RLock()
	authStore := m.authStore
	m.mu.RUnlock()

	if authStore == nil {
		return &ProviderQuotaResponse{
			Provider:    provider,
			Accounts:    make(map[string]*ProviderQuotaData),
			LastUpdated: time.Now(),
		}, nil
	}

	// List all auth records
	auths, err := authStore.List(ctx)
	if err != nil {
		return nil, err
	}

	response := &ProviderQuotaResponse{
		Provider:    provider,
		Accounts:    make(map[string]*ProviderQuotaData),
		LastUpdated: time.Now(),
	}

	// Filter and fetch for this provider
	for _, auth := range auths {
		authProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if authProvider != provider {
			continue
		}

		accountID := m.getAccountID(auth)
		quotaData := m.fetchQuotaForAuth(ctx, auth, false)
		response.Accounts[accountID] = quotaData
	}

	return response, nil
}

// FetchAccountQuota fetches quota for a specific account.
func (m *Manager) FetchAccountQuota(ctx context.Context, provider, accountID string) (*AccountQuotaResponse, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))

	// Validate provider is known
	if !m.IsKnownProvider(provider) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, provider)
	}

	m.mu.RLock()
	authStore := m.authStore
	m.mu.RUnlock()

	if authStore == nil {
		return &AccountQuotaResponse{
			Provider: provider,
			Account:  accountID,
			Quota:    UnavailableQuota("auth store not configured"),
		}, nil
	}

	// List all auth records
	auths, err := authStore.List(ctx)
	if err != nil {
		return nil, err
	}

	// Find the matching auth
	for _, auth := range auths {
		authProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
		authAccountID := m.getAccountID(auth)
		if authProvider != provider || authAccountID != accountID {
			continue
		}

		quotaData := m.fetchQuotaForAuth(ctx, auth, false)
		return &AccountQuotaResponse{
			Provider: provider,
			Account:  accountID,
			Quota:    quotaData,
		}, nil
	}

	return &AccountQuotaResponse{
		Provider: provider,
		Account:  accountID,
		Quota:    UnavailableQuota("account not found"),
	}, nil
}

// RefreshQuotas forces a refresh of quota data.
func (m *Manager) RefreshQuotas(ctx context.Context, providers []string) (*QuotaResponse, error) {
	// Invalidate cache
	if len(providers) == 0 {
		m.cache.InvalidateAll()
	} else {
		for _, p := range providers {
			m.cache.InvalidateProvider(p)
		}
	}

	// Fetch fresh data
	return m.FetchAllQuotas(ctx)
}

// GetSubscriptionInfo fetches subscription info for Antigravity/Gemini-CLI accounts.
func (m *Manager) GetSubscriptionInfo(ctx context.Context) (*SubscriptionInfoResponse, error) {
	m.mu.RLock()
	authStore := m.authStore
	m.mu.RUnlock()

	response := &SubscriptionInfoResponse{
		Subscriptions: make(map[string]*SubscriptionInfo),
	}

	if authStore == nil {
		return response, nil
	}

	// List all auth records
	auths, err := authStore.List(ctx)
	if err != nil {
		return nil, err
	}

	// Get subscription info for Antigravity/Gemini-CLI accounts
	for _, auth := range auths {
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider != "antigravity" && provider != "gemini-cli" {
			continue
		}

		accountID := m.getAccountID(auth)
		fetcher := m.getFetcherForProvider(provider)
		if fetcher == nil {
			continue
		}

		// Type assert to get subscription info
		if af, ok := fetcher.(*AntigravityFetcher); ok {
			info, err := af.GetSubscriptionInfo(ctx, auth)
			if err != nil {
				log.Warnf("quota manager: failed to get subscription info for %s: %v", accountID, err)
				continue
			}
			response.Subscriptions[accountID] = info
		}
	}

	return response, nil
}

// fetchQuotaForAuth fetches quota for a single auth record.
func (m *Manager) fetchQuotaForAuth(ctx context.Context, auth *coreauth.Auth, bypassCache bool) *ProviderQuotaData {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	accountID := m.getAccountID(auth)

	// Check cache first
	if !bypassCache {
		if cached, ok := m.cache.Get(provider, accountID); ok {
			return cached
		}
	}

	// Check if provider is unsupported
	if isUnsupportedProvider(provider) {
		data := UnavailableQuota("quota API not available for this provider")
		m.cache.Set(provider, accountID, data)
		return data
	}

	// Find appropriate fetcher
	fetcher := m.getFetcherForProvider(provider)
	if fetcher == nil {
		data := UnavailableQuota("no fetcher available for this provider")
		m.cache.Set(provider, accountID, data)
		return data
	}

	// Fetch quota
	data, err := fetcher.FetchQuota(ctx, auth)
	if err != nil {
		log.Warnf("quota manager: fetch failed for %s/%s: %v", provider, accountID, err)
		data = UnavailableQuota(err.Error())
	}

	if data == nil {
		data = UnavailableQuota("fetcher returned nil")
	}

	// Cache the result
	m.cache.Set(provider, accountID, data)

	return data
}

// getAccountID extracts a human-readable account ID from an auth record.
func (m *Manager) getAccountID(auth *coreauth.Auth) string {
	// Try to get email from metadata
	if auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok && email != "" {
			return strings.TrimSpace(email)
		}
	}

	// Try to get from attributes
	if auth.Attributes != nil {
		if email, ok := auth.Attributes["email"]; ok && email != "" {
			return strings.TrimSpace(email)
		}
	}

	// Fall back to ID
	if auth.ID != "" {
		return auth.ID
	}

	// Fall back to label
	if auth.Label != "" {
		return auth.Label
	}

	return "unknown"
}
