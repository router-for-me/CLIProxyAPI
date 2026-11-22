package clawmaster

import (
	"strings"
	"time"
)

// Each subscription's weekly Fable allowance, tracked beside the weekly and five-hour figures. The usage
// poll is authoritative (it can report a lower figure or a moved reset) unless a response's 7d_oi headers
// arrived while it was in flight; headers only ever raise the figure within a window, and a later reset
// starts a new window.

func (s *backendSeriesSelector) fablePollRevision(authID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fableRevision[authID]
}

// observePolledFable records a polled Fable allowance, unless headers newer than the poll's start came in.
func (s *backendSeriesSelector) observePolledFable(authID string, quota ClaudeFableQuota, revision uint64) bool {
	if strings.TrimSpace(authID) == "" || authID == s.backupAuthID {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.fableRevision[authID] != revision {
		return false
	}
	if s.fable == nil {
		s.fable = make(map[string]ClaudeFableQuota, len(s.authIDs))
	}
	quota.UsedFraction = min(max(quota.UsedFraction, 0), 1)
	s.fable[authID] = quota
	return true
}

// observeFableHeaders records the Fable allowance a response reported.
func (s *backendSeriesSelector) observeFableHeaders(authID string, quota ClaudeFableQuota) {
	if strings.TrimSpace(authID) == "" || authID == s.backupAuthID || quota.ResetsAt.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.fable == nil {
		s.fable = make(map[string]ClaudeFableQuota, len(s.authIDs))
	}
	if s.fableRevision == nil {
		s.fableRevision = make(map[string]uint64, len(s.authIDs))
	}
	s.fableRevision[authID]++
	quota.UsedFraction = min(max(quota.UsedFraction, 0), 1)
	if existing, known := s.fable[authID]; known && !existing.ResetsAt.IsZero() {
		switch {
		case quota.ResetsAt.Before(existing.ResetsAt):
			return
		case quota.ResetsAt.Equal(existing.ResetsAt) && quota.UsedFraction < existing.UsedFraction:
			return
		}
	}
	s.fable[authID] = quota
}

// fableNowLocked is an account's Fable allowance as of now: once its reset has passed, nothing of the
// new week is used, whatever the last reading said.
func (s *backendSeriesSelector) fableNowLocked(authID string, now time.Time) (quota ClaudeFableQuota, known bool) {
	quota, known = s.fable[authID]
	if known && !quota.ResetsAt.IsZero() && !now.Before(quota.ResetsAt) {
		quota = ClaudeFableQuota{}
	}
	return quota, known
}
