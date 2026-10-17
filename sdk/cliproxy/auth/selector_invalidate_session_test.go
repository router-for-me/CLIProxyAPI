package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// scriptedSelector returns a caller-chosen auth and counts how often the
// affinity selector actually fell through to it, so a test can distinguish a
// cache hit from a fresh selection instead of guessing from round-robin state.
type scriptedSelector struct {
	next  string
	calls int
}

func (s *scriptedSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.calls++
	for _, auth := range auths {
		if auth != nil && auth.ID == s.next {
			return auth, nil
		}
	}
	return nil, &Error{Code: "auth_not_found", Message: "scripted selector has no candidate " + s.next}
}

func claudeSessionOptions(sessionID string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Headers: http.Header{"X-Claude-Code-Session-Id": []string{sessionID}},
	}
}

// TestSessionAffinitySelector_InvalidateSessionReleasesOnlyThatSession is the
// core contract: the named session is re-selected from the pool on its next
// request while every other session keeps its binding.
func TestSessionAffinitySelector_InvalidateSessionReleasesOnlyThatSession(t *testing.T) {
	t.Parallel()

	fallback := &scriptedSelector{next: "auth-a"}
	selector := NewSessionAffinitySelector(fallback)
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	const (
		movingUUID = "8046da35-c12f-4b6b-8c9e-86ecb162fcee"
		stayUUID   = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	)

	moving, errMoving := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", claudeSessionOptions(movingUUID), auths)
	if errMoving != nil || moving == nil {
		t.Fatalf("Pick() moving session error = %v, auth = %v", errMoving, moving)
	}
	staying, errStaying := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", claudeSessionOptions(stayUUID), auths)
	if errStaying != nil || staying == nil {
		t.Fatalf("Pick() staying session error = %v, auth = %v", errStaying, staying)
	}

	// From here the pool would hand out auth-b, so any later auth-a is a binding.
	fallback.next = "auth-b"
	callsBefore := fallback.calls
	repeat, errRepeat := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", claudeSessionOptions(movingUUID), auths)
	if errRepeat != nil {
		t.Fatalf("Pick() repeat error = %v", errRepeat)
	}
	if repeat.ID != "auth-a" || fallback.calls != callsBefore {
		t.Fatalf("session was not bound before invalidation: auth = %q, fallback calls = %d, want auth-a and no fallback call", repeat.ID, fallback.calls-callsBefore)
	}

	result := selector.InvalidateSession("claude:" + movingUUID)
	if !result.Removed() {
		t.Fatalf("InvalidateSession() removed nothing: %+v", result)
	}
	if result.CacheGroups != 1 {
		t.Fatalf("InvalidateSession() cache groups = %d, want 1 (%+v)", result.CacheGroups, result)
	}

	callsBefore = fallback.calls
	afterMoving, errAfter := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", claudeSessionOptions(movingUUID), auths)
	if errAfter != nil {
		t.Fatalf("Pick() after invalidation error = %v", errAfter)
	}
	if fallback.calls != callsBefore+1 {
		t.Fatalf("invalidated session did not fall through to the pool: fallback calls = %d, want 1", fallback.calls-callsBefore)
	}
	if afterMoving.ID != "auth-b" {
		t.Fatalf("invalidated session resolved to %q, want the freshly selected auth-b", afterMoving.ID)
	}

	callsBefore = fallback.calls
	afterStaying, errStayingAfter := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", claudeSessionOptions(stayUUID), auths)
	if errStayingAfter != nil {
		t.Fatalf("Pick() untouched session error = %v", errStayingAfter)
	}
	if afterStaying.ID != staying.ID || fallback.calls != callsBefore {
		t.Fatalf("untouched session moved from %q to %q (fallback calls = %d)", staying.ID, afterStaying.ID, fallback.calls-callsBefore)
	}
}

// TestSessionAffinitySelector_InvalidateSessionSpansProvidersAndModels covers the
// composite cache key: one logical session owns one entry per provider/model.
func TestSessionAffinitySelector_InvalidateSessionSpansProvidersAndModels(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	const sessionUUID = "8046da35-c12f-4b6b-8c9e-86ecb162fcee"

	for _, target := range []struct{ provider, model string }{
		{"claude", "claude-sonnet-4-5"},
		{"claude", "claude-opus-4-1"},
		{"codex", "gpt-5"},
	} {
		if _, err := selector.Pick(context.Background(), target.provider, target.model, claudeSessionOptions(sessionUUID), auths); err != nil {
			t.Fatalf("Pick(%s/%s) error = %v", target.provider, target.model, err)
		}
	}

	result := selector.InvalidateSession("claude:" + sessionUUID)
	if result.CacheGroups != 3 {
		t.Fatalf("InvalidateSession() cache groups = %d, want 3 (%+v)", result.CacheGroups, result)
	}
	if again := selector.InvalidateSession("claude:" + sessionUUID); again.Removed() {
		t.Fatalf("second InvalidateSession() removed %+v, want nothing", again)
	}
}

// TestSessionAffinitySelector_InvalidateSessionMatchesExactly guards the blast
// radius: a subagent session is its own routing identity and must survive when
// its parent is released, and an unknown key must be a no-op.
func TestSessionAffinitySelector_InvalidateSessionMatchesExactly(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	const parentUUID = "8046da35-c12f-4b6b-8c9e-86ecb162fcee"

	agentOptions := claudeSessionOptions(parentUUID)
	agentOptions.Headers.Set("X-Claude-Code-Agent-Id", "explore-1")
	agent, errAgent := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", agentOptions, auths)
	if errAgent != nil || agent == nil {
		t.Fatalf("Pick() subagent error = %v, auth = %v", errAgent, agent)
	}
	if _, errParent := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", claudeSessionOptions(parentUUID), auths); errParent != nil {
		t.Fatalf("Pick() parent error = %v", errParent)
	}

	if unknown := selector.InvalidateSession("claude:00000000-0000-4000-8000-000000000000"); unknown.Removed() {
		t.Fatalf("InvalidateSession() on an unknown session removed %+v", unknown)
	}

	result := selector.InvalidateSession("claude:" + parentUUID)
	if result.CacheGroups != 1 {
		t.Fatalf("InvalidateSession() cache groups = %d, want 1 (subagent key must not match) (%+v)", result.CacheGroups, result)
	}

	agentOptionsAfter := claudeSessionOptions(parentUUID)
	agentOptionsAfter.Headers.Set("X-Claude-Code-Agent-Id", "explore-1")
	agentAfter, errAgentAfter := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", agentOptionsAfter, auths)
	if errAgentAfter != nil {
		t.Fatalf("Pick() subagent after invalidation error = %v", errAgentAfter)
	}
	if agentAfter.ID != agent.ID {
		t.Fatalf("subagent binding moved from %q to %q when only the parent was invalidated", agent.ID, agentAfter.ID)
	}
}

func TestAffinityCacheKeyOwnedBySession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		alias     string
		sessionID string
		want      bool
	}{
		{"plain", "claude::claude:8046da35::claude-sonnet-4-5", "claude:8046da35", true},
		{"subagent id is its own session", "claude::claude:8046da35:agent:explore::claude-sonnet-4-5", "claude:8046da35:agent:explore", true},
		{"parent does not own the subagent key", "claude::claude:8046da35:agent:explore::claude-sonnet-4-5", "claude:8046da35", false},
		{"prompt cache key session", "claude::pck:abc123::claude-sonnet-4-5", "pck:abc123", true},
		{"session id containing the separator", "claude::weird::session::model", "weird::session", true},
		{"model containing the separator", "claude::s1::foo::bar", "s1", true},
		// "claude::s1::foo::bar" is genuinely ambiguous: it is a valid key for
		// session "s1" with model "foo::bar" AND for session "s1::foo" with
		// model "bar". Neither reading can be ruled out from the key alone, so
		// both session ids own it. Removal is the safe resolution — a spurious
		// release costs one re-selection, while a missed release leaves the
		// caller pinned, which is the failure this endpoint exists to fix.
		{"ambiguous key belongs to either candidate session", "claude::s1::foo::bar", "s1::foo", true},
		{"different session", "claude::s1::claude-sonnet-4-5", "s2", false},
		{"not a composite key", "claude:8046da35", "claude:8046da35", false},
		{"empty alias", "", "s1", false},
		{"empty session", "claude::s1::model", "", false},
	}
	for _, tt := range tests {
		if got := affinityCacheKeyOwnedBySession(tt.alias, tt.sessionID); got != tt.want {
			t.Errorf("%s: affinityCacheKeyOwnedBySession(%q, %q) = %v, want %v", tt.name, tt.alias, tt.sessionID, got, tt.want)
		}
	}
}

func TestSessionAffinitySelector_InvalidateSessionNilSafe(t *testing.T) {
	t.Parallel()

	var selector *SessionAffinitySelector
	if result := selector.InvalidateSession("claude:whatever"); result.Removed() {
		t.Fatalf("nil selector reported %+v", result)
	}
	live := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer live.Stop()
	if result := live.InvalidateSession(""); result.Removed() {
		t.Fatalf("empty session id reported %+v", result)
	}
}
