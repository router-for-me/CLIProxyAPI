package helps

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ResponseCacheRegistry owns response caches for one executor instance. Keeping
// the registry executor-scoped lets configuration reloads retire caches for
// removed providers when the old executor becomes unreachable.
type ResponseCacheRegistry struct {
	mu       sync.Mutex
	caches   map[string]*cache.ResponseCache
	profiles map[string]string
}

// NewResponseCacheRegistry creates an empty executor-scoped cache registry.
func NewResponseCacheRegistry() *ResponseCacheRegistry {
	return &ResponseCacheRegistry{
		caches:   make(map[string]*cache.ResponseCache),
		profiles: make(map[string]string),
	}
}

// ResponseCacheSettings holds the resolved cache parameters for a provider.
type ResponseCacheSettings struct {
	TTL           time.Duration
	MaxEntries    int
	MaxEntryBytes int
	Models        []string
}

// ResolveResponseCacheSettings normalizes the provider response-cache configuration.
// The second return value reports whether caching is enabled for the provider.
func ResolveResponseCacheSettings(compat *config.OpenAICompatibility) (ResponseCacheSettings, bool) {
	if compat == nil || !compat.ResponseCache.Enabled {
		return ResponseCacheSettings{}, false
	}
	maxEntryBytes := compat.ResponseCache.MaxEntryBytes
	if maxEntryBytes <= 0 {
		maxEntryBytes = cache.DefaultResponseCacheMaxEntryBytes
	}
	settings := ResponseCacheSettings{
		TTL:           cache.DefaultResponseCacheTTL,
		MaxEntries:    compat.ResponseCache.MaxEntries,
		MaxEntryBytes: maxEntryBytes,
		Models:        compat.ResponseCache.Models,
	}
	if raw := strings.TrimSpace(compat.ResponseCache.TTL); raw != "" {
		if parsed, errParse := time.ParseDuration(raw); errParse == nil && parsed > 0 {
			settings.TTL = parsed
		}
	}
	return settings, true
}

// CacheFor returns the cache for one provider configuration, creating or
// replacing it when the effective settings change.
func (r *ResponseCacheRegistry) CacheFor(compat *config.OpenAICompatibility, providerKey string) (*cache.ResponseCache, ResponseCacheSettings) {
	settings, enabled := ResolveResponseCacheSettings(compat)
	if !enabled || r == nil {
		return nil, ResponseCacheSettings{}
	}
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return nil, ResponseCacheSettings{}
	}
	profile := fmt.Sprintf("%s|%d|%d|%s", settings.TTL, settings.MaxEntries, settings.MaxEntryBytes, strings.Join(settings.Models, ","))

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.caches[providerKey]; ok && r.profiles[providerKey] == profile {
		return existing, settings
	}
	created := cache.NewResponseCache(settings.TTL, settings.MaxEntries, settings.MaxEntryBytes)
	r.caches[providerKey] = created
	r.profiles[providerKey] = profile
	return created, settings
}

// Lookup resolves the cache and key for one upstream request. providerKey must
// identify the concrete provider configuration, not only its display name.
func (r *ResponseCacheRegistry) Lookup(compat *config.OpenAICompatibility, providerKey, authID, url, model, variant string, stream bool, headers http.Header, payload []byte) (*cache.ResponseCache, string, ResponseCacheSettings) {
	if len(payload) == 0 {
		return nil, "", ResponseCacheSettings{}
	}
	responseCache, settings := r.CacheFor(compat, providerKey)
	if responseCache == nil {
		return nil, "", ResponseCacheSettings{}
	}
	if !cache.ResponseCacheModelAllowed(settings.Models, model) {
		return nil, "", settings
	}
	keyProvider := providerKey
	if authID != "" {
		keyProvider += "|" + authID
	}
	if variant != "" {
		keyProvider += "|" + variant
	}
	return responseCache, cache.ResponseCacheKey(keyProvider, url, model, stream, headers, payload), settings
}

// EncodeCachedStreamFrames serializes raw upstream SSE data frames using a
// length-prefixed encoding. SSE events may contain pretty-printed JSON joined
// with newlines, so newline-delimited storage would be ambiguous.
func EncodeCachedStreamFrames(frames []string) []byte {
	if len(frames) == 0 {
		return nil
	}
	var encoded bytes.Buffer
	for _, frame := range frames {
		if errWrite := binary.Write(&encoded, binary.BigEndian, uint64(len(frame))); errWrite != nil {
			return nil
		}
		encoded.WriteString(frame)
	}
	return encoded.Bytes()
}

// DecodeCachedStreamFrames restores length-prefixed upstream SSE data frames.
// Malformed or truncated payloads are rejected in full to avoid partial replay.
func DecodeCachedStreamFrames(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	reader := bytes.NewReader(payload)
	frames := make([]string, 0)
	for reader.Len() > 0 {
		var size uint64
		if errRead := binary.Read(reader, binary.BigEndian, &size); errRead != nil || size > uint64(reader.Len()) {
			return nil
		}
		frame := make([]byte, int(size))
		if _, errRead := reader.Read(frame); errRead != nil {
			return nil
		}
		frames = append(frames, string(frame))
	}
	return frames
}
