// Package keepalive implements agent-aware prompt-cache keepalive for Claude
// Code sessions.
//
// An orchestrator session that blocks on a subagent for longer than the prompt
// cache TTL pays a full cache write on the return turn instead of a read. On the
// 1h pool that write costs roughly ten times the read it replaces, so refreshing
// the entry with one minimal request per TTL window is close to free.
//
// The proxy is the right place for that refresh because it holds the last request
// body, the credential, and the session-to-account binding, so the probe warms the
// same per-account cache the next real request will hit.
//
// The scheduler probes only while a task belonging to the session is still
// running. A session idling on human input is never probed: that wait is
// unbounded, and the guarantee that another turn is coming is what makes the
// probe pay for itself.
package keepalive

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ProbeMetadataKey marks an execution as a keepalive probe.
//
// A probe travels the ordinary request path, so without this marker the
// observation hook would record the probe as a real request: that would reset
// the consecutive-probe budget on every probe and let an idle session probe
// forever, defeating max-probes.
const ProbeMetadataKey = "cache_keepalive_probe"

// IsProbeExecution reports whether execution metadata belongs to a keepalive probe.
func IsProbeExecution(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	marker, ok := metadata[ProbeMetadataKey].(bool)
	return ok && marker
}

// Timer is the subset of time.Timer the scheduler needs. Tests substitute a
// deterministic implementation.
type Timer interface {
	Stop() bool
}

// Config holds the resolved runtime settings for the scheduler.
type Config struct {
	// Enabled turns scheduling on. A disabled scheduler ignores every observation.
	Enabled bool
	// BeforeExpiry fires a probe at t_start + ttl - BeforeExpiry on the 1h pool.
	BeforeExpiry time.Duration
	// BeforeExpiry5m is the same lead time for the 5m pool. Zero falls back to
	// BeforeExpiry, which on a 5m TTL leaves no window and drops the session.
	BeforeExpiry5m time.Duration
	// Probe5m selects when a 5m session is probed: Probe5mAuto, Probe5mAlways
	// or Probe5mNever. An empty value is treated as auto.
	Probe5m string
	// Probe5mModels overrides the built-in cheap-cache-read list Probe5mAuto
	// matches the request model against. Empty means CheapCacheReadModels.
	Probe5mModels []string
	// OnlyWhenAgentsActive gates every probe on the liveness check.
	OnlyWhenAgentsActive bool
	// AgentIdleWindow is how long an agent may be silent and still count as
	// running. It is deliberately not the cache TTL: an agent that has written
	// nothing for an hour is finished, not busy.
	AgentIdleWindow time.Duration
	// MaxProbes caps consecutive probes without an intervening real request on
	// the 1h pool.
	MaxProbes int
	// MaxProbes5m is the same cap for the 5m pool. Zero falls back to MaxProbes.
	MaxProbes5m int
	// MaxTokens is the max_tokens value the probe body carries.
	MaxTokens int
}

// beforeExpiryFor is the lead time the tier is scheduled with.
func (c Config) beforeExpiryFor(tier string) time.Duration {
	if tier == TTLTier5m && c.BeforeExpiry5m > 0 {
		return c.BeforeExpiry5m
	}
	return c.BeforeExpiry
}

// maxProbesFor is the consecutive-probe budget the tier is allowed.
func (c Config) maxProbesFor(tier string) int {
	if tier == TTLTier5m && c.MaxProbes5m > 0 {
		return c.MaxProbes5m
	}
	return c.MaxProbes
}

// ProbeRequest describes one keepalive probe to send upstream.
type ProbeRequest struct {
	// SessionID is the Claude Code session the probe warms.
	SessionID string
	// AuthID is the credential the session is bound to. The probe must not run
	// on any other credential: a probe on a different account warms nothing.
	AuthID string
	// Provider is the auth provider namespace, normally "claude".
	Provider string
	// Model is the model the observed request used.
	Model string
	// Body is the probe-shaped request body.
	Body []byte
	// Headers are the observed request headers, including Anthropic-Beta.
	Headers http.Header
}

// ProbeResult reports what the upstream said about the refreshed entry.
type ProbeResult struct {
	// CacheReadInputTokens is cache_read_input_tokens from the probe response.
	// A value greater than zero means the probe hit the cached prefix.
	CacheReadInputTokens int64
	// CacheCreationInputTokens is cache_creation_input_tokens from the probe
	// response. A probe that writes rather than reads has already lost the race.
	CacheCreationInputTokens int64
	// Diagnosis is diagnostics.cache_miss_reason.type from the response, when
	// the account has the cache-diagnosis beta and the response supplied one.
	Diagnosis string
	// CacheMissedInputTokens is diagnostics.cache_miss_reason.cache_missed_input_tokens:
	// how much of the prefix the upstream had to re-read.
	CacheMissedInputTokens int64
}

// Prober sends a probe through the same execution path a real request uses.
type Prober interface {
	Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)
}

// Liveness reports whether a session still has work in flight.
type Liveness interface {
	// Live reports whether any agent of the session is still running. Window is
	// the agent idle window: how stale the newest filesystem signal may be and
	// still count as running.
	Live(sessionID string, window time.Duration) bool
}

// BindingState describes what the proxy knows about a session's credential binding.
type BindingState int

const (
	// BindingUnknown means routing is not session-sticky, so there is no binding
	// to check. The scheduler proceeds: with no affinity there is no binding to lose.
	BindingUnknown BindingState = iota
	// BindingBound means the session is bound to the reported credential.
	BindingBound
	// BindingLost means the session had a binding and no longer has one, normally
	// because the credential entered cooldown or rotated out.
	BindingLost
)

// Binding reports the credential a session is currently bound to.
//
// bindingSessionID is the identity credential selection keyed the binding by,
// which is not the bare Claude Code session id: selection canonicalizes it, for
// example to "claude:<session>:agent:main".
type Binding interface {
	SessionBinding(provider, bindingSessionID, model string) (authID string, state BindingState)
}

// ObserveInput describes one completed real request.
type ObserveInput struct {
	// SessionID is the Claude Code session id. It keys the scheduler and the
	// liveness check, which reads that session's on-disk task state.
	SessionID string
	// BindingSessionID is the identity credential selection keyed the session
	// affinity binding by. Empty disables the binding check.
	BindingSessionID string
	AuthID           string
	Provider         string
	Model            string
	// Body is the inbound client body, stored verbatim so the probe reproduces
	// the same prefix the next real request will send.
	Body []byte
	// Headers are the inbound client headers.
	Headers http.Header
	// TTL is the cache TTL the request asked for.
	TTL time.Duration
	// CacheReadInputTokens is what the observed real request read from the
	// cache. It is the baseline a later probe is judged against. Zero means
	// unknown, which disables the proportional check.
	CacheReadInputTokens int64
	// StartedAt is when the observed request began.
	StartedAt time.Time
}

type sessionState struct {
	generation       uint64
	bindingSessionID string
	authID           string
	provider         string
	model            string
	body             []byte
	headers          http.Header
	ttl              time.Duration
	tier             string
	probe5m          string
	probes           int
	timer            Timer

	// Observability. These outlive the probe body: a retired session keeps its
	// history so the management endpoint can explain what happened, but drops
	// the body and headers immediately.
	lastRequestAt time.Time
	baselineRead  int64
	nextProbeAt   time.Time
	probesSent    int
	lastProbe     *ProbeOutcome
	retired       bool
	retiredAt     time.Time
	retiredReason string
}

// ProbeOutcome records what happened on one probe attempt.
type ProbeOutcome struct {
	At                       time.Time `json:"at"`
	Status                   string    `json:"status"`
	CacheReadInputTokens     int64     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64     `json:"cache_creation_input_tokens"`
	SkippedReason            string    `json:"skipped_reason,omitempty"`
	// BaselineReadInputTokens is what the observed real request read. A probe
	// that reads far less than this refreshed only part of the prefix.
	BaselineReadInputTokens int64  `json:"baseline_read_input_tokens,omitempty"`
	Error                   string `json:"error,omitempty"`
	Diagnosis               string `json:"diagnosis,omitempty"`
	CacheMissedInputTokens  int64  `json:"cache_missed_input_tokens,omitempty"`
}

// Probe outcome statuses.
const (
	// ProbeStatusHit means the probe read the cached prefix, which is the
	// working state.
	ProbeStatusHit = "hit"
	// ProbeStatusMiss means the probe itself found no cached prefix. The entry it
	// was meant to refresh was already gone, so keepalive is not working for that
	// session.
	ProbeStatusMiss = "miss"
	// ProbeStatusError means the probe request failed.
	ProbeStatusError = "error"
	// ProbeStatusSkipped means the probe was deliberately not sent.
	ProbeStatusSkipped = "skipped"
)

// SessionSnapshot is the read-only view of one tracked session.
type SessionSnapshot struct {
	SessionID         string        `json:"session_id"`
	AuthID            string        `json:"auth_id"`
	Provider          string        `json:"provider"`
	Model             string        `json:"model"`
	TTL               string        `json:"ttl"`
	TTLSeconds        float64       `json:"ttl_seconds"`
	TTLTier           string        `json:"ttl_tier"`
	Probe5mDecision   string        `json:"probe_5m_decision"`
	LastRequestAt     *time.Time    `json:"last_request_at"`
	NextProbeAt       *time.Time    `json:"next_probe_at"`
	ProbesSent        int           `json:"probes_sent"`
	ConsecutiveProbes int           `json:"consecutive_probes"`
	Active            bool          `json:"active"`
	RetiredAt         *time.Time    `json:"retired_at,omitempty"`
	RetiredReason     string        `json:"retired_reason,omitempty"`
	LastProbe         *ProbeOutcome `json:"last_probe"`
}

// Counters are the process-wide keepalive totals.
type Counters struct {
	Scheduled       uint64            `json:"scheduled"`
	Fired           uint64            `json:"fired"`
	Hits            uint64            `json:"hits"`
	Misses          uint64            `json:"misses"`
	Errors          uint64            `json:"errors"`
	SkippedByReason map[string]uint64 `json:"skipped_by_reason"`
}

// Snapshot is the read-only view of the whole scheduler.
type Snapshot struct {
	Enabled              bool              `json:"enabled"`
	BeforeExpiry         string            `json:"before_expiry"`
	BeforeExpiry5m       string            `json:"before_expiry_5m"`
	Probe5m              string            `json:"probe_5m"`
	Probe5mModels        []string          `json:"probe_5m_models"`
	OnlyWhenAgentsActive bool              `json:"only_when_agents_active"`
	AgentIdleWindow      string            `json:"agent_idle_window"`
	MaxProbes            int               `json:"max_probes"`
	MaxProbes5m          int               `json:"max_probes_5m"`
	MaxTokens            int               `json:"max_tokens"`
	Sessions             []SessionSnapshot `json:"sessions"`
	Counters             Counters          `json:"counters"`
}

// Cache TTL tiers. Every observed session belongs to exactly one of them, and
// the tier decides the lead time, the probe budget, and whether the session is
// eligible at all.
const (
	// TTLTier5m is the default ephemeral pool.
	TTLTier5m = "5m"
	// TTLTier1h is the extended pool a client opts into with ttl: "1h".
	TTLTier1h = "1h"
)

// defaultCacheTTL is the pool a bare {"type":"ephemeral"} marker selects.
const defaultCacheTTL = 5 * time.Minute

// defaultAgentIdleWindow is the fallback when no idle window is configured.
const defaultAgentIdleWindow = 10 * time.Minute

// maxRetiredSessions bounds the retired-session history the snapshot exposes.
// Retired records hold no request body, only counters and the last outcome.
const maxRetiredSessions = 64

// Scheduler keeps one pending keepalive probe per Claude Code session.
//
// Stored bodies live in memory only, are replaced on every real request, and are
// dropped when the session stops qualifying or exhausts its probe budget. They
// are never persisted.
type Scheduler struct {
	mu       sync.Mutex
	cfg      Config
	sessions map[string]*sessionState
	stopped  bool

	prober   Prober
	liveness Liveness
	binding  Binding

	newTimer func(time.Duration, func()) Timer
	now      func() time.Time

	counters Counters
}

// New creates a scheduler with the given configuration.
func New(cfg Config) *Scheduler {
	return &Scheduler{
		cfg:      cfg,
		sessions: make(map[string]*sessionState),
		counters: Counters{SkippedByReason: make(map[string]uint64)},
		newTimer: func(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) },
		now:      time.Now,
	}
}

// SetTimerFactory replaces the timer constructor.
//
// Tests use it to drive scheduling deterministically; the repository forbids
// wall-clock sleeps in TTL and ordering tests for exactly this reason.
func (s *Scheduler) SetTimerFactory(factory func(time.Duration, func()) Timer) {
	if s == nil || factory == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.newTimer = factory
}

// SetProber installs the upstream probe sender.
func (s *Scheduler) SetProber(prober Prober) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prober = prober
}

// SetLiveness installs the liveness strategy.
func (s *Scheduler) SetLiveness(liveness Liveness) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness = liveness
}

// SetBinding installs the session-to-credential binding lookup.
func (s *Scheduler) SetBinding(binding Binding) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binding = binding
}

// ApplyConfig swaps the runtime configuration. Disabling the feature cancels
// every pending probe so a hot config reload takes effect immediately.
func (s *Scheduler) ApplyConfig(cfg Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	if cfg.Enabled {
		s.mu.Unlock()
		return
	}
	states := s.takeAllLocked()
	s.mu.Unlock()
	for _, state := range states {
		stopTimer(state)
	}
}

// Enabled reports whether scheduling is currently on.
func (s *Scheduler) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled && !s.stopped
}

// Stop cancels every pending probe. The scheduler stays usable but inert.
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	states := s.takeAllLocked()
	s.mu.Unlock()
	for _, state := range states {
		stopTimer(state)
	}
}

func (s *Scheduler) takeAllLocked() []*sessionState {
	states := make([]*sessionState, 0, len(s.sessions))
	for key, state := range s.sessions {
		states = append(states, state)
		delete(s.sessions, key)
	}
	return states
}

func stopTimer(state *sessionState) {
	if state != nil && state.timer != nil {
		state.timer.Stop()
	}
}

// Observe records a completed real request and reschedules that session's probe.
//
// Callers pass any request that selected a cache pool at all; the tier policy
// lives here so the log line and the counters see every decision. A session on
// the 5m pool is scheduled only when Probe5m allows it: probing there is worth
// it exactly when the model's cache reads are cheap relative to the write they
// avoid, which is what CheapCacheReadModels records.
func (s *Scheduler) Observe(in ObserveInput) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	stopped := s.stopped
	s.mu.Unlock()
	if stopped || !cfg.Enabled {
		return
	}
	if in.SessionID == "" || in.AuthID == "" || in.TTL <= 0 || len(in.Body) == 0 {
		return
	}
	tier := TTLTier(in.TTL)
	decision := Probe5mDecisionNotApplicable
	if tier == TTLTier5m {
		var allowed bool
		allowed, decision = probe5mDecision(cfg.Probe5m, cfg.Probe5mModels, in.Model)
		if !allowed {
			s.countSkip(decision)
			log.Debugf("cache-keepalive: skipped scheduling | session=%s ttl_tier=5m model=%s reason=%s",
				truncateSession(in.SessionID), in.Model, decision)
			return
		}
	}
	beforeExpiry := cfg.beforeExpiryFor(tier)
	delay := in.TTL - beforeExpiry
	if delay <= 0 {
		log.Debugf("cache-keepalive: skipped scheduling | session=%s ttl_tier=%s reason=before-expiry-exceeds-ttl ttl=%s before-expiry=%s",
			truncateSession(in.SessionID), tier, in.TTL, beforeExpiry)
		return
	}
	if !in.StartedAt.IsZero() {
		if elapsed := s.now().Sub(in.StartedAt); elapsed > 0 {
			delay -= elapsed
		}
		if delay <= 0 {
			log.Debugf("cache-keepalive: skipped scheduling | session=%s reason=window-already-elapsed", truncateSession(in.SessionID))
			return
		}
	}

	headers := in.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	body := make([]byte, len(in.Body))
	copy(body, in.Body)

	s.mu.Lock()
	if s.stopped || !s.cfg.Enabled {
		s.mu.Unlock()
		return
	}
	previous := s.sessions[in.SessionID]
	generation := uint64(1)
	if previous != nil {
		generation = previous.generation + 1
	}
	state := &sessionState{
		generation:       generation,
		bindingSessionID: in.BindingSessionID,
		authID:           in.AuthID,
		provider:         in.Provider,
		model:            in.Model,
		body:             body,
		headers:          headers,
		ttl:              in.TTL,
		tier:             tier,
		probe5m:          decision,
		// A real request resets the consecutive-probe budget.
		probes: 0,
	}
	now := s.now()
	state.lastRequestAt = in.StartedAt
	if state.lastRequestAt.IsZero() {
		state.lastRequestAt = now
	}
	state.nextProbeAt = now.Add(delay)
	state.baselineRead = in.CacheReadInputTokens
	// A session that had already probed keeps its lifetime probe count; only the
	// consecutive budget resets on a real request.
	if previous != nil {
		state.probesSent = previous.probesSent
		state.lastProbe = previous.lastProbe
	}
	s.sessions[in.SessionID] = state
	s.pruneRetiredLocked()
	s.counters.Scheduled++
	sessionID := in.SessionID
	state.timer = s.newTimer(delay, func() { s.fire(sessionID, generation) })
	s.mu.Unlock()

	stopTimer(previous)
	log.Infof("cache-keepalive: scheduled | session=%s auth=%s model=%s ttl=%s ttl_tier=%s probe_5m=%s fires_in=%s next_probe_at=%s",
		truncateSession(sessionID), in.AuthID, in.Model, in.TTL, tier, decision, delay.Round(time.Second), state.nextProbeAt.Format(time.RFC3339))
}

// countSkip records a decision that stopped a session before it was tracked, so
// a policy that silently drops every 5m session is still visible in the counters.
func (s *Scheduler) countSkip(reason string) {
	if reason == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counters.SkippedByReason == nil {
		s.counters.SkippedByReason = make(map[string]uint64)
	}
	s.counters.SkippedByReason[reason]++
}

func (s *Scheduler) fire(sessionID string, generation uint64) {
	s.mu.Lock()
	cfg := s.cfg
	state := s.sessions[sessionID]
	if s.stopped || !cfg.Enabled || state == nil || state.generation != generation {
		s.mu.Unlock()
		if state != nil && state.generation != generation {
			log.Debugf("cache-keepalive: skipped | session=%s reason=superseded", truncateSession(sessionID))
		}
		return
	}
	tier := state.tier
	if tier == "" {
		tier = TTLTier(state.ttl)
	}
	if state.probes >= cfg.maxProbesFor(tier) {
		s.mu.Unlock()
		s.retire(sessionID, generation, "max-probes")
		log.Infof("cache-keepalive: skipped | session=%s ttl_tier=%s reason=max-probes consecutive_probes=%d", truncateSession(sessionID), tier, state.probes)
		return
	}
	liveness := s.liveness
	binding := s.binding
	prober := s.prober
	authID := state.authID
	bindingSessionID := state.bindingSessionID
	provider := state.provider
	model := state.model
	ttl := state.ttl
	probe5m := state.probe5m
	baselineRead := state.baselineRead
	body := state.body
	headers := state.headers
	s.mu.Unlock()

	if cfg.OnlyWhenAgentsActive {
		idleWindow := cfg.AgentIdleWindow
		if idleWindow <= 0 {
			idleWindow = defaultAgentIdleWindow
		}
		if liveness == nil || !liveness.Live(sessionID, idleWindow) {
			s.retire(sessionID, generation, "no-live-agents")
			log.Infof("cache-keepalive: skipped | session=%s auth=%s model=%s ttl_tier=%s reason=no-live-agents", truncateSession(sessionID), authID, model, tier)
			return
		}
	}
	if binding != nil && bindingSessionID != "" {
		boundAuthID, state := binding.SessionBinding(provider, bindingSessionID, model)
		if state == BindingLost || (state == BindingBound && boundAuthID != authID) {
			s.retire(sessionID, generation, "auth-binding-lost")
			log.Infof("cache-keepalive: skipped | session=%s auth=%s model=%s reason=auth-binding-lost want_auth=%s bound_auth=%s",
				truncateSession(sessionID), authID, model, authID, boundAuthID)
			return
		}
	}
	if prober == nil {
		s.retire(sessionID, generation, "no-prober")
		log.Warnf("cache-keepalive: skipped | session=%s reason=no-prober", truncateSession(sessionID))
		return
	}

	probeBody, errBody := ProbeBody(body, cfg.MaxTokens)
	if errBody != nil {
		s.retire(sessionID, generation, "probe-body-build-failed")
		log.Warnf("cache-keepalive: skipped | session=%s reason=probe-body-build-failed error=%v", truncateSession(sessionID), errBody)
		return
	}

	probeStart := s.now()
	result, errProbe := prober.Probe(context.Background(), ProbeRequest{
		SessionID: sessionID,
		AuthID:    authID,
		Provider:  provider,
		Model:     model,
		Body:      probeBody,
		Headers:   headers.Clone(),
	})
	elapsed := s.now().Sub(probeStart)
	if errProbe != nil {
		s.recordProbe(sessionID, generation, ProbeOutcome{
			At:     probeStart,
			Status: ProbeStatusError,
			Error:  errProbe.Error(),
		})
		s.retire(sessionID, generation, "probe-error")
		log.Warnf("cache-keepalive: probe | session=%s auth=%s model=%s status=error duration=%s error=%v",
			truncateSession(sessionID), authID, model, elapsed.Round(time.Millisecond), errProbe)
		return
	}

	outcome := ProbeOutcome{
		At:                       probeStart,
		Status:                   ProbeStatusHit,
		CacheReadInputTokens:     result.CacheReadInputTokens,
		CacheCreationInputTokens: result.CacheCreationInputTokens,
		BaselineReadInputTokens:  baselineRead,
		Diagnosis:                result.Diagnosis,
		CacheMissedInputTokens:   result.CacheMissedInputTokens,
	}
	if probeMissed(result.CacheReadInputTokens, baselineRead) {
		outcome.Status = ProbeStatusMiss
	}
	s.recordProbe(sessionID, generation, outcome)

	// Reschedule from the probe's own start time so the next window is measured
	// against the entry the probe just refreshed.
	next := ttl - cfg.beforeExpiryFor(tier) - elapsed
	s.mu.Lock()
	current := s.sessions[sessionID]
	if s.stopped || !s.cfg.Enabled || current == nil || current.generation != generation {
		s.mu.Unlock()
		s.logProbe(sessionID, authID, model, tier, probe5m, outcome, elapsed, 0, 0, false, time.Time{})
		return
	}
	current.probes++
	current.probesSent++
	consecutive := current.probes
	total := current.probesSent
	rescheduled := false
	var nextProbeAt time.Time
	if consecutive < s.cfg.maxProbesFor(tier) && next > 0 {
		current.timer = s.newTimer(next, func() { s.fire(sessionID, generation) })
		nextProbeAt = s.now().Add(next)
		current.nextProbeAt = nextProbeAt
		rescheduled = true
	} else {
		s.retireLocked(current, sessionID, "max-probes")
	}
	s.mu.Unlock()

	s.logProbe(sessionID, authID, model, tier, probe5m, outcome, elapsed, total, consecutive, rescheduled, nextProbeAt)
}

// probeMissed reports whether a probe failed to refresh the entry it was sent for.
//
// Reading nothing is an outright miss. Reading less than half the baseline is
// also a miss: the probe found only a fragment of the prefix cached, so most of
// the context it was meant to keep warm had already expired. A zero baseline
// means the observed request's usage was unavailable, which leaves only the
// outright check.
func probeMissed(read, baseline int64) bool {
	if read <= 0 {
		return true
	}
	return baseline > 0 && read*2 < baseline
}

// logProbe emits the single line that tells the whole story of one probe, so
// `grep cache-keepalive` needs no other source.
//
// A probe that found nothing cached is the malfunction signal: the entry it was
// meant to refresh had already expired, so it is logged at warning level.
func (s *Scheduler) logProbe(sessionID, authID, model, tier, probe5m string, outcome ProbeOutcome, elapsed time.Duration, total, consecutive int, rescheduled bool, nextProbeAt time.Time) {
	next := "none"
	if !nextProbeAt.IsZero() {
		next = nextProbeAt.Format(time.RFC3339)
	}
	fields := "session=%s auth=%s model=%s ttl_tier=%s probe_5m=%s status=%s cache_read_input_tokens=%d cache_creation_input_tokens=%d baseline_read_input_tokens=%d duration=%s probes_sent=%d consecutive_probes=%d rescheduled=%t next_probe_at=%s"
	args := []any{
		truncateSession(sessionID), authID, model, tier, probe5m, outcome.Status,
		outcome.CacheReadInputTokens, outcome.CacheCreationInputTokens, outcome.BaselineReadInputTokens,
		elapsed.Round(time.Millisecond), total, consecutive, rescheduled, next,
	}
	if outcome.Status == ProbeStatusMiss {
		fields += " cache_miss_reason=%q cache_missed_input_tokens=%d"
		args = append(args, outcome.Diagnosis, outcome.CacheMissedInputTokens)
		log.Warnf("cache-keepalive: probe MISSED | "+fields, args...)
		return
	}
	log.Infof("cache-keepalive: probe | "+fields, args...)
}

// recordProbe stores the outcome against the session and updates the counters.
func (s *Scheduler) recordProbe(sessionID string, generation uint64, outcome ProbeOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters.Fired++
	switch outcome.Status {
	case ProbeStatusHit:
		s.counters.Hits++
	case ProbeStatusMiss:
		s.counters.Misses++
	case ProbeStatusError:
		s.counters.Errors++
	}
	if state := s.sessions[sessionID]; state != nil && state.generation == generation {
		stored := outcome
		state.lastProbe = &stored
	}
}

// retire ends a session's scheduling and keeps its history for the snapshot.
func (s *Scheduler) retire(sessionID string, generation uint64, reason string) {
	s.mu.Lock()
	state := s.sessions[sessionID]
	if state == nil || state.generation != generation {
		s.mu.Unlock()
		return
	}
	s.retireLocked(state, sessionID, reason)
	s.mu.Unlock()
	stopTimer(state)
}

// retireLocked drops the stored body and marks the session inactive. The record
// stays visible to the snapshot so an operator can see why probing stopped, but
// it never keeps the request body: those live in memory only, for as long as a
// probe might still need them.
func (s *Scheduler) retireLocked(state *sessionState, sessionID, reason string) {
	if state == nil || state.retired {
		return
	}
	state.retired = true
	state.retiredAt = s.now()
	state.retiredReason = reason
	state.body = nil
	state.headers = nil
	state.timer = nil
	state.nextProbeAt = time.Time{}
	if reason != "" {
		if s.counters.SkippedByReason == nil {
			s.counters.SkippedByReason = make(map[string]uint64)
		}
		s.counters.SkippedByReason[reason]++
		// A skip that happened instead of a probe is the session's last outcome.
		// A skip that ends a session which did probe must not erase the result of
		// that probe: the reason is reported separately.
		if state.lastProbe == nil {
			state.lastProbe = &ProbeOutcome{At: state.retiredAt, Status: ProbeStatusSkipped, SkippedReason: reason}
		}
	}
	s.pruneRetiredLocked()
	_ = sessionID
}

// pruneRetiredLocked bounds the retired history to the newest maxRetiredSessions.
func (s *Scheduler) pruneRetiredLocked() {
	retired := make([]string, 0, len(s.sessions))
	for key, state := range s.sessions {
		if state.retired {
			retired = append(retired, key)
		}
	}
	if len(retired) <= maxRetiredSessions {
		return
	}
	sort.Slice(retired, func(i, j int) bool {
		return s.sessions[retired[i]].retiredAt.Before(s.sessions[retired[j]].retiredAt)
	})
	for _, key := range retired[:len(retired)-maxRetiredSessions] {
		delete(s.sessions, key)
	}
}

// Snapshot returns the read-only view the management endpoint serves.
func (s *Scheduler) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Sessions: []SessionSnapshot{}, Counters: Counters{SkippedByReason: map[string]uint64{}}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		Enabled:              s.cfg.Enabled && !s.stopped,
		BeforeExpiry:         s.cfg.BeforeExpiry.String(),
		BeforeExpiry5m:       s.cfg.BeforeExpiry5m.String(),
		Probe5m:              s.cfg.Probe5m,
		Probe5mModels:        probe5mModels(s.cfg.Probe5mModels),
		OnlyWhenAgentsActive: s.cfg.OnlyWhenAgentsActive,
		AgentIdleWindow:      s.cfg.AgentIdleWindow.String(),
		MaxProbes:            s.cfg.MaxProbes,
		MaxProbes5m:          s.cfg.MaxProbes5m,
		MaxTokens:            s.cfg.MaxTokens,
		Sessions:             make([]SessionSnapshot, 0, len(s.sessions)),
		Counters: Counters{
			Scheduled:       s.counters.Scheduled,
			Fired:           s.counters.Fired,
			Hits:            s.counters.Hits,
			Misses:          s.counters.Misses,
			Errors:          s.counters.Errors,
			SkippedByReason: make(map[string]uint64, len(s.counters.SkippedByReason)),
		},
	}
	for reason, count := range s.counters.SkippedByReason {
		out.Counters.SkippedByReason[reason] = count
	}
	for sessionID, state := range s.sessions {
		out.Sessions = append(out.Sessions, sessionSnapshot(sessionID, state))
	}
	sort.Slice(out.Sessions, func(i, j int) bool {
		return out.Sessions[i].SessionID < out.Sessions[j].SessionID
	})
	return out
}

// probe5mModels reports the list "auto" is matching against, so the endpoint
// answers "which models does this build consider cheap" without a rebuild.
func probe5mModels(configured []string) []string {
	source := configured
	if len(source) == 0 {
		source = CheapCacheReadModels
	}
	out := make([]string, len(source))
	copy(out, source)
	return out
}

func sessionSnapshot(sessionID string, state *sessionState) SessionSnapshot {
	tier := state.tier
	if tier == "" {
		tier = TTLTier(state.ttl)
	}
	snapshot := SessionSnapshot{
		SessionID:         sessionID,
		AuthID:            state.authID,
		Provider:          state.provider,
		Model:             state.model,
		TTL:               state.ttl.String(),
		TTLSeconds:        state.ttl.Seconds(),
		TTLTier:           tier,
		Probe5mDecision:   state.probe5m,
		ProbesSent:        state.probesSent,
		ConsecutiveProbes: state.probes,
		Active:            !state.retired,
	}
	if !state.lastRequestAt.IsZero() {
		at := state.lastRequestAt
		snapshot.LastRequestAt = &at
	}
	if !state.nextProbeAt.IsZero() {
		at := state.nextProbeAt
		snapshot.NextProbeAt = &at
	}
	if !state.retiredAt.IsZero() {
		at := state.retiredAt
		snapshot.RetiredAt = &at
		snapshot.RetiredReason = state.retiredReason
	}
	if state.lastProbe != nil {
		probe := *state.lastProbe
		snapshot.LastProbe = &probe
	}
	return snapshot
}

// ProbeBody derives the minimal refresh request from an observed client body.
//
// Everything that participates in the cache prefix — tools, system, messages and
// every cache_control marker — is left byte-identical. Only the fields that
// decide how much the probe generates are rewritten.
func ProbeBody(body []byte, maxTokens int) ([]byte, error) {
	if maxTokens <= 0 {
		maxTokens = 1
	}
	out, err := sjson.SetBytes(body, "max_tokens", maxTokens)
	if err != nil {
		return nil, err
	}
	out, err = sjson.SetBytes(out, "stream", false)
	if err != nil {
		return nil, err
	}
	// Extended thinking forces a minimum max_tokens well above the probe budget,
	// so the probe drops it. It is not part of the cached prefix.
	out, err = sjson.DeleteBytes(out, "thinking")
	if err != nil {
		return nil, err
	}
	// Dropping thinking invalidates any context-management strategy that requires
	// it: Claude Code sends clear_thinking_20251015, and the upstream rejects the
	// request outright when thinking is absent. Neither the strategy nor thinking
	// participates in the cached prefix, so the probe drops both together.
	return removeThinkingDependentContextManagement(out)
}

// contextManagementThinkingPrefix marks the strategies that require thinking.
const contextManagementThinkingPrefix = "clear_thinking"

func removeThinkingDependentContextManagement(body []byte) ([]byte, error) {
	edits := gjson.GetBytes(body, "context_management.edits")
	if !edits.IsArray() {
		return body, nil
	}
	kept := make([]json.RawMessage, 0, len(edits.Array()))
	for _, edit := range edits.Array() {
		if strings.HasPrefix(edit.Get("type").String(), contextManagementThinkingPrefix) {
			continue
		}
		kept = append(kept, json.RawMessage(edit.Raw))
	}
	if len(kept) == len(edits.Array()) {
		return body, nil
	}
	if len(kept) == 0 {
		// An empty edits array is not a valid context_management block.
		return sjson.DeleteBytes(body, "context_management")
	}
	return sjson.SetBytes(body, "context_management.edits", kept)
}

// ExtendedCacheTTL returns the longest explicit cache_control TTL in the body,
// or zero when the body carries no explicit TTL.
//
// A bare {"type":"ephemeral"} marker is the 5m wire default and returns zero:
// only a TTL the client wrote out is reported here. Use RequestCacheTTL to get
// the pool the request actually selected.
func ExtendedCacheTTL(body []byte) time.Duration {
	if len(body) == 0 || !json.Valid(body) {
		return 0
	}
	var longest time.Duration
	walkCacheControl(gjson.ParseBytes(body), func(ttl time.Duration) {
		if ttl > longest {
			longest = ttl
		}
	})
	return longest
}

// RequestCacheTTL returns the cache TTL the body actually selected, resolving
// the wire default.
//
// A marker that spells out a ttl selects that pool. A bare
// {"type":"ephemeral"} marker selects the 5m default, so any body carrying a
// cache_control marker selects at least 5m. A body with no marker caches
// nothing and returns zero.
func RequestCacheTTL(body []byte) time.Duration {
	if len(body) == 0 || !json.Valid(body) {
		return 0
	}
	var longest time.Duration
	var marked bool
	walkCacheControl(gjson.ParseBytes(body), func(ttl time.Duration) {
		marked = true
		if ttl > longest {
			longest = ttl
		}
	})
	switch {
	case longest > 0:
		return longest
	case marked:
		return defaultCacheTTL
	default:
		return 0
	}
}

// walkCacheControl visits every cache_control marker in the body, passing the
// marker's explicit TTL or zero when it carries none.
func walkCacheControl(value gjson.Result, visit func(time.Duration)) {
	switch {
	case value.IsObject():
		value.ForEach(func(key, child gjson.Result) bool {
			if key.String() == "cache_control" && child.IsObject() {
				visit(parseCacheTTL(child.Get("ttl").String()))
				return true
			}
			walkCacheControl(child, visit)
			return true
		})
	case value.IsArray():
		value.ForEach(func(_, child gjson.Result) bool {
			walkCacheControl(child, visit)
			return true
		})
	}
}

func parseCacheTTL(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return 0
	}
	return ttl
}

func truncateSession(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[:8] + "..."
}

// IsExtendedCacheTTL reports whether a TTL selects the long cache pool.
func IsExtendedCacheTTL(ttl time.Duration) bool {
	return ttl >= time.Hour
}

// TTLTier names the cache pool a TTL belongs to. It is what the logs and the
// management snapshot report, and it selects the lead time and probe budget the
// session is scheduled with.
func TTLTier(ttl time.Duration) string {
	if IsExtendedCacheTTL(ttl) {
		return TTLTier1h
	}
	return TTLTier5m
}
