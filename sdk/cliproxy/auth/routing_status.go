package auth

import (
	"context"
	"sort"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type RoutingAccountStatus struct {
	AuthIndex     string         `json:"auth_index"`
	Priority      int            `json:"priority"`
	Eligible      bool           `json:"eligible"`
	SkippedReason string         `json:"skipped_reason"`
	Rank          int            `json:"rank"`
	QuotaState    string         `json:"quota_state"`
	ObservedAt    *time.Time     `json:"observed_at,omitempty"`
	AgeSeconds    *int64         `json:"age_seconds,omitempty"`
	Window        *RoutingWindow `json:"window,omitempty"`
	RefreshError  string         `json:"refresh_error,omitempty"`
}

type RoutingStatus struct {
	RequestProfile         string                 `json:"request_profile"`
	Transport              string                 `json:"transport"`
	SchemaVersion          int                    `json:"schema_version"`
	Strategy               string                 `json:"strategy"`
	Provider               string                 `json:"provider"`
	Model                  string                 `json:"model"`
	Pool                   string                 `json:"pool,omitempty"`
	Pools                  []RoutingPool          `json:"pools,omitempty"`
	GeneratedAt            time.Time              `json:"generated_at"`
	PredictionScope        string                 `json:"prediction_scope"`
	NextAuthIndex          string                 `json:"next_auth_index,omitempty"`
	FallbackReason         string                 `json:"fallback_reason"`
	SessionAffinity        bool                   `json:"session_affinity"`
	RefreshIntervalSeconds int                    `json:"refresh_interval_seconds"`
	MaxAgeSeconds          int                    `json:"max_age_seconds"`
	Accounts               []RoutingAccountStatus `json:"accounts"`
}

// RoutingStatus is a read-only prediction for a new default HTTP session. It never
// advances rotation cursors, looks up user sessions, or initiates network calls.
func (m *Manager) RoutingStatus(ctx context.Context, provider, model string, now time.Time) RoutingStatus {
	return m.RoutingStatusForPool(ctx, provider, model, "", now)
}

// RoutingStatusForPool ranks accounts by one quota pool from RoutingPools. Without a
// model or pool, a provider with pools is ranked by its first pool, because none of
// its windows applies to an unnamed model. Callers validate pool against RoutingPools.
func (m *Manager) RoutingStatusForPool(ctx context.Context, provider, model, pool string, now time.Time) RoutingStatus {
	pools := RoutingPools(provider)
	if pool == "" && model == "" && len(pools) > 0 {
		pool = pools[0].Key
	}
	windowPool, _ := routingPoolWindow(provider, pool)
	if windowPool == "" {
		pool = ""
	}
	status := RoutingStatus{Pool: pool, Pools: pools, SchemaVersion: 1, Provider: provider, Model: model, GeneratedAt: now.UTC(), PredictionScope: "new_session", RefreshIntervalSeconds: int(RoutingQuotaRefreshInterval.Seconds()), MaxAgeSeconds: int(RoutingQuotaMaxAge.Seconds()), Accounts: []RoutingAccountStatus{}}
	status.RequestProfile = "default"
	status.Transport = "http"
	if cliproxyexecutor.DownstreamWebsocket(ctx) {
		status.Transport = "websocket"
	}
	if provider == "codex" {
		status.RequestProfile = "codex"
		ctx = WithCodexClientRequest(ctx, true)
	}
	selector := m.Selector()
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		status.SessionAffinity = true
		selector = affinity.fallback
	}
	switch selector.(type) {
	case *SoonestResetSelector:
		status.Strategy = "soonest-reset-first"
	case *FillFirstSelector:
		status.Strategy = "fill-first"
	case *WeightedRoundRobinSelector:
		status.Strategy = "weighted-round-robin"
	case *RoundRobinSelector:
		status.Strategy = "round-robin"
	default:
		status.Strategy = "custom"
	}
	s := soonestSelector(selector)
	if s == nil {
		s = &SoonestResetSelector{}
		status.FallbackReason = "strategy_not_predictable"
	}
	if m.HomeEnabled() {
		status.FallbackReason = "home_authoritative"
	} else if m.hasPluginScheduler() {
		status.FallbackReason = "plugin_scheduler"
	}
	auths := m.List()
	sort.Slice(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
	usable := make([]*Auth, 0, len(auths))
	byID := make(map[string]int)
	for _, a := range auths {
		if canonicalSchedulingProvider(a.Provider) != canonicalSchedulingProvider(provider) {
			continue
		}
		a.EnsureIndex()
		checkModel := m.selectionModelForAuth(a, model)
		r, w, state, until := s.viewScoped(a, checkModel, windowPool, now)
		row := RoutingAccountStatus{AuthIndex: a.Index, Priority: authPriority(a), QuotaState: state, Window: w, RefreshError: r.errorCode}
		if !r.observedAt.IsZero() {
			observed := r.observedAt.UTC()
			row.ObservedAt = &observed
			age := int64(now.Sub(observed).Seconds())
			if age < 0 {
				age = 0
			}
			row.AgeSeconds = &age
		}
		blocked, reason, _ := isAuthBlockedForModelForRequest(ctx, a, checkModel, now)
		switch {
		case a.Disabled || a.Status == StatusDisabled:
			row.SkippedReason = "disabled"
		case !m.authSupportsRouteModel(registry.GetGlobalRegistry(), a, model):
			row.SkippedReason = "model_unsupported"
		case blocked:
			row.SkippedReason = "unavailable"
			if reason == blockReasonCooldown {
				row.SkippedReason = "cooldown"
			}
		case !until.IsZero():
			row.SkippedReason = "quota_exhausted"
		default:
			row.Eligible = true
			usable = append(usable, a)
		}
		byID[a.ID] = len(status.Accounts)
		status.Accounts = append(status.Accounts, row)
	}
	// Resolve per-auth aliases before comparing model-specific quota windows.
	sort.Slice(usable, func(i, j int) bool {
		a, b := usable[i], usable[j]
		if authPriority(a) != authPriority(b) {
			return authPriority(a) > authPriority(b)
		}
		if provider == "codex" && cliproxyexecutor.DownstreamWebsocket(ctx) && authWebsocketsEnabled(a) != authWebsocketsEnabled(b) {
			return authWebsocketsEnabled(a)
		}
		wa := status.Accounts[byID[a.ID]].Window
		wb := status.Accounts[byID[b.ID]].Window
		if (wa != nil) != (wb != nil) {
			return wa != nil
		}
		if wa != nil && !wa.ResetsAt.Equal(wb.ResetsAt) {
			return wa.ResetsAt.Before(wb.ResetsAt)
		}
		return a.ID < b.ID
	})
	for i, a := range usable {
		status.Accounts[byID[a.ID]].Rank = i + 1
	}
	if status.FallbackReason == "" {
		if len(usable) == 0 {
			status.FallbackReason = "no_eligible_accounts"
		} else {
			status.NextAuthIndex = usable[0].Index
			if status.Accounts[byID[usable[0].ID]].Window == nil {
				status.FallbackReason = "quota_unavailable"
			}
		}
	}
	return status
}
