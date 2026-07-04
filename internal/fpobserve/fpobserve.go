// Package fpobserve is a tiny leaf store for sampled outbound-fingerprint observations.
// It is written by the runtime executor (which applies the fingerprint) and read by the
// management API (which serves the observatory page), so it lives in its own leaf package
// to keep both sides importing it without an import cycle. Log-only feature data; holds at
// most the latest observation per (provider, account) plus a running count. See
// config.FingerprintObserve.
package fpobserve

import (
	"sort"
	"sync"
	"time"
)

// Record is one account's most recent observed outbound fingerprint. Account identifiers
// are already reduced to shape/presence/fnv-tag upstream — this struct never holds a raw
// email, token, or session UUID.
type Record struct {
	Account    string `json:"account"`
	Provider   string `json:"provider"`
	Host       string `json:"host"`
	TLSProfile string `json:"tls_profile"`
	UserAgent  string `json:"user_agent"`
	Originator string `json:"originator,omitempty"`
	SessionHdr string `json:"session_hdr,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	AcceptEnc  string `json:"accept_enc"`
	// Claude-only extras (empty for codex/antigravity).
	XApp         string `json:"x_app,omitempty"`
	StainlessPkg string `json:"stainless_pkg,omitempty"`
	StainlessOS  string `json:"stainless_os,omitempty"`
	StainlessRT  string `json:"stainless_rt,omitempty"`
	Arch         string `json:"arch,omitempty"`

	LastSeenUnix int64 `json:"last_seen_unix"`
	Count        int64 `json:"count"`
}

var (
	mu    sync.Mutex
	store = map[string]*Record{}
)

// Put records/updates the latest observation for its account and increments its count.
// nowUnix defaults to time.Now() when <= 0 (a param keeps it testable/deterministic).
func Put(rec Record, nowUnix int64) {
	if nowUnix <= 0 {
		nowUnix = time.Now().Unix()
	}
	rec.LastSeenUnix = nowUnix
	key := rec.Provider + "|" + rec.Account
	mu.Lock()
	if prev, ok := store[key]; ok {
		rec.Count = prev.Count + 1
	} else {
		rec.Count = 1
	}
	r := rec
	store[key] = &r
	mu.Unlock()
}

// Snapshot returns a stable-sorted copy of all current observations.
func Snapshot() []Record {
	mu.Lock()
	out := make([]Record, 0, len(store))
	for _, r := range store {
		out = append(out, *r)
	}
	mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Account < out[j].Account
	})
	return out
}

// Reset clears the store (used by tests).
func Reset() {
	mu.Lock()
	store = map[string]*Record{}
	mu.Unlock()
}
