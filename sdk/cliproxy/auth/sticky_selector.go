package auth

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// defaultStickyGjsonPaths are the JSON paths used to extract session keys from request bodies.
// These cover the common CLI tools that support session affinity:
//   - metadata.user_id: Claude Code sends this in /v1/messages requests
//   - prompt_cache_key: Codex CLI sends this in /v1/responses requests
var defaultStickyGjsonPaths = []string{"metadata.user_id", "prompt_cache_key"}

// StickySelector implements session-affinity routing using an LRU cache.
// It extracts a session key from the request body (via configurable gjson paths),
// looks up the key in the cache, and pins the request to the cached auth if found.
// On cache miss it delegates to round-robin and records the binding.
type StickySelector struct {
	inner      *RoundRobinSelector
	cache      *lruCache
	gjsonPaths []string
}

// NewStickySelector creates a sticky selector with the given LRU capacity and gjson paths.
// A zero or negative lruSize defaults to 1024. Nil gjsonPaths defaults to
// ["metadata.user_id", "prompt_cache_key"].
func NewStickySelector(lruSize int, gjsonPaths []string) *StickySelector {
	if len(gjsonPaths) == 0 {
		gjsonPaths = defaultStickyGjsonPaths
	}
	return &StickySelector{
		inner:      &RoundRobinSelector{},
		cache:      newLRUCache(lruSize),
		gjsonPaths: gjsonPaths,
	}
}

// Pick selects an auth using session affinity when a session key is present,
// falling back to round-robin otherwise.
func (s *StickySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	sessionKey := s.extractSessionKey(opts.OriginalRequest)
	if sessionKey == "" {
		return s.inner.Pick(ctx, provider, model, opts, auths)
	}

	// Check cache for an existing binding.
	if cachedAuthID, ok := s.cache.Get(sessionKey); ok {
		for _, auth := range auths {
			if auth != nil && auth.ID == cachedAuthID {
				return auth, nil
			}
		}
		// Cached auth is not in the candidate list (filtered out by tried/cooldown/disabled).
		// Evict the stale entry and fall through to round-robin.
		s.cache.Remove(sessionKey)
	}

	// No cache hit — delegate to round-robin.
	selected, err := s.inner.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		return nil, err
	}

	// Record the binding.
	s.cache.Put(sessionKey, selected.ID)
	return selected, nil
}

// extractSessionKey tries each configured gjson path against the request body
// and returns the first non-empty match.
func (s *StickySelector) extractSessionKey(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range s.gjsonPaths {
		result := gjson.GetBytes(payload, path)
		if result.Exists() {
			value := strings.TrimSpace(result.String())
			if value != "" {
				return value
			}
		}
	}
	return ""
}

// EvictSession removes the binding for the given session key.
func (s *StickySelector) EvictSession(sessionKey string) {
	s.cache.Remove(sessionKey)
}

// CacheLen returns the number of active session bindings.
func (s *StickySelector) CacheLen() int {
	return s.cache.Len()
}
