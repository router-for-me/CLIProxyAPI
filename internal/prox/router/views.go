package router

import (
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/session"
)

// expiringSoon flags accounts whose usable long-window capacity resets within this horizon.
const expiringSoon = 24 * time.Hour

// WindowView is one quota window prepared for display. Remaining is always
// capacity left (0–100); Used is utilization. Both are given so no client has
// to invert a value.
type WindowView struct {
	Key        string     `json:"key"`
	Kind       string     `json:"kind"`
	Family     string     `json:"family,omitempty"`
	Label      string     `json:"label"`
	Used       float64    `json:"used_percent"`
	Remaining  float64    `json:"remaining_percent"`
	ResetsAt   *time.Time `json:"resets_at,omitempty"`
	Rejected   bool       `json:"rejected,omitempty"`
	Source     string     `json:"source"`
	ObservedAt time.Time  `json:"observed_at"`
	Stale      bool       `json:"stale"`
	Passed     bool       `json:"reset_passed"`
}

// AccountView is one account for the dashboard.
type AccountView struct {
	ID            string       `json:"id"`
	Display       string       `json:"display"`
	Alias         string       `json:"alias,omitempty"`
	Provider      string       `json:"provider"`
	Plan          string       `json:"plan,omitempty"`
	HostStatus    string       `json:"host_status,omitempty"`
	HostMessage   string       `json:"host_message,omitempty"`
	Present       bool         `json:"present"`
	Disabled      bool         `json:"disabled"`
	Unavailable   bool         `json:"unavailable"`
	CooldownUntil *time.Time   `json:"cooldown_until,omitempty"`
	CooldownStale bool         `json:"cooldown_stale"`
	HostPriority  int          `json:"host_priority"`
	PluginPrio    int          `json:"plugin_priority"`
	Polled        bool         `json:"polled"`
	PollError     string       `json:"poll_error,omitempty"`
	LastPollAt    *time.Time   `json:"last_poll_at,omitempty"`
	LastRefreshAt *time.Time   `json:"last_refresh_at,omitempty"`
	LastObserved  *time.Time   `json:"last_observed_at,omitempty"`
	NextPollAt    *time.Time   `json:"next_poll_at,omitempty"`
	Windows       []WindowView `json:"windows"`
	QuotaState    string       `json:"quota_state"`
	QuotaReason   string       `json:"quota_reason"`
	Routing       string       `json:"routing"`
	RoutingReason string       `json:"routing_reason,omitempty"`
	NextReset     *time.Time   `json:"next_reset,omitempty"`
	Remaining     float64      `json:"remaining_percent"`
	ExpiringSoon  bool         `json:"expiring_soon"`
	ActiveSess    int          `json:"active_sessions"`
	Preferred     bool         `json:"preferred"`
	Rank          int          `json:"rank"`
	Health        HealthView   `json:"health"`
}

// HealthView summarizes learned failure state.
type HealthView struct {
	AuthFailed     bool       `json:"auth_failed"`
	AuthSuspect    bool       `json:"auth_suspect"`
	RateLimitedTo  *time.Time `json:"rate_limited_until,omitempty"`
	CircuitTo      *time.Time `json:"circuit_open_until,omitempty"`
	BlockedModels  []string   `json:"blocked_models,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt  *time.Time `json:"last_failure_at,omitempty"`
	LastFailStatus int        `json:"last_failure_status,omitempty"`
}

// SessionView is one binding for the dashboard. Session keys are shortened hashes.
type SessionView struct {
	Session        string     `json:"session"`
	Parent         string     `json:"parent,omitempty"`
	Client         string     `json:"client,omitempty"`
	Kind           string     `json:"kind"`
	Provider       string     `json:"provider"`
	Model          string     `json:"model"`
	AuthID         string     `json:"auth_id"`
	Account        string     `json:"account"`
	ActualAuthID   string     `json:"actual_auth_id,omitempty"`
	Origin         string     `json:"origin"`
	Applied        bool       `json:"applied"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeen       time.Time  `json:"last_seen"`
	ExpiresAt      time.Time  `json:"expires_at"`
	Requests       int        `json:"requests"`
	Failovers      int        `json:"failovers"`
	LastFailoverAt *time.Time `json:"last_failover_at,omitempty"`
	Rebinds        int        `json:"rebinds"`
	LastRebindAt   *time.Time `json:"last_rebind_at,omitempty"`
	LastRebindWhy  string     `json:"last_rebind_cause,omitempty"`
	PreviousAuthID string     `json:"previous_auth_id,omitempty"`
	AccountUsable  bool       `json:"account_usable"`
}

// NextUp is the account a new session would get, per provider.
type NextUp struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Policy   string   `json:"policy"`
	AuthID   string   `json:"auth_id,omitempty"`
	Account  string   `json:"account,omitempty"`
	Reason   string   `json:"reason"`
	Fallback bool     `json:"fallback,omitempty"`
	Ranked   []Ranked `json:"ranked"`
	Skipped  []Skip   `json:"skipped"`
}

// Status summarizes the router for the overview.
type Status struct {
	Mode           string         `json:"mode"`
	Policy         string         `json:"policy"`
	RosterOK       bool           `json:"roster_ok"`
	RosterError    string         `json:"roster_error,omitempty"`
	RosterAt       time.Time      `json:"roster_at"`
	Counters       Counters       `json:"counters"`
	Sessions       int            `json:"sessions"`
	SessionsByProv map[string]int `json:"sessions_by_provider"`
}

func (r *Router) offeredCandidates(provider string) []Candidate {
	var out []Candidate
	for _, a := range r.accounts {
		if a.Provider != provider || !a.Present {
			continue
		}
		status := a.HostStatus
		if a.Disabled {
			status = "disabled"
		}
		out = append(out, Candidate{ID: a.ID, Provider: provider, Priority: a.Priority, Status: status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Router) simulateLocked(provider, model, strategy string, now time.Time) NextUp {
	if model == "" {
		model = r.lastModel[provider]
	}
	n := NextUp{Provider: provider, Model: model, Policy: strategy}
	var order []*eval
	var withheld []Skip
	for _, c := range r.offeredCandidates(provider) {
		// CPA does not offer accounts it is cooling down.
		if a := r.accounts[c.ID]; a != nil && a.Unavailable && a.CooldownUntil.After(now) {
			reason := "not_offered"
			if staleCPACooldown(a, now) {
				reason = "cooldown_stale"
			}
			withheld = append(withheld, Skip{AuthID: c.ID, Reason: reason})
			continue
		}
		order = append(order, r.evaluate(c, model, now))
	}
	n.Ranked = r.rank(order, now, strategy)
	for _, e := range order {
		if !e.rankable {
			n.Skipped = append(n.Skipped, Skip{AuthID: e.c.ID, Reason: e.cause})
		}
	}
	n.Skipped = append(n.Skipped, withheld...)
	id, fallback := r.choose(order, n.Ranked, "")
	n.AuthID, n.Fallback = id, fallback
	switch {
	case id == "":
		n.Reason = "no account has fresh quota; CPA would choose"
	case fallback:
		n.Reason = "no fresh quota; avoiding known-unusable accounts"
	default:
		n.Reason = n.Ranked[0].Reason
	}
	if a := r.accounts[id]; a != nil {
		n.Account = r.label(a)
	}
	return n
}

// NextUp returns, per provider, the account a new session would get now.
func (r *Router) NextUp() []NextUp {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	return []NextUp{r.simulateLocked("claude", "", r.cfg.Strategy, now), r.simulateLocked("codex", "", r.cfg.Strategy, now)}
}

// Simulate ranks a hypothetical new session under each strategy without
// changing any state.
func (r *Router) Simulate(provider, model string, strategies []string) []NextUp {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var out []NextUp
	for _, s := range strategies {
		out = append(out, r.simulateLocked(provider, model, s, now))
	}
	return out
}

func (r *Router) label(a *account) string {
	if alias := r.cfg.AccountAlias[a.ID]; alias != "" {
		return alias
	}
	return a.Display
}

// Accounts returns every known account with quota, health, and routing state.
func (r *Router) Accounts() []AccountView {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	active := map[string]int{}
	for _, b := range r.bindings {
		if now.Sub(b.LastSeen) < r.cfg.AffinityTTL {
			active[b.AuthID]++
		}
	}
	rankOf := map[string]int{}
	preferred := map[string]bool{}
	for _, p := range []string{"claude", "codex"} {
		n := r.simulateLocked(p, "", r.cfg.Strategy, now)
		for i, rk := range n.Ranked {
			rankOf[rk.AuthID] = i + 1
		}
		if n.AuthID != "" {
			preferred[n.AuthID] = true
		}
	}
	out := make([]AccountView, 0, len(r.accounts))
	for _, a := range r.accounts {
		v := AccountView{
			ID: a.ID, Display: a.Display, Alias: r.cfg.AccountAlias[a.ID], Provider: a.Provider, Plan: a.PlanType,
			HostStatus: a.HostStatus, HostMessage: a.HostMessage, Present: a.Present, Disabled: a.Disabled, Unavailable: a.Unavailable,
			CooldownUntil: tp(a.CooldownUntil), CooldownStale: staleCPACooldown(a, now),
			HostPriority: a.Priority, PluginPrio: r.cfg.AccountPriority[a.ID], Polled: !a.Snapshot.PolledAt.IsZero(), PollError: a.PollError,
			ActiveSess: active[a.ID], Preferred: preferred[a.ID], Rank: rankOf[a.ID],
		}
		v.LastPollAt = tp(a.LastPollAt)
		v.LastRefreshAt = tp(a.Snapshot.PolledAt)
		v.LastObserved = tp(a.Snapshot.LastObserved())
		if a.Present && !a.Disabled && !a.RuntimeOnly && !a.CustomURL {
			v.NextPollAt = tp(a.NextPoll)
		}
		for _, w := range a.Snapshot.Windows {
			v.Windows = append(v.Windows, windowView(w, now, r.cfg.MaxAge))
		}
		model := r.lastModel[a.Provider]
		e := r.evaluate(Candidate{ID: a.ID, Provider: a.Provider, Priority: a.Priority, Status: a.HostStatus}, model, now)
		v.QuotaState, v.QuotaReason = string(e.assessment.State), e.assessment.Reason
		v.Remaining = e.assessment.Remaining
		switch {
		case !a.Present:
			v.Routing, v.RoutingReason = "removed", "credential no longer listed by CPA"
		case e.rankable:
			v.Routing = "eligible"
		case e.usable:
			v.Routing, v.RoutingReason = "limited", e.cause
		default:
			v.Routing, v.RoutingReason = "blocked", e.cause
		}
		if ap := e.assessment.Applicable; ap != nil && ap.ResetsAt != nil {
			v.NextReset = tp(*ap.ResetsAt)
			v.ExpiringSoon = e.rankable && ap.ResetsAt.Sub(now) < expiringSoon && e.assessment.Remaining >= 10
		} else if e.assessment.Until != nil {
			v.NextReset = tp(*e.assessment.Until)
		}
		h := a.Health
		v.Health = HealthView{AuthFailed: !h.AuthFailedAt.IsZero(), AuthSuspect: h.PollAuthRejected, LastSuccessAt: tp(h.LastSuccessAt), LastFailureAt: tp(h.LastFailureAt), LastFailStatus: h.LastStatus}
		if now.Before(h.RateLimitedUntil) {
			v.Health.RateLimitedTo = tp(h.RateLimitedUntil)
		}
		if now.Before(h.CircuitUntil) {
			v.Health.CircuitTo = tp(h.CircuitUntil)
		}
		for fam, until := range h.ModelBlocked {
			if now.Before(until) {
				v.Health.BlockedModels = append(v.Health.BlockedModels, fam)
			}
		}
		sort.Strings(v.Health.BlockedModels)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func windowView(w quota.Window, now time.Time, maxAge time.Duration) WindowView {
	v := WindowView{
		Key: w.Key(), Kind: string(w.Kind), Family: w.Family, Label: windowLabel(w),
		Used: w.UsedPercent, Remaining: w.RemainingPercent(), Rejected: w.Rejected,
		Source: string(w.Source), ObservedAt: w.ObservedAt, Stale: now.Sub(w.ObservedAt) >= maxAge,
	}
	if w.ResetsAt != nil {
		t := *w.ResetsAt
		v.ResetsAt = &t
		v.Passed = !t.After(now)
	}
	return v
}

func windowLabel(w quota.Window) string {
	switch w.Kind {
	case quota.FiveHour:
		return "5-hour"
	case quota.Weekly:
		return "Weekly"
	case quota.Monthly:
		return "Monthly"
	case quota.ModelWeekly:
		if w.Family == "" {
			return "Model weekly"
		}
		return "Weekly " + strings.ToUpper(w.Family[:1]) + w.Family[1:]
	}
	return string(w.Kind)
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Sessions returns live bindings, most recently active first.
func (r *Router) Sessions() []SessionView {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]SessionView, 0, len(r.bindings))
	for _, b := range r.bindings {
		if now.Sub(b.LastSeen) >= r.cfg.AffinityTTL {
			continue
		}
		v := SessionView{
			Session: session.Short(b.SessionKey), Client: b.Client, Kind: b.Kind, Provider: b.Provider, Model: b.Scope,
			AuthID: b.AuthID, ActualAuthID: b.ActualAuthID, Origin: b.Origin, Applied: b.Applied,
			CreatedAt: b.CreatedAt, LastSeen: b.LastSeen, ExpiresAt: b.LastSeen.Add(r.cfg.AffinityTTL),
			Requests: b.Requests, Failovers: b.Failovers, LastFailoverAt: tp(b.LastFailoverAt),
			Rebinds: b.Rebinds, LastRebindAt: tp(b.LastRebindAt), LastRebindWhy: b.LastRebindWhy, PreviousAuthID: b.PreviousAuthID,
		}
		if b.ParentKey != "" {
			v.Parent = session.Short(b.ParentKey)
		}
		if v.Model == "*" {
			v.Model = "all models"
		}
		if a := r.accounts[b.AuthID]; a != nil {
			v.Account = r.label(a)
			e := r.evaluate(Candidate{ID: a.ID, Provider: a.Provider, Priority: a.Priority, Status: a.HostStatus}, b.Scope, now)
			v.AccountUsable = e.usable && a.Present
		} else {
			v.Account = redactID(b.AuthID)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// ClearSessions removes bindings matching the filter and returns how many.
// An empty filter matches every binding.
func (r *Router) ClearSessions(provider, authID, sessionShort string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, b := range r.bindings {
		if provider != "" && b.Provider != provider {
			continue
		}
		if authID != "" && b.AuthID != authID {
			continue
		}
		if sessionShort != "" && session.Short(b.SessionKey) != sessionShort {
			continue
		}
		delete(r.bindings, k)
		n++
	}
	if n > 0 {
		r.dirty = true
	}
	return n
}

// Decisions returns recent decisions, newest first, optionally filtered.
func (r *Router) Decisions(provider, outcome string, limit int) []Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Decision
	for _, d := range r.decisions.recent() {
		if provider != "" && d.Provider != provider {
			continue
		}
		if outcome != "" && d.Outcome != outcome {
			continue
		}
		out = append(out, d)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Decision returns one decision by ID.
func (r *Router) Decision(id uint64) (Decision, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.decisions.recent() {
		if d.ID == id {
			return d, true
		}
	}
	return Decision{}, false
}

// Labels maps account IDs to display labels (alias or redacted name).
func (r *Router) Labels() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.accounts))
	for id, a := range r.accounts {
		out[id] = r.label(a)
	}
	return out
}

// Status returns summary counters.
func (r *Router) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	s := Status{Mode: r.cfg.Mode, Policy: r.cfg.Strategy, RosterOK: r.rosterOK, RosterError: r.rosterErr, RosterAt: r.rosterAt, Counters: r.counters, SessionsByProv: map[string]int{}}
	for _, b := range r.bindings {
		if now.Sub(b.LastSeen) < r.cfg.AffinityTTL && (b.Applied || r.cfg.Mode == config.ModeShadow) {
			s.Sessions++
			s.SessionsByProv[b.Provider]++
		}
	}
	return s
}
