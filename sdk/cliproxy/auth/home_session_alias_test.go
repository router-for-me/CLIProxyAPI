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
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
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

func TestHomeDispatchSessionIDsExtractsParentSessionID(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1h"},
	})

	// 1. Root Claude session
	rootOpts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-1"}},
	}
	sessionID, parentID := manager.homeDispatchSessionIDs(rootOpts)
	if sessionID != "claude:claude-root-1" || parentID != "" {
		t.Fatalf("root session = (%q, %q), want (claude:claude-root-1, \"\")", sessionID, parentID)
	}

	// 2. Subagent Claude session
	subOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-1"},
			"X-Claude-Code-Agent-Id":   []string{"sub-checker"},
		},
	}
	subSessionID, subParentID := manager.homeDispatchSessionIDs(subOpts)
	if subSessionID != "claude:claude-root-1:agent:sub-checker" || subParentID != "claude:claude-root-1" {
		t.Fatalf("subagent session = (%q, %q), want (claude:claude-root-1:agent:sub-checker, claude:claude-root-1)", subSessionID, subParentID)
	}

	// 3. Pi slot session with parent
	piSubOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Slot-Session-Id":   []string{"pi-slot-worker-1"},
			"X-Parent-Session-ID": []string{"pi-slot-main-0"},
		},
	}
	piSessionID, piParentID := manager.homeDispatchSessionIDs(piSubOpts)
	if piSessionID != "slot:pi-slot-worker-1" || piParentID != "slot:pi-slot-main-0" {
		t.Fatalf("pi subagent session = (%q, %q), want (slot:pi-slot-worker-1, slot:pi-slot-main-0)", piSessionID, piParentID)
	}

	// 4. LCP-derived session in metadata
	lcpOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:abc12345",
		},
	}
	lcpSessionID, lcpParentID := manager.homeDispatchSessionIDs(lcpOpts)
	if lcpSessionID != "lcp:v1:abc12345" || lcpParentID != "" {
		t.Fatalf("lcp session = (%q, %q), want (lcp:v1:abc12345, \"\")", lcpSessionID, lcpParentID)
	}
}

type hierarchyCaptureDispatcher struct {
	mu         sync.Mutex
	sessionIDs []string
	parentIDs  []string
}

func (*hierarchyCaptureDispatcher) HeartbeatOK() bool { return true }

func (d *hierarchyCaptureDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return nil, fmt.Errorf("unexpected fallback to legacy RPopAuth")
}

func (d *hierarchyCaptureDispatcher) RPopAuthWithSessionHierarchy(_ context.Context, _ string, sessionID string, parentSessionID string, _ http.Header, _ int, _ string, _ *int, _ []string, _ string) ([]byte, error) {
	d.mu.Lock()
	d.sessionIDs = append(d.sessionIDs, sessionID)
	d.parentIDs = append(d.parentIDs, parentSessionID)
	d.mu.Unlock()
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "hierarchy-auth",
		Provider: "hierarchy-provider",
		Status:   StatusActive,
	}})
}

func (*hierarchyCaptureDispatcher) AbortAmbiguousDispatch() {}

func TestPickNextViaHomePassesParentSessionIDToHierarchyDispatcher(t *testing.T) {
	dispatcher := &hierarchyCaptureDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1h"},
	})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{provider: "hierarchy-provider"})

	subOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"tree-parent-1"},
			"X-Claude-Code-Agent-Id":   []string{"worker-agent"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"test"}]}`),
		Metadata:        map[string]any{},
	}

	auth, _, _, errPick := manager.pickNextViaHome(context.Background(), "test-model", subOpts, nil)
	if errPick != nil {
		t.Fatalf("pickNextViaHome() error = %v", errPick)
	}
	if auth == nil || auth.ID != "hierarchy-auth" {
		t.Fatalf("auth = %#v, want hierarchy-auth", auth)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.sessionIDs) != 1 || dispatcher.sessionIDs[0] != "claude:tree-parent-1:agent:worker-agent" {
		t.Fatalf("dispatched sessionID = %v, want claude:tree-parent-1:agent:worker-agent", dispatcher.sessionIDs)
	}
	if len(dispatcher.parentIDs) != 1 || dispatcher.parentIDs[0] != "claude:tree-parent-1" {
		t.Fatalf("dispatched parentID = %v, want claude:tree-parent-1", dispatcher.parentIDs)
	}
}

func TestPickNextViaHomeNestedRequestSubagentHierarchy(t *testing.T) {
	dispatcher := &hierarchyCaptureDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1h"},
	})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{provider: "hierarchy-provider"})

	nestedSubOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root-home-session",
				"metadata": {
					"agent_id": "worker-subagent"
				}
			}
		}`),
		Metadata: map[string]any{},
	}

	auth, _, _, errPick := manager.pickNextViaHome(context.Background(), "test-model", nestedSubOpts, nil)
	if errPick != nil {
		t.Fatalf("pickNextViaHome() error = %v", errPick)
	}
	if auth == nil || auth.ID != "hierarchy-auth" {
		t.Fatalf("auth = %#v, want hierarchy-auth", auth)
	}

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if len(dispatcher.sessionIDs) != 1 || dispatcher.sessionIDs[0] != "session:root-home-session:agent:worker-subagent" {
		t.Fatalf("dispatched sessionID = %v, want session:root-home-session:agent:worker-subagent", dispatcher.sessionIDs)
	}
	if len(dispatcher.parentIDs) != 1 || dispatcher.parentIDs[0] != "session:root-home-session" {
		t.Fatalf("dispatched parentID = %v, want session:root-home-session", dispatcher.parentIDs)
	}
}

func TestHomeDispatchSessionIDsExtractsParentFromHeaderPlusBody(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1h"},
	})

	// 1. Header session + Body parent_session_id
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"child-session-001"},
		},
		OriginalRequest: []byte(`{"parent_session_id":"parent-session-999"}`),
	}
	sessionID, parentID := manager.homeDispatchSessionIDs(opts)
	if sessionID != "claude:child-session-001" {
		t.Fatalf("sessionID = %q, want claude:child-session-001", sessionID)
	}
	if parentID != "claude:parent-session-999" {
		t.Fatalf("parentID = %q, want claude:parent-session-999", parentID)
	}

	// 2. Nested Claude metadata.user_id with parent_session_id
	nestedOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"metadata": {
				"user_id": "{\"session_id\":\"child-session-002\",\"parent_session_id\":\"parent-session-888\",\"agent_id\":\"sub-agent-1\"}"
			}
		}`),
	}
	nestedSessionID, nestedParentID := manager.homeDispatchSessionIDs(nestedOpts)
	if nestedSessionID != "claude:child-session-002:agent:sub-agent-1" {
		t.Fatalf("nestedSessionID = %q, want claude:child-session-002:agent:sub-agent-1", nestedSessionID)
	}
	if nestedParentID != "claude:parent-session-888" {
		t.Fatalf("nestedParentID = %q, want claude:parent-session-888", nestedParentID)
	}

	// 3. LCP metadata prioritization over msg-hash fallback
	lcpOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello world"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:canonical-hash-xyz",
		},
	}
	lcpSessionID, lcpParentID := manager.homeDispatchSessionIDs(lcpOpts)
	if lcpSessionID != "lcp:v1:canonical-hash-xyz" {
		t.Fatalf("lcpSessionID = %q, want lcp:v1:canonical-hash-xyz (not msg-hash)", lcpSessionID)
	}
	if lcpParentID != "" {
		t.Fatalf("lcpParentID = %q, want empty", lcpParentID)
	}

	// 3b. LCP with metadata parent
	lcpForkOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:fork-child-xyz",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "lcp:v1:parent-root-abc",
		},
	}
	lcpForkSessionID, lcpForkParentID := manager.homeDispatchSessionIDs(lcpForkOpts)
	if lcpForkSessionID != "lcp:v1:fork-child-xyz" || lcpForkParentID != "lcp:v1:parent-root-abc" {
		t.Fatalf("lcpFork = (%q, %q), want (lcp:v1:fork-child-xyz, lcp:v1:parent-root-abc)", lcpForkSessionID, lcpForkParentID)
	}

	// 3c. Self-referential parent is suppressed
	lcpSelfParentOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:same-session",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "lcp:v1:same-session",
		},
	}
	selfSessionID, selfParentID := manager.homeDispatchSessionIDs(lcpSelfParentOpts)
	if selfSessionID != "lcp:v1:same-session" || selfParentID != "" {
		t.Fatalf("self-referential parent = (%q, %q), want (%q, empty)", selfSessionID, selfParentID, "lcp:v1:same-session")
	}

	// 4. Antigravity hierarchy
	agyOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Http-Session-Id":   []string{"agy-child-101"},
			"X-Parent-Session-ID": []string{"agy-parent-100"},
		},
	}
	agySessionID, agyParentID := manager.homeDispatchSessionIDs(agyOpts)
	if agySessionID != "agy:agy-child-101" {
		t.Fatalf("agySessionID = %q, want agy:agy-child-101", agySessionID)
	}
	if agyParentID != "agy:agy-parent-100" {
		t.Fatalf("agyParentID = %q, want agy:agy-parent-100", agyParentID)
	}

	// 5. Gemini cachedContent hierarchy
	geminiOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"cachedContent":"cache-child-201","parent_session_id":"cache-parent-200"}`),
	}
	geminiSessionID, geminiParentID := manager.homeDispatchSessionIDs(geminiOpts)
	if geminiSessionID != "geminicache:cache-child-201" {
		t.Fatalf("geminiSessionID = %q, want geminicache:cache-child-201", geminiSessionID)
	}
	if geminiParentID != "geminicache:cache-parent-200" {
		t.Fatalf("geminiParentID = %q, want geminicache:cache-parent-200", geminiParentID)
	}
}

func TestHomeDispatchSessionIDsNestedRequestSubagent(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinityTTL: "1h"},
	})

	// 1. Nested request with sessionId and agent_id
	nestedAgentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root",
				"metadata": {
					"agent_id": "worker"
				}
			}
		}`),
	}
	sessionID, parentID := manager.homeDispatchSessionIDs(nestedAgentOpts)
	if sessionID != "session:root:agent:worker" {
		t.Fatalf("sessionID = %q, want session:root:agent:worker", sessionID)
	}
	if parentID != "session:root" {
		t.Fatalf("parentID = %q, want session:root", parentID)
	}

	// 2. Nested request with sessionId and subagent_id
	nestedSubagentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root",
				"metadata": {
					"subagent_id": "worker-sub"
				}
			}
		}`),
	}
	subSessionID, subParentID := manager.homeDispatchSessionIDs(nestedSubagentOpts)
	if subSessionID != "session:root:agent:worker-sub" {
		t.Fatalf("subSessionID = %q, want session:root:agent:worker-sub", subSessionID)
	}
	if subParentID != "session:root" {
		t.Fatalf("subParentID = %q, want session:root", subParentID)
	}
}
