// Package router is the scheduling core: it owns the account roster, quota
// snapshots, account health, session bindings, and the decision log, and it
// answers picks from that in-memory state without I/O.
package router

import (
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/session"
)

// Clock returns the current time. Tests inject a fixed clock.
type Clock func() time.Time

// Candidate is one account offered by CPA for a pick.
type Candidate struct {
	ID       string
	Provider string
	Priority int
	Status   string
}

// PickRequest is a scheduler pick translated out of the plugin SDK.
type PickRequest struct {
	Provider   string
	Model      string
	Stream     bool
	Session    session.Ref
	HasSession bool
	// PinnedAuthID is set when CPA pinned execution to one account
	// (WebSocket continuation or an explicit pin).
	PinnedAuthID string
	Candidates   []Candidate
}

// Outcome is one upstream attempt reported by CPA's usage stream.
type Outcome struct {
	At         time.Time
	AuthID     string
	Provider   string
	Model      string
	Session    session.Ref
	HasSession bool
	Failed     bool
	Status     int
	Headers    http.Header
}

// RosterEntry is one credential from host.auth.list.
type RosterEntry struct {
	ID          string
	AuthIndex   string
	Provider    string
	Name        string
	Label       string
	Email       string
	Account     string
	AccountType string
	Status      string
	StatusMsg   string
	Disabled    bool
	Unavailable bool
	RuntimeOnly bool
	Priority    int
	Path        string
	Size        int64
	ModTime     time.Time
	BaseURL     string
	NextRetry   time.Time
}

// PollTask is an account due for a quota poll.
type PollTask struct {
	AuthID    string
	AuthIndex string
	Provider  string
}

// Router is safe for concurrent use. All methods take one mutex; none perform I/O.
type Router struct {
	mu        sync.Mutex
	now       Clock
	jitter    func() float64
	cfg       config.Config
	accounts  map[string]*account
	bindings  map[string]*Binding
	decisions decisionLog
	rosterOK  bool
	rosterErr string
	rosterAt  time.Time
	manualAt  map[string]time.Time
	lastModel map[string]string
	counters  Counters
	dirty     bool
}

// Counters are cumulative since process start.
type Counters struct {
	Picks      uint64 `json:"picks"`
	Routed     uint64 `json:"routed"`
	Delegated  uint64 `json:"delegated"`
	Affinity   uint64 `json:"affinity"`
	NewSession uint64 `json:"new_session"`
	Failovers  uint64 `json:"failovers"`
	Rebinds    uint64 `json:"rebinds"`
	Inherited  uint64 `json:"inherited"`
	Observed   uint64 `json:"observed"`
	Unrelated  uint64 `json:"unrelated_provider"`
}

// New returns an empty router.
func New(cfg config.Config, now Clock) *Router {
	if now == nil {
		now = time.Now
	}
	return &Router{
		now:       now,
		jitter:    rand.Float64,
		cfg:       cfg,
		accounts:  map[string]*account{},
		bindings:  map[string]*Binding{},
		decisions: newDecisionLog(cfg.ActivityRetention),
		manualAt:  map[string]time.Time{},
		lastModel: map[string]string{},
	}
}

// SetJitter replaces the random source used for poll jitter (tests).
func (r *Router) SetJitter(f func() float64) {
	r.mu.Lock()
	r.jitter = f
	r.mu.Unlock()
}

// Configure applies new settings without discarding state. Switching from
// shadow to active adopts the accounts sessions were actually served by.
func (r *Router) Configure(cfg config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wasShadow := r.cfg.Mode == config.ModeShadow
	r.cfg = cfg
	r.decisions.resize(cfg.ActivityRetention)
	if wasShadow && cfg.Mode == config.ModeActive {
		for _, b := range r.bindings {
			if !b.Applied {
				if b.ActualAuthID != "" {
					b.AuthID = b.ActualAuthID
				}
				b.Applied = true
			}
		}
	}
	r.evictLocked(r.now())
	r.dirty = true
}

// Config returns the active settings.
func (r *Router) Config() config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

// Counters returns cumulative counters.
func (r *Router) Counters() Counters {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters
}

// TakeDirty reports whether persistent state changed since the last call.
func (r *Router) TakeDirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.dirty
	r.dirty = false
	return d
}

func supportedProvider(p string) bool { return p == "claude" || p == "codex" }

func normalizeProvider(p string) string { return strings.ToLower(strings.TrimSpace(p)) }
