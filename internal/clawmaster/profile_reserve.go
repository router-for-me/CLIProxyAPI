package clawmaster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A profile reserve is the owner's opt-in exception to "the poll is advisory": a reserve of N on a
// subscription profile holds the last N% of its weekly allowance back. Past the threshold, once the polled
// weekly usage reaches (100 - N)% (always, for N = 100, whatever the poll says), the profile is either
// out, exactly as a subscription Anthropic rejected with a credential-scoped 429 is (reservedLocked), or,
// in last-resort mode, still in but ranked after every other subscription (lastResortLocked): used only
// when all the others are out, and still before the paid API-key backup. N = 0 or no entry is the
// ordinary behaviour. Only the weekly window counts, and the API-key backup is never reserved.

// profileReserveRetryAfter is the retry-after the pool gives for a reserved subscription that has no
// weekly reset to wait for: one held at 100 (only a change to the reserve file lifts it), or one whose
// reset is not known.
const profileReserveRetryAfter = time.Hour

// profileReserveLastResort is the one mode word a reserve line may carry: PROFILE=N last-resort.
const profileReserveLastResort = "last-resort"

// ProfileReserve is one profile's reserve: the percent of its week held back, and whether past that
// threshold it is a last resort rather than out.
type ProfileReserve struct {
	Percent    int
	LastResort bool
}

func (r ProfileReserve) String() string {
	if r.LastResort {
		return strconv.Itoa(r.Percent) + " " + profileReserveLastResort
	}
	return strconv.Itoa(r.Percent)
}

var profileReserves atomic.Pointer[map[string]ProfileReserve]

// SetProfileReserves swaps in the reserves the pool uses (profile name -> reserve) and reports whether
// they changed.
func SetProfileReserves(reserves map[string]ProfileReserve) bool {
	next := maps.Clone(reserves)
	if next == nil {
		next = map[string]ProfileReserve{}
	}
	previous := profileReserves.Swap(&next)
	if previous == nil {
		return len(next) > 0
	}
	return !maps.Equal(*previous, next)
}

// profileReserveFor is the reserve configured for a profile name, the zero reserve when none.
func profileReserveFor(name string) ProfileReserve {
	if current := profileReserves.Load(); current != nil {
		return (*current)[name]
	}
	return ProfileReserve{}
}

var profileReservePercent = regexp.MustCompile(`^[0-9]{1,3}$`)

// ParseProfileReserves reads PROFILE=N and PROFILE=N last-resort lines: N an integer from 0 to 100, a
// line starting with # a comment, blank lines ignored. Any other line (another mode word, anything after
// it), a percent outside 0..100 or a profile named twice makes the whole file invalid. The error names the
// line number and what is wrong, never the line's text.
func ParseProfileReserves(data []byte) (map[string]ProfileReserve, error) {
	reserves := make(map[string]ProfileReserve)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || ValidateProfileName(name) != nil {
			return nil, fmt.Errorf("line %d: a profile reserve must be PROFILE=N or PROFILE=N %s", i+1, profileReserveLastResort)
		}
		fields := strings.Fields(value)
		if len(fields) == 0 || !profileReservePercent.MatchString(fields[0]) {
			return nil, fmt.Errorf("line %d: the reserve of %s must be a whole percent from 0 to 100", i+1, name)
		}
		percent, err := strconv.Atoi(fields[0])
		if err != nil || percent > 100 {
			return nil, fmt.Errorf("line %d: the reserve of %s must be a whole percent from 0 to 100", i+1, name)
		}
		reserve := ProfileReserve{Percent: percent}
		switch {
		case len(fields) == 2 && fields[1] == profileReserveLastResort:
			reserve.LastResort = true
		case len(fields) != 1:
			return nil, fmt.Errorf("line %d: after the percent of %s the only word allowed is %s", i+1, name, profileReserveLastResort)
		}
		if _, seen := reserves[name]; seen {
			return nil, fmt.Errorf("line %d: %s is named twice", i+1, name)
		}
		reserves[name] = reserve
	}
	return reserves, nil
}

// LoadProfileReserves reads and parses a --profile-reserve-file.
func LoadProfileReserves(file string) (map[string]ProfileReserve, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.New("cannot read --profile-reserve-file")
	}
	return ParseProfileReserves(data)
}

// describeProfileReserves is the reserves as "profile=N" (or "profile=N/last-resort") in name order, for
// the log.
func describeProfileReserves(reserves map[string]ProfileReserve) string {
	pairs := make([]string, 0, len(reserves))
	for name, reserve := range reserves {
		pair := fmt.Sprintf("%s=%d", name, reserve.Percent)
		if reserve.LastResort {
			pair += "/" + profileReserveLastResort
		}
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	if len(pairs) == 0 {
		return "none"
	}
	return strings.Join(pairs, " ")
}

// StartProfileReserves loads the reserve file once and then watches it (WatchProfileReserves) until ctx
// ends. A file that is missing or does not parse holds no profile back and is logged; the server starts
// regardless.
func StartProfileReserves(ctx context.Context, file string, every time.Duration) {
	start := accountLabelsFingerprint(file) // before the read: a change made right after it is still seen
	if reserves, err := LoadProfileReserves(file); err != nil {
		lg().Warn("profile reserve file was not loaded; no profile is held in reserve until it loads", "why", err.Error())
	} else {
		SetProfileReserves(reserves)
		lg().Info("profile reserves loaded", "reserves", describeProfileReserves(reserves))
	}
	go watchProfileReservesFrom(ctx, file, start, every)
}

// WatchProfileReserves re-reads the reserve file when it changes (a new inode, size or modification time:
// replace it with a rename) and swaps the reserves in, so a change reaches routing without a restart. A
// change is acted on only once it has held still for one check: a file edited in place can be read
// half-written, and a prefix may parse (PROFILE=100 cut to PROFILE=1, or an empty file, which is no
// reserves). A file that does not load keeps the reserves in use, and the failure is logged once for that
// version of the file. It returns when ctx ends.
func WatchProfileReserves(ctx context.Context, file string, every time.Duration) {
	watchProfileReservesFrom(ctx, file, accountLabelsFingerprint(file), every)
}

func watchProfileReservesFrom(ctx context.Context, file, last string, every time.Duration) {
	watch := &profileReserveWatch{file: file, last: last}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		watch.check()
	}
}

// profileReserveWatch is the watcher's state: the fingerprint last acted on, and a new one seen once and
// waiting to be seen unchanged at the next check.
type profileReserveWatch struct {
	file, last, pending string
}

// check is one look at the file. A new fingerprint is loaded only when the previous check saw the same
// one, so a write still in progress at one check is never loaded.
func (w *profileReserveWatch) check() {
	now := accountLabelsFingerprint(w.file)
	if now == "" || now == w.last {
		w.pending = ""
		return
	}
	if now != w.pending {
		w.pending = now
		return
	}
	w.last, w.pending = now, ""
	reserves, err := LoadProfileReserves(w.file)
	if err != nil {
		lg().Warn("profile reserve file changed but was not loaded; the previous reserves stay", "why", err.Error())
		return
	}
	if SetProfileReserves(reserves) {
		lg().Info("profile reserves loaded", "reserves", describeProfileReserves(reserves))
	}
}

// ---------------------------------------------------------------- selection

// profileReserveLocked is the reserve configured for a subscription (none for the API-key backup, which
// is never reserved, and for a profile with no name).
func (s *backendSeriesSelector) profileReserveLocked(authID string) ProfileReserve {
	if authID == "" || authID == s.backupAuthID {
		return ProfileReserve{}
	}
	name := s.names[authID]
	if name == "" {
		return ProfileReserve{}
	}
	if s.reserveFor != nil {
		return s.reserveFor(name)
	}
	return profileReserveFor(name)
}

// pastReserveLocked reports whether the subscription is past its reserve's threshold now: always at a
// reserve of 100, else once the weekly usage the poll and response headers report reaches (100 - N)% of
// the allowance. An unknown weekly figure is not past it (the poll stays advisory otherwise), and the
// week's reset (currentQuotaLocked starts a new week at 0) brings it back. A change is logged once.
func (s *backendSeriesSelector) pastReserveLocked(authID string) (ProfileReserve, bool) {
	reserve := s.profileReserveLocked(authID)
	q := s.currentQuotaLocked(authID)
	past := reserve.Percent >= 100 || reserve.Percent > 0 && q.known && q.used >= float64(100-reserve.Percent)/100
	state := ""
	if past && reserve.LastResort {
		state = profileReserveLastResort
	} else if past {
		state = "out"
	}
	s.noteReserveLocked(authID, state, reserve, q)
	return reserve, past
}

// reservedLocked: the subscription's reserve takes it out of the pool now (past its threshold, not in
// last-resort mode).
func (s *backendSeriesSelector) reservedLocked(authID string) bool {
	reserve, past := s.pastReserveLocked(authID)
	return past && !reserve.LastResort
}

// lastResortLocked: the subscription is past its last-resort reserve, so it ranks after every other
// subscription (but before the API-key backup).
func (s *backendSeriesSelector) lastResortLocked(authID string) bool {
	reserve, past := s.pastReserveLocked(authID)
	return past && reserve.LastResort
}

// heldLocked: the subscription is out of the pool, by Anthropic's rejection of a real request (a
// credential-scoped 429 still in force) or by its reserve.
func (s *backendSeriesSelector) heldLocked(authID string) bool {
	blocked := s.quotaBlockedLocked(authID)
	return s.reservedLocked(authID) || blocked
}

// reservedNoteLocked completes an all-out refusal's reason when a reserve holds any subscription out.
func (s *backendSeriesSelector) reservedNoteLocked() string {
	for _, authID := range s.authIDs {
		if s.reservedLocked(authID) {
			return " or held out of the pool by their reserve"
		}
	}
	return ""
}

// reserveBackLocked is when a reserved subscription is next in the pool: its weekly reset for a reserve
// below 100 (zero when no reset is known), never (zero) for 100.
func (s *backendSeriesSelector) reserveBackLocked(authID string) time.Time {
	if s.profileReserveLocked(authID).Percent >= 100 {
		return time.Time{}
	}
	return s.currentQuotaLocked(authID).resetsAt
}

// reserveRefusalLocked: this request cannot leave a reserved subscription (opaque state bound to it):
// the 429 a rejected subscription gets, until the week resets, or in an hour when nothing but a change
// to the reserve file lifts it.
func (s *backendSeriesSelector) reserveRefusalLocked(authID string) *backendRefusalError {
	percent := s.profileReserveLocked(authID).Percent
	back := s.reserveBackLocked(authID)
	refusal := &backendRefusalError{kind: backendRefusalQuota, authID: authID, resetAt: back}
	name := s.profileNameLocked(authID)
	if back.IsZero() {
		refusal.resetAt = s.nowOrReal().Add(profileReserveRetryAfter)
	}
	if percent >= 100 {
		refusal.reason = fmt.Sprintf("subscription %s is held out of the pool (reserve 100%%), and this conversation's state is bound to it and cannot move to another subscription; start a new conversation", name)
	} else {
		refusal.reason = fmt.Sprintf("subscription %s has reached its reserve (%d%% of its week held out of the pool; %v%% used), and this conversation's state is bound to it and cannot move to another subscription; it returns at the weekly reset, or start a new conversation", name, percent, usedPct(s.currentQuotaLocked(authID)))
	}
	return refusal
}

// noteReserveLocked logs, once per change, a subscription entering reserve-out or last-resort, or leaving
// either. state is "", "out" or "last-resort".
func (s *backendSeriesSelector) noteReserveLocked(authID, state string, reserve ProfileReserve, q backendWeeklyQuota) {
	if s.reserveState == nil {
		if state == "" {
			return
		}
		s.reserveState = make(map[string]string, len(s.authIDs))
	}
	if s.reserveState[authID] == state {
		return
	}
	fields := []any{"profile", s.profileNameLocked(authID), "reserve_pct", reserve.Percent, "threshold_pct", 100 - reserve.Percent,
		"used_pct", usedPct(q), "resets_in", s.resetsIn(q)}
	switch state {
	case "out":
		s.reserveState[authID] = state
		lg().Info("profile held in reserve: out of the pool", fields...)
	case profileReserveLastResort:
		s.reserveState[authID] = state
		lg().Info("profile past its reserve: used only as a last resort", fields...)
	default:
		delete(s.reserveState, authID)
		delete(s.lastResortInUse, authID)
		lg().Info("profile released from reserve: back in the pool", fields...)
	}
}

// noteLastResortUse logs, once each time it starts, that a request went to a subscription past its
// last-resort reserve (every other subscription was out). A pick of any other subscription ends that
// episode, so the next last-resort use is logged again.
func (s *backendSeriesSelector) noteLastResortUse(sessionID string, picked *coreauth.Auth) {
	if picked == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.initializeLocked()
	if picked.ID == s.backupAuthID || !s.lastResortLocked(picked.ID) {
		clear(s.lastResortInUse)
		return
	}
	if s.lastResortInUse[picked.ID] {
		return
	}
	// A conversation that stayed on it while others could serve is not a last-resort use.
	for _, authID := range s.authIDs {
		if authID != picked.ID && !s.heldLocked(authID) && !s.lastResortLocked(authID) {
			return
		}
	}
	if s.lastResortInUse == nil {
		s.lastResortInUse = make(map[string]bool, len(s.authIDs))
	}
	s.lastResortInUse[picked.ID] = true
	q := s.currentQuotaLocked(picked.ID)
	reserve := s.profileReserveLocked(picked.ID)
	lg().Info("last-resort profile used: every other subscription is out", "profile", s.profileNameLocked(picked.ID),
		"session", sessionTag(sessionID), "reserve_pct", reserve.Percent, "threshold_pct", 100-reserve.Percent, "used_pct", usedPct(q))
}
