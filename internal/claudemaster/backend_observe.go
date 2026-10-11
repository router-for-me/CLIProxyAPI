package claudemaster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Routing and quota events. Everything here runs with the selector's lock held (the *Locked methods) or
// takes it briefly, and logs only profile names, hashed conversation tags, numbers and fixed words.

const defaultSnapshotInterval = 5 * time.Minute

// reserveFraction is the share of a weekly allowance kept for continuations of conversations already on
// that account (see shouldMoveCleanWorkLocked).
const reserveFraction = 0.9

// selectorStats is what the selector counts between two snapshots.
type selectorStats struct {
	picks     map[string]uint64
	switches  uint64
	backup    uint64
	refusals  uint64
	relayed   uint64 // requests that ended with Anthropic's own error, which the session receives as sent
	rateLimit uint64
}

func profileDisplayName(c BackendCredential) string {
	return safeProfileName(c.Name, c.AuthID)
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// safeProfileName is a profile's name for logs and metrics. It is the profile's own name when there is
// one. Otherwise it is NEVER derived from a credential file name: those carry the account's email address
// (claude-<id>-<email>.json). A fallback is a short hash, which tells profiles apart and nothing else.
func safeProfileName(name, authID string) string {
	if plainName.MatchString(name) {
		return name
	}
	if plainName.MatchString(authID) {
		return authID // an internal runtime id such as claude-master-3-1
	}
	sum := sha256.Sum256([]byte(authID))
	return "profile-" + hex.EncodeToString(sum[:3])
}

func (s *backendSeriesSelector) profileNameLocked(authID string) string {
	if authID == "" {
		return "none"
	}
	if authID == s.backupAuthID && authID != "" {
		return "api-backup"
	}
	if name := s.names[authID]; name != "" {
		return name
	}
	return safeProfileName("", authID)
}

func (s *backendSeriesSelector) profileName(authID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profileNameLocked(authID)
}

// quotaBand names where a weekly allowance stands: ok, reserve (the kept-back last tenth) or exhausted.
func quotaBand(q backendWeeklyQuota) string {
	switch {
	case !q.known:
		return "unknown"
	case q.used >= 1:
		return "exhausted"
	case q.used >= reserveFraction:
		return "reserve"
	default:
		return "ok"
	}
}

func usedPct(q backendWeeklyQuota) any {
	if !q.known {
		return "unknown"
	}
	return math.Round(q.used*1000) / 10
}

func (s *backendSeriesSelector) resetsIn(q backendWeeklyQuota) any {
	if !q.known || q.resetsAt.IsZero() {
		return "unknown"
	}
	d := q.resetsAt.Sub(s.nowOrReal())
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

func (s *backendSeriesSelector) nowOrReal() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *backendSeriesSelector) statsLocked() *selectorStats {
	if s.stats == nil {
		s.stats = &selectorStats{picks: make(map[string]uint64)}
	}
	return s.stats
}

// noteSelection records one routing decision. It runs after Pick, outside the selector lock.
func (s *backendSeriesSelector) noteSelection(sessionID, model string, picked *coreauth.Auth, err error, took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.statsLocked()
	if err != nil {
		if s.limiter == nil {
			s.limiter = newEvery(time.Minute)
		}
		// Anthropic answered with an error and the session receives it as sent: the pool did have an
		// account. Logged apart from a real refusal, so the NoAccountAvailable alarm (a metric filter on
		// the refusal line) fires only when no account could take the request. Before this, Anthropic's
		// own 404s on the scheduler's retry held that alarm in ALARM (2026-10-10).
		var refusal *backendRefusalError
		if errors.As(err, &refusal) && refusal.relayed {
			stats.relayed++
			if s.limiter.allow("relayed:" + err.Error()) {
				lg().Info("request ended with Anthropic's error, relayed as sent", "session", sessionTag(sessionID), "model", modelLabel(model), "reason", err.Error())
			}
			return
		}
		stats.refusals++
		// The messages are this package's own fixed sentences, never upstream text.
		if s.limiter.allow("refusal:" + err.Error()) {
			lg().Warn("no inference account could be chosen", "session", sessionTag(sessionID), "model", modelLabel(model), "reason", err.Error())
		}
		return
	}
	if picked == nil {
		return
	}
	name := s.profileNameLocked(picked.ID)
	stats.picks[name]++
	isBackup := picked.ID == s.backupAuthID && s.backupAuthID != ""
	if isBackup {
		stats.backup++
	}
	observePick(name, took, isBackup)
	lg().Debug("account chosen", "session", sessionTag(sessionID), "model", modelLabel(model), "profile", name, "took", took.Round(time.Microsecond).String())
}

// noteBoundLocked runs when a conversation is bound: a change of account is the event that matters.
func (s *backendSeriesSelector) noteBoundLocked(sessionID, previousID string, hadPrevious bool, auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	switch {
	case !hadPrevious:
		lg().Debug("conversation bound", "session", sessionTag(sessionID), "profile", s.profileNameLocked(auth.ID))
	case previousID != auth.ID:
		s.noteSwitchLocked(sessionID, previousID, auth.ID)
	}
}

func (s *backendSeriesSelector) switchReasonLocked(fromID, toID string) string {
	switch {
	case s.backupAuthID != "" && toID == s.backupAuthID:
		return "subscriptions_exhausted"
	case s.backupAuthID != "" && fromID == s.backupAuthID:
		return "subscription_capacity_returned"
	case s.quotaBlockedLocked(fromID):
		return "rate_limited"
	}
	from := s.currentQuotaLocked(fromID)
	switch {
	case from.known && from.used >= 1:
		return "weekly_exhausted"
	case from.known && from.used >= reserveFraction:
		return "reserve_reached"
	}
	return "rebalanced"
}

func (s *backendSeriesSelector) noteSwitchLocked(sessionID, fromID, toID string) {
	s.statsLocked().switches++
	observeSwitch(s.profileNameLocked(fromID), s.profileNameLocked(toID), s.switchReasonLocked(fromID, toID))
	lg().Info("inference account switched",
		"session", sessionTag(sessionID),
		"from", s.profileNameLocked(fromID), "to", s.profileNameLocked(toID),
		"reason", s.switchReasonLocked(fromID, toID),
		"from_used_pct", usedPct(s.currentQuotaLocked(fromID)), "to_used_pct", usedPct(s.currentQuotaLocked(toID)))
}

// noteQuotaLocked records a stored quota snapshot and logs when its band changes.
func (s *backendSeriesSelector) noteQuotaLocked(authID string, q backendWeeklyQuota) {
	if s.quotaBands == nil {
		s.quotaBands = make(map[string]string, len(s.authIDs))
	}
	band, previous := quotaBand(q), s.quotaBands[authID]
	s.quotaBands[authID] = band
	name := s.profileNameLocked(authID)
	lg().Debug("quota observed", "profile", name, "used_pct", usedPct(q), "resets_in", s.resetsIn(q))
	switch {
	case previous == "" && band != "ok":
		lg().Info("quota band", "profile", name, "band", band, "used_pct", usedPct(q), "resets_in", s.resetsIn(q))
	case previous != "" && previous != band:
		lg().Info("quota band changed", "profile", name, "from", previous, "to", band, "used_pct", usedPct(q), "resets_in", s.resetsIn(q))
	}
}

// noteResultLocked logs what a finished upstream call says about a profile. Only the status and the
// error CODE are used: never the message, which can carry upstream text.
func (s *backendSeriesSelector) noteResultLocked(result coreauth.Result, cooldown time.Duration, until time.Time, retryAfter bool) {
	name := s.profileNameLocked(result.AuthID)
	if result.Success {
		if s.rateLimited[result.AuthID] && !s.quotaBlockedLocked(result.AuthID) {
			delete(s.rateLimited, result.AuthID)
			lg().Info("profile available again", "profile", name)
		}
		return
	}
	if result.Error == nil {
		return
	}
	status := result.Error.HTTPStatus
	if cooldown > 0 {
		if s.rateLimited == nil {
			s.rateLimited = make(map[string]bool)
		}
		s.rateLimited[result.AuthID] = true
		s.statsLocked().rateLimit++
		observeRateLimited(name)
		lg().Info("profile rate limited", "profile", name, "status", status,
			"cooldown", cooldown.Round(time.Second).String(), "until", until.UTC().Format(time.RFC3339), "retry_after_header", retryAfter)
		return
	}
	if s.limiter == nil {
		s.limiter = newEvery(time.Minute)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if s.limiter.allow(fmt.Sprintf("auth:%s:%d", result.AuthID, status)) {
			lg().Warn("profile credential rejected by Anthropic; the login may need to be redone", "profile", name, "status", status)
		}
	case status >= 500:
		if s.limiter.allow(fmt.Sprintf("5xx:%s:%d", result.AuthID, status)) {
			lg().Warn("Anthropic server error", "profile", name, "status", status)
		}
	default:
		lg().Debug("upstream call failed", "profile", name, "status", status)
	}
}

// ---------------------------------------------------------------- snapshots

// logSnapshot writes the per-profile quota state and what routing did since the last snapshot.
func (s *backendSeriesSelector) logSnapshot(interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.initializeLocked()
	for _, id := range s.authIDs {
		q := s.currentQuotaLocked(id)
		blocked := "no"
		if s.quotaBlockedLocked(id) {
			blocked = s.quotaBlockedUntil[id].Sub(s.nowOrReal()).Round(time.Second).String()
		}
		fiveHourPct, fiveHourResets := "unknown", "unknown"
		if five, known := s.fiveHourNowLocked(id, s.nowOrReal()); known {
			fiveHourPct, fiveHourResets = fmt.Sprintf("%.0f", five.UsedFraction*100), "no window"
			if !five.ResetsAt.IsZero() {
				fiveHourResets = five.ResetsAt.Sub(s.nowOrReal()).Round(time.Minute).String()
			}
		}
		lg().Info("quota", "profile", s.profileNameLocked(id), "used_pct", usedPct(q), "band", quotaBand(q),
			"resets_in", s.resetsIn(q), "rate_limited_for", blocked,
			"five_hour_used_pct", fiveHourPct, "five_hour_resets_in", fiveHourResets)
	}
	stats := s.statsLocked()
	names := make([]string, 0, len(stats.picks))
	for name := range stats.picks {
		names = append(names, name)
	}
	sort.Strings(names)
	attrs := []any{"interval", interval.String(), "switches", stats.switches, "backup_picks", stats.backup,
		"rate_limited", stats.rateLimit, "refused", stats.refusals, "relayed", stats.relayed, "backup", s.backupStateLocked()}
	for _, name := range names {
		attrs = append(attrs, "requests_"+name, stats.picks[name])
	}
	lg().Info("routing summary", attrs...)
	s.stats = &selectorStats{picks: make(map[string]uint64)}
}

// startBackendObservation logs a snapshot now and then every interval until ctx ends. A negative interval
// turns it off. The returned channel closes when the goroutine has stopped.
func startBackendObservation(ctx context.Context, selector *backendSeriesSelector, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if selector == nil || interval < 0 {
		close(done)
		return done
	}
	if interval == 0 {
		interval = defaultSnapshotInterval
	}
	var once sync.Once
	go func() {
		defer once.Do(func() { close(done) })
		selector.logSnapshot(interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				selector.logSnapshot(interval)
			}
		}
	}()
	return done
}

// noteUsagePoll logs a failed usage poll, at most once a minute per profile: a poll that keeps failing is
// one line a minute, not sixty.
func (s *backendSeriesSelector) noteUsagePoll(authID, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limiter == nil {
		s.limiter = newEvery(time.Minute)
	}
	observeUsagePoll(s.profileNameLocked(authID), "failed")
	if s.limiter.allow("usage:" + authID) {
		lg().Warn("subscription usage poll failed", "profile", s.profileNameLocked(authID), "why", why)
	}
}

// metricsSnapshot is the selector's live state for the gauges: per profile the weekly quota, any
// rate-limit cooldown and the login's expiry (read from its saved credential).
func (s *backendSeriesSelector) metricsSnapshot(stores []*backendStore) stateSnapshot {
	expiry := make(map[string]float64, len(stores))
	for _, store := range stores {
		if disk, err := store.diskSnapshot(); err == nil {
			if t, errParse := time.Parse(time.RFC3339, credentialString(disk, "expired")); errParse == nil {
				expiry[store.runtimeID] = time.Until(t).Seconds()
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := stateSnapshot{}
	if s.stopped {
		return snap
	}
	s.initializeLocked()
	now := s.nowOrReal()
	for _, id := range s.authIDs {
		q := s.currentQuotaLocked(id)
		p := profileState{Name: s.profileNameLocked(id), Used: q.used, UsedKnown: q.known, Band: -1}
		switch quotaBand(q) {
		case "ok":
			p.Band = 0
		case "reserve":
			p.Band = 1
		case "exhausted":
			p.Band = 2
		}
		if q.known && !q.resetsAt.IsZero() {
			p.ResetsKnown, p.ResetsInSeconds = true, max(q.resetsAt.Sub(now).Seconds(), 0)
		}
		if s.quotaBlockedLocked(id) {
			p.BlockedSeconds = max(s.quotaBlockedUntil[id].Sub(now).Seconds(), 0)
		}
		if five, known := s.fiveHourNowLocked(id, now); known {
			p.FiveHourKnown, p.FiveHourUsed = true, five.UsedFraction
			if !five.ResetsAt.IsZero() {
				p.FiveHourResetsKnown, p.FiveHourResetsIn = true, max(five.ResetsAt.Sub(now).Seconds(), 0)
			}
		}
		if v, ok := expiry[id]; ok {
			p.TokenKnown, p.TokenExpiresIn = true, v
		}
		snap.Profiles = append(snap.Profiles, p)
	}
	if s.sessions != nil {
		snap.Sessions, snap.HasSessions = int64(s.sessions.Len()), true
	}
	return snap
}

// metricsState lets the backend feed the quota gauges.
func (b *Backend) metricsState() stateSnapshot {
	if b == nil || b.seriesSelector == nil {
		return stateSnapshot{}
	}
	return b.seriesSelector.metricsSnapshot(b.stores)
}
