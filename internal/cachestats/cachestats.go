// Package cachestats counts prompt-cache reads and writes per credential.
//
// Prompt caches belong to one upstream account, so a thread that hops between
// accounts makes upstream write its cache again on every hop. These counters
// let the console show, per account, how much input was served from cache
// versus written to it, which is how a broken session affinity shows up.
package cachestats

import (
	"context"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Window is how far back the recent counters reach.
const Window = time.Hour

const buckets = 60 // one per minute of Window

// Tokens is a set of input token counters.
type Tokens struct {
	Requests   int64 `json:"requests"`
	Uncached   int64 `json:"uncached_input_tokens"`
	CacheRead  int64 `json:"cache_read_tokens"`
	CacheWrite int64 `json:"cache_write_tokens"`
}

func (t *Tokens) add(o Tokens) {
	t.Requests += o.Requests
	t.Uncached += o.Uncached
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// Credential holds the counters for one credential.
type Credential struct {
	Recent Tokens `json:"recent"`
	Total  Tokens `json:"total"`
}

type bucket struct {
	minute int64
	tokens Tokens
}

type credential struct {
	ring  [buckets]bucket
	total Tokens
}

// Stats aggregates usage records by credential auth index.
type Stats struct {
	mu    sync.Mutex
	creds map[string]*credential
	now   func() time.Time
}

// New returns empty stats.
func New() *Stats {
	return &Stats{creds: make(map[string]*credential), now: time.Now}
}

var std = New()

func init() {
	coreusage.RegisterPlugin(std)
}

// Snapshot returns the counters of the process-wide stats.
func Snapshot() map[string]Credential { return std.Snapshot() }

// HandleUsage implements coreusage.Plugin.
func (s *Stats) HandleUsage(_ context.Context, r coreusage.Record) {
	if r.AuthIndex == "" || r.Failed {
		return
	}
	b := coreusage.EnsureTokenBreakdownForProvider(r.Detail, r.Provider, r.ExecutorType).TokenBreakdown
	if !b.Valid() {
		return
	}
	t := Tokens{
		Requests:   1,
		Uncached:   b.Input.UncachedTokens,
		CacheRead:  b.Input.CacheReadTokens,
		CacheWrite: b.Input.CacheWriteTokens,
	}
	at := r.RequestedAt
	if at.IsZero() {
		at = s.now()
	}
	s.add(r.AuthIndex, at, t)
}

func (s *Stats) add(authIndex string, at time.Time, t Tokens) {
	minute := at.Unix() / 60
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.creds[authIndex]
	if c == nil {
		c = new(credential)
		s.creds[authIndex] = c
	}
	b := &c.ring[minute%buckets]
	if b.minute < minute {
		*b = bucket{minute: minute}
	}
	// A record older than its slot's minute (a long stream reported late)
	// only counts toward the total, so it can't erase a newer minute.
	if b.minute == minute {
		b.tokens.add(t)
	}
	c.total.add(t)
}

// Snapshot returns the counters for every credential seen, keyed by auth index.
func (s *Stats) Snapshot() map[string]Credential {
	oldest := s.now().Unix()/60 - buckets + 1
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Credential, len(s.creds))
	for idx, c := range s.creds {
		var recent Tokens
		for _, b := range c.ring {
			if b.minute >= oldest {
				recent.add(b.tokens)
			}
		}
		out[idx] = Credential{Recent: recent, Total: c.total}
	}
	return out
}
