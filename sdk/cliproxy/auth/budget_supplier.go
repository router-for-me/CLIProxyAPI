package auth

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// BudgetKind distinguishes a rolling time window from a prepaid remaining
// balance. Hosts must not stuff a dollar amount into a window quota, and must
// not invent a resets_at for prepaid balance.
const (
	BudgetKindWindow  = "window"
	BudgetKindBalance = "balance"
)

// BudgetObservation is one auth's remaining budget as reported by a supplier.
// Remaining is a fraction in [0,1] when known; Amount/Currency are optional
// absolute prepaid fields. RecoverAt is zero when there is no clock (prepaid
// $0 parks until the next supplier refresh).
type BudgetObservation struct {
	AuthID    string
	Kind      string
	Remaining *float64
	Amount    string
	Currency  string
	Available *bool
	RecoverAt time.Time
}

// BudgetSupplier is the host-owned seam for pushing collected usage into the
// conductor. CLIProxyAPI stays agnostic about how numbers are gathered
// (DeepSeek /user/balance, Zhipu quota API, a host ParkSource, etc.).
//
// A nil supplier is a no-op: selection stays on today's cooldown ladder.
type BudgetSupplier interface {
	Snapshot(ctx context.Context) ([]BudgetObservation, error)
}

// BudgetApplyConfig controls how observations become ineligibility.
type BudgetApplyConfig struct {
	// Threshold is the remaining fraction at or below which a window is
	// exhausted. Default 0 (remaining <= 0).
	Threshold float64
	// Recheck is used when RecoverAt is zero (prepaid). Default 5m.
	Recheck time.Duration
	Now     func() time.Time
}

// ApplyBudgetObservations marks auths exhausted from supplier observations.
// It never sets Result.CredentialScope — that flag is Anthropic-window
// scoped and is the wrong tool for prepaid $ and non-Anthropic windows.
//
// Fail-open: missing remaining, unparseable amount, and absent observations
// leave the auth eligible.
func ApplyBudgetObservations(auths []*Auth, obs []BudgetObservation, cfg BudgetApplyConfig) {
	if len(auths) == 0 || len(obs) == 0 {
		return
	}
	now := time.Now()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	recheck := cfg.Recheck
	if recheck <= 0 {
		recheck = 5 * time.Minute
	}
	byID := make(map[string]*Auth, len(auths))
	for _, a := range auths {
		if a != nil && a.ID != "" {
			byID[a.ID] = a
		}
	}
	for _, o := range obs {
		a := byID[o.AuthID]
		if a == nil {
			continue
		}
		until, exhausted := observationExhausted(o, cfg.Threshold, recheck, now)
		if !exhausted {
			continue
		}
		a.Unavailable = true
		a.Quota.Exceeded = true
		a.Quota.Reason = "budget:" + normalizeBudgetKind(o.Kind)
		a.Quota.NextRecoverAt = until
	}
}

func observationExhausted(o BudgetObservation, threshold float64, recheck time.Duration, now time.Time) (time.Time, bool) {
	kind := normalizeBudgetKind(o.Kind)
	switch kind {
	case BudgetKindBalance:
		if o.Available != nil && !*o.Available {
			return recoverOrRecheck(o.RecoverAt, recheck, now), true
		}
		if amt, ok := parseBudgetAmount(o.Amount); ok && amt <= 0 {
			return recoverOrRecheck(o.RecoverAt, recheck, now), true
		}
		return time.Time{}, false
	default:
		if o.Remaining == nil {
			return time.Time{}, false
		}
		if *o.Remaining > threshold {
			return time.Time{}, false
		}
		return recoverOrRecheck(o.RecoverAt, recheck, now), true
	}
}

func recoverOrRecheck(recoverAt time.Time, recheck time.Duration, now time.Time) time.Time {
	if !recoverAt.IsZero() && recoverAt.After(now) {
		return recoverAt
	}
	return now.Add(recheck)
}

func normalizeBudgetKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case BudgetKindBalance:
		return BudgetKindBalance
	default:
		return BudgetKindWindow
	}
}

func parseBudgetAmount(raw string) (float64, bool) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
