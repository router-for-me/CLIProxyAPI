package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// QuotaResetHint is a non-secret, short-lived observation supplied by a quota
// collector. It is never persisted with OAuth credentials.
type QuotaResetHint struct {
	ObservedAt time.Time `json:"observed_at"`
	ResetAt    time.Time `json:"reset_at"`
}

// QuotaResetHintMaxAge bounds the lifetime of collector observations, including
// when the collector stops. Missing/stale hints fall back to quota weights.
const QuotaResetHintMaxAge = 15 * time.Minute

// SoonestResetFirstSelector chooses the earliest known reset among available
// credentials in the highest manual priority tier. Equal resets (or no usable
// hints) use smooth weighted round robin. Session affinity wraps this selector
// and remains authoritative for existing conversations.
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

func (s *SoonestResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	available, err := getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	var earliest time.Time
	var preferred []*Auth
	s.mu.RLock()
	for _, candidate := range available {
		hint, ok := s.hints[candidate.ID]
		if !ok || hint.ObservedAt.IsZero() || hint.ObservedAt.After(now) ||
			now.Sub(hint.ObservedAt) > QuotaResetHintMaxAge || !hint.ResetAt.After(now) {
			continue
		}
		if earliest.IsZero() || hint.ResetAt.Before(earliest) {
			earliest = hint.ResetAt
			preferred = []*Auth{candidate}
		} else if hint.ResetAt.Equal(earliest) {
			preferred = append(preferred, candidate)
		}
	}
	s.mu.RUnlock()
	if len(preferred) > 0 {
		available = preferred
	}
	return s.fallback.Pick(ctx, provider, model, opts, available)
}

func (s *SoonestResetFirstSelector) replaceResetHints(hints map[string]QuotaResetHint) {
	copyHints := make(map[string]QuotaResetHint, len(hints))
	for id, hint := range hints {
		copyHints[id] = hint
	}
	s.mu.Lock()
	s.hints = copyHints
	s.mu.Unlock()
}

// ReplaceQuotaResetHints atomically replaces a collector snapshot. It does not
// modify auth state, manual priorities, weights, cooldowns or affinity bindings.
// The caller resolves management auth indexes to current runtime IDs first.
func (m *Manager) ReplaceQuotaResetHints(hints map[string]QuotaResetHint) error {
	if m == nil {
		return errors.New("auth manager unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	selector := m.selector
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	resetSelector, ok := selector.(*SoonestResetFirstSelector)
	if !ok {
		return errors.New("soonest-reset-first routing is not active")
	}
	resetSelector.replaceResetHints(hints)
	return nil
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
