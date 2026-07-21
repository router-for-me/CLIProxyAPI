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
	users   *store.UserStore
	cfg     ServiceConfig

	mu    sync.RWMutex
	cache map[string]APIKeySnapshot // keyed by principal (plaintext key)

	rpmWindow        *slidingWindow
	hourlyWindow     *slidingWindow
	userRPMWindow    *slidingWindow
	userHourlyWindow *slidingWindow
	userTPMWindow    *slidingWindow
	parallel         *parallelLimiter

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
		apiKeys:          apiKeys,
		usage:            usage,
		cfg:              defaultServiceConfig(cfg),
		cache:            make(map[string]APIKeySnapshot),
		rpmWindow:        newSlidingWindow(),
		hourlyWindow:     newSlidingWindow(),
		userRPMWindow:    newSlidingWindow(),
		userHourlyWindow: newSlidingWindow(),
		userTPMWindow:    newSlidingWindow(),
		parallel:         newParallelLimiter(),
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
	}
}

// SetUserStore wires the per-user budget/RPM enforcement store. Optional: when
// left unset (nil), the per-user enforcement path is skipped but per-key
// enforcement still runs as before. The windows are allocated eagerly so the
// cleanup loop can sweep them even when no UserStore is attached.
func (s *service) SetUserStore(users *store.UserStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.users = users
	s.mu.Unlock()
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
				if s.userRPMWindow != nil {
					s.userRPMWindow.Cleanup(s.cfg.WindowRetention)
				}
				if s.userHourlyWindow != nil {
					s.userHourlyWindow.Cleanup(s.cfg.WindowRetention)
				}
				if s.userTPMWindow != nil {
					s.userTPMWindow.Cleanup(s.cfg.WindowRetention)
				}
				if s.parallel != nil {
					s.parallel.Compact()
				}
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
		if decision, denied := s.checkBudget(ctx, snap.APIKey.ID, snap.Policy, snap.APIKey.CreatedAt); denied {
			return decision, nil
		}
	}
	// Per-user enforcement. This acts as the fallback budget/RPM cap for
	// API keys whose own Policy leaves a limit unset (LiteLLM workflow): a
	// key can be created with all-zero per-key caps, and the owning
	// internal user's max_budget / rpm_limit / tpm_limit / Models grant
	// still apply. Bypassed for admin roles so operators are not throttled
	// by their own user-level caps.
	if snap.InternalUser != nil && !isAdminRole(snap.InternalUser.UserRole) {
		u := snap.InternalUser
		// Auto-reset spend window when the configured duration has elapsed.
		if u.BudgetResetAt != nil && !time.Now().Before(*u.BudgetResetAt) {
			if errReset := s.users.ResetSpend(ctx, u.ID); errReset == nil {
				// Refresh the in-scope copy so the budget cap below reads zero.
				refreshed, errGet := s.users.Get(ctx, u.ID)
				if errGet == nil {
					u = &refreshed
					snap.InternalUser = u
				}
			} else {
				log.WithError(errReset).WithField("user_id", u.ID).
					Debug("policy: reset internal user spend failed")
			}
		}
		// Model access (allowed list). Denied models block here.
		if len(u.Models) > 0 && !modelAllowed(u.Models, model) {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("model %q is not permitted for internal user %q", model, u.ID),
				StatusCode: 403,
			}, nil
		}
		if u.RPMLimit != nil && *u.RPMLimit > 0 {
			if !s.userRPMWindow.AllowAndIncrement(u.ID, int(*u.RPMLimit), time.Minute) {
				return Decision{
					Allow:      false,
					Reason:     fmt.Sprintf("user rate limit exceeded: %d requests per minute", *u.RPMLimit),
					StatusCode: 429,
				}, nil
			}
		}
		// TPM check (read-only precheck). The actual token count is unknown
		// at admission; reject only when the running total is already over
		// the limit. Consume then accrues the real total via AllowAndAddN.
		if u.TPMLimit != nil && *u.TPMLimit > 0 {
			if !s.userTPMWindow.PreAddCheck(u.ID, int(*u.TPMLimit), time.Minute) {
				return Decision{
					Allow:      false,
					Reason:     fmt.Sprintf("user TPM limit exceeded: %d tokens per minute", *u.TPMLimit),
					StatusCode: 429,
				}, nil
			}
		}
		if u.MaxBudget != nil && *u.MaxBudget > 0 {
			if u.Spend >= *u.MaxBudget {
				return Decision{
					Allow:      false,
					Reason:     fmt.Sprintf("user budget exceeded: %.2f USD spent of %.2f USD limit", u.Spend, *u.MaxBudget),
					StatusCode: 402,
				}, nil
			}
		}
	}
	// Touch last_used_at asynchronously so the hot path is not delayed.
	go s.apiKeys.TouchLastUsed(context.Background(), snap.APIKey.ID)
	return Decision{Allow: true}, nil
}

// checkBudget returns (decision, denied=true) when any budget cap has been
// exceeded. The decision carries the most restrictive cap that was hit so the
// caller can surface a meaningful error. Window start/end are anchored to the
// API key's created_at so windows roll from the moment the key was issued.
func (s *service) checkBudget(ctx context.Context, apiKeyID string, p *store.Policy, createdAt time.Time) (Decision, bool) {
	if p == nil {
		return Decision{}, false
	}
	now := time.Now()
	if p.BudgetHourlyUSD != nil && *p.BudgetHourlyUSD > 0 {
		if spent, _ := s.windowSpent(ctx, apiKeyID, store.WindowTypeHourly, createdAt, now); spent >= *p.BudgetHourlyUSD {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("hourly budget exceeded: %.2f USD spent of %.2f USD limit", spent, *p.BudgetHourlyUSD),
				StatusCode: 402,
			}, true
		}
	}
	if p.BudgetWeeklyUSD != nil && *p.BudgetWeeklyUSD > 0 {
		if spent, _ := s.windowSpent(ctx, apiKeyID, store.WindowTypeWeekly, createdAt, now); spent >= *p.BudgetWeeklyUSD {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("weekly budget exceeded: %.2f USD spent of %.2f USD limit", spent, *p.BudgetWeeklyUSD),
				StatusCode: 402,
			}, true
		}
	}
	if p.BudgetMonthlyUSD != nil && *p.BudgetMonthlyUSD > 0 {
		if spent, _ := s.windowSpent(ctx, apiKeyID, store.WindowTypeMonthly, createdAt, now); spent >= *p.BudgetMonthlyUSD {
			return Decision{
				Allow:      false,
				Reason:     fmt.Sprintf("monthly budget exceeded: %.2f USD spent of %.2f USD limit", spent, *p.BudgetMonthlyUSD),
				StatusCode: 402,
			}, true
		}
	}
	return Decision{}, false
}

// windowSpent reads the persisted cost_usd for the given budget window. The
// window start is anchored to the supplied createdAt (api key or internal
// user) so Check and Consume resolve the same window_start. Errors are logged
// and treated as zero (fail-open) to avoid blocking traffic during transient
// PG outages.
func (s *service) windowSpent(ctx context.Context, apiKeyID, windowType string, anchor, now time.Time) (float64, error) {
	start, _ := windowFor(windowType, anchor, now)
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
	// Resolve cost when the caller did not supply one. The usage plugin path
	// passes Cost=0 (it does not have access to the pricing table); computing
	// it here keeps budget enforcement accurate and the dashboard's
	// usage-windows cost_usd up to date. Missing pricing ⇒ 0 (same behavior as
	// ComputeCost), which is safe because checkBudget guards on cap > 0.
	cost := tokens.Cost
	if cost == 0 && model != "" {
		if pricing, err := s.usage.GetPricing(ctx, model); err == nil {
			cost = computeCostFromTokens(pricing, tokens)
		}
	}
	now := time.Now()
	for _, wt := range []string{store.WindowTypeHourly, store.WindowTypeWeekly, store.WindowTypeMonthly} {
		start, end := windowFor(wt, snap.APIKey.CreatedAt, now)
		if err := s.usage.UpsertWindow(ctx, snap.APIKey.ID, wt, start, end, 1, total, cost); err != nil {
			log.WithError(err).WithField("window", wt).Debug("policy: upsert window failed")
			// Continue: a single failed upsert should not break the others.
		}
	}
	// Per-user accounting (best-effort). Failures do not abort the request;
	// the dashboard's benchmark numbers will simply not advance until the
	// next successful Consume.
	if snap.InternalUser != nil {
		uid := snap.InternalUser.ID
		// Anchor per-user windows to the internal user's created_at so the
		// dashboard's "Budget vs usage" card reflects the same rolling
		// window the per-key path enforces.
		userAnchor := snap.InternalUser.CreatedAt
		for _, wt := range []string{store.WindowTypeHourly, store.WindowTypeWeekly, store.WindowTypeMonthly} {
			start, end := windowFor(wt, userAnchor, now)
			if err := s.users.UpsertUserWindow(ctx, uid, wt, start, end, 1, total, cost); err != nil {
				log.WithError(err).WithField("window", wt).Debug("policy: upsert user window failed")
			}
		}
		if cost > 0 {
			if err := s.users.IncrementSpend(ctx, uid, cost); err != nil {
				log.WithError(err).WithField("user_id", uid).Debug("policy: increment user spend failed")
			}
		}
		// Accrue TPM tokens to the in-memory sliding window. Post-request
		// increment — the Check path's read-only precheck already rejected
		// requests when the running total was over the cap.
		if snap.InternalUser.TPMLimit != nil && *snap.InternalUser.TPMLimit > 0 && total > 0 {
			s.userTPMWindow.AllowAndAddN(uid, int(*snap.InternalUser.TPMLimit), time.Minute, total)
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
	if s.userRPMWindow != nil {
		s.userRPMWindow.Forget(principal)
	}
	if s.userHourlyWindow != nil {
		s.userHourlyWindow.Forget(principal)
	}
	if s.userTPMWindow != nil {
		s.userTPMWindow.Forget(principal)
	}
	if s.parallel != nil {
		s.parallel.Forget(principal)
	}
	return nil
}

// InvalidateAll flushes the cache map and sliding-window counters.
func (s *service) InvalidateAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = make(map[string]APIKeySnapshot)
	s.rpmWindow.Reset()
	s.hourlyWindow.Reset()
	if s.userRPMWindow != nil {
		s.userRPMWindow.Reset()
	}
	if s.userHourlyWindow != nil {
		s.userHourlyWindow.Reset()
	}
	if s.userTPMWindow != nil {
		s.userTPMWindow.Reset()
	}
	if s.parallel != nil {
		s.parallel.Reset()
	}
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
	// Resolve the owning internal user (if any) so Check/Consume can enforce
	// per-user budget/RPM. Failures are non-fatal: per-key enforcement still
	// runs. Lookups are cached alongside the snapshot so the per-request hot
	// path pays at most one extra DB round-trip every CacheTTL.
	s.mu.RLock()
	users := s.users
	s.mu.RUnlock()
	if users != nil && key.UserID != "" && snap.APIKey.UserID != "" {
		if u, errLookup := users.Get(ctx, key.UserID); errLookup == nil {
			snap.InternalUser = &u
		} else if !errors.Is(errLookup, store.ErrInternalUserNotFound) {
			log.WithError(errLookup).WithField("user_id", key.UserID).
				Debug("policy: lookup internal user failed; skipping per-user enforcement")
		}
	}
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

// AcquireParallel reserves an in-flight slot for the principal under the
// effective max_parallel_requests cap. The cap is resolved as min(key cap,
// user cap fallback) where key cap = snap.Policy.MaxParallelRequests and
// user cap = snap.InternalUser.MaxParallelRequests. Either may be nil
// (unset = unlimited); nil on both means no cap (always allow).
//
// The middleware MUST call ReleaseParallel once the request completes (or
// fails) so the counter does not leak. Idempotent on failed Acquire.
func (s *service) AcquireParallel(ctx context.Context, principal string) (bool, error) {
	if s == nil || principal == "" {
		return true, nil
	}
	snap, err := s.snapshot(ctx, principal)
	if err != nil {
		// Fail open on infra errors — mirrors the Check() guard.
		log.WithError(err).WithField("principal_prefix", safePrefix(principal)).
			Debug("policy: AcquireParallel lookup failed; skipping cap")
		return true, nil
	}
	limit := effectiveParallelLimit(snap)
	if limit <= 0 {
		return true, nil
	}
	if s.parallel.Acquire(principal, limit) {
		return true, nil
	}
	// Build a more informative reason for the middleware's 429 surface.
	// Returned to the caller; the middleware attaches reason to the response.
	return false, nil
}

// ReleaseParallel releases a slot previously acquired by AcquireParallel.
// Safe to call after a failed Acquire and never decrements below zero.
func (s *service) ReleaseParallel(_ context.Context, principal string) error {
	if s == nil || principal == "" {
		return nil
	}
	s.parallel.Release(principal)
	return nil
}

// effectiveParallelLimit returns the lower of the key-level and per-user
// max_parallel_requests caps. Zero means "no cap / unlimited".
func effectiveParallelLimit(snap APIKeySnapshot) int {
	limits := []int{}
	if snap.Policy != nil && snap.Policy.MaxParallelRequests != nil && *snap.Policy.MaxParallelRequests > 0 {
		limits = append(limits, *snap.Policy.MaxParallelRequests)
	}
	if snap.InternalUser != nil && snap.InternalUser.MaxParallelRequests != nil && *snap.InternalUser.MaxParallelRequests > 0 {
		limits = append(limits, *snap.InternalUser.MaxParallelRequests)
	}
	if len(limits) == 0 {
		return 0
	}
	min := limits[0]
	for _, v := range limits[1:] {
		if v < min {
			min = v
		}
	}
	return min
}
