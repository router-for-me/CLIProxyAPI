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
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Timer is the subset of time.Timer the scheduler needs. Tests substitute a
// deterministic implementation.
type Timer interface {
	Stop() bool
}

// Config holds the resolved runtime settings for the scheduler.
type Config struct {
	// Enabled turns scheduling on. A disabled scheduler ignores every observation.
	Enabled bool
	// BeforeExpiry fires a probe at t_start + ttl - BeforeExpiry.
	BeforeExpiry time.Duration
	// OnlyWhenAgentsActive gates every probe on the liveness check.
	OnlyWhenAgentsActive bool
	// MaxProbes caps consecutive probes without an intervening real request.
	MaxProbes int
	// MaxTokens is the max_tokens value the probe body carries.
	MaxTokens int
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
}

// Prober sends a probe through the same execution path a real request uses.
type Prober interface {
	Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)
}

// Liveness reports whether a session still has work in flight.
type Liveness interface {
	// Live reports whether any task of the session is still running. Window
	// bounds how stale a filesystem signal may be and still count as live.
	Live(sessionID string, window time.Duration) bool
}

// Binding reports the credential a session is currently bound to.
type Binding interface {
	BoundAuthID(provider, sessionID, model string) (string, bool)
}

// ObserveInput describes one completed real request.
type ObserveInput struct {
	SessionID string
	AuthID    string
	Provider  string
	Model     string
	// Body is the inbound client body, stored verbatim so the probe reproduces
	// the same prefix the next real request will send.
	Body []byte
	// Headers are the inbound client headers.
	Headers http.Header
	// TTL is the cache TTL the request asked for.
	TTL time.Duration
	// StartedAt is when the observed request began.
	StartedAt time.Time
}

type sessionState struct {
	generation uint64
	authID     string
	provider   string
	model      string
	body       []byte
	headers    http.Header
	ttl        time.Duration
	probes     int
	timer      Timer
}

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
}

// New creates a scheduler with the given configuration.
func New(cfg Config) *Scheduler {
	return &Scheduler{
		cfg:      cfg,
		sessions: make(map[string]*sessionState),
		newTimer: func(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) },
		now:      time.Now,
	}
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
// Callers must only pass requests that carried an explicit 1h cache_control TTL.
// Scheduling a probe for the 5m pool loses money: thirteen reads an hour cost
// more than the single write they avoid.
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
	delay := in.TTL - cfg.BeforeExpiry
	if delay <= 0 {
		log.Debugf("cache-keepalive: skipped scheduling | session=%s reason=before-expiry-exceeds-ttl ttl=%s before-expiry=%s",
			truncateSession(in.SessionID), in.TTL, cfg.BeforeExpiry)
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
		generation: generation,
		authID:     in.AuthID,
		provider:   in.Provider,
		model:      in.Model,
		body:       body,
		headers:    headers,
		ttl:        in.TTL,
		// A real request resets the consecutive-probe budget.
		probes: 0,
	}
	s.sessions[in.SessionID] = state
	sessionID := in.SessionID
	state.timer = s.newTimer(delay, func() { s.fire(sessionID, generation) })
	s.mu.Unlock()

	stopTimer(previous)
	log.Infof("cache-keepalive: scheduled | session=%s auth=%s model=%s ttl=%s fires_in=%s",
		truncateSession(sessionID), in.AuthID, in.Model, in.TTL, delay.Round(time.Second))
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
	if state.probes >= cfg.MaxProbes {
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		log.Infof("cache-keepalive: skipped | session=%s reason=max-probes probes=%d", truncateSession(sessionID), state.probes)
		return
	}
	liveness := s.liveness
	binding := s.binding
	prober := s.prober
	authID := state.authID
	provider := state.provider
	model := state.model
	ttl := state.ttl
	body := state.body
	headers := state.headers
	s.mu.Unlock()

	if cfg.OnlyWhenAgentsActive {
		if liveness == nil || !liveness.Live(sessionID, ttl) {
			s.drop(sessionID, generation)
			log.Infof("cache-keepalive: skipped | session=%s reason=no-live-agents", truncateSession(sessionID))
			return
		}
	}
	if binding != nil {
		boundAuthID, ok := binding.BoundAuthID(provider, sessionID, model)
		if !ok || boundAuthID != authID {
			s.drop(sessionID, generation)
			log.Infof("cache-keepalive: skipped | session=%s reason=auth-binding-lost want_auth=%s bound_auth=%s",
				truncateSession(sessionID), authID, boundAuthID)
			return
		}
	}
	if prober == nil {
		s.drop(sessionID, generation)
		log.Warnf("cache-keepalive: skipped | session=%s reason=no-prober", truncateSession(sessionID))
		return
	}

	probeBody, errBody := ProbeBody(body, cfg.MaxTokens)
	if errBody != nil {
		s.drop(sessionID, generation)
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
	if errProbe != nil {
		s.drop(sessionID, generation)
		log.Warnf("cache-keepalive: probe failed | session=%s auth=%s model=%s error=%v",
			truncateSession(sessionID), authID, model, errProbe)
		return
	}

	// Reschedule from the probe's own start time so the next window is measured
	// against the entry the probe just refreshed.
	next := ttl - cfg.BeforeExpiry - s.now().Sub(probeStart)
	s.mu.Lock()
	current := s.sessions[sessionID]
	if s.stopped || !s.cfg.Enabled || current == nil || current.generation != generation {
		s.mu.Unlock()
		log.Infof("cache-keepalive: fired | session=%s auth=%s model=%s cache_read_input_tokens=%d rescheduled=false",
			truncateSession(sessionID), authID, model, result.CacheReadInputTokens)
		return
	}
	current.probes++
	probes := current.probes
	rescheduled := false
	if probes < s.cfg.MaxProbes && next > 0 {
		current.timer = s.newTimer(next, func() { s.fire(sessionID, generation) })
		rescheduled = true
	} else {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	log.Infof("cache-keepalive: fired | session=%s auth=%s model=%s cache_read_input_tokens=%d probes=%d rescheduled=%t",
		truncateSession(sessionID), authID, model, result.CacheReadInputTokens, probes, rescheduled)
}

func (s *Scheduler) drop(sessionID string, generation uint64) {
	s.mu.Lock()
	state := s.sessions[sessionID]
	if state != nil && state.generation == generation {
		delete(s.sessions, sessionID)
	} else {
		state = nil
	}
	s.mu.Unlock()
	stopTimer(state)
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
	return out, nil
}

// ExtendedCacheTTL returns the longest explicit cache_control TTL in the body,
// or zero when the body carries no explicit TTL.
//
// A bare {"type":"ephemeral"} marker is the 5m wire default and returns zero:
// only a TTL the client wrote out is treated as an opt-in to the long pool.
func ExtendedCacheTTL(body []byte) time.Duration {
	if len(body) == 0 || !json.Valid(body) {
		return 0
	}
	var longest time.Duration
	walkCacheControlTTLs(gjson.ParseBytes(body), func(ttl time.Duration) {
		if ttl > longest {
			longest = ttl
		}
	})
	return longest
}

func walkCacheControlTTLs(value gjson.Result, visit func(time.Duration)) {
	switch {
	case value.IsObject():
		value.ForEach(func(key, child gjson.Result) bool {
			if key.String() == "cache_control" && child.IsObject() {
				if ttl := parseCacheTTL(child.Get("ttl").String()); ttl > 0 {
					visit(ttl)
				}
				return true
			}
			walkCacheControlTTLs(child, visit)
			return true
		})
	case value.IsArray():
		value.ForEach(func(_, child gjson.Result) bool {
			walkCacheControlTTLs(child, visit)
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
//
// Keepalive is only economical there. On the 5m pool a probe every window costs
// thirteen reads an hour, which is more than the single write it avoids.
func IsExtendedCacheTTL(ttl time.Duration) bool {
	return ttl >= time.Hour
}
