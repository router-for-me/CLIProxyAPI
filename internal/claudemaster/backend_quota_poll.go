package claudemaster

import (
	"context"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// backendQuotaPollInterval is how often each subscription's usage is asked for, jittered by
// backendPollJitterFraction. Anthropic limits the usage endpoint per account: polled every minute,
// every other request was refused with 429 (2026-10-10, about half of all polls on every profile).
// Busy accounts are also updated from every inference response's rate-limit headers; the poll is
// what keeps idle accounts and reset times current.
const backendQuotaPollInterval = 3 * time.Minute

// backendQuotaPollMaxBackoff caps how long a 429's Retry-After can hold an account's poll back.
const backendQuotaPollMaxBackoff = 15 * time.Minute

// startBackendQuotaPolling queries accounts concurrently, with at most one poll
// per account in flight. A stalled account cannot prevent other accounts from
// being polled again. No selection locks or inference streams are held during
// HTTP. The caller cancels ctx and joins done before releasing
// profile locks, including any credential refresh needed by a usage request.
// Tests supply ticks to drive rounds without depending on wall-clock timers.
// Without them, rounds come on a jittered interval and each account waits its
// own phase after the tick (backendQuotaPollPhase), so the accounts do not ask
// at the same instant. An account Anthropic answered with 429 is skipped until
// the backoff it set has passed (deferUsagePoll).
func startBackendQuotaPolling(ctx context.Context, manager *coreauth.Manager, selector *backendSeriesSelector, authIDs []string, quotaRequest ClaudeQuotaRequestFunc, ticks <-chan time.Time) <-chan struct{} {
	var phase func(i, n int) time.Duration
	if ticks == nil {
		ticks = jitteredTicks(ctx, backendQuotaPollInterval, backendPollJitterFraction)
		phase = func(i, n int) time.Duration { return backendQuotaPollPhase(i, n, backendQuotaPollInterval) }
	}
	return pollBackendQuotaRounds(ctx, manager, selector, authIDs, quotaRequest, ticks, phase)
}

func pollBackendQuotaRounds(ctx context.Context, manager *coreauth.Manager, selector *backendSeriesSelector, authIDs []string, quotaRequest ClaudeQuotaRequestFunc, ticks <-chan time.Time, phase func(i, n int) time.Duration) <-chan struct{} {
	done := make(chan struct{})
	ids := append([]string(nil), authIDs...)
	go func() {
		var workers sync.WaitGroup
		defer func() {
			workers.Wait()
			close(done)
		}()
		completed := make(chan string, len(ids))
		inFlight := make(map[string]bool, len(ids))
		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-ticks:
				if !open || ctx.Err() != nil {
					return
				}
				for i, authID := range ids {
					if inFlight[authID] || !selector.usagePollDue(authID) {
						continue
					}
					var wait time.Duration
					if phase != nil {
						wait = phase(i, len(ids))
					}
					inFlight[authID] = true
					workers.Add(1)
					go func(id string) {
						defer workers.Done()
						if sleepContext(ctx, wait) {
							loadBackendWeeklyQuotas(ctx, manager, selector, []string{id}, quotaRequest)
						}
						completed <- id
					}(authID)
				}
			case authID := <-completed:
				delete(inFlight, authID)
			}
		}
	}()
	return done
}

// deferUsagePoll holds an account's usage poll back after Anthropic refused one with 429: for its
// Retry-After when it sent one, else for one more interval, at most backendQuotaPollMaxBackoff.
func (s *backendSeriesSelector) deferUsagePoll(authID string, retryAfter time.Duration) {
	wait := max(retryAfter, backendQuotaPollInterval)
	wait = min(wait, backendQuotaPollMaxBackoff)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usagePollAfter == nil {
		s.usagePollAfter = make(map[string]time.Time)
	}
	s.usagePollAfter[authID] = s.nowOrReal().Add(wait)
}

// usagePollDue reports whether an account's usage may be polled now (no 429 backoff in force).
func (s *backendSeriesSelector) usagePollDue(authID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	after, deferred := s.usagePollAfter[authID]
	return !deferred || !s.nowOrReal().Before(after)
}

// observeFiveHourQuota records an account's polled five-hour window for the gauges and the log.
func (s *backendSeriesSelector) observeFiveHourQuota(authID string, quota ClaudeFiveHourQuota) {
	if strings.TrimSpace(authID) == "" || authID == s.backupAuthID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.fiveHour == nil {
		s.fiveHour = make(map[string]ClaudeFiveHourQuota, len(s.authIDs))
	}
	s.fiveHour[authID] = quota
}

// fiveHourNowLocked is an account's five-hour window as of now: once its reset has passed the
// window is over and nothing of it is used, whatever the last poll said.
func (s *backendSeriesSelector) fiveHourNowLocked(authID string, now time.Time) (quota ClaudeFiveHourQuota, known bool) {
	quota, known = s.fiveHour[authID]
	if known && !quota.ResetsAt.IsZero() && !now.Before(quota.ResetsAt) {
		quota = ClaudeFiveHourQuota{}
	}
	return quota, known
}

func (s *backendSeriesSelector) quotaPollRevision(authID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return 0
	}
	s.initializeLocked()
	return s.quotaRevision[authID]
}

// Unlike passive response headers, the usage endpoint can authoritatively
// report lower utilization or a changed reset timestamp. Apply its snapshot
// only if no newer quota observation/rejection arrived while HTTP was in flight.
// Weekly capacity alone does not prove a five-hour cooldown has cleared, so
// polling never erases credential cooldowns, non-quota failures, or bindings.
func (s *backendSeriesSelector) observePolledQuota(authID string, quota backendWeeklyQuota, revision uint64) bool {
	if strings.TrimSpace(authID) == "" || authID == s.backupAuthID || !quota.known {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	s.initializeLocked()
	if s.quotaRevision[authID] != revision {
		return false
	}
	quota.used = min(max(quota.used, 0), 1)
	s.quota[authID] = quota
	s.quotaRevision[authID]++
	s.noteQuotaLocked(authID, quota)
	observeUsagePoll(s.profileNameLocked(authID), "ok")
	return true
}
