package auth

import (
	"context"
	"sort"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	defaultResetPriorityUrgentThreshold = 15 * time.Minute
	defaultResetPriorityJitterWindow    = 60 * time.Second
	defaultResetPriorityMaxKeys         = 4096
)

// ResetPrioritySelector prefers the available credential whose quota window
// resets soonest, so the remaining quota is consumed before it expires.
//
// Outside the urgent window it delegates to a regular selector (weighted or
// round-robin). Inside the urgent window it orders candidates by the distance
// to their reset instant. Credentials whose resets fall within jitterWindow of
// the closest one are rotated with a per-provider/model cursor to avoid
// hammering a single credential with the same reset watermark.
type ResetPrioritySelector struct {
	mu              sync.Mutex
	fallback        Selector
	urgentThreshold time.Duration
	jitterWindow    time.Duration
	nowFunc         func() time.Time
	jitterCursor    map[string]string
	maxKeys         int
}

type resetPriorityCandidate struct {
	auth  *Auth
	delta time.Duration
}

// NewResetPrioritySelector builds a reset-priority selector. Non-positive
// durations fall back to the package defaults (15m urgent, 60s jitter). A nil
// fallback resolves to the first available credential.
func NewResetPrioritySelector(fallback Selector, urgentThreshold, jitterWindow time.Duration) *ResetPrioritySelector {
	return &ResetPrioritySelector{
		fallback:        fallback,
		urgentThreshold: urgentThreshold,
		jitterWindow:    jitterWindow,
		jitterCursor:    make(map[string]string),
	}
}

// Pick selects an available credential with the nearest upcoming quota reset.
func (s *ResetPrioritySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := s.now()
	available, errAvailable := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	switch len(available) {
	case 0:
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	case 1:
		return available[0], nil
	}

	urgent := make([]resetPriorityCandidate, 0, len(available))
	for _, candidate := range available {
		reset, ok := candidate.resetTimeFromQuota(now)
		if !ok {
			continue
		}
		delta := reset.Sub(now)
		if delta <= 0 || delta > s.urgent() {
			continue
		}
		urgent = append(urgent, resetPriorityCandidate{auth: candidate, delta: delta})
	}

	if len(urgent) == 0 {
		return s.pickFallback(ctx, provider, model, opts, available)
	}

	sort.Slice(urgent, func(i, j int) bool {
		if urgent[i].delta != urgent[j].delta {
			return urgent[i].delta < urgent[j].delta
		}
		return urgent[i].auth.ID < urgent[j].auth.ID
	})
	if len(urgent) == 1 || urgent[1].delta-urgent[0].delta > s.jitter() {
		return urgent[0].auth, nil
	}

	// Only credentials whose reset distance is within jitterWindow of the
	// closest reset form the rotation group; the rest would be picked later.
	group := make([]*Auth, 0, len(urgent))
	closest := urgent[0].delta
	for _, candidate := range urgent {
		if candidate.delta-closest <= s.jitter() {
			group = append(group, candidate.auth)
		}
	}
	sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
	return s.rotateJitterGroup(provider, model, group), nil
}

func (s *ResetPrioritySelector) pickFallback(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, available []*Auth) (*Auth, error) {
	fallback := s.fallbackSelector()
	if fallback == nil {
		return available[0], nil
	}
	picked, errPick := fallback.Pick(ctx, provider, model, opts, available)
	if errPick != nil {
		return nil, errPick
	}
	if picked == nil {
		return nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	return picked, nil
}

// rotateJitterGroup advances the per-provider/model cursor within a group of
// credentials whose reset instants are effectively tied. group must be sorted
// by auth ID, matching the rotation convention of the other built-in selectors.
func (s *ResetPrioritySelector) rotateJitterGroup(provider, model string, group []*Auth) *Auth {
	key := provider + ":" + canonicalModelKey(model)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jitterCursor == nil {
		s.jitterCursor = make(map[string]string)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = defaultResetPriorityMaxKeys
	}
	if _, exists := s.jitterCursor[key]; !exists && len(s.jitterCursor) >= limit {
		s.jitterCursor = make(map[string]string)
	}
	picked := group[successorIndex(group, s.jitterCursor[key])]
	s.jitterCursor[key] = picked.ID
	return picked
}

func (s *ResetPrioritySelector) now() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func (s *ResetPrioritySelector) urgent() time.Duration {
	if s != nil && s.urgentThreshold > 0 {
		return s.urgentThreshold
	}
	return defaultResetPriorityUrgentThreshold
}

func (s *ResetPrioritySelector) jitter() time.Duration {
	if s != nil && s.jitterWindow > 0 {
		return s.jitterWindow
	}
	return defaultResetPriorityJitterWindow
}

func (s *ResetPrioritySelector) fallbackSelector() Selector {
	if s == nil {
		return nil
	}
	return s.fallback
}
