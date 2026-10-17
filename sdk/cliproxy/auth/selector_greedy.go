package auth

import (
	"context"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// GreedySelector uses observed reset schedules without changing quota state.
// Existing availability, priority and transport preferences remain authoritative.
type GreedySelector struct{}

func (s *GreedySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	var best *Auth
	for _, a := range available {
		if best == nil || greedyAuthLess(a, best, now) {
			best = a
		}
	}
	return best, nil
}

func greedyAuthLess(a, b *Auth, now time.Time) bool {
	as, bs := EffectiveQuotaResetSchedule(a), EffectiveQuotaResetSchedule(b)
	for _, pair := range [][2]time.Time{{as.WeeklyResetAt, bs.WeeklyResetAt}, {as.FiveHourResetAt, bs.FiveHourResetAt}} {
		af, bf := pair[0].After(now), pair[1].After(now)
		if af != bf {
			return af
		}
		if af && !pair[0].Equal(pair[1]) {
			return pair[0].Before(pair[1])
		}
	}
	return a.ID < b.ID
}

func (v *readyView) pickGreedy(predicate func(*scheduledAuth) bool) *scheduledAuth {
	now := time.Now()
	var best *scheduledAuth
	for _, entry := range v.flat {
		if entry == nil || entry.auth == nil || (predicate != nil && !predicate(entry)) {
			continue
		}
		if best == nil || greedyAuthLess(entry.auth, best.auth, now) {
			best = entry
		}
	}
	return best
}
