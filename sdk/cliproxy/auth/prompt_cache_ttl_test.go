package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestEstimatePromptCacheTTL(t *testing.T) {
	nativeUA := http.Header{"User-Agent": []string{"claude-cli/2.1.280 (external, cli)"}}
	cases := []struct {
		name     string
		provider string
		headers  http.Header
		body     string
		want     time.Duration
	}{
		{
			name:     "native claude code default breakpoints",
			provider: "claude",
			headers:  nativeUA,
			body:     `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`,
			want:     claudePromptCacheTTL5m + promptCacheTTLGrace,
		},
		{
			name:     "native claude code 1h system breakpoint",
			provider: "claude",
			headers:  nativeUA,
			body:     `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
			want:     claudePromptCacheTTL1h + promptCacheTTLGrace,
		},
		{
			name:     "native claude code 1h message content breakpoint",
			provider: "claude",
			headers:  nativeUA,
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
			want:     claudePromptCacheTTL1h + promptCacheTTLGrace,
		},
		{
			name:     "non-native caller may be upgraded to 1h by the executor",
			provider: "claude",
			body:     `{"messages":[{"role":"user","content":"hi"}]}`,
			want:     claudePromptCacheTTL1h + promptCacheTTLGrace,
		},
		{
			name:     "mixed follows claude rules",
			provider: "mixed",
			headers:  nativeUA,
			body:     `{}`,
			want:     claudePromptCacheTTL5m + promptCacheTTLGrace,
		},
		{
			name:     "codex uses implicit cache default",
			provider: "codex",
			body:     `{"input":[]}`,
			want:     defaultPromptCacheTTL + promptCacheTTLGrace,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimatePromptCacheTTL(tc.provider, tc.headers, []byte(tc.body)); got != tc.want {
				t.Fatalf("estimatePromptCacheTTL() = %v, want %v", got, tc.want)
			}
			if got := estimatePromptCacheTTL(tc.provider, tc.headers, []byte(tc.body)); got > maxPromptCacheBindingTTL {
				t.Fatalf("estimate %v exceeds max binding TTL %v", got, maxPromptCacheBindingTTL)
			}
		})
	}
}

func TestSessionCacheTTLRefreshNeverShortensBinding(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()

	cache.SetAliasesTTL(time.Hour, "auth-a", "session")
	long := cache.entries["session"].expiresAt

	if !cache.TouchTTL("session", "auth-a", time.Minute) {
		t.Fatal("TouchTTL() = false, want true")
	}
	if got := cache.entries["session"].expiresAt; got.Before(long) {
		t.Fatalf("TouchTTL shortened expiry: %v < %v", got, long)
	}
	if _, ok := cache.GetAndRefreshTTL("session", time.Minute); !ok {
		t.Fatal("GetAndRefreshTTL() missed live binding")
	}
	if got := cache.entries["session"].expiresAt; got.Before(long) {
		t.Fatalf("GetAndRefreshTTL shortened expiry: %v < %v", got, long)
	}
	cache.SetAliasesTTL(time.Minute, "auth-a", "session")
	if got := cache.entries["session"].expiresAt; got.Before(long) {
		t.Fatalf("same-auth SetAliasesTTL shortened expiry: %v < %v", got, long)
	}

	// Moving to another credential starts from that credential's own cache state.
	cache.SetAliasesTTL(time.Minute, "auth-b", "session")
	if got := cache.entries["session"].expiresAt; !got.Before(long) {
		t.Fatalf("rebinding to another auth kept old expiry %v", got)
	}
}

func TestSessionAffinityCacheAwareTTLFollowsPromptCache(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:      &RoundRobinSelector{},
		TTL:           24 * time.Hour,
		CacheAwareTTL: true,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	headers := http.Header{
		"User-Agent":               []string{"claude-cli/2.1.280 (external, cli)"},
		"X-Claude-Code-Session-Id": []string{"cache-aware-session"},
	}
	opts := cliproxyexecutor.Options{Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

	before := time.Now()
	first, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	key := selectorCacheKeyFor(t, selector, first.ID)
	expiresAt := selector.cache.entries[key].expiresAt
	want := claudePromptCacheTTL5m + promptCacheTTLGrace
	if got := expiresAt.Sub(before); got < want || got > want+time.Minute {
		t.Fatalf("binding lifetime = %v, want about %v (configured TTL must be ignored)", got, want)
	}
	binding := selector.cache.entries[key].binding
	if binding.sessionID != "claude:cache-aware-session" || binding.provider != "claude" || binding.sequence == 0 {
		t.Fatalf("cache-aware selection lost guardian binding metadata: %+v", binding)
	}
	if !selector.cache.TouchTTL(key, first.ID, time.Minute) {
		t.Fatal("TouchTTL() missed selected binding")
	}
	if refreshed := selector.cache.entries[key]; refreshed.binding != binding || refreshed.expiresAt.Before(expiresAt) {
		t.Fatalf("result refresh changed selection metadata or shortened expiry: %+v", refreshed)
	}
	reused, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, auths)
	if err != nil || reused == nil || reused.ID != first.ID {
		t.Fatalf("cached Pick() = %v, %v, want auth %s", reused, err, first.ID)
	}
	if selected := selector.cache.entries[key]; selected.binding.sequence <= binding.sequence || selected.expiresAt.Before(expiresAt) {
		t.Fatalf("cached selection did not advance selection order while preserving expiry: %+v", selected)
	}

	// Once the 5m cache is gone the binding is released and the fallback picks again.
	selector.cache.mu.Lock()
	entry := selector.cache.entries[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	selector.cache.entries[key] = entry
	selector.cache.mu.Unlock()
	second, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, auths)
	if err != nil {
		t.Fatalf("second Pick() error = %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("expired binding was reused: %s", second.ID)
	}

	// A 1h-cached turn extends the binding to the 1h lifetime.
	opts.OriginalRequest = []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	before = time.Now()
	if _, err = selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, auths); err != nil {
		t.Fatalf("third Pick() error = %v", err)
	}
	want = claudePromptCacheTTL1h + promptCacheTTLGrace
	if got := selector.cache.entries[key].expiresAt.Sub(before); got < want || got > want+time.Minute {
		t.Fatalf("1h binding lifetime = %v, want about %v", got, want)
	}
}

func selectorCacheKeyFor(t *testing.T, selector *SessionAffinitySelector, authID string) string {
	t.Helper()
	selector.cache.mu.RLock()
	defer selector.cache.mu.RUnlock()
	for key, entry := range selector.cache.entries {
		if entry.authID == authID {
			return key
		}
	}
	t.Fatalf("no binding for auth %s", authID)
	return ""
}
