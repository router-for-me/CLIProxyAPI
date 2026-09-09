package helps

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestResolveResponseCacheSettingsDisabledByDefault(t *testing.T) {
	if _, enabled := ResolveResponseCacheSettings(nil); enabled {
		t.Fatal("expected nil provider to disable caching")
	}
	compat := &config.OpenAICompatibility{Name: "zen"}
	if _, enabled := ResolveResponseCacheSettings(compat); enabled {
		t.Fatal("expected caching to be opt-in")
	}
}

func TestResolveResponseCacheSettingsParsesTTL(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name: "zen",
		ResponseCache: config.ResponseCacheConfig{
			Enabled:       true,
			TTL:           "90s",
			MaxEntries:    10,
			MaxEntryBytes: 2048,
			Models:        []string{"claude-opus-5"},
		},
	}
	settings, enabled := ResolveResponseCacheSettings(compat)
	if !enabled {
		t.Fatal("expected caching to be enabled")
	}
	if settings.TTL != 90*time.Second {
		t.Fatalf("unexpected ttl: %s", settings.TTL)
	}
	if settings.MaxEntries != 10 || settings.MaxEntryBytes != 2048 {
		t.Fatalf("unexpected bounds: %+v", settings)
	}

	compat.ResponseCache.TTL = "not-a-duration"
	settings, _ = ResolveResponseCacheSettings(compat)
	if settings.TTL != 5*time.Minute {
		t.Fatalf("expected default ttl fallback, got %s", settings.TTL)
	}
}

func TestResponseCacheForReusesInstanceAndRebuildsOnChange(t *testing.T) {
	registry := NewResponseCacheRegistry()
	compat := &config.OpenAICompatibility{
		Name:          "zen",
		ResponseCache: config.ResponseCacheConfig{Enabled: true, TTL: "1m"},
	}
	first, _ := registry.CacheFor(compat, "zendigikey")
	second, _ := registry.CacheFor(compat, "zendigikey")
	if first == nil || first != second {
		t.Fatal("expected the same cache instance for unchanged settings")
	}

	compat.ResponseCache.TTL = "2m"
	third, _ := registry.CacheFor(compat, "zendigikey")
	if third == first {
		t.Fatal("expected a new cache instance after settings change")
	}
}

func TestResponseCacheRegistrySeparatesProviderConfigurations(t *testing.T) {
	registry := NewResponseCacheRegistry()
	compat := &config.OpenAICompatibility{
		Name:          "duplicate",
		ResponseCache: config.ResponseCacheConfig{Enabled: true, TTL: "1m"},
	}
	first, _ := registry.CacheFor(compat, "openai-compatibility|config:0")
	second, _ := registry.CacheFor(compat, "openai-compatibility|config:1")
	if first == nil || second == nil || first == second {
		t.Fatal("expected duplicate-name configurations to own separate caches")
	}
	again, _ := registry.CacheFor(compat, "openai-compatibility|config:0")
	if again != first {
		t.Fatal("expected provider configuration to reuse its own cache")
	}
}

func TestResponseCacheLookupRespectsModelAllowlist(t *testing.T) {
	registry := NewResponseCacheRegistry()
	compat := &config.OpenAICompatibility{
		Name: "zen",
		ResponseCache: config.ResponseCacheConfig{
			Enabled: true,
			Models:  []string{"claude-opus-5"},
		},
	}
	payload := []byte(`{"model":"claude-opus-5"}`)

	c, key, _ := registry.Lookup(compat, "zendigikey", "auth1", "https://x/v1/chat/completions", "claude-opus-5", "openai", false, nil, payload)
	if c == nil || key == "" {
		t.Fatal("expected allowlisted model to be cacheable")
	}

	c, _, _ = registry.Lookup(compat, "zendigikey", "auth1", "https://x/v1/chat/completions", "gpt-5.4-mini", "openai", false, nil, payload)
	if c != nil {
		t.Fatal("expected non-allowlisted model to skip caching")
	}
}

func TestResponseCacheLookupSkipsWhenDisabledOrEmptyPayload(t *testing.T) {
	registry := NewResponseCacheRegistry()
	enabled := &config.OpenAICompatibility{Name: "zen", ResponseCache: config.ResponseCacheConfig{Enabled: true}}
	if c, _, _ := registry.Lookup(enabled, "zendigikey", "auth1", "u", "m", "openai", false, nil, nil); c != nil {
		t.Fatal("expected empty payload to skip caching")
	}

	disabled := &config.OpenAICompatibility{Name: "zen"}
	if c, _, _ := registry.Lookup(disabled, "zendigikey", "auth1", "u", "m", "openai", false, nil, []byte(`{}`)); c != nil {
		t.Fatal("expected disabled provider to skip caching")
	}
}

func TestResponseCacheLookupSeparatesAuthAndVariant(t *testing.T) {
	registry := NewResponseCacheRegistry()
	compat := &config.OpenAICompatibility{Name: "zen", ResponseCache: config.ResponseCacheConfig{Enabled: true}}
	payload := []byte(`{"a":1}`)

	_, keyAuth1, _ := registry.Lookup(compat, "zendigikey", "auth1", "u", "m", "openai", false, nil, payload)
	_, keyAuth2, _ := registry.Lookup(compat, "zendigikey", "auth2", "u", "m", "openai", false, nil, payload)
	if keyAuth1 == keyAuth2 {
		t.Fatal("expected different credentials to use different cache keys")
	}

	_, keyClaude, _ := registry.Lookup(compat, "zendigikey", "auth1", "u", "m", "claude", false, nil, payload)
	if keyClaude == keyAuth1 {
		t.Fatal("expected different response formats to use different cache keys")
	}
}

func TestAppendCachedStreamFrameEnforcesEncodedLimit(t *testing.T) {
	encoded, ok := AppendCachedStreamFrame(nil, []byte("{}"), 10)
	if !ok || len(encoded) != 10 {
		t.Fatalf("encoded frame = %d bytes, ok=%v; want exactly 10 bytes", len(encoded), ok)
	}
	if got, okSecond := AppendCachedStreamFrame(encoded, []byte("{}"), 19); okSecond || got != nil {
		t.Fatalf("oversized append = %#v, ok=%v; want nil, false", got, okSecond)
	}
	if got, okSmall := AppendCachedStreamFrame(nil, []byte("{}"), 9); okSmall || got != nil {
		t.Fatalf("undersized limit append = %#v, ok=%v; want nil, false", got, okSmall)
	}
}

func TestCachedStreamFrameRoundTrip(t *testing.T) {
	frames := []string{`{"choices":[{"delta":{"content":"a"}}]}`, "{\n  \"choices\": [{\"delta\": {\"content\": \"b\"}}]\n}", "[DONE]"}
	decoded := DecodeCachedStreamFrames(EncodeCachedStreamFrames(frames))
	if len(decoded) != len(frames) {
		t.Fatalf("expected %d frames, got %d", len(frames), len(decoded))
	}
	for i := range frames {
		if decoded[i] != frames[i] {
			t.Fatalf("frame %d mismatch: %q vs %q", i, decoded[i], frames[i])
		}
	}
	if EncodeCachedStreamFrames(nil) != nil {
		t.Fatal("expected nil encoding for no frames")
	}
	if DecodeCachedStreamFrames(nil) != nil {
		t.Fatal("expected nil decoding for empty payload")
	}
}

func TestDecodeCachedStreamFramesRejectsTruncatedPayload(t *testing.T) {
	encoded := EncodeCachedStreamFrames([]string{`{"id":"1"}`})
	if got := DecodeCachedStreamFrames(encoded[:len(encoded)-1]); got != nil {
		t.Fatalf("decoded truncated payload = %#v, want nil", got)
	}
}
