package ignite

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	log "github.com/sirupsen/logrus"
)

const (
	fiveHour       = 5 * time.Hour
	modelSkipFor   = 24 * time.Hour
	maxEvents      = 200
	codexIdleSlack = 2 * time.Minute
)

// Result is the outcome of one ignition request.
type Result struct {
	// Status is the upstream HTTP status, 0 for network or local errors.
	Status int
	// Cooldown reports that CPA refused because the account is cooling down.
	Cooldown bool
	Err      error
}

// Host is what the igniter needs from the rest of prox.
type Host interface {
	// Accounts returns the router's current account views.
	Accounts() []router.AccountView
	// RequestRefresh schedules a quota poll for one account.
	RequestRefresh(authID string)
	// Models lists the model IDs CPA serves with this account.
	Models(authID string) []string
	// CredentialStamp changes whenever the account's credential changes.
	CredentialStamp(authID string) string
	// Fire sends one minimal request pinned to the account and returns once
	// the upstream has started answering (or failed).
	Fire(ctx context.Context, provider, authID, model string) Result
}

// Notifier receives operator-facing ignition problems.
type Notifier func(ev Event)

// Event is one ignition log entry.
type Event struct {
	At       time.Time `json:"at"`
	AuthID   string    `json:"auth_id"`
	Provider string    `json:"provider"`
	Kind     string    `json:"kind"`
	Model    string    `json:"model,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// Event kinds. Problem kinds are forwarded to the notifier.
const (
	EventFired          = "fired"
	EventSuccess        = "success"
	EventNoWindow       = "no_window_started"
	EventCredential     = "credential_invalid"
	EventCredentialOK   = "credential_restored"
	EventRateLimited    = "rate_limited"
	EventModelRefused   = "model_refused"
	EventNoModel        = "no_model"
	EventTransient      = "transient_failure"
	EventPaused         = "paused_until_tomorrow"
	EventCooldown       = "cpa_cooldown"
	EventManualFailure  = "manual_failure"
	EventManualAccepted = "manual_fired"
)

// AccountState is the persisted per-account ignition state.
type AccountState struct {
	NextAttempt     time.Time            `json:"next_attempt,omitempty"`
	PauseReason     string               `json:"pause_reason,omitempty"`
	Transient       int                  `json:"transient,omitempty"`
	CredBlocked     bool                 `json:"cred_blocked,omitempty"`
	CredStamp       string               `json:"cred_stamp,omitempty"`
	FiredAt         time.Time            `json:"fired_at,omitempty"`
	Confirming      bool                 `json:"confirming,omitempty"`
	ConfirmDeadline time.Time            `json:"confirm_deadline,omitempty"`
	LastSuccess     time.Time            `json:"last_success,omitempty"`
	LastModel       string               `json:"last_model,omitempty"`
	SkipModels      map[string]time.Time `json:"skip_models,omitempty"`
	refreshedAt     time.Time
}

// State is the persisted igniter state.
type State struct {
	Accounts map[string]*AccountState `json:"accounts"`
	Events   []Event                  `json:"events"`
}

// Plan describes the next ignition of one account for the dashboard.
type Plan struct {
	AuthID   string     `json:"auth_id"`
	Provider string     `json:"provider"`
	Display  string     `json:"display"`
	Status   string     `json:"status"`
	Reason   string     `json:"reason,omitempty"`
	NextAt   *time.Time `json:"next_at,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Model    string     `json:"model,omitempty"`
}

// Igniter runs the ignition schedule. Call Tick about once a second.
type Igniter struct {
	Now    func() time.Time
	Notify Notifier

	mu       sync.Mutex
	cfg      Config
	host     Host
	state    State
	inflight map[string]bool
	dirty    bool
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// New returns an igniter.
func New(host Host, cfg Config) *Igniter {
	ctx, cancel := context.WithCancel(context.Background())
	return &Igniter{
		host: host, cfg: cfg, ctx: ctx, cancel: cancel,
		state:    State{Accounts: map[string]*AccountState{}},
		inflight: map[string]bool{},
	}
}

func (g *Igniter) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Configure replaces settings.
func (g *Igniter) Configure(cfg Config) {
	g.mu.Lock()
	g.cfg = cfg
	g.mu.Unlock()
}

// Close cancels in-flight requests and waits for them.
func (g *Igniter) Close() {
	g.cancel()
	g.wg.Wait()
}

// Import restores persisted state.
func (g *Igniter) Import(s State) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.Accounts == nil {
		s.Accounts = map[string]*AccountState{}
	}
	g.state = s
}

// Export returns a copy of the state for persistence.
func (g *Igniter) Export() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := State{Accounts: make(map[string]*AccountState, len(g.state.Accounts)), Events: append([]Event(nil), g.state.Events...)}
	for k, v := range g.state.Accounts {
		c := *v
		c.SkipModels = make(map[string]time.Time, len(v.SkipModels))
		for m, t := range v.SkipModels {
			c.SkipModels[m] = t
		}
		out.Accounts[k] = &c
	}
	return out
}

// TakeDirty reports and clears whether state changed since the last call.
func (g *Igniter) TakeDirty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.dirty
	g.dirty = false
	return d
}

// Events returns recent events, newest first.
func (g *Igniter) Events(limit int) []Event {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.state.Events)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Event, 0, limit)
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, g.state.Events[i])
	}
	return out
}

func (g *Igniter) recordLocked(ev Event) {
	g.state.Events = append(g.state.Events, ev)
	if len(g.state.Events) > maxEvents {
		g.state.Events = append([]Event(nil), g.state.Events[len(g.state.Events)-maxEvents:]...)
	}
	g.dirty = true
	if g.Notify != nil && problem(ev.Kind) {
		go g.Notify(ev)
	}
}

func problem(kind string) bool {
	switch kind {
	case EventCredential, EventCredentialOK, EventRateLimited, EventNoWindow, EventNoModel, EventPaused:
		return true
	}
	return false
}

func (g *Igniter) accountLocked(id string) *AccountState {
	st := g.state.Accounts[id]
	if st == nil {
		st = &AccountState{}
		g.state.Accounts[id] = st
	}
	return st
}

// windowState classifies an account's five-hour window.
type windowState int

const (
	windowUnknown windowState = iota
	windowIdle
	windowActive
)

func classify(a router.AccountView, now time.Time) (windowState, time.Time) {
	for _, w := range a.Windows {
		if w.Kind != "five_hour" {
			continue
		}
		if w.Stale && w.ResetsAt != nil && w.ResetsAt.After(now) {
			// Old data still predicting a future reset is good enough to wait on.
			return windowActive, *w.ResetsAt
		}
		if w.Stale {
			return windowUnknown, time.Time{}
		}
		if w.ResetsAt == nil {
			return windowIdle, time.Time{}
		}
		if !w.ResetsAt.After(now) {
			// The window ended; resetAt lets the caller honor the grace period.
			return windowIdle, *w.ResetsAt
		}
		// An unused Codex window reports a reset one full window after each
		// observation; it has not actually started.
		if a.Provider == "codex" && w.Used == 0 {
			if d := w.ResetsAt.Sub(w.ObservedAt.Add(fiveHour)); d > -codexIdleSlack && d < codexIdleSlack {
				return windowIdle, time.Time{}
			}
		}
		return windowActive, *w.ResetsAt
	}
	return windowUnknown, time.Time{}
}

// longExhausted reports whether an account-wide long window is used up.
func longExhausted(a router.AccountView) bool {
	for _, w := range a.Windows {
		if w.Kind == "five_hour" || w.Kind == "model_weekly" {
			continue
		}
		if w.Rejected || w.Remaining <= 0 {
			return true
		}
	}
	return false
}

// Tick evaluates every account and starts due ignitions.
func (g *Igniter) Tick() {
	g.mu.Lock()
	cfg, host := g.cfg, g.host
	g.mu.Unlock()
	if !cfg.Enabled || host == nil {
		return
	}
	now := g.now()
	for _, a := range host.Accounts() {
		pc, ok := cfg.provider(a.Provider)
		if !ok || !a.Present || a.Disabled {
			continue
		}
		g.evaluate(cfg, pc, host, a, now)
	}
}

func (g *Igniter) evaluate(cfg Config, pc ProviderConfig, host Host, a router.AccountView, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[a.ID] {
		return
	}
	st := g.accountLocked(a.ID)
	if st.CredBlocked {
		stamp := host.CredentialStamp(a.ID)
		if stamp == "" || stamp == st.CredStamp {
			return
		}
		st.CredBlocked, st.PauseReason, st.NextAttempt = false, "", time.Time{}
		g.recordLocked(Event{At: now, AuthID: a.ID, Provider: a.Provider, Kind: EventCredentialOK})
	}
	state, resetAt := classify(a, now)
	if st.Confirming {
		g.confirmLocked(cfg, host, st, a, state, resetAt, now)
		return
	}
	if now.Before(st.NextAttempt) {
		return
	}
	if st.PauseReason != "" {
		st.PauseReason = ""
		g.dirty = true
	}
	if longExhausted(a) {
		return
	}
	if state == windowUnknown {
		return
	}
	if !resetAt.IsZero() && now.Before(resetAt.Add(time.Duration(cfg.GraceSeconds)*time.Second)) {
		return
	}
	if !cfg.Allowed(now) {
		return
	}
	g.startLocked(cfg, pc, host, a, now, false)
}

func (g *Igniter) confirmLocked(cfg Config, host Host, st *AccountState, a router.AccountView, state windowState, resetAt, now time.Time) {
	if state == windowActive && resetAt.After(st.FiredAt) {
		st.Confirming, st.Transient, st.LastSuccess = false, 0, now
		g.recordLocked(Event{At: now, AuthID: a.ID, Provider: a.Provider, Kind: EventSuccess, Model: st.LastModel, Detail: "window resets " + resetAt.UTC().Format(time.RFC3339)})
		return
	}
	if now.After(st.ConfirmDeadline) {
		st.Confirming = false
		g.pauseLocked(cfg, st, a, now, EventNoWindow, "request succeeded but no five-hour window started")
		return
	}
	// Ask for a fresh poll every 30 seconds while confirming.
	if now.Sub(st.refreshedAt) >= 30*time.Second && now.Sub(st.FiredAt) >= 5*time.Second {
		st.refreshedAt = now
		go host.RequestRefresh(a.ID)
	}
}

func (g *Igniter) pauseLocked(cfg Config, st *AccountState, a router.AccountView, now time.Time, kind, detail string) {
	st.NextAttempt = cfg.NextStart(now)
	st.PauseReason = kind
	st.Transient = 0
	g.recordLocked(Event{At: now, AuthID: a.ID, Provider: a.Provider, Kind: kind, Model: st.LastModel, Detail: detail + "; paused until " + st.NextAttempt.Format(time.RFC3339)})
}

// Ignite fires one account now, outside the schedule. Failures are recorded
// but do not change the automatic schedule.
func (g *Igniter) Ignite(authID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.host == nil || g.inflight[authID] {
		return false
	}
	for _, a := range g.host.Accounts() {
		if a.ID != authID {
			continue
		}
		pc, _ := g.cfg.Providers[a.Provider]
		if a.Provider != "claude" && a.Provider != "codex" {
			return false
		}
		g.startLocked(g.cfg, pc, g.host, a, g.now(), true)
		return true
	}
	return false
}

func (g *Igniter) startLocked(cfg Config, pc ProviderConfig, host Host, a router.AccountView, now time.Time, manual bool) {
	st := g.accountLocked(a.ID)
	models := g.candidatesLocked(pc, host, st, a, now)
	if len(models) == 0 {
		if manual {
			g.recordLocked(Event{At: now, AuthID: a.ID, Provider: a.Provider, Kind: EventManualFailure, Detail: "no ignition model available"})
			return
		}
		g.pauseLocked(cfg, st, a, now, EventNoModel, "no ignition model available")
		return
	}
	g.inflight[a.ID] = true
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.run(cfg, host, a, models, manual)
	}()
}

func (g *Igniter) candidatesLocked(pc ProviderConfig, host Host, st *AccountState, a router.AccountView, now time.Time) []string {
	if m := strings.TrimSpace(pc.Model); m != "" {
		return []string{m}
	}
	var out []string
	for _, m := range PickModels(a.Provider, host.Models(a.ID)) {
		if until, ok := st.SkipModels[m]; ok && now.Before(until) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// PickModels orders a provider's models from cheapest-looking to most
// expensive: Haiku for Claude, Luna/mini for Codex, newest first.
func PickModels(provider string, models []string) []string {
	var prefer []string
	switch provider {
	case "claude":
		prefer = []string{"haiku"}
	case "codex":
		prefer = []string{"luna", "mini", "nano"}
	}
	var out []string
	for _, p := range prefer {
		var tier []string
		for _, m := range models {
			if strings.Contains(strings.ToLower(m), p) && !contains(out, m) {
				tier = append(tier, m)
			}
		}
		// Newer model IDs sort later lexically within a family.
		sort.Sort(sort.Reverse(sort.StringSlice(tier)))
		out = append(out, tier...)
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (g *Igniter) run(cfg Config, host Host, a router.AccountView, models []string, manual bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("prox ignite: panic: %v", r)
		}
		g.mu.Lock()
		delete(g.inflight, a.ID)
		g.mu.Unlock()
	}()
	for i, model := range models {
		res := host.Fire(g.ctx, a.Provider, a.ID, model)
		if g.ctx.Err() != nil {
			return
		}
		g.mu.Lock()
		retry := g.applyLocked(cfg, host, a, model, res, manual, i == len(models)-1)
		g.mu.Unlock()
		if !retry {
			return
		}
	}
}

// applyLocked records one result and reports whether to try the next model.
func (g *Igniter) applyLocked(cfg Config, host Host, a router.AccountView, model string, res Result, manual, last bool) bool {
	now := g.now()
	st := g.accountLocked(a.ID)
	st.LastModel = model
	detail := ""
	if res.Err != nil {
		detail = firstSentence(res.Err.Error())
	}
	ev := Event{At: now, AuthID: a.ID, Provider: a.Provider, Model: model, Detail: detail}
	if res.Err == nil {
		st.FiredAt = now
		st.Confirming = true
		st.ConfirmDeadline = now.Add(time.Duration(cfg.ConfirmSeconds) * time.Second)
		st.refreshedAt = time.Time{}
		ev.Kind = EventFired
		if manual {
			ev.Kind = EventManualAccepted
		}
		g.recordLocked(ev)
		return false
	}
	if manual {
		ev.Kind = EventManualFailure
		g.recordLocked(ev)
		return false
	}
	switch {
	case res.Cooldown:
		// CPA is cooling the account down; retry when the cooldown ends,
		// without counting a failure.
		next := now.Add(time.Minute)
		if a.CooldownUntil != nil && a.CooldownUntil.After(now) {
			next = a.CooldownUntil.Add(time.Duration(cfg.GraceSeconds) * time.Second)
		}
		st.NextAttempt = next
		ev.Kind = EventCooldown
		g.recordLocked(ev)
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		st.CredBlocked = true
		st.CredStamp = host.CredentialStamp(a.ID)
		st.PauseReason = EventCredential
		ev.Kind = EventCredential
		g.recordLocked(ev)
	case res.Status == http.StatusTooManyRequests:
		g.pauseLocked(cfg, st, a, now, EventRateLimited, detail)
	case res.Status == http.StatusBadRequest || res.Status == http.StatusNotFound || res.Status == http.StatusUnprocessableEntity:
		if st.SkipModels == nil {
			st.SkipModels = map[string]time.Time{}
		}
		st.SkipModels[model] = now.Add(modelSkipFor)
		ev.Kind = EventModelRefused
		g.recordLocked(ev)
		if last {
			g.pauseLocked(cfg, st, a, now, EventNoModel, "every ignition model was refused")
			return false
		}
		return true
	default:
		st.Transient++
		if st.Transient >= cfg.MaxTransientFailures {
			g.pauseLocked(cfg, st, a, now, EventPaused, "repeated failures: "+detail)
			return false
		}
		wait := float64(cfg.FailureRetrySeconds) * math.Pow(cfg.FailureBackoffMultiplier, float64(st.Transient-1))
		st.NextAttempt = now.Add(time.Duration(wait) * time.Second)
		ev.Kind = EventTransient
		g.recordLocked(ev)
	}
	return false
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Plans describes the schedule for every account the igniter would handle.
func (g *Igniter) Plans() []Plan {
	g.mu.Lock()
	cfg, host := g.cfg, g.host
	g.mu.Unlock()
	if host == nil {
		return nil
	}
	now := g.now()
	out := []Plan{}
	for _, a := range host.Accounts() {
		if a.Provider != "claude" && a.Provider != "codex" {
			continue
		}
		p := Plan{AuthID: a.ID, Provider: a.Provider, Display: a.Display}
		state, resetAt := classify(a, now)
		if !resetAt.IsZero() {
			r := resetAt
			p.ResetsAt = &r
		}
		g.mu.Lock()
		st := g.state.Accounts[a.ID]
		inflight := g.inflight[a.ID]
		g.mu.Unlock()
		_, enabled := cfg.provider(a.Provider)
		switch {
		case !cfg.Enabled || !enabled:
			p.Status = "disabled"
		case !a.Present || a.Disabled:
			p.Status = "unavailable"
		case inflight:
			p.Status = "firing"
		case st != nil && st.CredBlocked:
			p.Status, p.Reason = "paused", EventCredential
		case st != nil && st.Confirming:
			p.Status = "confirming"
		case st != nil && now.Before(st.NextAttempt):
			p.Status, p.Reason = "waiting", st.PauseReason
			t := st.NextAttempt
			p.NextAt = &t
		case longExhausted(a):
			p.Status, p.Reason = "blocked", "long_window_exhausted"
		case state == windowUnknown:
			p.Status, p.Reason = "unknown", "no fresh five-hour window"
		default:
			next := now
			if state == windowActive {
				p.Status = "active"
				next = resetAt.Add(time.Duration(cfg.GraceSeconds) * time.Second)
			} else {
				p.Status = "idle"
			}
			if !cfg.Allowed(next) {
				next = cfg.NextStart(next)
			}
			p.NextAt = &next
		}
		if st != nil {
			p.Model = st.LastModel
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AuthID < out[j].AuthID })
	return out
}
