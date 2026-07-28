package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

func newIdentityTestSelector(t *testing.T) *SessionAffinitySelector {
	t.Helper()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	t.Cleanup(selector.Stop)
	return selector
}

func callerOptions(callerScope string, headers http.Header) cliproxyexecutor.Options {
	opts := cliproxyexecutor.Options{Headers: headers}
	if callerScope != "" {
		opts.Metadata = map[string]any{cliproxyexecutor.CallerScopeMetadataKey: cliproxysession.CallerScope(callerScope)}
	}
	return opts
}

// Two downstream API keys may legitimately reuse the same client session ID.
// They must not share one upstream binding.
func TestSessionAffinityIsolatesCallersSharingASessionID(t *testing.T) {
	selector := newIdentityTestSelector(t)
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	headers := http.Header{"X-Session-Id": {"ses_shared"}}

	firstCaller, err := selector.Pick(context.Background(), "openai", "gpt-test", callerOptions("api-key-one", headers), auths)
	if err != nil {
		t.Fatalf("first caller Pick() error = %v", err)
	}
	secondCaller, err := selector.Pick(context.Background(), "openai", "gpt-test", callerOptions("api-key-two", headers), auths)
	if err != nil {
		t.Fatalf("second caller Pick() error = %v", err)
	}
	if secondCaller.ID == firstCaller.ID {
		t.Fatalf("both callers bound to %q; the shared raw session ID leaked across callers", firstCaller.ID)
	}

	repeat, err := selector.Pick(context.Background(), "openai", "gpt-test", callerOptions("api-key-one", headers), auths)
	if err != nil {
		t.Fatalf("repeat Pick() error = %v", err)
	}
	if repeat.ID != firstCaller.ID {
		t.Fatalf("same caller re-bound to %q, want %q", repeat.ID, firstCaller.ID)
	}
}

func TestSessionAffinityKeepsUnauthenticatedCallersOnOneBinding(t *testing.T) {
	selector := newIdentityTestSelector(t)
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	headers := http.Header{"X-Session-Id": {"ses_anonymous"}}

	first, err := selector.Pick(context.Background(), "openai", "gpt-test", callerOptions("", headers), auths)
	if err != nil {
		t.Fatalf("first Pick() error = %v", err)
	}
	second, err := selector.Pick(context.Background(), "openai", "gpt-test", callerOptions("", headers), auths)
	if err != nil {
		t.Fatalf("second Pick() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("binding changed from %q to %q without a caller scope", first.ID, second.ID)
	}
}

// The identity resolved by Enrich must be the one the selector routes on,
// so local routing, Home dispatch and usage never disagree.
func TestSessionAffinityReusesEnrichedIdentity(t *testing.T) {
	selector := newIdentityTestSelector(t)
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	req := cliproxyexecutor.Request{Payload: []byte(`{"prompt_cache_key":"cache-1","conversation":{"id":"conv_1"}}`)}
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"cc-1"}}}
	_, opts = cliproxysession.Enrich(req, opts)
	opts.OriginalRequest = req.Payload

	picked, err := selector.Pick(context.Background(), "claude", "claude-test", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	if _, ok := cliproxysession.FromMetadata(opts.Metadata); !ok {
		t.Fatal("options metadata is missing the session identity")
	}
	wantKey := sessionCacheKey("", "claude", "claude-test", "claude:cc-1")
	bound, ok := selector.cache.Get(wantKey)
	if !ok {
		t.Fatalf("no binding stored under %q", wantKey)
	}
	if bound != picked.ID {
		t.Fatalf("binding = %q, want %q", bound, picked.ID)
	}
}

func TestSessionIdentityFromOptionsFallsBackToExtraction(t *testing.T) {
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": {"ses_direct"}}}

	if got := sessionIdentityFromOptions(opts).ID; got != "header:ses_direct" {
		t.Fatalf("ID = %q, want header:ses_direct", got)
	}
}

func TestSessionCacheKeyRoundTripsOpaqueSessionIDs(t *testing.T) {
	tests := []string{"pck:cache-1", "conv:a::pck:b", "claude:11111111-1111-4111-8111-111111111111", ""}

	for _, sessionID := range tests {
		key := sessionCacheKey("scope", "openai", "gpt-test", sessionID)
		if got := sessionCacheKeySessionID(key); got != sessionID {
			t.Fatalf("sessionCacheKeySessionID(%q) = %q, want %q", key, got, sessionID)
		}
	}
}

// An opaque conversation ID may itself contain the prompt-cache marker.
// It must not be mistaken for a prompt cache alias.
func TestIsLocalPromptCacheSessionAliasUsesTheSessionSegment(t *testing.T) {
	promptKey := sessionCacheKey("", "openai", "gpt-test", "pck:cache-1")
	if !isLocalPromptCacheSessionAlias(promptKey) {
		t.Fatalf("%q was not detected as a prompt cache alias", promptKey)
	}
	opaqueConversation := sessionCacheKey("", "openai", "gpt-test", "conv:a::pck:b")
	if isLocalPromptCacheSessionAlias(opaqueConversation) {
		t.Fatalf("%q was misdetected as a prompt cache alias", opaqueConversation)
	}
}

func TestSessionCacheEnforcesCapacityBoundWithLRUEviction(t *testing.T) {
	const limit = 8
	cache := NewSessionCacheWithLimit(time.Minute, limit)
	defer cache.Stop()

	for index := 0; index < limit*4; index++ {
		cache.Set(fmt.Sprintf("session-%03d", index), "auth-a")
	}

	stats := cache.Stats()
	if stats.Entries > limit {
		t.Fatalf("entries = %d, want at most %d", stats.Entries, limit)
	}
	if stats.Evictions == 0 {
		t.Fatal("evictions = 0, want the capacity bound to have evicted older sessions")
	}
	if _, ok := cache.Get("session-000"); ok {
		t.Fatal("the oldest session survived the capacity bound")
	}
	if _, ok := cache.Get(fmt.Sprintf("session-%03d", limit*4-1)); !ok {
		t.Fatal("the newest session was evicted")
	}
}

func TestSessionCacheCapacityBoundKeepsRefreshedSessions(t *testing.T) {
	const limit = 4
	cache := NewSessionCacheWithLimit(time.Minute, limit)
	defer cache.Stop()

	cache.Set("hot-session", "auth-a")
	for index := 0; index < limit*3; index++ {
		cache.Set(fmt.Sprintf("cold-%03d", index), "auth-b")
		if _, ok := cache.GetAndRefresh("hot-session"); !ok {
			t.Fatalf("hot session was evicted after %d cold sessions despite being refreshed", index+1)
		}
	}
}

func TestSessionCacheCapacityBoundDropsWholeAliasGroups(t *testing.T) {
	const limit = 4
	cache := NewSessionCacheWithLimit(time.Minute, limit)
	defer cache.Stop()

	cache.SetAliases("auth-a", "pck:first", "conv:first")
	for index := 0; index < limit*2; index++ {
		cache.SetAliases("auth-b", fmt.Sprintf("pck:filler-%03d", index), fmt.Sprintf("conv:filler-%03d", index))
	}

	if _, ok := cache.Get("pck:first"); ok {
		t.Fatal("evicted group left its primary identifier bound")
	}
	if _, ok := cache.Get("conv:first"); ok {
		t.Fatal("evicted group left its alias bound")
	}
	if stats := cache.Stats(); stats.Entries > limit {
		t.Fatalf("entries = %d, want at most %d", stats.Entries, limit)
	}
}

func TestSessionCacheInvalidateKeepsSiblingAliasesBound(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	cache.SetAliases("auth-a", "pck:cache-1", "conv:conv-1")
	cache.Invalidate("pck:cache-1")

	if _, ok := cache.Get("pck:cache-1"); ok {
		t.Fatal("invalidated identifier is still bound")
	}
	authID, ok := cache.Get("conv:conv-1")
	if !ok {
		t.Fatal("sibling alias lost its binding")
	}
	if authID != "auth-a" {
		t.Fatalf("sibling alias bound to %q, want auth-a", authID)
	}
}

func TestSessionCacheInvalidateAuthClearsEveryAlias(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()

	cache.SetAliases("auth-a", "pck:cache-1", "conv:conv-1")
	cache.Set("session-b", "auth-b")
	cache.InvalidateAuth("auth-a")

	for _, alias := range []string{"pck:cache-1", "conv:conv-1"} {
		if _, ok := cache.Get(alias); ok {
			t.Fatalf("%q survived InvalidateAuth", alias)
		}
	}
	if _, ok := cache.Get("session-b"); !ok {
		t.Fatal("an unrelated auth binding was cleared")
	}
	if stats := cache.Stats(); stats.Groups != 1 {
		t.Fatalf("groups = %d, want 1", stats.Groups)
	}
}
