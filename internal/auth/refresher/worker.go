// Package refresher provides a background worker for proactive OAuth token refresh.
// It monitors registered tokens and refreshes them before expiry to prevent
// authentication failures during active use.
package refresher

import (
	"context"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// DefaultRefreshLeadTime is the default duration before expiry to trigger refresh.
const DefaultRefreshLeadTime = 10 * time.Minute

// DefaultCheckInterval is the default interval between refresh checks.
const DefaultCheckInterval = 1 * time.Minute

// ErrWorkerStopped is returned when operations are attempted on a stopped worker.
var ErrWorkerStopped = errors.New("refresher: worker is stopped")

// Token represents a refreshable OAuth token.
type Token struct {
	// ID is a unique identifier for this token (e.g., auth ID).
	ID string

	// Provider identifies the OAuth provider (e.g., "claude", "codex").
	Provider string

	// RefreshToken is the OAuth refresh token.
	RefreshToken string

	// ExpiresAt is the access token expiration time.
	ExpiresAt time.Time

	// LastRefresh is the timestamp of the last refresh attempt.
	LastRefresh time.Time

	// RefreshError captures the last refresh error, if any.
	RefreshError error
}

// NeedsRefresh checks if the token should be refreshed based on lead time.
func (t *Token) NeedsRefresh(leadTime time.Duration) bool {
	if t == nil || t.RefreshToken == "" {
		return false
	}
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(leadTime).After(t.ExpiresAt)
}

// Refresher is a function that performs the actual token refresh.
// It should return the new expiration time on success.
type Refresher func(ctx context.Context, token *Token) (newExpiresAt time.Time, err error)

// Hook provides callbacks for refresh events.
type Hook interface {
	// OnRefreshSuccess is called when a token is successfully refreshed.
	OnRefreshSuccess(token *Token, newExpiresAt time.Time)

	// OnRefreshError is called when a token refresh fails.
	OnRefreshError(token *Token, err error)
}

// NoopHook is a no-op implementation of Hook.
type NoopHook struct{}

func (NoopHook) OnRefreshSuccess(*Token, time.Time) {}
func (NoopHook) OnRefreshError(*Token, error)       {}

// Config configures the refresh worker.
type Config struct {
	// RefreshLeadTime is how far before expiry to trigger refresh.
	// Default: 10 minutes.
	RefreshLeadTime time.Duration

	// CheckInterval is how often to check for tokens needing refresh.
	// Default: 1 minute.
	CheckInterval time.Duration

	// MaxConcurrency limits concurrent refresh operations.
	// Default: 5.
	MaxConcurrency int

	// RetryDelay is the delay before retrying a failed refresh.
	// Default: 5 minutes.
	RetryDelay time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		RefreshLeadTime: DefaultRefreshLeadTime,
		CheckInterval:   DefaultCheckInterval,
		MaxConcurrency:  5,
		RetryDelay:      5 * time.Minute,
	}
}

// Worker manages background token refresh operations.
type Worker struct {
	config    Config
	refresher Refresher
	hook      Hook

	mu      sync.RWMutex
	tokens  map[string]*Token
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewWorker creates a new refresh worker.
func NewWorker(refresher Refresher, config Config, hook Hook) *Worker {
	if refresher == nil {
		panic("refresher: refresher function cannot be nil")
	}
	if hook == nil {
		hook = NoopHook{}
	}
	if config.RefreshLeadTime <= 0 {
		config.RefreshLeadTime = DefaultRefreshLeadTime
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = DefaultCheckInterval
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = 5
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = 5 * time.Minute
	}

	return &Worker{
		config:    config,
		refresher: refresher,
		hook:      hook,
		tokens:    make(map[string]*Token),
	}
}

// Start begins the background refresh loop.
func (w *Worker) Start() {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.mu.Unlock()

	w.wg.Add(1)
	go w.loop(ctx)
}

// Stop gracefully stops the worker, waiting for in-flight refreshes.
func (w *Worker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()

	w.wg.Wait()
}

// Register adds or updates a token for monitoring.
func (w *Worker) Register(token *Token) error {
	if token == nil || token.ID == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return ErrWorkerStopped
	}

	// Clone to avoid external mutation
	clone := &Token{
		ID:           token.ID,
		Provider:     token.Provider,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
		LastRefresh:  token.LastRefresh,
		RefreshError: token.RefreshError,
	}
	w.tokens[token.ID] = clone
	return nil
}

// Unregister removes a token from monitoring.
func (w *Worker) Unregister(tokenID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.tokens, tokenID)
}

// Get returns a copy of the token state, or nil if not found.
func (w *Worker) Get(tokenID string) *Token {
	w.mu.RLock()
	defer w.mu.RUnlock()
	t, ok := w.tokens[tokenID]
	if !ok || t == nil {
		return nil
	}
	return &Token{
		ID:           t.ID,
		Provider:     t.Provider,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.ExpiresAt,
		LastRefresh:  t.LastRefresh,
		RefreshError: t.RefreshError,
	}
}

// TokenCount returns the number of registered tokens.
func (w *Worker) TokenCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.tokens)
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkAndRefresh(ctx)
		}
	}
}

func (w *Worker) checkAndRefresh(ctx context.Context) {
	// Get tokens that need refresh
	var candidates []*Token
	now := time.Now()

	w.mu.RLock()
	for _, t := range w.tokens {
		if t == nil || t.RefreshToken == "" {
			continue
		}
		// Skip if recently tried and failed
		if t.RefreshError != nil && now.Sub(t.LastRefresh) < w.config.RetryDelay {
			continue
		}
		if t.NeedsRefresh(w.config.RefreshLeadTime) {
			candidates = append(candidates, &Token{
				ID:           t.ID,
				Provider:     t.Provider,
				RefreshToken: t.RefreshToken,
				ExpiresAt:    t.ExpiresAt,
				LastRefresh:  t.LastRefresh,
			})
		}
	}
	w.mu.RUnlock()

	if len(candidates) == 0 {
		return
	}

	log.Debugf("refresher: %d token(s) need refresh", len(candidates))

	// Limit concurrency
	sem := make(chan struct{}, w.config.MaxConcurrency)
	var wg sync.WaitGroup

	for _, token := range candidates {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(t *Token) {
			defer wg.Done()
			defer func() { <-sem }()
			w.refreshToken(ctx, t)
		}(token)
	}

	wg.Wait()
}

func (w *Worker) refreshToken(ctx context.Context, token *Token) {
	if token == nil {
		return
	}

	log.Debugf("refresher: refreshing token %s (%s)", token.ID, token.Provider)

	newExpiry, err := w.refresher(ctx, token)
	now := time.Now()

	w.mu.Lock()
	t, exists := w.tokens[token.ID]
	if !exists || t == nil {
		w.mu.Unlock()
		return
	}

	t.LastRefresh = now

	if err != nil {
		t.RefreshError = err
		w.mu.Unlock()
		log.Warnf("refresher: failed to refresh token %s: %v", token.ID, err)
		w.hook.OnRefreshError(token, err)
		return
	}

	t.ExpiresAt = newExpiry
	t.RefreshError = nil
	w.mu.Unlock()

	log.Infof("refresher: successfully refreshed token %s (expires: %s)", token.ID, newExpiry.Format(time.RFC3339))
	w.hook.OnRefreshSuccess(token, newExpiry)
}
