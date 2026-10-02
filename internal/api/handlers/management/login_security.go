package management

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// loginSettingsCacheTTL bounds how often the auth path reads the policy from
// PG. Writes refresh the cache immediately, so the TTL only governs changes
// made outside this process.
const loginSettingsCacheTTL = 30 * time.Second

// loginEventBuffer is the size of the bounded async event channel. Events are
// dropped (best-effort) when the buffer is full.
const loginEventBuffer = 512

// loginEventFlushSize triggers an immediate batch flush.
const loginEventFlushSize = 32

// loginSecurityStore is the subset of *store.ManagementLoginStore the handler
// depends on. Declared as an interface so the auth path can be unit tested
// with a fake and so a nil store is a clean feature check.
type loginSecurityStore interface {
	GetLoginSettings(ctx context.Context) (store.LoginSecuritySettings, error)
	UpsertLoginSettings(ctx context.Context, s store.LoginSecuritySettings) (store.LoginSecuritySettings, error)
	RecordLoginEvents(ctx context.Context, events []store.LoginEvent) error
	ListLoginEvents(ctx context.Context, f store.LoginEventFilter, page, pageSize int) ([]store.LoginEvent, int64, error)
	PurgeLoginEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	ClearLoginEvents(ctx context.Context) (int64, error)
}

// loginDecision is the pure outcome of evaluating one management credential.
type loginDecision struct {
	allowed bool
	status  int
	message string
	outcome string
}

// SetLoginSecurityStore wires the PG-backed management-login store. No-op for a
// nil store so the feature stays dormant when PG is not configured. Seeds the
// settings cache, starts the async event recorder, and starts the event
// retention sweep.
func (h *Handler) SetLoginSecurityStore(s *store.ManagementLoginStore) {
	if h == nil || s == nil {
		return
	}
	h.mu.Lock()
	h.pgLogin = s
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if set, err := s.GetLoginSettings(ctx); err == nil {
		h.loginSettingsMu.Lock()
		h.loginSettings = set
		h.loginSettingsAt = time.Now()
		h.loginSettingsMu.Unlock()
	}
	cancel()

	h.startLoginEventRecorder()
	h.startLoginEventRetentionSweep()
}

// currentLoginSettings returns the cached policy without ever blocking on a
// database read. When the cache is stale it triggers at most one background
// refresh (single-flight) and returns the currently cached value immediately.
// It never returns a zero-value policy: a cold cache is seeded synchronously
// with the built-in defaults (and stamped) so the auth path always has a
// usable, fail-open policy.
func (h *Handler) currentLoginSettings() store.LoginSecuritySettings {
	h.loginSettingsMu.RLock()
	cached := h.loginSettings
	stamped := h.loginSettingsAt
	h.loginSettingsMu.RUnlock()

	if stamped.IsZero() {
		// Never seeded: apply defaults synchronously (no DB) so we never
		// return a zero-value policy, then let the async refresh pick up any
		// configured policy from PG.
		h.loginSettingsMu.Lock()
		if h.loginSettingsAt.IsZero() {
			h.loginSettings = store.DefaultLoginSecuritySettings()
			h.loginSettingsAt = time.Now()
		}
		cached = h.loginSettings
		h.loginSettingsMu.Unlock()
		h.refreshLoginSettingsAsync()
		return cached
	}

	if time.Since(stamped) >= loginSettingsCacheTTL {
		h.refreshLoginSettingsAsync()
	}
	return cached
}

// refreshLoginSettingsAsync refreshes the cached policy from PG in the
// background. A single-flight flag guarantees at most one refresh runs at a
// time so a burst of auth requests cannot stampede the database. A read
// failure falls back to the built-in fail-open defaults (never keeps a
// possibly-strict last-known policy).
func (h *Handler) refreshLoginSettingsAsync() {
	if !h.loginSettingsRefresh.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer h.loginSettingsRefresh.Store(false)

		h.mu.Lock()
		s := h.pgLogin
		h.mu.Unlock()
		if s == nil {
			h.setLoginSettingsCache(store.DefaultLoginSecuritySettings())
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		set, err := s.GetLoginSettings(ctx)
		if err != nil {
			// Fail open on a read error and stamp the cache so an outage does
			// not trigger a DB read for every subsequent request.
			h.setLoginSettingsCache(store.DefaultLoginSecuritySettings())
			return
		}
		h.setLoginSettingsCache(set)
	}()
}

// setLoginSettingsCache stores a freshly persisted policy.
func (h *Handler) setLoginSettingsCache(set store.LoginSecuritySettings) {
	h.loginSettingsMu.Lock()
	h.loginSettings = set
	h.loginSettingsAt = time.Now()
	h.loginSettingsMu.Unlock()
}

// startLoginEventRecorder launches the single background writer that drains
// the bounded event channel into batched PG inserts. Idempotent.
func (h *Handler) startLoginEventRecorder() {
	h.loginRecorderOnce.Do(func() {
		ch := make(chan store.LoginEvent, loginEventBuffer)
		h.mu.Lock()
		h.loginEventCh = ch
		h.mu.Unlock()

		go func() {
			batch := make([]store.LoginEvent, 0, loginEventFlushSize)
			flush := func() {
				if len(batch) == 0 {
					return
				}
				h.mu.Lock()
				s := h.pgLogin
				h.mu.Unlock()
				if s == nil {
					batch = batch[:0]
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := s.RecordLoginEvents(ctx, batch); err != nil {
					log.WithError(err).Warn("management login: failed to record login events")
				}
				cancel()
				batch = batch[:0]
			}
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case ev := <-ch:
					batch = append(batch, ev)
					if len(batch) >= loginEventFlushSize {
						flush()
					}
				case <-ticker.C:
					flush()
				}
			}
		}()
	})
}

// shouldRecordLoginEvent reports whether an outcome should be persisted under
// the given policy. Successful outcomes are suppressed when log_successes is
// disabled; every other outcome is always recorded.
func shouldRecordLoginEvent(set store.LoginSecuritySettings, outcome string) bool {
	if outcome == store.LoginOutcomeSuccess {
		return set.LogSuccesses
	}
	return true
}

// recordLoginEvent enqueues a login event, best-effort. No-op when PG is not
// configured or when success logging is disabled.
func (h *Handler) recordLoginEvent(ev store.LoginEvent) {
	if h == nil {
		return
	}
	h.mu.Lock()
	ch := h.loginEventCh
	h.mu.Unlock()
	if ch == nil {
		return
	}
	if !shouldRecordLoginEvent(h.currentLoginSettings(), ev.Outcome) {
		return
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	select {
	case ch <- ev:
	default:
		log.Debug("management login: event buffer full; dropping event")
	}
}

// startLoginEventRetentionSweep periodically deletes events older than the
// configured retention window. Started only once and only when PG is
// configured.
func (h *Handler) startLoginEventRetentionSweep() {
	h.loginSweepOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				h.mu.Lock()
				s := h.pgLogin
				h.mu.Unlock()
				if s == nil {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				cutoff := time.Now().Add(-h.currentLoginSettings().Retention())
				n, err := s.PurgeLoginEventsBefore(ctx, cutoff)
				cancel()
				if err != nil {
					log.WithError(err).Warn("management login: event retention sweep failed")
				} else if n > 0 {
					log.WithField("deleted", n).Debug("management login: purged aged events")
				}
			}
		}()
	})
}

// evaluateManagementKey is the pure credential evaluation. It performs no
// mutation of the ban state (callers apply side effects), so a static-secret
// miss that is later rescued by management-token auth never accrues a ban.
func (h *Handler) evaluateManagementKey(clientIP string, localClient bool, provided string) loginDecision {
	cfg := h.cfg
	var (
		allowRemote bool
		secretHash  string
	)
	if cfg != nil {
		allowRemote = cfg.RemoteManagement.AllowRemote
		secretHash = cfg.RemoteManagement.SecretKey
	}
	if h.allowRemoteOverride {
		allowRemote = true
	}
	envSecret := h.envSecret

	set := h.currentLoginSettings()
	if set.Enabled {
		now := time.Now()
		h.attemptsMu.Lock()
		ai := h.failedAttempts[clientIP]
		banned := ai != nil && !ai.blockedUntil.IsZero() && now.Before(ai.blockedUntil)
		var remaining time.Duration
		if banned {
			remaining = ai.blockedUntil.Sub(now).Round(time.Second)
		}
		h.attemptsMu.Unlock()
		if banned {
			return loginDecision{
				allowed: false,
				status:  http.StatusForbidden,
				message: fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining),
				outcome: store.LoginOutcomeBanned,
			}
		}
	}

	if !localClient && !allowRemote {
		return loginDecision{false, http.StatusForbidden, "remote management disabled", store.LoginOutcomeRemoteDisabled}
	}
	if secretHash == "" && envSecret == "" {
		return loginDecision{false, http.StatusForbidden, "remote management key not set", ""}
	}
	if provided == "" {
		return loginDecision{false, http.StatusUnauthorized, "missing management key", store.LoginOutcomeMissingKey}
	}
	if localClient {
		if lp := h.localPassword; lp != "" {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(lp)) == 1 {
				return loginDecision{true, 0, "", store.LoginOutcomeSuccess}
			}
		}
	}
	if envSecret != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(envSecret)) == 1 {
		return loginDecision{true, 0, "", store.LoginOutcomeSuccess}
	}
	if secretHash == "" || bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(provided)) != nil {
		return loginDecision{false, http.StatusUnauthorized, "invalid management key", store.LoginOutcomeInvalidKey}
	}
	return loginDecision{true, 0, "", store.LoginOutcomeSuccess}
}

// banAccountingOutcome reports whether a failed outcome should increment the
// brute-force counter. Remote-disabled and not-configured outcomes do not.
func banAccountingOutcome(outcome string) bool {
	switch outcome {
	case store.LoginOutcomeMissingKey, store.LoginOutcomeInvalidKey:
		return true
	}
	return false
}

// applyManagementFailure records one failed attempt and returns the recorded
// outcome (ban_started when the threshold is reached) and the resulting count.
func (h *Handler) applyManagementFailure(clientIP string, fallbackOutcome string) (string, int) {
	set := h.currentLoginSettings()
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()

	ai := h.failedAttempts[clientIP]
	if ai == nil {
		ai = &attemptInfo{}
		h.failedAttempts[clientIP] = ai
	}
	now := time.Now()
	if !ai.blockedUntil.IsZero() && !now.Before(ai.blockedUntil) {
		ai.blockedUntil = time.Time{}
		ai.count = 0
		ai.windowStart = time.Time{}
	}
	ai.lastActivity = now

	if !set.Enabled {
		return fallbackOutcome, ai.count
	}
	if w := set.FailureWindow(); w > 0 {
		if ai.windowStart.IsZero() || now.Sub(ai.windowStart) > w {
			ai.count = 0
			ai.windowStart = now
		}
	}
	ai.count++
	if ai.count >= set.MaxFailedAttempts {
		ai.blockedUntil = now.Add(set.BanDuration())
		ai.count = 0
		ai.windowStart = time.Time{}
		return store.LoginOutcomeBanStarted, 0
	}
	return fallbackOutcome, ai.count
}

// resetManagementAttempts clears the failure/ban state for an IP.
func (h *Handler) resetManagementAttempts(clientIP string) {
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	if ai := h.failedAttempts[clientIP]; ai != nil {
		ai.count = 0
		ai.blockedUntil = time.Time{}
		ai.windowStart = time.Time{}
	}
}
