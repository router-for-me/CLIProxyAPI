package claudemaster

import (
	"context"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const backendQuotaPollInterval = time.Minute

// startBackendQuotaPolling queries accounts concurrently, with at most one poll
// per account in flight. A stalled account cannot prevent other accounts from
// being polled again. No selection locks or inference streams are held during
// HTTP. The caller cancels ctx and joins done before releasing
// profile locks, including any credential refresh needed by a usage request.
// Tests supply ticks to drive rounds without depending on wall-clock timers.
func startBackendQuotaPolling(ctx context.Context, manager *coreauth.Manager, selector *backendSeriesSelector, authIDs []string, quotaRequest ClaudeQuotaRequestFunc, ticks <-chan time.Time) <-chan struct{} {
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
		if ticks == nil {
			ticker := time.NewTicker(backendQuotaPollInterval)
			defer ticker.Stop()
			ticks = ticker.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-ticks:
				if !open || ctx.Err() != nil {
					return
				}
				for _, authID := range ids {
					if inFlight[authID] {
						continue
					}
					inFlight[authID] = true
					workers.Add(1)
					go func(id string) {
						defer workers.Done()
						loadBackendWeeklyQuotas(ctx, manager, selector, []string{id}, quotaRequest)
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
	return true
}
