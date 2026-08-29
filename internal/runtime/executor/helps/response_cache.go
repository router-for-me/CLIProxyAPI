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

// responseCacheRegistry keeps one response cache per OpenAI-compatible provider.
// Executors are rebuilt whenever the configuration reloads, so the caches must
// outlive them to stay useful across hot reloads.
var (
	responseCacheMu       sync.Mutex
	responseCaches        = make(map[string]*cache.ResponseCache)
	responseCacheProfiles = make(map[string]string)
)

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

// ResponseCacheFor returns the shared cache for a provider, creating or replacing
// it when the effective settings change. It returns nil when caching is disabled.
func ResponseCacheFor(compat *config.OpenAICompatibility, providerKey string) (*cache.ResponseCache, ResponseCacheSettings) {
	settings, enabled := ResolveResponseCacheSettings(compat)
	if !enabled {
		return nil, ResponseCacheSettings{}
	}
	name := strings.TrimSpace(compat.Name)
	if name == "" {
		name = strings.TrimSpace(providerKey)
	}
	if name == "" {
		return nil, ResponseCacheSettings{}
	}
	profile := fmt.Sprintf("%s|%d|%d|%s", settings.TTL, settings.MaxEntries, settings.MaxEntryBytes, strings.Join(settings.Models, ","))

	responseCacheMu.Lock()
	defer responseCacheMu.Unlock()
	if existing, ok := responseCaches[name]; ok && responseCacheProfiles[name] == profile {
		return existing, settings
	}
	created := cache.NewResponseCache(settings.TTL, settings.MaxEntries, settings.MaxEntryBytes)
	responseCaches[name] = created
	responseCacheProfiles[name] = profile
	return created, settings
}

// ResponseCacheLookup resolves the cache and key for one upstream request.
// It returns a nil cache when the provider, model, or payload is not cacheable,
// along with the effective provider cache settings.
// variant separates entries that share an upstream payload but need different
// downstream output, such as two response formats translated from one request.
func ResponseCacheLookup(compat *config.OpenAICompatibility, providerKey, authID, url, model, variant string, stream bool, headers http.Header, payload []byte) (*cache.ResponseCache, string, ResponseCacheSettings) {
	if len(payload) == 0 {
		return nil, "", ResponseCacheSettings{}
	}
	responseCache, settings := ResponseCacheFor(compat, providerKey)
	if responseCache == nil {
		return nil, "", ResponseCacheSettings{}
	}
	if !cache.ResponseCacheModelAllowed(settings.Models, model) {
		return nil, "", settings
	}
	keyProvider := providerKey
	if authID != "" {
		keyProvider = providerKey + "|" + authID
	}
	if variant != "" {
		keyProvider = keyProvider + "|" + variant
	}
	return responseCache, cache.ResponseCacheKey(keyProvider, url, model, stream, headers, payload), settings
}

// ResetResponseCaches drops every provider cache. Used by tests.
func ResetResponseCaches() {
	responseCacheMu.Lock()
	defer responseCacheMu.Unlock()
	responseCaches = make(map[string]*cache.ResponseCache)
	responseCacheProfiles = make(map[string]string)
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
