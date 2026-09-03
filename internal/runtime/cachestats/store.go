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
	"sort"
	"strings"
	"sync"
	"time"
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
}

// Observation is one completed upstream Claude request.
type Observation struct {
	SessionID string
	Model     string
	AuthID    string
	At        time.Time

	InputTokens  int64
	OutputTokens int64
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
	Model                 string    `json:"model"`
	AuthID                string    `json:"auth_id"`
	InputTokens           int64     `json:"input_tokens"`
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
}

// Aggregate is the counter set shared by the global, per-model, per-auth and
// per-session summaries.
type Aggregate struct {
	Requests              int64     `json:"requests"`
	Hits                  int64     `json:"hits"`
	Misses                int64     `json:"misses"`
	T0s                   int64     `json:"t0s"`
	Probes                int64     `json:"probes"`
	HitRate               float64   `json:"hit_rate"`
	InputTokens           int64     `json:"input_tokens"`
	OutputTokens          int64     `json:"output_tokens"`
	CacheReadTokens       int64     `json:"cache_read_tokens"`
	CacheCreationTokens   int64     `json:"cache_creation_tokens"`
	CacheCreation5mTokens int64     `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int64     `json:"cache_creation_1h_tokens"`
	LostTokens            int64     `json:"lost_tokens"`
	Sessions              int64     `json:"sessions"`
	FirstSeen             time.Time `json:"first_seen"`
	LastSeen              time.Time `json:"last_seen"`
}

// KeyedAggregate is an Aggregate labelled with the model or auth it covers.
type KeyedAggregate struct {
	Key string `json:"key"`
	Aggregate
}

// SessionSummary describes one retained session.
type SessionSummary struct {
	ID      string `json:"id"`
	ShortID string `json:"short_id"`
	Model   string `json:"model"`
	AuthID  string `json:"auth_id"`
	Aggregate
	Regime Regime `json:"regime"`
}

// SessionDetail pairs a session summary with its retained request sequence.
type SessionDetail struct {
	Summary  SessionSummary `json:"session"`
	Requests []Request      `json:"requests"`
}

type session struct {
	id      string
	model   string
	authID  string
	first   time.Time
	last    time.Time
	seq     int
	maxRead int64
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
	tier := TierHit
	switch {
	case observation.CacheReadTokens <= 0:
		tier = TierT0
	case observation.CacheReadTokens < entry.maxRead:
		tier = TierMiss
	}

	request := Request{
		Seq:                   entry.seq,
		At:                    at,
		Model:                 strings.TrimSpace(observation.Model),
		AuthID:                strings.TrimSpace(observation.AuthID),
		InputTokens:           observation.InputTokens,
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
	}

	entry.agg.Requests++
	switch tier {
	case TierT0:
		entry.agg.T0s++
	case TierMiss:
		entry.agg.Misses++
		if lost := entry.maxRead - observation.CacheReadTokens; lost > 0 {
			entry.agg.LostTokens += lost
		}
	default:
		entry.agg.Hits++
	}
	if observation.IsProbe {
		entry.agg.Probes++
	}
	entry.agg.InputTokens += observation.InputTokens
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

	entry.requests = append(entry.requests, request)
	entry.trim(s.cfg.PerSessionRequests)
	s.evictLocked()
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

// Session returns one session's summary and retained request sequence.
func (s *Store) Session(id string) (SessionDetail, bool) {
	if s == nil {
		return SessionDetail{}, false
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.sessions[id]
	if !ok {
		return SessionDetail{}, false
	}
	requests := make([]Request, len(entry.requests))
	copy(requests, entry.requests)
	return SessionDetail{Summary: entry.summary(), Requests: requests}, true
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
	return SessionSummary{
		ID:        e.id,
		ShortID:   shortID(e.id),
		Model:     e.model,
		AuthID:    e.authID,
		Aggregate: agg,
		Regime:    regimeFor(agg.CacheCreation5mTokens, agg.CacheCreation1hTokens),
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

func shortID(id string) string {
	id = strings.TrimSpace(id)
	trimmed := strings.ReplaceAll(id, "-", "")
	if len(trimmed) > 8 {
		return trimmed[:8]
	}
	if trimmed != "" {
		return trimmed
	}
	return id
}

func (a *Aggregate) add(other Aggregate) {
	a.Requests += other.Requests
	a.Hits += other.Hits
	a.Misses += other.Misses
	a.T0s += other.T0s
	a.Probes += other.Probes
	a.InputTokens += other.InputTokens
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
	if a.Requests > 0 {
		a.HitRate = float64(a.Hits) / float64(a.Requests)
	}
}
