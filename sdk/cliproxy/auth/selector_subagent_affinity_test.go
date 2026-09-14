package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestSessionAffinityAntigravitySubagentInheritsParentByDefault reproduces issue #5417:
// Subagents across all providers (including Antigravity and Gemini) should inherit
// the parent session's credential by default to maximize prompt and KV cache reuse.
func TestSessionAffinityAntigravitySubagentInheritsParentByDefault(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-ag-1", Provider: "antigravity"},
		{ID: "auth-ag-2", Provider: "antigravity"},
	}

	// 1. Parent request binds to auth-ag-1.
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-100"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-ag-1" {
		t.Fatalf("parent auth = %q, want auth-ag-1", parentAuth.ID)
	}

	// 2. Subagent request carries child agent ID and should inherit parent auth-ag-1 by default.
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID != parentAuth.ID {
		t.Fatalf("subagent did not inherit parent auth %q, got %q", parentAuth.ID, subagentAuth.ID)
	}

	// 3. Subsequent turn of subagent must stay sticky to the inherited auth.
	subagentTurn2Auth, errSub2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagentOpts, auths)
	if errSub2 != nil {
		t.Fatalf("subagent turn 2 Pick() error = %v", errSub2)
	}
	if subagentTurn2Auth.ID != "auth-ag-1" {
		t.Fatalf("subagent turn 2 did not stay sticky: got %q, want auth-ag-1", subagentTurn2Auth.ID)
	}
}

// TestSessionAffinityGeminiSubagentInheritsParentWhenExplicitlyTrue verifies that when
// SubagentAffinity is explicitly set to true, Gemini subagents inherit the parent's auth.
func TestSessionAffinityGeminiSubagentInheritsParentWhenExplicitlyTrue(t *testing.T) {
	t.Parallel()

	enabled := true
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: &enabled,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-gem-1", Provider: "gemini"},
		{ID: "auth-gem-2", Provider: "gemini"},
	}

	// 1. Parent request binds to auth-gem-1.
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"gem-root-200"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent gemini task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-gem-1" {
		t.Fatalf("parent auth = %q, want auth-gem-1", parentAuth.ID)
	}

	// 2. Subagent request inherits parent auth-gem-1.
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"gem-root-200"},
			"X-Claude-Code-Agent-Id":   []string{"gem-sub-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent gemini task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID != parentAuth.ID {
		t.Fatalf("subagent did not inherit parent auth %q, got %q", parentAuth.ID, subagentAuth.ID)
	}
}

// TestSessionAffinitySubagentIsolatesWhenExplicitlyFalse verifies that when
// SubagentAffinity is false, subagents do not inherit parent credentials and balance across pool.
func TestSessionAffinitySubagentIsolatesWhenExplicitlyFalse(t *testing.T) {
	t.Parallel()

	disabled := false
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: &disabled,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "claude"},
		{ID: "auth-2", Provider: "claude"},
	}

	// 1. Parent request binds to auth-1.
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"sess-iso-300"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-1" {
		t.Fatalf("parent auth = %q, want auth-1", parentAuth.ID)
	}

	// 2. Subagent request should NOT inherit auth-1; it picks auth-2 via fallback round-robin.
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"sess-iso-300"},
			"X-Claude-Code-Agent-Id":   []string{"sub-iso-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID == parentAuth.ID {
		t.Fatalf("subagent incorrectly inherited parent auth %q when SubagentAffinity is false", parentAuth.ID)
	}
	if subagentAuth.ID != "auth-2" {
		t.Fatalf("subagent auth = %q, want auth-2", subagentAuth.ID)
	}
}

// TestSessionAffinitySubagentFailureIsolation verifies that failure on a subagent session
// only deletes the subagent's own binding, leaving the parent's binding intact.
func TestSessionAffinitySubagentFailureIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "antigravity"},
		{ID: "auth-2", Provider: "antigravity"},
	}

	// 1. Parent request binds to auth-1.
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"sess-fail-400"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-1" {
		t.Fatalf("parent auth = %q, want auth-1", parentAuth.ID)
	}

	// Parent cache key must be bound to auth-1.
	parentCacheKey := "antigravity::claude:sess-fail-400::gemini-3.7-flash-high"
	if bound, ok := selector.cache.Get(parentCacheKey); !ok || bound != "auth-1" {
		t.Fatalf("parent cache binding = (%q, %v), want (auth-1, true)", bound, ok)
	}

	// 2. Subagent inherits auth-1 and binds its own cache key.
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"sess-fail-400"},
			"X-Claude-Code-Agent-Id":   []string{"sub-fail-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID != "auth-1" {
		t.Fatalf("subagent auth = %q, want auth-1", subagentAuth.ID)
	}

	subagentCacheKey := "antigravity::claude:sess-fail-400:agent:sub-fail-001::gemini-3.7-flash-high"
	if bound, ok := selector.cache.Get(subagentCacheKey); !ok || bound != "auth-1" {
		t.Fatalf("subagent cache binding = (%q, %v), want (auth-1, true)", bound, ok)
	}

	// 3. Subagent fails with an upstream error via OnResult.
	selector.OnResult(Result{
		AuthID:  "auth-1",
		Success: false,
		Options: subagentOpts,
	})

	// 4. Subagent's cache key must be cleared.
	if bound, ok := selector.cache.Get(subagentCacheKey); ok {
		t.Fatalf("subagent cache binding was not cleared after failure: got %q", bound)
	}

	// 5. Parent session binding in cache MUST still remain intact on auth-1.
	if bound, ok := selector.cache.Get(parentCacheKey); !ok || bound != "auth-1" {
		t.Fatalf("parent binding was damaged by subagent failure: got (%q, %v), want (auth-1, true)", bound, ok)
	}

	// 6. Parent request turn 2 must still hit auth-1.
	parentTurn2Auth, errParent2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent2 != nil {
		t.Fatalf("parent turn 2 Pick() error = %v", errParent2)
	}
	if parentTurn2Auth.ID != "auth-1" {
		t.Fatalf("parent turn 2 auth = %q, want auth-1", parentTurn2Auth.ID)
	}
}

// TestSessionAffinitySubagentAliasIsolation verifies that subagents bind only their
// own cacheKey and never register an alias that overrides the parent's fallbackKey.
func TestSessionAffinitySubagentAliasIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "antigravity"},
		{ID: "auth-2", Provider: "antigravity"},
	}

	// 1. Parent binds auth-1.
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"sess-alias-500"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	_, errParent := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. Subagent picks and inherits auth-1.
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"sess-alias-500"},
			"X-Claude-Code-Agent-Id":   []string{"sub-alias-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"sub task"}]}`),
		Metadata:        map[string]any{},
	}
	_, errSub := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}

	// 3. Invalidate subagent's auth by calling OnResult(Success: false).
	selector.OnResult(Result{
		AuthID:  "auth-1",
		Success: false,
		Options: subagentOpts,
	})

	// Parent fallbackKey in cache MUST still resolve to auth-1, proving subagent did not alias it.
	parentCacheKey := "antigravity::claude:sess-alias-500::gemini-3.7-flash-high"
	val, ok := selector.cache.Get(parentCacheKey)
	if !ok || val != "auth-1" {
		t.Fatalf("parent cache key %s = (%q, %v), want (auth-1, true)", parentCacheKey, val, ok)
	}
}
