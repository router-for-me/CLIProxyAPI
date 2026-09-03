// Package cachestats retains a bounded, in-memory, per-session view of Claude
// prompt-cache behavior.
//
// The proxy's usage pipeline already receives every field needed to diagnose a
// prompt-cache miss, but it publishes each record fire-and-forget and keeps no
// history. This package keeps the last N requests per Claude Code session in
// order, classifies each one against the session's own cache high-water mark,
// and reports how many cached tokens each regression cost.
package cachestats

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Tier classifies one request against the cache high-water mark of its session.
type Tier string

const (
	// TierT0 is a request that read nothing from cache: the session's cold start,
	// or a prefix that expired entirely.
	TierT0 Tier = "T0"
	// TierMiss is a request that read less than an earlier request in the same
	// session already read. Part of the cached prefix stopped matching.
	TierMiss Tier = "miss"
	// TierHit is a request that read at least as much as every earlier request.
	TierHit Tier = "hit"
	// TierNA is a request from a provider that reports no cache accounting at
	// all. It has no high-water mark to be measured against, so it is counted as
	// a request but never as a hit, a miss or a T0.
	TierNA Tier = "n/a"
)

// Signal describes how much prompt-cache accounting a provider reports.
type Signal string

const (
	// SignalFull is Anthropic: cache reads, cache creation, the 5m/1h pool split
	// and, with the diagnosis beta on, a miss reason.
	SignalFull Signal = "full"
	// SignalRead is a provider that reports cached input tokens but no cache
	// creation: OpenAI-compatible cached_tokens, Gemini cachedContentTokenCount.
	// Tiers still apply; the 5m/1h split and miss reasons never do.
	SignalRead Signal = "read"
	// SignalNone is a provider that reports no cache accounting. Showing it a
	// zero hit rate would be a fabricated number, so it is excluded from every
	// hit-rate denominator.
	SignalNone Signal = "none"
)

// KeyedBy names how a session row was identified.
type KeyedBy string

const (
	// KeyedBySession is a real Claude Code agent session UUID.
	KeyedBySession KeyedBy = "session"
	// KeyedByAPIKeyModel is the fallback identity for callers that send no
	// session id: the API key digest, the model and the client fingerprint.
	KeyedByAPIKeyModel KeyedBy = "apikey-model"
)

// T0Cause explains why a request read nothing from cache.
type T0Cause string

const (
	// T0CauseFirst is the session's very first request. Nothing was cached yet.
	T0CauseFirst T0Cause = "first"
	// T0CauseRebind is a request served by a different credential than the one
	// before it. The prefix is cached on the other account, not lost.
	T0CauseRebind T0Cause = "rebind"
	// T0CauseExpiry is a request on the same credential that still read nothing:
	// the cached prefix aged out.
	T0CauseExpiry T0Cause = "expiry"
)

// Regime names the cache pool a session is predominantly writing into.
type Regime string

const (
	// RegimeNone means the session has written no cache-creation tokens.
	RegimeNone Regime = "none"
	// Regime5m means most cache-creation tokens went to the 5-minute pool.
	Regime5m Regime = "5m"
	// Regime1h means most cache-creation tokens went to the 1-hour pool.
	Regime1h Regime = "1h"
	// RegimeMixed means the two pools carry the same number of tokens.
	RegimeMixed Regime = "mixed"
)

// Config bounds the store. A zero Enabled keeps the store inert.
type Config struct {
	Enabled            bool
	MaxSessions        int
	PerSessionRequests int
	IdleTTL            time.Duration
	Alert              AlertConfig
}

// AlertConfig configures the sustained-loss alert.
type AlertConfig struct {
	// Enabled turns the alert on.
	Enabled bool
	// LostTokensPerHour is the sliding-window threshold. A session crossing it
	// logs once at WARN and is flagged alerting until the window drains below
	// half the threshold.
	LostTokensPerHour int64
}

// AlertWindow is the width of the sliding loss window. The threshold is named
// per hour, so the window is an hour.
const AlertWindow = time.Hour

// Observation is one completed upstream request.
type Observation struct {
	// SessionID is the identity the request is grouped under: a Claude Code
	// session UUID where one exists, otherwise the API-key/model fallback key.
	SessionID string
	// KeyedBy records which of the two identities SessionID is.
	KeyedBy  KeyedBy
	Provider string
	Model    string
	AuthID   string
	At       time.Time
	// Signal is how much cache accounting this provider reports. A SignalNone
	// observation is counted but never classified into a tier.
	Signal Signal

	InputTokens  int64
	OutputTokens int64
	// PromptTokens is the whole input side including cached tokens, taken from
	// the normalized token breakdown so providers that count cache inside their
	// input total and providers that count it separately are comparable.
	PromptTokens int64
	// MaxTokens is the request body's max_tokens, so keepalive probes stay
	// recognizable even when no probe flag reached the record.
	MaxTokens int64

	CacheReadTokens       int64
	CacheCreationTokens   int64
	CacheCreation5mTokens int64
	CacheCreation1hTokens int64

	// CacheMissReason is diagnostics.cache_miss_reason.type from the upstream
	// response, present only while the cache-diagnosis beta is enabled.
	CacheMissReason string
	// CacheMissedTokens is diagnostics.cache_miss_reason.cache_missed_input_tokens.
	CacheMissedTokens int64

	// IsProbe marks a request issued by the prompt-cache keepalive prober
	// rather than by a client.
	IsProbe bool
}

// Request is one retained request in a session's ring buffer.
type Request struct {
	Seq                   int       `json:"seq"`
	At                    time.Time `json:"at"`
	Provider              string    `json:"provider"`
	Model                 string    `json:"model"`
	AuthID                string    `json:"auth_id"`
	Signal                Signal    `json:"cache_signal"`
	InputTokens           int64     `json:"input_tokens"`
	PromptTokens          int64     `json:"prompt_tokens"`
	OutputTokens          int64     `json:"output_tokens"`
	MaxTokens             int64     `json:"max_tokens"`
	CacheReadTokens       int64     `json:"cache_read_tokens"`
	CacheCreationTokens   int64     `json:"cache_creation_tokens"`
	CacheCreation5mTokens int64     `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int64     `json:"cache_creation_1h_tokens"`
	Tier                  Tier      `json:"tier"`
	DeltaRead             int64     `json:"delta_read"`
	MissReason            string    `json:"miss_reason"`
	MissedTokens          int64     `json:"missed_tokens"`
	IsProbe               bool      `json:"is_probe"`
	// Rebind marks a request served by a different credential than the request
	// before it in the same session.
	Rebind bool `json:"rebind"`
	// T0Cause explains a T0 tier and is empty for every other tier.
	T0Cause T0Cause `json:"t0_cause,omitempty"`
}

// Aggregate is the counter set shared by the global, per-model, per-auth and
// per-session summaries.
type Aggregate struct {
	Requests int64 `json:"requests"`
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
	T0s      int64 `json:"t0s"`
	Probes   int64 `json:"probes"`
	Rebinds  int64 `json:"rebinds"`
	// Classified counts the requests a tier could be assigned to. It is the
	// hit-rate denominator, so a provider that reports no cache accounting does
	// not drag the rate toward a fabricated zero.
	Classified int64   `json:"classified"`
	HitRate    float64 `json:"hit_rate"`
	// CachedShare is cached prompt tokens over all prompt tokens. It is the only
	// meaningful cache number for a provider that reports reads but no creation.
	CachedShare           float64 `json:"cached_share"`
	InputTokens           int64   `json:"input_tokens"`
	PromptTokens          int64   `json:"prompt_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	CacheReadTokens       int64   `json:"cache_read_tokens"`
	CacheCreationTokens   int64   `json:"cache_creation_tokens"`
	CacheCreation5mTokens int64   `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int64   `json:"cache_creation_1h_tokens"`
	LostTokens            int64   `json:"lost_tokens"`
	// T0Rebinds and T0Expiries split the T0 count by cause; the remainder is
	// the session's first request.
	T0Rebinds  int64     `json:"t0_rebinds"`
	T0Expiries int64     `json:"t0_expiries"`
	Sessions   int64     `json:"sessions"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

// KeyedAggregate is an Aggregate labelled with the model or auth it covers.
type KeyedAggregate struct {
	Key string `json:"key"`
	Aggregate
}

// SessionSummary describes one retained session.
type SessionSummary struct {
	ID       string  `json:"id"`
	ShortID  string  `json:"short_id"`
	KeyedBy  KeyedBy `json:"keyed_by"`
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	AuthID   string  `json:"auth_id"`
	Signal   Signal  `json:"cache_signal"`
	Aggregate
	Regime Regime `json:"regime"`
	// Alerting reports whether sustained cache loss crossed the configured
	// threshold and has not yet drained below half of it.
	Alerting bool `json:"alerting"`
	// LostTokensInWindow is the loss inside the current sliding alert window.
	LostTokensInWindow int64 `json:"lost_tokens_in_window"`
}

// SessionDetail pairs a session summary with its retained request sequence.
type SessionDetail struct {
	Summary  SessionSummary `json:"session"`
	Requests []Request      `json:"requests"`
}

type lossEvent struct {
	at     time.Time
	tokens int64
}

type session struct {
	id       string
	keyedBy  KeyedBy
	provider string
	model    string
	authID   string
	signal   Signal
	first    time.Time
	last     time.Time
	seq      int
	maxRead  int64
	// lastReason is the most recent upstream miss reason, quoted by the alert.
	lastReason string
	// losses is the sliding window backing the sustained-loss alert.
	losses   []lossEvent
	lossSum  int64
	alerting bool
	// prevRead is the cache_read of the immediately preceding request, which is
	// not necessarily retained once the ring buffer wraps.
	prevRead int64
	agg      Aggregate
	requests []Request
}

// Store holds the bounded per-session statistics.
type Store struct {
	mu  sync.RWMutex
	cfg Config
	// clock is the newest observation timestamp seen. Idle eviction is measured
	// against it rather than wall time so retention tracks actual traffic.
	clock    time.Time
	sessions map[string]*session
}

// NewStore builds a store with the supplied bounds.
func NewStore(cfg Config) *Store {
	return &Store{cfg: normalizeConfig(cfg), sessions: make(map[string]*session)}
}

func normalizeConfig(cfg Config) Config {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 500
	}
	if cfg.PerSessionRequests <= 0 {
		cfg.PerSessionRequests = 200
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 24 * time.Hour
	}
	return cfg
}

// ApplyConfig replaces the bounds and immediately enforces the new ones, so the
// store is hot-reloadable. Disabling it drops everything retained.
func (s *Store) ApplyConfig(cfg Config) {
	if s == nil {
		return
	}
	cfg = normalizeConfig(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	if !cfg.Enabled {
		s.sessions = make(map[string]*session)
		return
	}
	for _, entry := range s.sessions {
		entry.trim(cfg.PerSessionRequests)
		// A changed threshold re-arms or clears the alert without waiting for
		// the session's next request.
		entry.evaluateAlert(entry.last, cfg.Alert)
	}
	s.pruneLocked()
}

// Enabled reports whether the store is recording.
func (s *Store) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled
}

// Record ingests one completed Claude request.
func (s *Store) Record(observation Observation) {
	if s == nil {
		return
	}
	sessionID := strings.TrimSpace(observation.SessionID)
	if sessionID == "" {
		return
	}
	at := observation.At
	if at.IsZero() {
		at = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return
	}
	if at.After(s.clock) {
		s.clock = at
	}
	s.pruneLocked()

	entry := s.sessions[sessionID]
	if entry == nil {
		entry = &session{id: sessionID, first: at}
		s.sessions[sessionID] = entry
	}

	entry.seq++
	signal := observation.Signal
	if signal == "" {
		signal = SignalNone
	}
	authID := strings.TrimSpace(observation.AuthID)
	// A credential change between consecutive requests moves the session to an
	// account that never saw its prefix, so the resulting cold read is a rebind,
	// not an expiry.
	rebind := entry.seq > 1 && authID != "" && entry.authID != "" && authID != entry.authID

	tier := TierNA
	if signal != SignalNone {
		tier = TierHit
		switch {
		case observation.CacheReadTokens <= 0:
			tier = TierT0
		case observation.CacheReadTokens < entry.maxRead:
			tier = TierMiss
		}
	}

	var t0Cause T0Cause
	if tier == TierT0 {
		switch {
		case entry.seq == 1:
			t0Cause = T0CauseFirst
		case rebind:
			t0Cause = T0CauseRebind
		default:
			t0Cause = T0CauseExpiry
		}
	}

	request := Request{
		Seq:                   entry.seq,
		At:                    at,
		Provider:              strings.TrimSpace(observation.Provider),
		Model:                 strings.TrimSpace(observation.Model),
		AuthID:                authID,
		Signal:                signal,
		InputTokens:           observation.InputTokens,
		PromptTokens:          observation.PromptTokens,
		OutputTokens:          observation.OutputTokens,
		MaxTokens:             observation.MaxTokens,
		CacheReadTokens:       observation.CacheReadTokens,
		CacheCreationTokens:   observation.CacheCreationTokens,
		CacheCreation5mTokens: observation.CacheCreation5mTokens,
		CacheCreation1hTokens: observation.CacheCreation1hTokens,
		Tier:                  tier,
		DeltaRead:             observation.CacheReadTokens - entry.prevRead,
		MissReason:            strings.TrimSpace(observation.CacheMissReason),
		MissedTokens:          observation.CacheMissedTokens,
		IsProbe:               observation.IsProbe,
		Rebind:                rebind,
		T0Cause:               t0Cause,
	}

	entry.agg.Requests++
	switch tier {
	case TierNA:
		// Counted as a request, never classified: no tier, no hit-rate weight.
	case TierT0:
		entry.agg.Classified++
		entry.agg.T0s++
		switch t0Cause {
		case T0CauseRebind:
			entry.agg.T0Rebinds++
		case T0CauseExpiry:
			entry.agg.T0Expiries++
		}
	case TierMiss:
		entry.agg.Classified++
		entry.agg.Misses++
		if lost := entry.maxRead - observation.CacheReadTokens; lost > 0 {
			entry.agg.LostTokens += lost
			entry.observeLoss(at, lost)
		}
	default:
		entry.agg.Classified++
		entry.agg.Hits++
	}
	if rebind {
		entry.agg.Rebinds++
	}
	if observation.IsProbe {
		entry.agg.Probes++
	}
	entry.agg.InputTokens += observation.InputTokens
	entry.agg.PromptTokens += observation.PromptTokens
	entry.agg.OutputTokens += observation.OutputTokens
	entry.agg.CacheReadTokens += observation.CacheReadTokens
	entry.agg.CacheCreationTokens += observation.CacheCreationTokens
	entry.agg.CacheCreation5mTokens += observation.CacheCreation5mTokens
	entry.agg.CacheCreation1hTokens += observation.CacheCreation1hTokens

	if observation.CacheReadTokens > entry.maxRead {
		entry.maxRead = observation.CacheReadTokens
	}
	entry.prevRead = observation.CacheReadTokens
	entry.last = at
	if entry.first.IsZero() || at.Before(entry.first) {
		entry.first = at
	}
	if request.Model != "" {
		entry.model = request.Model
	}
	if request.AuthID != "" {
		entry.authID = request.AuthID
	}
	if request.Provider != "" {
		entry.provider = request.Provider
	}
	if observation.KeyedBy != "" {
		entry.keyedBy = observation.KeyedBy
	}
	entry.signal = signal
	if request.MissReason != "" {
		entry.lastReason = request.MissReason
	}
	entry.evaluateAlert(at, s.cfg.Alert)

	entry.requests = append(entry.requests, request)
	entry.trim(s.cfg.PerSessionRequests)
	s.evictLocked()
}

// observeLoss appends one miss loss to the sliding window.
func (e *session) observeLoss(at time.Time, tokens int64) {
	e.losses = append(e.losses, lossEvent{at: at, tokens: tokens})
	e.lossSum += tokens
}

// dropExpiredLosses drops loss events that fell out of the sliding window.
func (e *session) dropExpiredLosses(now time.Time) {
	cutoff := now.Add(-AlertWindow)
	index := 0
	for index < len(e.losses) && !e.losses[index].at.After(cutoff) {
		e.lossSum -= e.losses[index].tokens
		index++
	}
	if index > 0 {
		e.losses = append(e.losses[:0], e.losses[index:]...)
	}
	if e.lossSum < 0 {
		e.lossSum = 0
	}
}

// evaluateAlert raises the sustained-loss alert once per crossing and re-arms it
// only after the window drains below half the threshold, so a session hovering
// at the line does not log on every request.
func (e *session) evaluateAlert(now time.Time, cfg AlertConfig) {
	e.dropExpiredLosses(now)
	if !cfg.Enabled || cfg.LostTokensPerHour <= 0 {
		e.alerting = false
		return
	}
	if !e.alerting && e.lossSum >= cfg.LostTokensPerHour {
		e.alerting = true
		log.Warnf("cache-stats: session %s lost %d cached tokens in the last hour (misses=%d, t0=%d, rebinds=%d, last_reason=%s)",
			e.id, e.lossSum, e.agg.Misses, e.agg.T0s, e.agg.Rebinds, alertReason(e.lastReason))
		return
	}
	if e.alerting && e.lossSum*2 < cfg.LostTokensPerHour {
		e.alerting = false
	}
}

func alertReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "unreported"
	}
	return reason
}

func (e *session) trim(limit int) {
	if limit > 0 && len(e.requests) > limit {
		e.requests = append(e.requests[:0], e.requests[len(e.requests)-limit:]...)
	}
}

// pruneLocked drops sessions idle beyond the TTL, measured against the newest
// observation the store has seen.
func (s *Store) pruneLocked() {
	if s.cfg.IdleTTL <= 0 || s.clock.IsZero() {
		return
	}
	cutoff := s.clock.Add(-s.cfg.IdleTTL)
	for id, entry := range s.sessions {
		if entry.last.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}

// evictLocked enforces MaxSessions by dropping the least recently seen sessions.
func (s *Store) evictLocked() {
	if s.cfg.MaxSessions <= 0 || len(s.sessions) <= s.cfg.MaxSessions {
		return
	}
	type candidate struct {
		id   string
		last time.Time
	}
	candidates := make([]candidate, 0, len(s.sessions))
	for id, entry := range s.sessions {
		candidates = append(candidates, candidate{id: id, last: entry.last})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].last.Equal(candidates[j].last) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].last.Before(candidates[j].last)
	})
	for i := 0; i < len(candidates)-s.cfg.MaxSessions; i++ {
		delete(s.sessions, candidates[i].id)
	}
}

// Session returns one session's summary and retained request sequence. The id
// may be the full session key or the eight-character short id, because a
// fallback key embeds a model name and is neither memorable nor URL-shaped.
func (s *Store) Session(id string) (SessionDetail, bool) {
	if s == nil {
		return SessionDetail{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return SessionDetail{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.sessions[id]
	if !ok {
		entry, ok = s.sessionByShortIDLocked(id)
	}
	if !ok {
		return SessionDetail{}, false
	}
	requests := make([]Request, len(entry.requests))
	copy(requests, entry.requests)
	return SessionDetail{Summary: entry.summary(), Requests: requests}, true
}

// sessionByShortIDLocked resolves a short id, refusing an ambiguous prefix
// rather than returning an arbitrary one of several matches.
func (s *Store) sessionByShortIDLocked(shortID string) (*session, bool) {
	var match *session
	for _, entry := range s.sessions {
		if !strings.EqualFold(shortIDOf(entry.id), shortID) {
			continue
		}
		if match != nil {
			return nil, false
		}
		match = entry
	}
	return match, match != nil
}

// Sessions returns every retained session summary, newest activity first.
func (s *Store) Sessions() []SessionSummary {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	summaries := make([]SessionSummary, 0, len(s.sessions))
	for _, entry := range s.sessions {
		summaries = append(summaries, entry.summary())
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].LastSeen.Equal(summaries[j].LastSeen) {
			return summaries[i].ID < summaries[j].ID
		}
		return summaries[i].LastSeen.After(summaries[j].LastSeen)
	})
	return summaries
}

// Global aggregates every retained session.
func (s *Store) Global() Aggregate {
	if s == nil {
		return Aggregate{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total Aggregate
	for _, entry := range s.sessions {
		total.add(entry.agg)
		total.Sessions++
		total.observeWindow(entry.first, entry.last)
	}
	total.finish()
	return total
}

// ByModel groups retained sessions by the model they last used.
func (s *Store) ByModel() []KeyedAggregate {
	return s.groupBy(func(e *session) string { return e.model })
}

// ByAuth groups retained sessions by the credential they last used.
func (s *Store) ByAuth() []KeyedAggregate {
	return s.groupBy(func(e *session) string { return e.authID })
}

// groupBy aggregates sessions by a label. A session that switched model or
// credential mid-run is attributed to the last one it used.
func (s *Store) groupBy(key func(*session) string) []KeyedAggregate {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	buckets := make(map[string]*Aggregate)
	for _, entry := range s.sessions {
		label := strings.TrimSpace(key(entry))
		if label == "" {
			label = "unknown"
		}
		bucket := buckets[label]
		if bucket == nil {
			bucket = &Aggregate{}
			buckets[label] = bucket
		}
		bucket.add(entry.agg)
		bucket.Sessions++
		bucket.observeWindow(entry.first, entry.last)
	}
	grouped := make([]KeyedAggregate, 0, len(buckets))
	for label, bucket := range buckets {
		bucket.finish()
		grouped = append(grouped, KeyedAggregate{Key: label, Aggregate: *bucket})
	}
	sort.Slice(grouped, func(i, j int) bool {
		if grouped[i].Requests == grouped[j].Requests {
			return grouped[i].Key < grouped[j].Key
		}
		return grouped[i].Requests > grouped[j].Requests
	})
	return grouped
}

// Reset drops every retained session and reports how many were cleared.
func (s *Store) Reset() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := len(s.sessions)
	s.sessions = make(map[string]*session)
	s.clock = time.Time{}
	return cleared
}

func (e *session) summary() SessionSummary {
	agg := e.agg
	agg.Sessions = 1
	agg.FirstSeen = e.first
	agg.LastSeen = e.last
	agg.finish()
	keyedBy := e.keyedBy
	if keyedBy == "" {
		keyedBy = KeyedBySession
	}
	signal := e.signal
	if signal == "" {
		signal = SignalNone
	}
	return SessionSummary{
		ID:                 e.id,
		ShortID:            shortIDOf(e.id),
		KeyedBy:            keyedBy,
		Provider:           e.provider,
		Model:              e.model,
		AuthID:             e.authID,
		Signal:             signal,
		Aggregate:          agg,
		Regime:             regimeFor(agg.CacheCreation5mTokens, agg.CacheCreation1hTokens),
		Alerting:           e.alerting,
		LostTokensInWindow: e.lossSum,
	}
}

func regimeFor(fiveMinute, oneHour int64) Regime {
	switch {
	case fiveMinute <= 0 && oneHour <= 0:
		return RegimeNone
	case oneHour > fiveMinute:
		return Regime1h
	case fiveMinute > oneHour:
		return Regime5m
	default:
		return RegimeMixed
	}
}

// uuidPattern matches the canonical session UUID the Claude executors resolve.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// shortID gives every session a stable eight-character label. A UUID keeps its
// own first block so it stays recognizable; a composite fallback key is hashed,
// because its readable prefix is identical across every session on one API key.
func shortIDOf(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if uuidPattern.MatchString(id) {
		return id[:8]
	}
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])[:8]
}

func (a *Aggregate) add(other Aggregate) {
	a.Requests += other.Requests
	a.Hits += other.Hits
	a.Misses += other.Misses
	a.T0s += other.T0s
	a.Probes += other.Probes
	a.Rebinds += other.Rebinds
	a.Classified += other.Classified
	a.T0Rebinds += other.T0Rebinds
	a.T0Expiries += other.T0Expiries
	a.InputTokens += other.InputTokens
	a.PromptTokens += other.PromptTokens
	a.OutputTokens += other.OutputTokens
	a.CacheReadTokens += other.CacheReadTokens
	a.CacheCreationTokens += other.CacheCreationTokens
	a.CacheCreation5mTokens += other.CacheCreation5mTokens
	a.CacheCreation1hTokens += other.CacheCreation1hTokens
	a.LostTokens += other.LostTokens
}

func (a *Aggregate) observeWindow(first, last time.Time) {
	if !first.IsZero() && (a.FirstSeen.IsZero() || first.Before(a.FirstSeen)) {
		a.FirstSeen = first
	}
	if last.After(a.LastSeen) {
		a.LastSeen = last
	}
}

func (a *Aggregate) finish() {
	// Only classified requests carry a tier, so only they may weigh on the rate.
	// A provider reporting no cache accounting leaves HitRate at zero with a
	// zero Classified count, which the API and the TUI read as "no cache data"
	// rather than as a zero percent hit rate.
	if a.Classified > 0 {
		a.HitRate = float64(a.Hits) / float64(a.Classified)
	}
	if a.PromptTokens > 0 {
		a.CachedShare = float64(a.CacheReadTokens) / float64(a.PromptTokens)
	}
}

// Filter narrows a snapshot. A zero Filter matches every session.
type Filter struct {
	// Provider keeps only sessions last served by that provider, case-insensitively.
	Provider string
}

func (f Filter) matches(entry *session) bool {
	provider := strings.TrimSpace(f.Provider)
	if provider == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(entry.provider), provider)
}

// Snapshot is one consistent read of the store.
type Snapshot struct {
	Enabled   bool             `json:"enabled"`
	Global    Aggregate        `json:"global"`
	Providers []KeyedAggregate `json:"providers"`
	Models    []KeyedAggregate `json:"models"`
	Auths     []KeyedAggregate `json:"auths"`
	Sessions  []SessionSummary `json:"sessions"`
}

// Snapshot reads the whole store under one lock, so the global summary, the
// groupings and the session list always describe the same set of requests.
func (s *Store) Snapshot(filter Filter) Snapshot {
	snapshot := Snapshot{
		Providers: []KeyedAggregate{},
		Models:    []KeyedAggregate{},
		Auths:     []KeyedAggregate{},
		Sessions:  []SessionSummary{},
	}
	if s == nil {
		return snapshot
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot.Enabled = s.cfg.Enabled

	providers := make(map[string]*Aggregate)
	models := make(map[string]*Aggregate)
	auths := make(map[string]*Aggregate)
	for _, entry := range s.sessions {
		if !filter.matches(entry) {
			continue
		}
		snapshot.Global.add(entry.agg)
		snapshot.Global.Sessions++
		snapshot.Global.observeWindow(entry.first, entry.last)
		snapshot.Sessions = append(snapshot.Sessions, entry.summary())
		addGroup(providers, entry.provider, entry)
		addGroup(models, entry.model, entry)
		addGroup(auths, entry.authID, entry)
	}
	snapshot.Global.finish()
	snapshot.Providers = finishGroups(providers)
	snapshot.Models = finishGroups(models)
	snapshot.Auths = finishGroups(auths)
	sortSessionSummaries(snapshot.Sessions)
	return snapshot
}

func addGroup(buckets map[string]*Aggregate, label string, entry *session) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "unknown"
	}
	bucket := buckets[label]
	if bucket == nil {
		bucket = &Aggregate{}
		buckets[label] = bucket
	}
	bucket.add(entry.agg)
	bucket.Sessions++
	bucket.observeWindow(entry.first, entry.last)
}

func finishGroups(buckets map[string]*Aggregate) []KeyedAggregate {
	grouped := make([]KeyedAggregate, 0, len(buckets))
	for label, bucket := range buckets {
		bucket.finish()
		grouped = append(grouped, KeyedAggregate{Key: label, Aggregate: *bucket})
	}
	sort.Slice(grouped, func(i, j int) bool {
		if grouped[i].Requests == grouped[j].Requests {
			return grouped[i].Key < grouped[j].Key
		}
		return grouped[i].Requests > grouped[j].Requests
	})
	return grouped
}

func sortSessionSummaries(summaries []SessionSummary) {
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].LastSeen.Equal(summaries[j].LastSeen) {
			return summaries[i].ID < summaries[j].ID
		}
		return summaries[i].LastSeen.After(summaries[j].LastSeen)
	})
}

// ByProvider groups retained sessions by the provider that last served them.
func (s *Store) ByProvider() []KeyedAggregate {
	return s.groupBy(func(e *session) string { return e.provider })
}
