package ollama

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// cacheTTL bounds how long a successful fetch is served from memory. It is
// deliberately short: balances change with usage, and the quota page refresh
// is on-demand rather than polled.
const cacheTTL = 120 * time.Second

// Connection is a resolved Ollama Cloud upstream connection. APIKey is
// server-side only and must never be serialized or logged.
type Connection struct {
	// Name is the configured openai-compatibility display name.
	Name string
	// BaseURL is the verified upstream base URL (https://ollama.com/...).
	BaseURL string
	// APIKey is the selected credential for account calls.
	APIKey   string
	ProxyURL string
}

// QuotaService resolves Ollama Cloud connections from server configuration
// and fetches their usage and balance with caching.
type QuotaService struct {
	client   *Client
	now      func() time.Time
	cacheTTL time.Duration

	mu        sync.Mutex
	cache     map[string]*cacheEntry
	inFlight  map[string]*inflightCall
	cooldowns map[string]time.Time
	slots     chan struct{}
}

type cacheEntry struct {
	resp      *QuotaResponse
	expiresAt time.Time
}

type inflightCall struct {
	done chan struct{}
	resp *QuotaResponse
	err  error
}

// NewQuotaService returns a service using the default client.
func NewQuotaService() *QuotaService {
	return &QuotaService{
		client:    NewClient(),
		now:       time.Now,
		cacheTTL:  cacheTTL,
		cache:     make(map[string]*cacheEntry),
		inFlight:  make(map[string]*inflightCall),
		cooldowns: make(map[string]time.Time),
		slots:     make(chan struct{}, 4),
	}
}

// NewQuotaServiceWithClient injects a client and clock, for tests.
func NewQuotaServiceWithClient(client *Client, now func() time.Time) *QuotaService {
	svc := NewQuotaService()
	if client != nil {
		svc.client = client
	}
	if now != nil {
		svc.now = now
	}
	return svc
}

// ErrConnectionNotFound indicates no configured connection matches.
var ErrConnectionNotFound = fmt.Errorf("ollama cloud connection not found")

// ConnectionInfo is safe to expose: no key or proxy configuration is included.
type ConnectionInfo struct {
	ID          string `json:"connection_id"`
	DisplayName string `json:"display_name"`
}

func credentialID(name, key string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + key))
	return fmt.Sprintf("ollama-%x", sum[:16])
}

// Connections enumerates each configured credential without assuming shared accounts.
func (s *QuotaService) Connections(cfg *config.Config) []ConnectionInfo {
	result := make([]ConnectionInfo, 0)
	if cfg == nil {
		return result
	}
	seen := make(map[string]bool)
	for _, group := range cfg.OpenAICompatibility {
		if _, err := s.resolveNamedConnection(cfg, strings.TrimSpace(group.Name)); err != nil {
			continue
		}
		for index, entry := range group.APIKeyEntries {
			if strings.TrimSpace(entry.APIKey) == "" || (entry.Weight != nil && *entry.Weight <= 0) {
				continue
			}
			id := credentialID(group.Name, strings.TrimSpace(entry.APIKey))
			if seen[id] {
				continue
			}
			seen[id] = true
			label := group.Name
			if len(group.APIKeyEntries) > 1 {
				label = fmt.Sprintf("%s · %d", label, index+1)
			}
			result = append(result, ConnectionInfo{ID: id, DisplayName: label})
		}
	}
	return result
}

// ResolveConnection finds the named openai-compatibility group and verifies
// it is an Ollama Cloud upstream. It fails closed: the base URL must use
// https with host exactly ollama.com, and at least one usable API key is
// required. Local Ollama hosts or arbitrary OpenAI-compatible URLs are never
// accepted as quota-fetch destinations.
func (s *QuotaService) ResolveConnection(cfg *config.Config, providerID string) (*Connection, error) {
	providerID = strings.TrimSpace(providerID)
	if cfg != nil && strings.HasPrefix(providerID, "ollama-") {
		for _, group := range cfg.OpenAICompatibility {
			for _, entry := range group.APIKeyEntries {
				if providerID != credentialID(group.Name, strings.TrimSpace(entry.APIKey)) {
					continue
				}
				copyConfig := *cfg
				copyGroup := group
				copyGroup.APIKeyEntries = []config.OpenAICompatibilityAPIKey{entry}
				copyConfig.OpenAICompatibility = []config.OpenAICompatibility{copyGroup}
				return s.resolveNamedConnection(&copyConfig, group.Name)
			}
		}
	}
	return s.resolveNamedConnection(cfg, providerID)
}

func (s *QuotaService) resolveNamedConnection(cfg *config.Config, providerID string) (*Connection, error) {
	if providerID == "" {
		return nil, fmt.Errorf("%w: provider_id is required", ErrConnectionNotFound)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: no configuration loaded", ErrConnectionNotFound)
	}

	var matched *config.OpenAICompatibility
	for i := range cfg.OpenAICompatibility {
		group := &cfg.OpenAICompatibility[i]
		if !strings.EqualFold(strings.TrimSpace(group.Name), providerID) {
			continue
		}
		if matched != nil {
			return nil, errors.New("ambiguous Ollama Cloud connection")
		}
		matched = group
	}
	if matched == nil {
		return nil, fmt.Errorf("%w: no openai-compatibility connection named %q", ErrConnectionNotFound, providerID)
	}
	if matched.Disabled {
		return nil, fmt.Errorf("ollama cloud connection %q is disabled", providerID)
	}

	base := strings.TrimSpace(matched.BaseURL)
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("ollama cloud connection %q has an invalid base URL", providerID)
	}
	if !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), Host) {
		return nil, fmt.Errorf("ollama cloud connection %q does not point at the Ollama Cloud origin", providerID)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "/v1" && parsed.Path != "/v1/") {
		return nil, errors.New("invalid Ollama Cloud connection URL")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("ollama cloud connection %q does not point at the Ollama Cloud origin", providerID)
	}

	for i := range matched.APIKeyEntries {
		entry := &matched.APIKeyEntries[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		if entry.Weight != nil && *entry.Weight <= 0 {
			continue
		}
		// An opaque per-credential identity prevents replacement credentials from
		// inheriting account-wide browser state under the same display name.
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		if proxyURL == "" {
			proxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
		if proxyURL == "" {
			proxyURL = "direct"
		}
		return &Connection{Name: strings.TrimSpace(matched.Name), BaseURL: base, APIKey: key, ProxyURL: proxyURL}, nil
	}

	return nil, fmt.Errorf("ollama cloud connection %q has no usable API key", providerID)
}

// FetchQuota resolves the connection and fetches usage plus balance with a
// short-lived cache and in-flight deduplication. The returned response is
// immutable: callers must not mutate it, because concurrent callers sharing
// a deduplicated fetch receive the same value.
func (s *QuotaService) FetchQuota(ctx context.Context, cfg *config.Config, providerID string, rng UsageRange, scope UsageScope) (*QuotaResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !rng.Valid() || !scope.Valid() {
		return nil, errors.New("invalid Ollama Cloud usage parameters")
	}
	conn, err := s.ResolveConnection(cfg, providerID)
	if err != nil {
		return nil, err
	}

	// A changed key must never inherit another account's cached balance.
	fingerprint := sha256.Sum256([]byte(conn.APIKey))
	key := cacheKey(conn.Name, rng, scope) + fmt.Sprintf("\x00%x\x00%s", fingerprint, conn.ProxyURL)
	now := s.now()

	s.mu.Lock()
	credentialKey := fmt.Sprintf("%x", fingerprint)
	if until := s.cooldowns[credentialKey]; now.Before(until) {
		s.mu.Unlock()
		return nil, &UpstreamError{StatusCode: 429, Message: "Ollama Cloud: Too many requests", RetryAfter: until.Sub(now)}
	}
	if call, ok := s.inFlight[key]; ok {
		s.mu.Unlock()
		select {
		case <-call.done:
			return call.resp, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if entry, ok := s.cache[key]; ok && now.Before(entry.expiresAt) {
		s.mu.Unlock()
		return entry.resp, nil
	}
	call := &inflightCall{done: make(chan struct{})}
	s.inFlight[key] = call
	s.mu.Unlock()

	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.inFlight, key)
		s.mu.Unlock()
		call.err = ctx.Err()
		close(call.done)
		return nil, call.err
	}
	resp, err := s.fetch(ctx, conn, rng, scope)
	<-s.slots

	s.mu.Lock()
	delete(s.inFlight, key)
	for credential, until := range s.cooldowns {
		if !s.now().Before(until) {
			delete(s.cooldowns, credential)
		}
	}
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) && upstreamErr.StatusCode == 429 {
		delay := upstreamErr.RetryAfter
		if delay <= 0 {
			delay = 30 * time.Second
		}
		if len(s.cooldowns) < 256 {
			s.cooldowns[credentialKey] = s.now().Add(delay)
		}
	}
	if err == nil && resp.UsageError == "" && resp.BalanceError == "" {
		// Keep memory bounded even with many configured connections/ranges.
		if len(s.cache) >= 64 {
			for oldest := range s.cache {
				delete(s.cache, oldest)
				break
			}
		}
		s.cache[key] = &cacheEntry{resp: resp, expiresAt: s.now().Add(s.cacheTTL)}
		s.pruneLocked()
	}
	s.mu.Unlock()

	// Publish the result before closing done: waiters read these fields only
	// after receiving on done, so the close provides the happens-before edge.
	call.resp = resp
	call.err = err
	close(call.done)

	return resp, err
}

// fetch performs the two upstream calls in parallel and assembles a response
// with partial-success semantics.
func (s *QuotaService) fetch(ctx context.Context, conn *Connection, rng UsageRange, scope UsageScope) (*QuotaResponse, error) {
	client := *s.client
	if conn.ProxyURL != "" {
		transport, _, err := proxyutil.BuildHTTPTransport(conn.ProxyURL)
		if err != nil {
			return nil, errors.New("invalid quota proxy configuration")
		}
		defer transport.CloseIdleConnections()
		httpClient := *client.HTTPClient
		httpClient.Transport = transport
		client.HTTPClient = &httpClient
	}
	usageCtx, cancelUsage := context.WithCancel(ctx)
	defer cancelUsage()
	balanceCtx, cancelBalance := context.WithCancel(ctx)
	defer cancelBalance()

	var (
		usage      *UsageResponse
		balance    *BalanceResponse
		usageErr   error
		balanceErr error
		wg         sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		usage, usageErr = client.FetchUsage(usageCtx, conn.APIKey, rng, scope)
	}()
	go func() {
		defer wg.Done()
		balance, balanceErr = client.FetchBalance(balanceCtx, conn.APIKey)
	}()
	wg.Wait()

	resp := &QuotaResponse{
		Provider:     ProviderKey,
		ConnectionID: credentialID(conn.Name, conn.APIKey),
		DisplayName:  conn.Name,
		Scope:        scope,
		Range:        rng,
		FetchedAt:    s.now().UTC(),
		Source:       SourceName,
	}

	if usageErr != nil {
		resp.UsageError = sanitizeErrorMessage(usageErr)
	} else {
		resp.Usage = usage
	}
	if balanceErr != nil {
		resp.BalanceError = sanitizeErrorMessage(balanceErr)
	} else {
		resp.Balance = balance
	}

	for _, err := range []error{usageErr, balanceErr} {
		var upstreamErr *UpstreamError
		if errors.As(err, &upstreamErr) && upstreamErr.StatusCode == 429 {
			return resp, &fetchFailure{cause: err}
		}
	}
	if usageErr != nil && balanceErr != nil {
		return resp, &fetchFailure{cause: usageErr}
	}
	return resp, nil
}

// fetchFailure reports a complete failure or a rate-limited section. It displays a
// sanitized message while preserving the error chain so callers can inspect
// the underlying cause (for example context cancellation) via errors.Is.
type fetchFailure struct {
	cause error
}

func (e *fetchFailure) Error() string {
	return "ollama cloud quota fetch failed: " + sanitizeErrorMessage(e.cause)
}

func (e *fetchFailure) Unwrap() error { return e.cause }

// cacheKey builds the per-connection, per-range, per-scope cache key.
func cacheKey(name string, rng UsageRange, scope UsageScope) string {
	return strings.ToLower(strings.TrimSpace(name)) + "\x00" + string(rng) + "\x00" + string(scope)
}

// pruneLocked drops expired cache entries once the map grows past a bound.
// Callers must hold s.mu.
func (s *QuotaService) pruneLocked() {
	if len(s.cache) <= 64 {
		return
	}
	now := s.now()
	for key, entry := range s.cache {
		if !now.Before(entry.expiresAt) {
			delete(s.cache, key)
		}
	}
}

// sanitizeErrorMessage renders an error for client-facing fields without
// leaking credential material or raw upstream bodies.
func sanitizeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.Message
	}
	return "Ollama Cloud request failed"
}
