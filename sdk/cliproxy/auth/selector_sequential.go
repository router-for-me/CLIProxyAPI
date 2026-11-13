package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// DefaultSequentialReservePercent is the share of each window kept back while
// another account in the pool still has room.
const DefaultSequentialReservePercent = 5.0

// sequentialUsageEpsilon absorbs float rounding in 0..1 readings scaled to percent.
const sequentialUsageEpsilon = 1e-9

// SequentialSelector sends all traffic for a provider to one account at a time.
//
// The pool stays on its current account until any of that account's usage
// windows reaches 100 minus the reserve. It then moves forward, wrapping round,
// to the next account below that mark. A reset on an account the pool has left
// does not bring traffic back. When every account is at the reserve, the
// reserves are spent in pool order until an account resets.
//
// Pool order is credential priority (highest first), then credential ID.
type SequentialSelector struct {
	reserve   float64
	statePath string
	now       func() time.Time

	mu      sync.Mutex
	loaded  bool
	current map[string]sequentialPosition
}

// sequentialPosition records the current account and its place in pool order,
// so the next account can be found even after the current one leaves the pool.
type sequentialPosition struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

type sequentialState struct {
	Current map[string]sequentialPosition `json:"current"`
}

// NewSequentialSelector builds a sequential selector. reservePercent outside
// [0, 100) falls back to DefaultSequentialReservePercent. statePath, when set,
// keeps each pool's current account across restarts.
func NewSequentialSelector(reservePercent float64, statePath string) *SequentialSelector {
	if !(reservePercent >= 0 && reservePercent < 100) {
		reservePercent = DefaultSequentialReservePercent
	}
	return &SequentialSelector{
		reserve:   reservePercent,
		statePath: strings.TrimSpace(statePath),
		now:       time.Now,
		current:   make(map[string]sequentialPosition),
	}
}

type sequentialPoolContextKey struct{}

// withSequentialPool passes every credential of the pool, including ones that
// are cooling down or were already tried, so the selector can read the current
// account's usage even when it cannot serve this request.
func withSequentialPool(ctx context.Context, pool []*Auth) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sequentialPoolContextKey{}, pool)
}

func sequentialPoolFromContext(ctx context.Context) []*Auth {
	if ctx == nil {
		return nil
	}
	pool, _ := ctx.Value(sequentialPoolContextKey{}).([]*Auth)
	return pool
}

// Pick implements Selector. auths are the credentials that may serve this request.
func (s *SequentialSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = provider
	_ = model
	_ = opts
	allowed := make(map[string]*Auth, len(auths))
	for _, candidate := range auths {
		if candidate != nil && candidate.ID != "" {
			allowed[candidate.ID] = candidate
		}
	}
	if len(allowed) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	now := s.clock()
	ordered := sequentialOrder(sequentialPoolFromContext(ctx), auths)
	key := sequentialPoolKey(ordered)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	position, hasPosition := s.current[key]
	var current *Auth
	if hasPosition {
		for _, candidate := range ordered {
			if candidate.ID == position.ID {
				current = candidate
				break
			}
		}
	}
	usable := func(candidate *Auth) bool { return allowed[candidate.ID] != nil }

	if current != nil && s.hasHeadroom(current, now) {
		if selected := allowed[current.ID]; selected != nil {
			return selected, nil
		}
		// The current account still has room but cannot take this request (it is
		// cooling down or was already tried). Serve the request elsewhere and keep
		// the pool where it is.
		return allowed[s.fallbackLocked(ordered, position, true, usable, now).ID], nil
	}
	if hasPosition && current == nil {
		// The current account is outside this pool (it does not serve this model,
		// or it was disabled or removed). Serve elsewhere without moving the pool.
		return allowed[s.fallbackLocked(ordered, position, true, usable, now).ID], nil
	}

	// No current account yet, or it has reached the reserve: move forward.
	next := sequentialFirstAfter(ordered, position, hasPosition, func(candidate *Auth) bool {
		return usable(candidate) && s.hasHeadroom(candidate, now)
	})
	reason := "reached the reserve"
	if !hasPosition {
		reason = "first selection"
	}
	if next == nil && current != nil && s.hasSpare(current, now) {
		// Every account is at the reserve and the current one still has some
		// left: keep spending it. A request it cannot take is served elsewhere
		// without moving the pool, so the pool does not bounce between accounts.
		if selected := allowed[current.ID]; selected != nil {
			return selected, nil
		}
		return allowed[s.fallbackLocked(ordered, position, false, usable, now).ID], nil
	}
	if next == nil {
		next = s.fallbackLocked(ordered, position, false, usable, now)
		reason = "every account is at the reserve"
	}
	s.moveLocked(key, position, hasPosition, next, reason)
	return allowed[next.ID], nil
}

// fallbackLocked chooses an account when the pool's current account cannot be
// used for this request. With afterCurrent it first looks forward for an account
// with room; otherwise it goes straight to spending reserves in pool order.
func (s *SequentialSelector) fallbackLocked(ordered []*Auth, position sequentialPosition, afterCurrent bool, usable func(*Auth) bool, now time.Time) *Auth {
	if afterCurrent {
		if next := sequentialFirstAfter(ordered, position, true, func(candidate *Auth) bool {
			return usable(candidate) && s.hasHeadroom(candidate, now)
		}); next != nil {
			return next
		}
	}
	for _, candidate := range ordered {
		if usable(candidate) && s.hasSpare(candidate, now) {
			return candidate
		}
	}
	for _, candidate := range ordered {
		if usable(candidate) {
			return candidate
		}
	}
	return nil
}

// hasHeadroom reports whether every live window is below the reserve mark.
// An account with no reading yet counts as having room.
func (s *SequentialSelector) hasHeadroom(auth *Auth, now time.Time) bool {
	used, ok := quotaWindowUsage(auth, now)
	return !ok || used < 100-s.reserve-sequentialUsageEpsilon
}

// hasSpare reports whether every live window is below its limit.
func (s *SequentialSelector) hasSpare(auth *Auth, now time.Time) bool {
	used, ok := quotaWindowUsage(auth, now)
	return !ok || used < 100-sequentialUsageEpsilon
}

// ShouldRelease reports whether a session pinned to auth should leave it: the
// account has reached the reserve and another account in pool has room.
func (s *SequentialSelector) ShouldRelease(auth *Auth, pool []*Auth) bool {
	if s == nil || auth == nil {
		return false
	}
	now := s.clock()
	if s.hasHeadroom(auth, now) {
		return false
	}
	for _, candidate := range pool {
		if candidate != nil && candidate.ID != auth.ID && s.hasHeadroom(candidate, now) {
			return true
		}
	}
	return false
}

func (s *SequentialSelector) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Current returns each pool's current account ID, keyed by pool (for example
// "claude" or "codex"). A pool that has not picked an account yet is absent.
func (s *SequentialSelector) Current() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	current := make(map[string]string, len(s.current))
	for key, position := range s.current {
		current[key] = position.ID
	}
	return current
}

func (s *SequentialSelector) moveLocked(key string, from sequentialPosition, hadPosition bool, to *Auth, reason string) {
	if to == nil || (hadPosition && from.ID == to.ID) {
		return
	}
	s.current[key] = sequentialPosition{ID: to.ID, Priority: authPriority(to)}
	if hadPosition {
		log.Infof("sequential routing: %s moved from %s to %s (%s)", key, from.ID, to.ID, reason)
	} else {
		log.Infof("sequential routing: %s starts on %s (%s)", key, to.ID, reason)
	}
	s.saveLocked()
}

func (s *SequentialSelector) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.statePath == "" {
		return
	}
	data, errRead := os.ReadFile(s.statePath)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.Warnf("sequential routing: read state %s: %v", s.statePath, errRead)
		}
		return
	}
	var state sequentialState
	if errDecode := json.Unmarshal(data, &state); errDecode != nil {
		log.Warnf("sequential routing: decode state %s: %v", s.statePath, errDecode)
		return
	}
	for key, position := range state.Current {
		if strings.TrimSpace(position.ID) != "" {
			s.current[key] = position
		}
	}
}

func (s *SequentialSelector) saveLocked() {
	if s.statePath == "" {
		return
	}
	data, errEncode := json.Marshal(sequentialState{Current: s.current})
	if errEncode != nil {
		log.Warnf("sequential routing: encode state: %v", errEncode)
		return
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.statePath), 0o700); errMkdir != nil {
		log.Warnf("sequential routing: create state directory: %v", errMkdir)
		return
	}
	temp := s.statePath + ".tmp"
	if errWrite := os.WriteFile(temp, data, 0o600); errWrite != nil {
		log.Warnf("sequential routing: write state: %v", errWrite)
		return
	}
	if errRename := os.Rename(temp, s.statePath); errRename != nil {
		log.Warnf("sequential routing: save state: %v", errRename)
	}
}

// sequentialOrder merges the pool and the request's candidates, then sorts them
// by priority (highest first) and ID.
func sequentialOrder(pool, auths []*Auth) []*Auth {
	seen := make(map[string]struct{}, len(pool)+len(auths))
	ordered := make([]*Auth, 0, len(pool)+len(auths))
	for _, list := range [][]*Auth{pool, auths} {
		for _, candidate := range list {
			if candidate == nil || candidate.ID == "" {
				continue
			}
			if _, duplicate := seen[candidate.ID]; duplicate {
				continue
			}
			seen[candidate.ID] = struct{}{}
			ordered = append(ordered, candidate)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return sequentialBefore(sequentialPosition{ID: ordered[i].ID, Priority: authPriority(ordered[i])},
			sequentialPosition{ID: ordered[j].ID, Priority: authPriority(ordered[j])})
	})
	return ordered
}

func sequentialBefore(a, b sequentialPosition) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.ID < b.ID
}

// sequentialPoolKey identifies a pool by the providers it contains.
func sequentialPoolKey(ordered []*Auth) string {
	providers := make([]string, 0, 1)
	seen := make(map[string]struct{}, 1)
	for _, candidate := range ordered {
		provider := strings.ToLower(strings.TrimSpace(candidate.Provider))
		if _, duplicate := seen[provider]; duplicate {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return strings.Join(providers, "+")
}

// sequentialFirstAfter returns the first account matching match, starting just
// after position in pool order and wrapping round. The account at position is
// skipped. Without a position it starts at the top of the pool.
func sequentialFirstAfter(ordered []*Auth, position sequentialPosition, hasPosition bool, match func(*Auth) bool) *Auth {
	start := 0
	if hasPosition {
		start = len(ordered)
		for i, candidate := range ordered {
			if sequentialBefore(position, sequentialPosition{ID: candidate.ID, Priority: authPriority(candidate)}) {
				start = i
				break
			}
		}
	}
	for offset := 0; offset < len(ordered); offset++ {
		candidate := ordered[(start+offset)%len(ordered)]
		if hasPosition && candidate.ID == position.ID {
			continue
		}
		if match(candidate) {
			return candidate
		}
	}
	return nil
}
