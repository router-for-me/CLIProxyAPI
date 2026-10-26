package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// QuotaResetHint is a non-secret quota observation supplied by a collector.
// It stays in memory and is never persisted with OAuth credentials.
type QuotaResetHint struct {
	ObservedAt time.Time `json:"observed_at"`
	ResetAt    time.Time `json:"reset_at"`
}

// usable reports whether the observation still describes a future reset. A
// reset timestamp does not change when later quota reads fail, so the last
// known value stays authoritative until the reset passes or a newer
// observation replaces it.
func (h QuotaResetHint) usable(now time.Time) bool {
	return !h.ObservedAt.IsZero() && !h.ObservedAt.After(now) && h.ResetAt.After(now)
}

// SoonestResetFirstSelector chooses the earliest known reset among available
// credentials in the highest manual priority tier. Equal resets, and
// credentials without a known reset, use smooth weighted round robin. Session
// affinity wraps this selector and remains authoritative for existing
// conversations.
//
// This deliberately uses the manager's route-aware custom-selector path, not
// the built-in scheduler fast path, so aliases, retries and model cooldowns are
// resolved before comparing reset observations.
type SoonestResetFirstSelector struct {
	mu       sync.RWMutex
	hints    map[string]QuotaResetHint
	fallback WeightedRoundRobinSelector
	now      func() time.Time
}

func (s *SoonestResetFirstSelector) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *SoonestResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := s.clock()
	available, err := getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	s.mu.RLock()
	preferred, _ := soonestResetGroup(available, s.hints, now)
	s.mu.RUnlock()
	if len(preferred) > 0 {
		available = preferred
	}
	return s.fallback.Pick(ctx, provider, model, opts, available)
}

// soonestResetGroup returns the candidates that share the earliest usable
// reset, preserving input order. It returns nil when no candidate has one.
func soonestResetGroup(candidates []*Auth, hints map[string]QuotaResetHint, now time.Time) ([]*Auth, time.Time) {
	var earliest time.Time
	var group []*Auth
	for _, candidate := range candidates {
		hint, ok := hints[candidate.ID]
		if !ok || !hint.usable(now) {
			continue
		}
		switch {
		case earliest.IsZero() || hint.ResetAt.Before(earliest):
			earliest = hint.ResetAt
			group = []*Auth{candidate}
		case hint.ResetAt.Equal(earliest):
			group = append(group, candidate)
		}
	}
	return group, earliest
}

// replaceResetHints installs an exact hint set. Tests use it to arrange state.
func (s *SoonestResetFirstSelector) replaceResetHints(hints map[string]QuotaResetHint) {
	copyHints := make(map[string]QuotaResetHint, len(hints))
	for id, hint := range hints {
		copyHints[id] = hint
	}
	s.mu.Lock()
	s.hints = copyHints
	s.mu.Unlock()
}

// mergeResetHints keeps earlier observations until their reset passes and
// lets newer observations replace them. Hints of removed credentials are
// dropped. It returns the number of accepted updates.
func (s *SoonestResetFirstSelector) mergeResetHints(updates map[string]QuotaResetHint, exists func(string) bool, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]QuotaResetHint, len(s.hints)+len(updates))
	for id, hint := range s.hints {
		if exists(id) && hint.ResetAt.After(now) {
			next[id] = hint
		}
	}
	accepted := 0
	for id, hint := range updates {
		if !exists(id) || !hint.usable(now) {
			continue
		}
		if current, ok := next[id]; ok && current.ObservedAt.After(hint.ObservedAt) {
			continue
		}
		next[id] = hint
		accepted++
	}
	s.hints = next
	return accepted
}

// MergeQuotaResetHints applies collector observations to the active selector.
// It does not modify auth state, manual priorities, weights, cooldowns or
// affinity bindings. The caller resolves management auth indexes to runtime IDs.
func (m *Manager) MergeQuotaResetHints(hints map[string]QuotaResetHint) (int, error) {
	if m == nil {
		return 0, errors.New("auth manager unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	resetSelector, _ := m.soonestResetSelectorLocked()
	if resetSelector == nil {
		return 0, errors.New("soonest-reset-first routing is not active")
	}
	exists := func(id string) bool { return m.auths[id] != nil }
	return resetSelector.mergeResetHints(hints, exists, resetSelector.clock()), nil
}

func (m *Manager) soonestResetSelectorLocked() (*SoonestResetFirstSelector, bool) {
	selector := m.selector
	affinity := false
	if wrapped, ok := selector.(*SessionAffinitySelector); ok {
		affinity = true
		selector = wrapped.fallback
	}
	resetSelector, _ := selector.(*SoonestResetFirstSelector)
	return resetSelector, affinity
}

// Routing states of a credential in the account-level routing order.
const (
	RoutingStateEligible    = "eligible"
	RoutingStateZeroWeight  = "zero_weight"
	RoutingStateCooldown    = "cooldown"
	RoutingStateUnavailable = "unavailable"
	RoutingStateDisabled    = "disabled"
)

// Reasons why an eligible credential holds its rank.
const (
	RoutingReasonOnlyInTier    = "only_in_tier"
	RoutingReasonEarliestReset = "earliest_reset"
	RoutingReasonLaterReset    = "later_reset"
	RoutingReasonNoReset       = "no_reset"
)

// RoutingAccountView is one credential in the account-level routing order.
// Rank 1 receives new conversations; a higher rank takes over only when every
// lower rank is unavailable. Credentials with the same rank share traffic by
// weight. Ineligible credentials have no rank.
type RoutingAccountView struct {
	AuthIndex      string     `json:"auth_index"`
	Rank           int        `json:"rank,omitempty"`
	Shared         bool       `json:"shared,omitempty"`
	Priority       int        `json:"priority"`
	Weight         int64      `json:"weight"`
	State          string     `json:"state"`
	Reason         string     `json:"reason,omitempty"`
	ResetAt        *time.Time `json:"reset_at,omitempty"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
	NextRetryAfter *time.Time `json:"next_retry_after,omitempty"`
}

// QuotaRoutingSnapshot is a read-only view of the installed selector. Hints are
// keyed by management auth index, never credential ID, path, or token.
type QuotaRoutingSnapshot struct {
	Strategy        string                          `json:"strategy"`
	SessionAffinity bool                            `json:"session_affinity"`
	GeneratedAt     time.Time                       `json:"generated_at"`
	Hints           map[string]QuotaResetHint       `json:"hints"`
	Providers       map[string][]RoutingAccountView `json:"providers,omitempty"`
}

func (m *Manager) QuotaRoutingSnapshot() QuotaRoutingSnapshot {
	snapshot := QuotaRoutingSnapshot{Strategy: "unknown", GeneratedAt: time.Now().UTC(), Hints: make(map[string]QuotaResetHint)}
	m.mu.RLock()
	defer m.mu.RUnlock()
	selector := m.selector
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		snapshot.SessionAffinity = true
		selector = affinity.fallback
	}
	switch selected := selector.(type) {
	case *SoonestResetFirstSelector:
		snapshot.Strategy = "soonest-reset-first"
		now := selected.clock()
		snapshot.GeneratedAt = now.UTC()
		selected.mu.RLock()
		defer selected.mu.RUnlock()
		for id, hint := range selected.hints {
			if auth := m.auths[id]; auth != nil && auth.Index != "" && hint.usable(now) {
				snapshot.Hints[auth.Index] = hint
			}
		}
		auths := make([]*Auth, 0, len(m.auths))
		for _, auth := range m.auths {
			auths = append(auths, auth)
		}
		snapshot.Providers = routingOrder(auths, selected.hints, now)
	case *WeightedRoundRobinSelector:
		snapshot.Strategy = "weighted-round-robin"
	case *RoundRobinSelector:
		snapshot.Strategy = "round-robin"
	case *FillFirstSelector:
		snapshot.Strategy = "fill-first"
	}
	return snapshot
}

// routingOrder ranks credentials per provider with the selector's own rules:
// strict priority tiers, then the earliest known reset inside a tier, then a
// weighted share for equal or unknown resets. It is an account-level view:
// model-specific cooldowns, model support, transport preference and session
// affinity still apply to individual requests.
func routingOrder(auths []*Auth, hints map[string]QuotaResetHint, now time.Time) map[string][]RoutingAccountView {
	pools := make(map[string][]*Auth)
	for _, auth := range auths {
		if auth == nil || auth.Index == "" {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		pools[provider] = append(pools[provider], auth)
	}
	out := make(map[string][]RoutingAccountView, len(pools))
	for provider, pool := range pools {
		sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
		views := make(map[string]*RoutingAccountView, len(pool))
		eligible := make([]*Auth, 0, len(pool))
		for _, auth := range pool {
			view := &RoutingAccountView{AuthIndex: auth.Index, Priority: authPriority(auth), Weight: authWeight(auth)}
			if hint, ok := hints[auth.ID]; ok && hint.usable(now) {
				resetAt, observedAt := hint.ResetAt.UTC(), hint.ObservedAt.UTC()
				view.ResetAt, view.ObservedAt = &resetAt, &observedAt
			}
			blocked, reason, next := isAuthBlockedForModel(auth, "", now)
			switch {
			case blocked && reason == blockReasonDisabled:
				view.State = RoutingStateDisabled
			case blocked && reason == blockReasonCooldown:
				view.State = RoutingStateCooldown
			case blocked:
				view.State = RoutingStateUnavailable
			case view.Weight <= 0:
				view.State = RoutingStateZeroWeight
			default:
				view.State = RoutingStateEligible
				eligible = append(eligible, auth)
			}
			if blocked && !next.IsZero() {
				retry := next.UTC()
				view.NextRetryAfter = &retry
			}
			views[auth.ID] = view
		}
		rankEligible(eligible, hints, now, views)
		ordered := make([]RoutingAccountView, 0, len(views))
		for _, auth := range pool {
			ordered = append(ordered, *views[auth.ID])
		}
		sort.SliceStable(ordered, func(i, j int) bool {
			left, right := ordered[i], ordered[j]
			if (left.Rank > 0) != (right.Rank > 0) {
				return left.Rank > 0
			}
			if left.Rank != right.Rank {
				return left.Rank < right.Rank
			}
			return routingStateOrder(left.State) < routingStateOrder(right.State)
		})
		out[provider] = ordered
	}
	return out
}

func rankEligible(eligible []*Auth, hints map[string]QuotaResetHint, now time.Time, views map[string]*RoutingAccountView) {
	rank := 0
	remaining := eligible
	for len(remaining) > 0 {
		tier := append([]*Auth(nil), highestPriorityAuths(remaining)...)
		tierSize := len(tier)
		first := true
		for len(tier) > 0 {
			group, _ := soonestResetGroup(tier, hints, now)
			reason := RoutingReasonNoReset
			if len(group) > 0 {
				reason = RoutingReasonLaterReset
				if first {
					reason = RoutingReasonEarliestReset
				}
			} else {
				group = tier
			}
			if tierSize == 1 {
				reason = RoutingReasonOnlyInTier
			}
			rank++
			for _, auth := range group {
				view := views[auth.ID]
				view.Rank, view.Reason, view.Shared = rank, reason, len(group) > 1
			}
			tier = withoutAuths(tier, group)
			first = false
		}
		remaining = withoutAuths(remaining, highestPriorityAuths(remaining))
	}
}

func withoutAuths(auths, remove []*Auth) []*Auth {
	removed := make(map[string]struct{}, len(remove))
	for _, auth := range remove {
		removed[auth.ID] = struct{}{}
	}
	kept := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if _, ok := removed[auth.ID]; !ok {
			kept = append(kept, auth)
		}
	}
	return kept
}

func routingStateOrder(state string) int {
	switch state {
	case RoutingStateZeroWeight:
		return 0
	case RoutingStateCooldown:
		return 1
	case RoutingStateUnavailable:
		return 2
	default:
		return 3
	}
}

func selectorUsesQuotaWeights(selector Selector) bool {
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	switch selector.(type) {
	case *WeightedRoundRobinSelector, *SoonestResetFirstSelector:
		return true
	default:
		return false
	}
}
