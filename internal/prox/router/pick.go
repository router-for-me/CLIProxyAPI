package router

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/session"
)

// Binding keeps one session on one account.
type Binding struct {
	Key            string    `json:"key"`
	Provider       string    `json:"provider"`
	Scope          string    `json:"scope"`
	SessionKey     string    `json:"session_key"`
	ParentKey      string    `json:"parent_key,omitempty"`
	Client         string    `json:"client,omitempty"`
	Kind           string    `json:"kind"`
	AuthID         string    `json:"auth_id"`
	Origin         string    `json:"origin"`
	Applied        bool      `json:"applied"`
	ActualAuthID   string    `json:"actual_auth_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastSeen       time.Time `json:"last_seen"`
	Requests       int       `json:"requests"`
	Failovers      int       `json:"failovers"`
	LastFailoverAt time.Time `json:"last_failover_at,omitempty"`
	LastFailoverTo string    `json:"last_failover_to,omitempty"`
	Rebinds        int       `json:"rebinds"`
	LastRebindAt   time.Time `json:"last_rebind_at,omitempty"`
	LastRebindWhy  string    `json:"last_rebind_cause,omitempty"`
	PreviousAuthID string    `json:"previous_auth_id,omitempty"`
}

// Decision outcomes.
const (
	OutcomeAffinity   = "affinity"
	OutcomeInherited  = "inherited"
	OutcomeNewSession = "new_session"
	OutcomeNoSession  = "no_session"
	OutcomeFailover   = "failover"
	OutcomeRebind     = "rebind"
	OutcomeHostPinned = "host_pinned"
	OutcomeDelegated  = "delegated"
)

// Ranked is one usable candidate in policy order.
type Ranked struct {
	AuthID string  `json:"auth_id"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
	State  string  `json:"quota_state"`
}

// Skip is one candidate that could not be ranked and why.
type Skip struct {
	AuthID string `json:"auth_id"`
	Reason string `json:"reason"`
}

// Decision explains one pick.
type Decision struct {
	ID               uint64    `json:"id"`
	At               time.Time `json:"at"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	Stream           bool      `json:"stream"`
	Mode             string    `json:"mode"`
	Policy           string    `json:"policy"`
	SessionKey       string    `json:"session,omitempty"`
	SessionKind      string    `json:"session_kind,omitempty"`
	Client           string    `json:"client,omitempty"`
	Outcome          string    `json:"outcome"`
	Cause            string    `json:"cause,omitempty"`
	AuthID           string    `json:"auth_id,omitempty"`
	BoundAuthID      string    `json:"bound_auth_id,omitempty"`
	Applied          bool      `json:"applied"`
	AffinityOverrode bool      `json:"affinity_overrode_ranking"`
	Fallback         bool      `json:"fallback,omitempty"`
	Ranked           []Ranked  `json:"ranked"`
	Skipped          []Skip    `json:"skipped"`
	Result           *Result   `json:"result,omitempty"`
	sessionFull      string
}

// Result is the observed upstream outcome attributed to a decision.
type Result struct {
	At     time.Time `json:"at"`
	AuthID string    `json:"auth_id"`
	Status int       `json:"status"`
	Failed bool      `json:"failed"`
}

type eval struct {
	c          Candidate
	offered    bool
	usable     bool
	rankable   bool
	lowRoom    bool
	permanent  bool
	cause      string
	assessment quota.Assessment
	plugin     int
}

// Pick chooses an account. AuthID is empty when CPA should decide.
func (r *Router) Pick(req PickRequest) Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	provider := normalizeProvider(req.Provider)
	d := Decision{At: now, Provider: provider, Model: req.Model, Stream: req.Stream, Mode: r.cfg.Mode, Policy: r.cfg.Strategy}
	if !supportedProvider(provider) {
		r.counters.Unrelated++
		d.Outcome, d.Cause = OutcomeDelegated, "provider_not_supported"
		return d
	}
	for _, c := range req.Candidates {
		if normalizeProvider(c.Provider) != provider {
			r.counters.Unrelated++
			d.Outcome, d.Cause = OutcomeDelegated, "mixed_provider_candidates"
			return d
		}
	}
	r.counters.Picks++
	if req.Model != "" {
		r.lastModel[provider] = req.Model
	}
	if req.HasSession {
		d.SessionKey = session.Short(req.Session.Key)
		d.SessionKind = string(req.Session.Kind)
		d.Client = req.Session.Client
		d.sessionFull = req.Session.Key
	}
	evals := make(map[string]*eval, len(req.Candidates))
	offered := make(map[string]bool, len(req.Candidates))
	var order []*eval
	for _, c := range req.Candidates {
		if c.ID == "" {
			continue
		}
		offered[c.ID] = true
		e := r.evaluate(c, req.Model, now)
		evals[c.ID] = e
		order = append(order, e)
	}
	ranked := r.rank(order, now, r.cfg.Strategy)
	if len(ranked) == 0 {
		// Every ready account is below the new-session minimum: still prefer
		// one of them by policy over handing the request back to the host.
		for _, e := range order {
			if e.lowRoom {
				e.rankable, e.cause = true, ""
			}
		}
		ranked = r.rank(order, now, r.cfg.Strategy)
	}
	d.Ranked = ranked
	for _, e := range order {
		if !e.rankable {
			d.Skipped = append(d.Skipped, Skip{AuthID: e.c.ID, Reason: e.cause})
		}
	}
	var absentStale []string
	for id, a := range r.accounts {
		if a.Present && a.Provider == provider && !offered[id] && staleCPACooldown(a, now) {
			absentStale = append(absentStale, id)
		}
	}
	sort.Strings(absentStale)
	for _, id := range absentStale {
		d.Skipped = append(d.Skipped, Skip{AuthID: id, Reason: "cooldown_stale"})
	}

	if req.PinnedAuthID != "" {
		if e, ok := evals[req.PinnedAuthID]; ok {
			d.Outcome, d.AuthID = OutcomeHostPinned, e.c.ID
			if req.HasSession && r.cfg.AffinityEnabled {
				if b := r.binding(provider, req, now); b != nil {
					d.BoundAuthID = b.AuthID
					b.touch(now)
				} else {
					r.bind(provider, req, e.c.ID, "host_pinned", now)
				}
			}
			return r.finish(d)
		}
	}

	if req.HasSession && r.cfg.AffinityEnabled {
		if b := r.binding(provider, req, now); b != nil {
			d.BoundAuthID = b.AuthID
			e := evals[b.AuthID]
			if e != nil && e.usable {
				d.Outcome, d.AuthID = OutcomeAffinity, b.AuthID
				d.AffinityOverrode = len(ranked) > 0 && ranked[0].AuthID != b.AuthID
				b.touch(now)
				return r.finish(d)
			}
			cause, permanent := r.boundCause(b.AuthID, e, req.Model, now)
			alt, fallback := r.choose(order, ranked, b.AuthID)
			d.Fallback = fallback
			if alt == "" {
				d.Outcome, d.Cause = OutcomeDelegated, "bound_unusable_no_alternative:"+cause
				b.touch(now)
				return r.finish(d)
			}
			rebind := false
			switch r.cfg.RebindPolicy {
			case config.RebindConservative:
				rebind = permanent
			case config.RebindEager:
				// Temporary causes fail over first; learnLocked moves the binding once
				// the alternative succeeds.
				rebind = permanent
			case config.RebindNever:
				rebind = cause == "removed"
			}
			d.AuthID, d.Cause = alt, cause
			if rebind {
				d.Outcome = OutcomeRebind
				b.rebind(alt, cause, now)
				r.counters.Rebinds++
			} else {
				d.Outcome = OutcomeFailover
				b.Failovers++
				b.LastFailoverAt = now
				b.LastFailoverTo = alt
				r.counters.Failovers++
			}
			b.touch(now)
			r.dirty = true
			return r.finish(d)
		}
		if r.cfg.AffinityInheritParent && req.Session.ParentKey != "" {
			if pb := r.parentBinding(provider, req, now); pb != nil {
				if e := evals[pb.AuthID]; e != nil && e.usable {
					d.Outcome, d.AuthID = OutcomeInherited, pb.AuthID
					d.AffinityOverrode = len(ranked) > 0 && ranked[0].AuthID != pb.AuthID
					r.bind(provider, req, pb.AuthID, "inherited", now)
					r.counters.Inherited++
					return r.finish(d)
				}
			}
		}
		alt, fallback := r.choose(order, ranked, "")
		d.Fallback = fallback
		if alt == "" {
			d.Outcome, d.Cause = OutcomeDelegated, "no_trustworthy_quota"
			return r.finish(d)
		}
		d.Outcome, d.AuthID = OutcomeNewSession, alt
		r.bind(provider, req, alt, "selected", now)
		r.counters.NewSession++
		return r.finish(d)
	}

	alt, fallback := r.choose(order, ranked, "")
	d.Fallback = fallback
	if alt == "" {
		d.Outcome, d.Cause = OutcomeDelegated, "no_trustworthy_quota"
		return r.finish(d)
	}
	d.Outcome, d.AuthID = OutcomeNoSession, alt
	if req.HasSession {
		d.Cause = "affinity_disabled"
	}
	return r.finish(d)
}

func (r *Router) finish(d Decision) Decision {
	d.Applied = r.cfg.Mode == config.ModeActive && d.AuthID != ""
	switch {
	case d.Outcome == OutcomeDelegated:
		r.counters.Delegated++
	case d.Applied:
		r.counters.Routed++
	}
	if d.Outcome == OutcomeAffinity {
		r.counters.Affinity++
	}
	d.ID = r.decisions.add(d)
	return d
}

// choose returns the top-ranked candidate other than exclude. When nothing is
// rankable but some candidates are known-bad, it falls back to the usable
// candidates with unknown quota rather than handing CPA a known-bad pool.
func (r *Router) choose(order []*eval, ranked []Ranked, exclude string) (string, bool) {
	for _, rk := range ranked {
		if rk.AuthID != exclude {
			return rk.AuthID, false
		}
	}
	knownBad := false
	var usable []*eval
	for _, e := range order {
		if e.c.ID == exclude {
			continue
		}
		if !e.usable {
			knownBad = true
			continue
		}
		usable = append(usable, e)
	}
	if !knownBad || len(usable) == 0 {
		return "", false
	}
	sort.SliceStable(usable, func(i, j int) bool {
		if usable[i].c.Priority != usable[j].c.Priority {
			return usable[i].c.Priority > usable[j].c.Priority
		}
		return usable[i].c.ID < usable[j].c.ID
	})
	return usable[0].c.ID, true
}

func (r *Router) evaluate(c Candidate, model string, now time.Time) *eval {
	e := &eval{c: c, offered: true, plugin: r.cfg.AccountPriority[c.ID]}
	a := r.accounts[c.ID]
	block := func(cause string, permanent bool) *eval {
		e.cause, e.permanent = cause, permanent
		return e
	}
	if strings.EqualFold(c.Status, "disabled") || (a != nil && a.Disabled) {
		return block("disabled", true)
	}
	if a != nil && !a.Health.AuthFailedAt.IsZero() {
		return block(fmt.Sprintf("auth_failed_%d", a.Health.AuthFailStatus), true)
	}
	if a == nil || !a.HasSnapshot {
		if a != nil && now.Before(a.Health.RateLimitedUntil) {
			return block("rate_limited", false)
		}
		if a != nil && now.Before(a.Health.CircuitUntil) {
			return block("circuit_open", false)
		}
		e.usable = true
		e.assessment = quota.Assessment{State: quota.Unknown, Reason: "no_quota_data", FiveHourUsed: -1}
		return block("quota_unknown", false)
	}
	h := a.Health
	e.assessment = quota.Assess(a.Snapshot, model, now, r.cfg.MaxAge)
	if e.assessment.State == quota.Exhausted {
		return block("exhausted:"+e.assessment.Reason, true)
	}
	if fam := family(model); fam != "" {
		if until, ok := h.ModelBlocked[fam]; ok && now.Before(until) {
			return block("model_unavailable:"+fam, true)
		}
	}
	if until, ok := h.ModelBlocked[strings.ToLower(model)]; ok && now.Before(until) {
		return block("model_unavailable", true)
	}
	if now.Before(h.RateLimitedUntil) {
		return block("rate_limited", false)
	}
	if now.Before(h.CircuitUntil) {
		return block("circuit_open", false)
	}
	if e.assessment.State == quota.ResetPending && strings.HasSuffix(e.assessment.Reason, "_awaiting_reset_evidence") {
		return block("awaiting_reset_verification", false)
	}
	e.usable = true
	if e.assessment.State != quota.Ready {
		return block("quota_"+string(e.assessment.State)+":"+e.assessment.Reason, false)
	}
	if h.PollAuthRejected {
		return block("quota_auth_suspect", false)
	}
	if e.assessment.Remaining < r.cfg.NewSessionMinRemaining {
		e.lowRoom = true
		return block("low_headroom:"+strconv.FormatFloat(math.Round(e.assessment.Remaining*10)/10, 'f', -1, 64), false)
	}
	e.rankable = true
	e.cause = ""
	return e
}

// boundCause explains why a bound account cannot serve this request and
// whether the reason is permanent (allows a conservative rebind).
func (r *Router) boundCause(authID string, e *eval, model string, now time.Time) (string, bool) {
	if e != nil {
		return e.cause, e.permanent
	}
	a := r.accounts[authID]
	switch {
	case a == nil && r.rosterOK:
		return "removed", true
	case a == nil:
		return "not_offered", false
	case !a.Present && r.rosterOK:
		return "removed", true
	case a.Disabled:
		return "disabled", true
	case !a.Health.AuthFailedAt.IsZero():
		return fmt.Sprintf("auth_failed_%d", a.Health.AuthFailStatus), true
	}
	if a.HasSnapshot {
		if as := quota.Assess(a.Snapshot, model, now, r.cfg.MaxAge); as.State == quota.Exhausted {
			return "exhausted:" + as.Reason, true
		}
	}
	if fam := family(model); fam != "" {
		if until, ok := a.Health.ModelBlocked[fam]; ok && now.Before(until) {
			return "model_unavailable:" + fam, true
		}
	}
	return "not_offered", false
}

// rank orders rankable candidates: CPA priority, then plugin priority under
// manual_priority, then the policy score, then remaining capacity and ID.
func (r *Router) rank(order []*eval, now time.Time, strategy string) []Ranked {
	type scored struct {
		e      *eval
		score  float64
		group  int
		reason string
	}
	base := strategy
	if strategy == config.StrategyManualPriority {
		base = r.cfg.ManualPriorityBase
	}
	var list []scored
	for _, e := range order {
		if !e.rankable {
			continue
		}
		s := scored{e: e}
		a := e.assessment
		hours := math.Inf(1)
		resetText := "no reset reported"
		if a.Applicable != nil && a.Applicable.ResetsAt != nil {
			hours = a.Applicable.ResetsAt.Sub(now).Hours()
			resetText = fmt.Sprintf("%s resets in %s", windowName(*a.Applicable), human(a.Applicable.ResetsAt.Sub(now)))
		}
		switch base {
		case config.StrategyQuotaPressure:
			h := hours
			if math.IsInf(h, 1) {
				h = 168
			}
			s.score = a.Remaining / math.Max(h, r.cfg.PressureMinHours)
			s.reason = fmt.Sprintf("pressure %.2f (%.0f%% left, %s)", s.score, a.Remaining, resetText)
		case config.StrategyBalanced:
			s.score = -hours
			if a.FiveHourUsed >= r.cfg.BalancedFiveHourSoftLimit {
				s.group = 1
				s.reason = fmt.Sprintf("5h at %.0f%% (over %.0f%% soft limit); %s", a.FiveHourUsed, r.cfg.BalancedFiveHourSoftLimit, resetText)
			} else {
				s.reason = fmt.Sprintf("%s, %.0f%% left", resetText, a.Remaining)
			}
		default:
			s.score = -hours
			s.reason = fmt.Sprintf("%s, %.0f%% left", resetText, a.Remaining)
		}
		if strategy == config.StrategyManualPriority && e.plugin != 0 {
			s.reason = fmt.Sprintf("priority %d; %s", e.plugin, s.reason)
		}
		list = append(list, s)
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.e.c.Priority != b.e.c.Priority {
			return a.e.c.Priority > b.e.c.Priority
		}
		if strategy == config.StrategyManualPriority && a.e.plugin != b.e.plugin {
			return a.e.plugin > b.e.plugin
		}
		if a.group != b.group {
			return a.group < b.group
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if a.e.assessment.Remaining != b.e.assessment.Remaining {
			return a.e.assessment.Remaining > b.e.assessment.Remaining
		}
		return a.e.c.ID < b.e.c.ID
	})
	out := make([]Ranked, len(list))
	for i, s := range list {
		score := s.score
		if math.IsInf(score, 0) {
			score = 0
		}
		out[i] = Ranked{AuthID: s.e.c.ID, Score: score, Reason: s.reason, State: string(s.e.assessment.State)}
	}
	return out
}

func windowName(w quota.Window) string {
	switch w.Kind {
	case quota.Weekly:
		return "weekly"
	case quota.Monthly:
		return "monthly"
	case quota.ModelWeekly:
		return w.Family + " weekly"
	}
	return string(w.Kind)
}

// human formats like the dashboard's duration(): truncated, two units.
func human(d time.Duration) string {
	m := int(max(d, 0).Round(time.Second).Minutes())
	switch {
	case m >= 24*60:
		return fmt.Sprintf("%dd %dh", m/(24*60), m%(24*60)/60)
	case m >= 60:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func (r *Router) scope(model string) string {
	if r.cfg.AffinityScope == config.ScopeProvider {
		return "*"
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(m, '('); i > 0 {
		m = m[:i] // thinking suffix, e.g. claude-opus-4-6(high)
	}
	return m
}

func bindingKey(provider, scope, sessionKey string) string {
	return provider + "|" + scope + "|" + sessionKey
}

func (r *Router) binding(provider string, req PickRequest, now time.Time) *Binding {
	key := bindingKey(provider, r.scope(req.Model), req.Session.Key)
	b := r.bindings[key]
	if b == nil {
		return nil
	}
	if now.Sub(b.LastSeen) >= r.cfg.AffinityTTL {
		delete(r.bindings, key)
		r.dirty = true
		return nil
	}
	if !b.Applied && r.cfg.Mode == config.ModeActive {
		return nil
	}
	return b
}

func (r *Router) parentBinding(provider string, req PickRequest, now time.Time) *Binding {
	if b := r.bindings[bindingKey(provider, r.scope(req.Model), req.Session.ParentKey)]; b != nil && now.Sub(b.LastSeen) < r.cfg.AffinityTTL {
		return b
	}
	// A subagent may use a different model than its parent; take the parent's
	// most recently active binding for the same provider.
	var best *Binding
	for _, b := range r.bindings {
		if b.Provider != provider || b.SessionKey != req.Session.ParentKey || now.Sub(b.LastSeen) >= r.cfg.AffinityTTL {
			continue
		}
		if best == nil || b.LastSeen.After(best.LastSeen) {
			best = b
		}
	}
	return best
}

func (r *Router) bind(provider string, req PickRequest, authID, origin string, now time.Time) *Binding {
	scope := r.scope(req.Model)
	key := bindingKey(provider, scope, req.Session.Key)
	b := &Binding{
		Key: key, Provider: provider, Scope: scope, SessionKey: req.Session.Key, ParentKey: req.Session.ParentKey,
		Client: req.Session.Client, Kind: string(req.Session.Kind), AuthID: authID, Origin: origin,
		Applied: r.cfg.Mode == config.ModeActive, CreatedAt: now, LastSeen: now, Requests: 1,
	}
	r.bindings[key] = b
	r.dirty = true
	if len(r.bindings) > r.cfg.AffinityMaxSessions {
		r.evictLocked(now)
	}
	return b
}

func (b *Binding) touch(now time.Time) {
	b.LastSeen = now
	b.Requests++
}

func (b *Binding) rebind(authID, cause string, now time.Time) {
	b.PreviousAuthID = b.AuthID
	b.AuthID = authID
	b.Rebinds++
	b.LastRebindAt = now
	b.LastRebindWhy = cause
}

// evictLocked drops expired bindings and, above the cap, the least recently
// used 1% so eviction cost is amortized.
func (r *Router) evictLocked(now time.Time) {
	for k, b := range r.bindings {
		if now.Sub(b.LastSeen) >= r.cfg.AffinityTTL {
			delete(r.bindings, k)
		}
	}
	over := len(r.bindings) - r.cfg.AffinityMaxSessions
	if over <= 0 {
		return
	}
	over += r.cfg.AffinityMaxSessions / 100
	all := make([]*Binding, 0, len(r.bindings))
	for _, b := range r.bindings {
		all = append(all, b)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].LastSeen.Before(all[j].LastSeen) })
	for i := 0; i < over && i < len(all); i++ {
		delete(r.bindings, all[i].Key)
	}
}

// Sweep expires idle bindings. The runtime calls it periodically.
func (r *Router) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	before := len(r.bindings)
	r.evictLocked(r.now())
	if len(r.bindings) != before {
		r.dirty = true
	}
}

// learnLocked uses an observed attempt to attach results to decisions and to
// create or repair bindings the plugin did not choose itself (delegated
// requests, shadow mode, and failovers whose usage arrived late).
func (r *Router) learnLocked(o Outcome, provider string, now time.Time) {
	if !o.HasSession {
		return
	}
	r.decisions.attach(provider, o.Session.Key, Result{At: now, AuthID: o.AuthID, Status: o.Status, Failed: o.Failed}, now)
	if !r.cfg.AffinityEnabled || o.Failed {
		return
	}
	key := bindingKey(provider, r.scope(o.Model), o.Session.Key)
	b := r.bindings[key]
	if b != nil && now.Sub(b.LastSeen) >= r.cfg.AffinityTTL {
		delete(r.bindings, key)
		b = nil
	}
	if b == nil {
		req := PickRequest{Model: o.Model, Session: o.Session, HasSession: true}
		nb := r.bind(provider, req, o.AuthID, "learned", now)
		nb.ActualAuthID = o.AuthID
		return
	}
	b.ActualAuthID = o.AuthID
	if !b.Applied || b.AuthID == o.AuthID {
		return
	}
	cause, permanent := r.boundCause(b.AuthID, nil, o.Model, now)
	if r.cfg.RebindPolicy == config.RebindEager || (r.cfg.RebindPolicy == config.RebindConservative && permanent) || cause == "removed" {
		b.rebind(o.AuthID, "learned:"+cause, now)
		r.counters.Rebinds++
		r.dirty = true
	}
}
