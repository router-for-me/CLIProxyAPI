package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type sessionAliasCaptureDispatcher struct {
	mu       sync.Mutex
	sessions []string
}

func (*sessionAliasCaptureDispatcher) HeartbeatOK() bool { return true }

func (d *sessionAliasCaptureDispatcher) RPopAuth(_ context.Context, _ string, sessionID string, _ http.Header, _ int) ([]byte, error) {
	d.mu.Lock()
	d.sessions = append(d.sessions, sessionID)
	d.mu.Unlock()
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-session-alias-auth",
		Provider: "home-session-alias",
		Status:   StatusActive,
	}})
}

func (*sessionAliasCaptureDispatcher) AbortAmbiguousDispatch() {}

func (d *sessionAliasCaptureDispatcher) sessionIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sessions...)
}

func TestHomeSessionAliasCacheClearsWhenConfiguredTTLChanges(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1h"},
	})
	combined := cliproxyexecutor.Options{OriginalRequest: []byte(
		`{"conversation":{"id":"ttl-conversation"},"prompt_cache_key":"ttl-prompt"}`,
	)}
	conversationOnly := cliproxyexecutor.Options{OriginalRequest: []byte(
		`{"conversation":{"id":"ttl-conversation"}}`,
	)}
	if got := manager.homeDispatchSessionID(combined); got != "pck:ttl-prompt" {
		t.Fatalf("combined canonical = %q, want pck:ttl-prompt", got)
	}
	if got := manager.homeDispatchSessionID(conversationOnly); got != "pck:ttl-prompt" {
		t.Fatalf("conversation canonical before reload = %q, want existing prompt canonical", got)
	}

	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1m"},
	})
	if got := manager.homeDispatchSessionID(conversationOnly); got != "conv:ttl-conversation" {
		t.Fatalf("conversation canonical after TTL change = %q, want cleared alias cache", got)
	}
}

func TestHomeDispatchCanonicalizesPromptCacheAndConversationAliases(t *testing.T) {
	tests := []struct {
		name     string
		payloads []string
		want     string
	}{
		{
			name: "conversation then combined then prompt cache",
			payloads: []string{
				`{"conversation":{"id":"conversation-session"}}`,
				`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`,
				`{"prompt_cache_key":"shared-cache-bucket"}`,
			},
			want: "conv:conversation-session",
		},
		{
			name: "prompt cache then combined then conversation",
			payloads: []string{
				`{"prompt_cache_key":"shared-cache-bucket"}`,
				`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`,
				`{"conversation":{"id":"conversation-session"}}`,
			},
			want: "pck:shared-cache-bucket",
		},
		{
			name: "combined request establishes prompt cache primary",
			payloads: []string{
				`{"conversation":{"id":"conversation-session"},"prompt_cache_key":"shared-cache-bucket"}`,
				`{"conversation":{"id":"conversation-session"}}`,
				`{"prompt_cache_key":"shared-cache-bucket"}`,
			},
			want: "pck:shared-cache-bucket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := &sessionAliasCaptureDispatcher{}
			manager := newHomeSelectionTestManager(t, dispatcher)
			manager.RegisterExecutor(schedulerTestExecutor{provider: "home-session-alias"})

			for _, payload := range tt.payloads {
				selection, errSelection := manager.pickHomeDispatchSelection(context.Background(), "gpt-test", cliproxyexecutor.Options{
					OriginalRequest: []byte(payload),
				})
				if errSelection != nil {
					t.Fatalf("pickHomeDispatchSelection() error = %v", errSelection)
				}
				selection.End("test_complete")
			}

			got := dispatcher.sessionIDs()
			if len(got) != len(tt.payloads) {
				t.Fatalf("Home session IDs = %#v, want %d entries", got, len(tt.payloads))
			}
			for index, sessionID := range got {
				if sessionID != tt.want {
					t.Fatalf("Home session ID[%d] = %q, want %q; all=%#v", index, sessionID, tt.want, got)
				}
			}
		})
	}
}

func TestHomeSessionAliasCachePrimaryAccessRefreshesWholeAliasGroup(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const primary = "pck:shared-cache-bucket"
	const fallback = "conv:conversation-session"

	if got := cache.canonical(primary, fallback, time.Minute, now); got != primary {
		t.Fatalf("initial canonical = %q, want %q", got, primary)
	}
	cache.mu.Lock()
	fallbackEntry := cache.entries[fallback]
	fallbackEntry.expiresAt = now.Add(-time.Second)
	cache.entries[fallback] = fallbackEntry
	cache.mu.Unlock()

	if got := cache.canonical(primary, "", time.Minute, now.Add(10*time.Second)); got != primary {
		t.Fatalf("primary-only canonical = %q, want %q", got, primary)
	}
	if got := cache.canonical(fallback, "", time.Minute, now.Add(20*time.Second)); got != primary {
		t.Fatalf("fallback canonical after active primary traffic = %q, want %q", got, primary)
	}
}

func TestHomeSessionAliasCacheSharedPromptKeyPreservesConversationAliases(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const promptKey = "pck:shared-cache-bucket"
	const conversationA = "conv:conversation-a"
	const conversationB = "conv:conversation-b"

	if got := cache.canonical(promptKey, conversationA, time.Minute, now); got != promptKey {
		t.Fatalf("conversation A canonical = %q, want %q", got, promptKey)
	}
	if got := cache.canonical(promptKey, conversationB, time.Minute, now.Add(time.Second)); got != promptKey {
		t.Fatalf("conversation B canonical = %q, want %q", got, promptKey)
	}
	if got := cache.canonical(conversationA, "", time.Minute, now.Add(2*time.Second)); got != promptKey {
		t.Fatalf("conversation A alias canonical = %q, want %q", got, promptKey)
	}
	if got := cache.canonical(conversationB, "", time.Minute, now.Add(3*time.Second)); got != promptKey {
		t.Fatalf("conversation B alias canonical = %q, want %q", got, promptKey)
	}
}

func TestHomeSessionAliasCacheConversationIDContainingPromptMarkerRemainsStable(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const promptKey = "pck:shared-cache-bucket"
	const conversation = "conv:a::pck:b"
	if got := cache.canonical(promptKey, conversation, time.Minute, now); got != promptKey {
		t.Fatalf("combined canonical = %q, want %q", got, promptKey)
	}
	if got := cache.canonical(conversation, "", time.Minute, now.Add(time.Second)); got != promptKey {
		t.Fatalf("conversation-only canonical = %q, want %q", got, promptKey)
	}
}

func TestHomeSessionAliasCacheSharedPromptKeyCapsStableAliasesByRecency(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const promptKey = "pck:shared-cache-bucket"
	for index := 0; index < 128; index++ {
		conversation := fmt.Sprintf("conv:conversation-%03d", index)
		cache.canonical(promptKey, conversation, time.Minute, now.Add(time.Duration(index)*time.Second))
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) > 65 {
		t.Fatalf("home alias entries = %d, want one prompt key plus at most 64 stable aliases", len(cache.entries))
	}
	if _, ok := cache.entries["conv:conversation-127"]; !ok {
		t.Fatal("newest Home conversation alias was not retained")
	}
	if _, ok := cache.entries["conv:conversation-000"]; ok {
		t.Fatal("oldest Home conversation alias was retained after stable-alias cap")
	}
}

func TestHomeSessionAliasCacheRotatingPrimaryEvictsObsoleteAliases(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const fallback = "conv:conversation-session"
	wantCanonical := "pck:cache-00"
	for index := 0; index < 16; index++ {
		primary := fmt.Sprintf("pck:cache-%02d", index)
		if got := cache.canonical(primary, fallback, time.Minute, now.Add(time.Duration(index)*time.Second)); got != wantCanonical {
			t.Fatalf("canonical at index %d = %q, want %q", index, got, wantCanonical)
		}
	}
	latest := "pck:cache-15"

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 2 {
		t.Fatalf("home alias entries = %d, want only latest primary and fallback", len(cache.entries))
	}
	if _, ok := cache.entries[latest]; !ok {
		t.Fatalf("latest primary %q was not retained", latest)
	}
	if _, ok := cache.entries[fallback]; !ok {
		t.Fatalf("fallback %q was not retained", fallback)
	}
	if _, ok := cache.entries[wantCanonical]; ok {
		t.Fatalf("obsolete canonical alias %q was retained as a lookup key", wantCanonical)
	}
	if aliases := cache.entries[fallback].aliases; len(aliases) != 2 {
		t.Fatalf("home fallback alias group = %#v, want exactly two active identifiers", aliases)
	}
}

func TestHomeSessionAliasCacheDoesNotReconnectCompactedCanonicalAlias(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const obsoletePrompt = "pck:cache-a"
	const currentPrompt = "pck:cache-b"
	const conversation = "conv:conversation-session"

	if got := cache.canonical(obsoletePrompt, conversation, time.Minute, now); got != obsoletePrompt {
		t.Fatalf("initial canonical = %q, want %q", got, obsoletePrompt)
	}
	if got := cache.canonical(currentPrompt, conversation, time.Minute, now.Add(time.Second)); got != obsoletePrompt {
		t.Fatalf("rotated canonical = %q, want stable %q", got, obsoletePrompt)
	}

	cache.mu.Lock()
	if _, ok := cache.entries[obsoletePrompt]; ok {
		cache.mu.Unlock()
		t.Fatalf("obsolete prompt alias %q remained live after compaction", obsoletePrompt)
	}
	cache.mu.Unlock()

	if got := cache.canonical(obsoletePrompt, "", time.Minute, now.Add(2*time.Second)); got != obsoletePrompt {
		t.Fatalf("obsolete prompt canonical = %q, want standalone %q", got, obsoletePrompt)
	}

	cache.mu.Lock()
	conversationEntry, conversationOK := cache.entries[conversation]
	currentEntry, currentOK := cache.entries[currentPrompt]
	_, obsoleteOK := cache.entries[obsoletePrompt]
	cache.mu.Unlock()
	if obsoleteOK {
		t.Fatalf("stale canonical %q replaced the live group", obsoletePrompt)
	}
	if !conversationOK || !currentOK || !sameHomeSessionAliasGroup(conversationEntry, currentEntry) {
		t.Fatalf("live aliases were disconnected: conversation=%#v current=%#v", conversationEntry, currentEntry)
	}
	if got := cache.canonical(conversation, "", time.Minute, now.Add(3*time.Second)); got != obsoletePrompt {
		t.Fatalf("live conversation canonical = %q, want %q", got, obsoletePrompt)
	}
}

func TestHomeSessionAliasCacheSoftLimitEvictsOldestTouchedGroup(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	const oldest = "session:zzzz-oldest"
	cache.canonical(oldest, "", time.Hour, now)
	for index := 0; index < homeSessionAliasSoftLimit; index++ {
		cache.canonical(fmt.Sprintf("session:%05d", index), "", time.Hour, now)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) > homeSessionAliasSoftLimit {
		t.Fatalf("alias entries = %d, want at most %d", len(cache.entries), homeSessionAliasSoftLimit)
	}
	if _, ok := cache.entries[oldest]; ok {
		t.Fatalf("oldest insertion %q remained after incremental eviction", oldest)
	}
	if _, ok := cache.entries["session:00000"]; !ok {
		t.Fatal("newer insertion was evicted instead of the oldest group")
	}
}

func TestHomeSessionAliasCacheEnforcesSoftLimit(t *testing.T) {
	var cache homeSessionAliasCache
	now := time.Now()
	for i := 0; i < homeSessionAliasSoftLimit+32; i++ {
		cache.canonical(fmt.Sprintf("session:%05d", i), "", time.Hour, now.Add(time.Duration(i)*time.Nanosecond))
	}

	cache.mu.Lock()
	entryCount := len(cache.entries)
	_, oldestPresent := cache.entries["session:00000"]
	_, newestPresent := cache.entries[fmt.Sprintf("session:%05d", homeSessionAliasSoftLimit+31)]
	cache.mu.Unlock()
	if entryCount > homeSessionAliasSoftLimit {
		t.Fatalf("alias entries = %d, want at most %d", entryCount, homeSessionAliasSoftLimit)
	}
	if oldestPresent {
		t.Fatal("oldest alias remained after enforcing soft limit")
	}
	if !newestPresent {
		t.Fatal("newest alias was evicted while enforcing soft limit")
	}
}
