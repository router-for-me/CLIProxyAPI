package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// defaultStickyGjsonPaths are the JSON paths used to extract session keys from request bodies.
// These cover the common CLI tools that support session affinity:
//   - metadata.user_id: Claude Code sends this in /v1/messages requests
//   - prompt_cache_key: Codex CLI sends this in /v1/responses requests
var defaultStickyGjsonPaths = []string{"metadata.user_id", "prompt_cache_key"}

// defaultBodyPrefixHashSize is the number of leading bytes of the request body
// used to compute a fallback session key when no gjson path matches.
// 32 KiB covers the system prompt plus the first few conversation turns,
// providing a stable session fingerprint for multi-turn conversations.
const defaultBodyPrefixHashSize = 32 * 1024

// minBodySizeForHash is the minimum request body size required to enable
// body-prefix hashing. Bodies smaller than this are likely single-turn or
// trivial requests where sticky routing adds little value and collision
// risk is higher.
const minBodySizeForHash = 512

// StickySelector implements session-affinity routing using an LRU cache.
// It extracts a session key from the request body (via configurable gjson paths),
// looks up the key in the cache, and pins the request to the cached auth if found.
// On cache miss it delegates to round-robin and records the binding.
//
// When no gjson path produces a key and the request body is large enough,
// a SHA-256 hash of the first bodyPrefixHashSize bytes is used as a fallback
// session key. This covers clients (Cursor, Windsurf, Gemini SDK, etc.) that
// do not send metadata.user_id or prompt_cache_key.
type StickySelector struct {
	inner              *RoundRobinSelector
	cache              *lruCache
	gjsonPaths         []string
	bodyPrefixHashSize int
}

// StickyBodyHashConfig controls the body-prefix-hash fallback.
type StickyBodyHashConfig struct {
	Enabled bool // Whether body-prefix hashing is enabled. Default true.
	SizeKB  int  // Leading KB to hash. 0 → defaultBodyPrefixHashSize (32 KB).
}

// NewStickySelector creates a sticky selector with the given LRU capacity and gjson paths.
// A zero or negative lruSize defaults to 1024. Nil gjsonPaths defaults to
// ["metadata.user_id", "prompt_cache_key"].
func NewStickySelector(lruSize int, gjsonPaths []string, bodyHash *StickyBodyHashConfig) *StickySelector {
	if len(gjsonPaths) == 0 {
		gjsonPaths = defaultStickyGjsonPaths
	}

	hashSize := defaultBodyPrefixHashSize
	if bodyHash != nil && !bodyHash.Enabled {
		hashSize = 0 // disabled
	} else if bodyHash != nil && bodyHash.SizeKB > 0 {
		hashSize = bodyHash.SizeKB * 1024
	}

	return &StickySelector{
		inner:              &RoundRobinSelector{},
		cache:              newLRUCache(lruSize),
		gjsonPaths:         gjsonPaths,
		bodyPrefixHashSize: hashSize,
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
// and returns the first non-empty match. If no gjson path matches and the body
// is large enough, it falls back to a SHA-256 hash of the body prefix.
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
	return s.bodyPrefixHash(payload)
}

// bodyPrefixHash computes a SHA-256 digest of the first N bytes of payload
// and returns it as a hex-encoded session key prefixed with "bph:".
// Returns "" if the payload is too small to produce a meaningful fingerprint.
func (s *StickySelector) bodyPrefixHash(payload []byte) string {
	if len(payload) < minBodySizeForHash || s.bodyPrefixHashSize <= 0 {
		return ""
	}
	data := payload
	if len(data) > s.bodyPrefixHashSize {
		data = data[:s.bodyPrefixHashSize]
	}
	sum := sha256.Sum256(data)
	return "bph:" + hex.EncodeToString(sum[:16])
}

// EvictSession removes the binding for the given session key.
func (s *StickySelector) EvictSession(sessionKey string) {
	s.cache.Remove(sessionKey)
}

// CacheLen returns the number of active session bindings.
func (s *StickySelector) CacheLen() int {
	return s.cache.Len()
}
