package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ServiceConfig tunes the PolicyService concrete implementation.
type ServiceConfig struct {
	// CacheTTL bounds how long a (apiKey, policy) snapshot is reused without
	// re-reading from PG. Default 5s balances freshness against DB load.
	CacheTTL time.Duration
	// CleanupInterval governs the background sweep that drops stale
	// sliding-window counters from memory.
	CleanupInterval time.Duration
	// WindowRetention bounds how long a sliding-window counter is kept after
	// its bucket expires, before being garbage-collected.
	WindowRetention time.Duration
}

func defaultServiceConfig(c ServiceConfig) ServiceConfig {
	if c.CacheTTL <= 0 {
		c.CacheTTL = 5 * time.Second
	}
	if c.CleanupInterval <= 0 {
		c.CleanupInterval = 30 * time.Second
	}
	if c.WindowRetention <= 0 {
		c.WindowRetention = 5 * time.Minute
	}
	return c
}

// service is the concrete PolicyService. It is safe for concurrent use; the
// only mutable shared state is the cache map (RWMutex-guarded) and the
// in-memory sliding-window counter (Mutex-guarded).
type service struct {
	apiKeys *store.APIKeyStore
	usage   *store.UsageStore
	cfg     ServiceConfig

	mu    sync.RWMutex
	cache map[string]APIKeySnapshot // keyed by principal (plaintext key)

	rpmWindow    *slidingWindow
	hourlyWindow *slidingWindow

	stop chan struct{}
	done chan struct{}
}

// NewService constructs a PolicyService backed by the supplied stores. Either
// store may be nil only when the PG backend is inactive — in that case the
// service reports Active() == false and the middleware fast-paths through.
func NewService(apiKeys *store.APIKeyStore, usage *store.UsageStore, cfg ServiceConfig) PolicyService {
	if apiKeys == nil || usage == nil {
		return &service{cfg: defaultServiceConfig(cfg)}
	}
	return &service{
		apiKeys:      apiKeys,
		usage:        usage,
		cfg:          defaultServiceConfig(cfg),
		cache:        make(map[string]APIKeySnapshot),
		rpmWindow:    newSlidingWindow(),
		hourlyWindow: newSlidingWindow(),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Active reports whether PG-backed policy enforcement is enabled.
func (s *service) Active() bool {
	if s == nil {
		return false
	}
	return s.apiKeys != nil && s.usage != nil
}

// Start launches the background cleanup goroutine. Idempotent; subsequent
// calls are no-ops. Stop() releases the goroutine.
func (s *service) Start(ctx context.Context) error {
	if s == nil || !s.Active() {
		return nil
	}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.cfg.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.rpmWindow.Cleanup(s.cfg.WindowRetention)
				s.hourlyWindow.Cleanup(s.cfg.WindowRetention)
			}
		}
	}()
	return nil
}

// Stop signals the cleanup goroutine to exit and blocks until it has.
func (s *service) Stop() {
	if s == nil || !s.Active() {
		return
	}
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		// don't block forever on a stuck goroutine
	}
}

// Check evaluates the per-request policy. Returns a Decision with Allow=true
// when the request is permitted, or Allow=false (with StatusCode set to 429
// for rate limits, 403 for model access, or 402 for budget cap exceeded)
// otherwise.
func (s *service) Check(ctx context.Context, principal, model string) (Decision, error) {
	if !s.Active() {
		return Decision{Allow: true}, nil
	}
	if principal == "" {
		// No principal (anonymous request handled by another provider path):
		// defer to existing middleware behavior, do not block here.
		return Decision{Allow: true}, nil
	}
	snap, err := s.snapshot(ctx, principal)
	if err != nil {
		// On infra failure we fail open; the existing AuthMiddleware already
		// validated the credential, so blocking here would deny all traffic
		// during a PG outage.
		log.WithError(err).WithField("principal_prefix", safePrefix(principal)).
			Warn("policy check: lookup failed; failing open")
		return Decision{Allow: true}, nil
	}
	if snap.APIKey.Status != store.APIKeyStatusActive {
		return Decision{
			Allow:      false,
			Reason:     fmt.Sprintf("API key status is %q", snap.APIKey.Status),
			StatusCode: 401,
		}, nil
	}
	if snap.APIKey.ExpiresAt != nil && time.Now().After(*snap.APIKey.ExpiresAt) {
		return Decision{
			Allow:      false,
			Reason:     "API key has expired",
			StatusCode: 401,
		}, nil
	}
	if snap.Policy != nil {
		if !Allowed(fromStorePolicy(*snap.Policy), model) {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("model %q is not permitted for this API key", model),
				StatusCode: 403,
			}, nil
		}
		if snap.Policy.RPMLimit != nil && *snap.Policy.RPMLimit > 0 {
			if !s.rpmWindow.AllowAndIncrement(principal, *snap.Policy.RPMLimit, time.Minute) {
				return Decision{
					Allow:      false,
					Reason:     fmt.Sprintf("rate limit exceeded: %d requests per minute", *snap.Policy.RPMLimit),
					StatusCode: 429,
				}, nil
			}
		}
		if snap.Policy.HourlyRateLimit != nil && *snap.Policy.HourlyRateLimit > 0 {
			if !s.hourlyWindow.AllowAndIncrement(principal, *snap.Policy.HourlyRateLimit, time.Hour) {
				return Decision{
					Allow:      false,
					Reason:     fmt.Sprintf("rate limit exceeded: %d requests per hour", *snap.Policy.HourlyRateLimit),
					StatusCode: 429,
				}, nil
			}
		}
		// Budget caps are checked against the persisted usage_windows row.
		if decision, denied := s.checkBudget(ctx, snap.APIKey.ID, snap.Policy); denied {
			return decision, nil
		}
	}
	// Touch last_used_at asynchronously so the hot path is not delayed.
	go s.apiKeys.TouchLastUsed(context.Background(), snap.APIKey.ID)
	return Decision{Allow: true}, nil
}

// checkBudget returns (decision, denied=true) when any budget cap has been
// exceeded. The decision carries the most restrictive cap that was hit so the
// caller can surface a meaningful error.
func (s *service) checkBudget(ctx context.Context, apiKeyID string, p *store.Policy) (Decision, bool) {
	if p == nil {
		return Decision{}, false
	}
	now := time.Now()
	if p.BudgetHourlyUSD != nil && *p.BudgetHourlyUSD > 0 {
		if spent, _ := s.windowSpent(ctx, apiKeyID, store.WindowTypeHourly, now); spent >= *p.BudgetHourlyUSD {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("hourly budget exceeded: %.2f USD spent of %.2f USD limit", spent, *p.BudgetHourlyUSD),
				StatusCode: 402,
			}, true
		}
	}
	if p.BudgetWeeklyUSD != nil && *p.BudgetWeeklyUSD > 0 {
		if spent, _ := s.windowSpent(ctx, apiKeyID, store.WindowTypeWeekly, now); spent >= *p.BudgetWeeklyUSD {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("weekly budget exceeded: %.2f USD spent of %.2f USD limit", spent, *p.BudgetWeeklyUSD),
				StatusCode: 402,
			}, true
		}
	}
	if p.BudgetMonthlyUSD != nil && *p.BudgetMonthlyUSD > 0 {
		if spent, _ := s.windowSpent(ctx, apiKeyID, store.WindowTypeMonthly, now); spent >= *p.BudgetMonthlyUSD {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("monthly budget exceeded: %.2f USD spent of %.2f USD limit", spent, *p.BudgetMonthlyUSD),
				StatusCode: 402,
			}, true
		}
	}
	return Decision{}, false
}

// windowSpent reads the persisted cost_usd for the given budget window. Errors
// are logged and treated as zero (fail-open) to avoid blocking traffic during
// transient PG outages.
func (s *service) windowSpent(ctx context.Context, apiKeyID, windowType string, now time.Time) (float64, error) {
	start, _ := windowFor(windowType, now)
	w, err := s.usage.GetWindow(ctx, apiKeyID, windowType, start)
	if err != nil {
		log.WithError(err).WithField("window", windowType).Debug("policy: read budget window failed")
		return 0, err
	}
	return w.CostUSD, nil
}

// Consume records the supplied tokens+cost against every applicable budget
// window. The principal is resolved to an api_key_id via the cache (or a
// fresh lookup if the entry has expired).
func (s *service) Consume(ctx context.Context, principal, model string, tokens TokenCounts) error {
	if !s.Active() || principal == "" {
		return nil
	}
	snap, err := s.snapshot(ctx, principal)
	if err != nil {
		return err
	}
	if snap.Policy == nil {
		return nil // no policy → no window increments
	}
	total := tokens.Total
	if total == 0 {
		total = tokens.Input + tokens.Output + tokens.Reasoning
	}
	now := time.Now()
	for _, wt := range []string{store.WindowTypeHourly, store.WindowTypeWeekly, store.WindowTypeMonthly} {
		start, end := windowFor(wt, now)
		if err := s.usage.UpsertWindow(ctx, snap.APIKey.ID, wt, start, end, 1, total, tokens.Cost); err != nil {
			log.WithError(err).WithField("window", wt).Debug("policy: upsert window failed")
			// Continue: a single failed upsert should not break the others.
		}
	}
	return nil
}

// InvalidateKey drops the cached snapshot for the supplied principal. Called
// after management-API mutations.
func (s *service) InvalidateKey(_ context.Context, principal string) error {
	if s == nil || principal == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, principal)
	// Also clear the in-memory RPM/hourly entries so reactivated keys are
	// not falsely throttled.
	s.rpmWindow.Forget(principal)
	s.hourlyWindow.Forget(principal)
	return nil
}

// InvalidateAll flushes the cache map and sliding-window counters.
func (s *service) InvalidateAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = make(map[string]APIKeySnapshot)
	s.rpmWindow.Reset()
	s.hourlyWindow.Reset()
}

func (s *service) snapshot(ctx context.Context, principal string) (APIKeySnapshot, error) {
	s.mu.RLock()
	cached, ok := s.cache[principal]
	s.mu.RUnlock()
	if ok && time.Since(cached.LoadedAt) < s.cfg.CacheTTL {
		return cached, nil
	}
	hash := store.HashSecret(principal)
	key, policy, err := s.apiKeys.LookupByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			// Not a PG-managed key (legacy file-only key, or unknown
			// principal): defer to AuthMiddleware's existing verdict.
			return APIKeySnapshot{APIKey: store.APIKey{Status: store.APIKeyStatusActive}}, nil
		}
		return APIKeySnapshot{}, err
	}
	snap := APIKeySnapshot{APIKey: *key, Policy: policy, LoadedAt: time.Now()}
	s.mu.Lock()
	s.cache[principal] = snap
	s.mu.Unlock()
	return snap, nil
}

// fromStorePolicy widens a store.Policy to the local Policy alias. The two
// structs are intentionally identical in shape; this helper exists only so
// the model access evaluator can take the package-local type.
func fromStorePolicy(p store.Policy) Policy {
	return Policy{
		APIKeyID:         p.APIKeyID,
		RPMLimit:         p.RPMLimit,
		HourlyRateLimit:  p.HourlyRateLimit,
		BudgetHourlyUSD:  p.BudgetHourlyUSD,
		BudgetWeeklyUSD:  p.BudgetWeeklyUSD,
		BudgetMonthlyUSD: p.BudgetMonthlyUSD,
		AllowedModels:    p.AllowedModels,
		BlockedModels:    p.BlockedModels,
	}
}

// safePrefix returns the first few characters of a secret for log fields,
// never enough to reconstruct it. Used only for log correlation.
func safePrefix(principal string) string {
	if len(principal) <= 4 {
		return strings.Repeat("*", len(principal))
	}
	return principal[:4] + "..."
}

// Compile-time assertion that *service implements PolicyService.
var _ PolicyService = (*service)(nil)
