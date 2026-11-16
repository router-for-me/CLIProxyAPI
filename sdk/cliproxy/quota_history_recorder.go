package cliproxy

import (
	"context"
	"path/filepath"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// The quota history recorder samples every credential's weekly quota once a
// minute so the console can chart the pool over time.
const (
	quotaHistoryTick         = time.Minute
	quotaHistoryCompactEvery = 24 * time.Hour
)

type quotaHistoryRecorder struct {
	store       *coreauth.QuotaHistory
	list        func() []*coreauth.Auth
	now         func() time.Time
	lastCompact time.Time
}

func (s *Service) runQuotaHistoryRecorder(ctx context.Context) {
	if s.coreManager == nil {
		return
	}
	now := time.Now()
	store := s.openQuotaHistory(now)
	coreauth.SetActiveQuotaHistory(store)
	defer func() {
		coreauth.SetActiveQuotaHistory(nil)
		if errFlush := store.Flush(); errFlush != nil {
			log.WithError(errFlush).Debug("quota history: final flush failed")
		}
		if errClose := store.Close(); errClose != nil {
			log.WithError(errClose).Warn("quota history: failed to close file")
		}
	}()
	// Opening already compacted, so the first tick only appends.
	recorder := &quotaHistoryRecorder{store: store, list: s.coreManager.List, now: time.Now, lastCompact: now}
	ticker := time.NewTicker(quotaHistoryTick)
	defer ticker.Stop()
	for {
		recorder.tick()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// openQuotaHistory opens the history file in the auth dir, falling back to a
// memory-only store when the file cannot be used.
func (s *Service) openQuotaHistory(now time.Time) *coreauth.QuotaHistory {
	s.cfgMu.RLock()
	authDir, errAuthDir := resolveCooldownStateAuthDir(s.cfg)
	s.cfgMu.RUnlock()
	if errAuthDir != nil {
		log.WithError(errAuthDir).Warn("quota history: auth dir unavailable; keeping history in memory only")
		return coreauth.NewQuotaHistory()
	}
	if authDir == "" {
		log.Warn("quota history: no auth dir; keeping history in memory only")
		return coreauth.NewQuotaHistory()
	}
	store, errOpen := coreauth.OpenQuotaHistory(filepath.Join(authDir, coreauth.QuotaHistoryFileName), now)
	if errOpen != nil {
		log.WithError(errOpen).Warn("quota history: file unavailable; keeping history in memory only")
	}
	return store
}

// tick records each credential's newest observations, appends them to the
// file, and compacts daily or right after a failed write.
func (r *quotaHistoryRecorder) tick() {
	now := r.now()
	for _, auth := range r.list() {
		r.store.Observe(auth.ID, coreauth.QuotaObservations(auth), now)
	}
	// The store itself warns about write failures, at most once an hour.
	if errFlush := r.store.Flush(); errFlush != nil {
		log.WithError(errFlush).Debug("quota history: flush failed")
	}
	if !r.store.Dirty() && now.Sub(r.lastCompact) < quotaHistoryCompactEvery {
		return
	}
	if errCompact := r.store.Compact(now); errCompact != nil {
		log.WithError(errCompact).Debug("quota history: compaction failed")
		return
	}
	r.lastCompact = now
}
