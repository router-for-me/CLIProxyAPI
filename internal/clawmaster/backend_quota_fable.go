package clawmaster

import (
	"fmt"
	"net/http"
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
	s.shortenGuessedFableHoldLocked(authID, quota.ResetsAt)
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
		// The poll gives the reset to the fraction of a second and the headers to the second: resets
		// within a minute are the same week.
		sameWeek := quota.ResetsAt.Sub(existing.ResetsAt).Abs() < time.Minute
		switch {
		case sameWeek && quota.UsedFraction < existing.UsedFraction:
			return
		case !sameWeek && quota.ResetsAt.Before(existing.ResetsAt):
			return
		}
	}
	s.fable[authID] = quota
	s.shortenGuessedFableHoldLocked(authID, quota.ResetsAt)
}

// shortenGuessedFableHoldLocked: a hold that is only a backoff ends at the Fable reset once one is known, if
// that comes first. A figure below 100% alone does not end a hold: only Anthropic's answer does.
func (s *backendSeriesSelector) shortenGuessedFableHoldLocked(authID string, reset time.Time) {
	// A reset already past (a poll that started before the refusal) says nothing about this hold.
	if !s.fableHoldGuessed[authID] || !reset.After(s.nowOrReal()) || !s.fableBlockedLocked(authID) {
		return
	}
	if floor := s.fableHoldFloor[authID]; floor.After(reset) {
		reset = floor
	}
	if reset.Before(s.fableBlockedUntil[authID]) {
		s.fableBlockedUntil[authID] = reset
	}
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

// observeFableRejection holds a subscription back from Fable requests until its Fable allowance resets.
// Nothing else changes: the subscription keeps serving every other model.
func (s *backendSeriesSelector) observeFableRejection(authID string, headers http.Header) {
	if strings.TrimSpace(authID) == "" || authID == s.backupAuthID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	now := s.nowOrReal()
	known, _ := s.fableNowLocked(authID, now)
	if s.fableBlockedUntil == nil {
		s.fableBlockedUntil = make(map[string]time.Time, len(s.authIDs))
	}
	if s.fableBackoffLevel == nil {
		s.fableBackoffLevel = make(map[string]int, len(s.authIDs))
	}
	already := s.fableBlockedLocked(authID)
	until, firm := fableRejectionUntil(headers, known.ResetsAt, now)
	if s.fableHoldGuessed == nil {
		s.fableHoldGuessed = make(map[string]bool, len(s.authIDs))
	}
	guessed := !firm
	if firm {
		if s.fableHoldFloor == nil {
			s.fableHoldFloor = make(map[string]time.Time, len(s.authIDs))
		}
		s.fableHoldFloor[authID] = until
		// A reset Anthropic or the poll gave replaces an estimate, earlier or later.
		delete(s.fableBackoffLevel, authID)
	} else {
		// No reset known: back off as the core does for a quota with no deadline, one second doubling
		// to half an hour, and never shorten a hold already in force.
		level := s.fableBackoffLevel[authID]
		s.fableBackoffLevel[authID] = level + 1
		until = now.Add(min(time.Second<<min(level, 11), 30*time.Minute))
		if previous := s.fableBlockedUntil[authID]; previous.After(until) {
			// The hold in force outlasts the backoff: it stays, and stays what it was.
			until, guessed = previous, s.fableHoldGuessed[authID]
		}
	}
	s.fableHoldGuessed[authID] = guessed
	s.fableBlockedUntil[authID] = until
	if !already {
		lg().Info("profile out of its weekly Fable allowance; Fable requests go to other subscriptions",
			"profile", s.profileNameLocked(authID), "until", until.UTC().Format(time.RFC3339))
	}
}

func (s *backendSeriesSelector) fableBlockedLocked(authID string) bool {
	until, blocked := s.fableBlockedUntil[authID]
	if blocked && !s.nowOrReal().Before(until) {
		delete(s.fableBlockedUntil, authID)
		return false
	}
	return blocked
}

// fableBandRank orders subscriptions for a Fable request by what the poll and headers say is left of the
// allowance, as backendQuotaBandRank does for the week: ok, unknown, reserve (last tenth), used up.
func (s *backendSeriesSelector) fableBandRankLocked(authID string) int {
	quota, known := s.fableNowLocked(authID, s.nowOrReal())
	switch {
	case !known:
		return 1
	case quota.UsedFraction >= 1:
		return 3
	case quota.UsedFraction >= 0.9:
		return 2
	}
	return 0
}

// fableBlockedRefusalLocked: this request cannot leave a subscription whose Fable allowance is used up.
func (s *backendSeriesSelector) fableBlockedRefusalLocked(authID string) error {
	until := s.fableBlockedUntil[authID]
	return &backendRefusalError{
		kind:    backendRefusalQuota,
		fable:   true,
		reason:  fmt.Sprintf("subscription %s has used its weekly Fable allowance (Anthropic answered 429) until %s, and this conversation cannot move to another subscription; switch model with /model, or start a new conversation", s.profileNameLocked(authID), until.UTC().Format(time.RFC3339)),
		resetAt: until,
		authID:  authID,
	}
}

// fableAvailableAtLocked: the earliest a subscription can take a Fable request again, counting its Fable
// hold, an ordinary rate limit and its reserve (one reserved at 100 never comes back); zero when none is
// held back by any.
func (s *backendSeriesSelector) fableAvailableAtLocked() time.Time {
	var earliest time.Time
	for _, authID := range s.authIDs {
		var until time.Time
		if s.reservedLocked(authID) {
			if until = s.reserveBackLocked(authID); until.IsZero() {
				continue
			}
		}
		if s.fableBlockedLocked(authID) && s.fableBlockedUntil[authID].After(until) {
			until = s.fableBlockedUntil[authID]
		}
		if s.quotaBlockedLocked(authID) && s.quotaBlockedUntil[authID].After(until) {
			until = s.quotaBlockedUntil[authID]
		}
		if !until.IsZero() && (earliest.IsZero() || until.Before(earliest)) {
			earliest = until
		}
	}
	return earliest
}

// fableRefusalLocked: no subscription has Fable left. The earliest a subscription can take Fable again.
func (s *backendSeriesSelector) fableRefusalLocked() error {
	return &backendRefusalError{
		kind:    backendRefusalQuota,
		fable:   true,
		reason:  "every subscription has used its weekly Fable allowance or is rate limited by Anthropic; switch model with /model",
		resetAt: s.fableAvailableAtLocked(),
	}
}

// allFableBlockedLocked: Anthropic refuses every subscription Fable. Only then is a Fable request told its
// limit is reached; a subscription held back by an ordinary rate limit comes back sooner, so that is a
// plain rate-limit refusal the client retries.
func (s *backendSeriesSelector) allFableBlockedLocked() bool {
	for _, authID := range s.authIDs {
		if !s.fableBlockedLocked(authID) {
			return false
		}
	}
	return len(s.authIDs) > 0
}

// noteFableServed: Anthropic served a Fable request, so a later refusal without a reset starts its backoff
// from the beginning.
func (s *backendSeriesSelector) noteFableServed(authID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fableBackoffLevel, authID)
}
