package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func boolPointer(b bool) *bool {
	return &b
}

func TestSessionAffinityAntigravitySubagentDoesNotInheritParentBinding(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: boolPointer(false),
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-ag-1", Provider: "antigravity"},
		{ID: "auth-ag-2", Provider: "antigravity"},
	}

	// 1. Parent request binds to auth-ag-1
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

	// 2. Subagent-1 request carries subagent ID.
	// For Antigravity/Gemini, subagent MUST NOT inherit parent auth to prevent 429 concurrency gate.
	subagent1Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent 1 task"}]}`),
		Metadata:        map[string]any{},
	}
	subagent1Auth, errSub1 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagent1Opts, auths)
	if errSub1 != nil {
		t.Fatalf("subagent 1 Pick() error = %v", errSub1)
	}
	if subagent1Auth.ID == parentAuth.ID {
		t.Fatalf("subagent 1 incorrectly inherited parent auth %q; should be isolated to prevent 429 rate limit", parentAuth.ID)
	}
	if subagent1Auth.ID != "auth-ag-2" {
		t.Fatalf("subagent 1 auth = %q, want auth-ag-2", subagent1Auth.ID)
	}

	// 3. Subagent-1 second turn must stay sticky to auth-ag-2
	subagent1Turn2Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent 1 second turn"}]}`),
		Metadata:        map[string]any{},
	}
	subagent1Turn2Auth, errSub1Turn2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagent1Turn2Opts, auths)
	if errSub1Turn2 != nil {
		t.Fatalf("subagent 1 turn 2 Pick() error = %v", errSub1Turn2)
	}
	if subagent1Turn2Auth.ID != "auth-ag-2" {
		t.Fatalf("subagent 1 turn 2 did not stay sticky: got %q, want auth-ag-2", subagent1Turn2Auth.ID)
	}

	// 4. Subagent-2 request must select next available credential (auth-ag-1) and not be locked into auth-ag-2
	subagent2Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-002"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent 2 task"}]}`),
		Metadata:        map[string]any{},
	}
	subagent2Auth, errSub2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagent2Opts, auths)
	if errSub2 != nil {
		t.Fatalf("subagent 2 Pick() error = %v", errSub2)
	}
	if subagent2Auth.ID != "auth-ag-1" {
		t.Fatalf("subagent 2 auth = %q, want auth-ag-1", subagent2Auth.ID)
	}

	// 5. Main parent session next turn must stay sticky to auth-ag-1
	parentTurn2Opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-100"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent follow-up"}]}`),
		Metadata:        map[string]any{},
	}
	parentTurn2Auth, errParent2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentTurn2Opts, auths)
	if errParent2 != nil {
		t.Fatalf("parent turn 2 Pick() error = %v", errParent2)
	}
	if parentTurn2Auth.ID != "auth-ag-1" {
		t.Fatalf("parent turn 2 auth = %q, want auth-ag-1", parentTurn2Auth.ID)
	}
}

func TestSessionAffinityMixedProviderAntigravitySubagentIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: boolPointer(false),
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-ag-1", Provider: "antigravity"},
		{ID: "auth-ag-2", Provider: "antigravity"},
	}

	// 1. Parent request in mixed pool binds to auth-ag-1
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-200"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "gemini-3.7-flash-high",
		},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "mixed", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-ag-1" {
		t.Fatalf("parent auth = %q, want auth-ag-1", parentAuth.ID)
	}

	// 2. Subagent request in mixed pool with Antigravity candidates must NOT inherit parent auth
	subOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-200"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-999"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "gemini-3.7-flash-high",
		},
	}
	subAuth, errSub := selector.Pick(context.Background(), "mixed", "gemini-3.7-flash-high", subOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subAuth.ID == parentAuth.ID {
		t.Fatalf("subagent in mixed pool incorrectly inherited parent auth %q", parentAuth.ID)
	}
	if subAuth.ID != "auth-ag-2" {
		t.Fatalf("subagent auth = %q, want auth-ag-2", subAuth.ID)
	}
}

func TestSessionAffinityClaudeAndCodexStillInheritParentBinding(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	claudeAuths := []*Auth{
		{ID: "auth-claude-1", Provider: "claude"},
		{ID: "auth-claude-2", Provider: "claude"},
	}

	// 1. Claude parent binds to auth-claude-1
	claudeParentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-300"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"claude parent"}]}`),
		Metadata:        map[string]any{},
	}
	claudeParentAuth, errClaudeParent := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", claudeParentOpts, claudeAuths)
	if errClaudeParent != nil {
		t.Fatalf("claude parent Pick() error = %v", errClaudeParent)
	}
	if claudeParentAuth.ID != "auth-claude-1" {
		t.Fatalf("claude parent auth = %q, want auth-claude-1", claudeParentAuth.ID)
	}

	// 2. Claude subagent MUST inherit parent auth for KV cache reuse
	claudeSubOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-300"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"claude subagent"}]}`),
		Metadata:        map[string]any{},
	}
	claudeSubAuth, errClaudeSub := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", claudeSubOpts, claudeAuths)
	if errClaudeSub != nil {
		t.Fatalf("claude sub Pick() error = %v", errClaudeSub)
	}
	if claudeSubAuth.ID != "auth-claude-1" {
		t.Fatalf("claude subagent auth = %q, want inherited parent auth-claude-1", claudeSubAuth.ID)
	}

	// 3. Codex parent and subagent inheritance
	codexAuths := []*Auth{
		{ID: "auth-codex-1", Provider: "codex"},
		{ID: "auth-codex-2", Provider: "codex"},
	}
	codexParentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session-Id": []string{"codex-parent-400"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"codex parent"}]}`),
		Metadata:        map[string]any{},
	}
	codexParentAuth, errCodexParent := selector.Pick(context.Background(), "codex", "gpt-5.4", codexParentOpts, codexAuths)
	if errCodexParent != nil {
		t.Fatalf("codex parent Pick() error = %v", errCodexParent)
	}
	if codexParentAuth.ID != "auth-codex-1" {
		t.Fatalf("codex parent auth = %q, want auth-codex-1", codexParentAuth.ID)
	}

	codexSubOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id":               []string{"codex-child-400"},
			"x-codex-parent-thread-id": []string{"codex-parent-400"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"codex subagent"}]}`),
		Metadata:        map[string]any{},
	}
	codexSubAuth, errCodexSub := selector.Pick(context.Background(), "codex", "gpt-5.4", codexSubOpts, codexAuths)
	if errCodexSub != nil {
		t.Fatalf("codex sub Pick() error = %v", errCodexSub)
	}
	if codexSubAuth.ID != "auth-codex-1" {
		t.Fatalf("codex subagent auth = %q, want inherited parent auth-codex-1", codexSubAuth.ID)
	}
}

func TestSessionAffinityMixedPoolSubagentInheritsParentAcrossProviders(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	// 1. Parent session selects Claude credential in mixed pool
	claudeCandidate := []*Auth{
		{ID: "auth-claude-primary", Provider: "claude"},
	}
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"mixed-sess-claude"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-3-7-sonnet",
		},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", parentOpts, claudeCandidate)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-claude-primary" {
		t.Fatalf("parent auth = %q, want auth-claude-primary", parentAuth.ID)
	}

	// 2. Subagent of Claude parent in mixed pool (where both Claude and Antigravity are available)
	// MUST inherit auth-claude-primary for prompt cache reuse
	mixedAuths := []*Auth{
		{ID: "auth-ag-other", Provider: "antigravity"},
		{ID: "auth-claude-primary", Provider: "claude"},
	}
	subClaudeOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"mixed-sess-claude"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-claude-1"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-3-7-sonnet",
		},
	}
	subClaudeAuth, errSubClaude := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", subClaudeOpts, mixedAuths)
	if errSubClaude != nil {
		t.Fatalf("subagent Claude Pick() error = %v", errSubClaude)
	}
	if subClaudeAuth.ID != "auth-claude-primary" {
		t.Fatalf("subagent in mixed pool with Claude parent did not inherit Claude auth: got %q, want auth-claude-primary", subClaudeAuth.ID)
	}

	// 3. Parent session that selected Antigravity credential in mixed pool
	agCandidate := []*Auth{
		{ID: "auth-ag-primary", Provider: "antigravity"},
	}
	parentAgOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"mixed-sess-ag"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-3-7-sonnet",
		},
	}
	parentAgAuth, errParentAg := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", parentAgOpts, agCandidate)
	if errParentAg != nil {
		t.Fatalf("parent Ag Pick() error = %v", errParentAg)
	}
	if parentAgAuth.ID != "auth-ag-primary" {
		t.Fatalf("parent Ag auth = %q, want auth-ag-primary", parentAgAuth.ID)
	}

	// 4. Subagent of Antigravity parent in mixed pool under unified inheritance
	// MUST inherit parent auth-ag-primary for prompt cache reuse
	mixedCandidates := []*Auth{
		{ID: "auth-ag-primary", Provider: "antigravity"},
		{ID: "auth-claude-primary", Provider: "claude"},
	}
	subAgOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"mixed-sess-ag"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-ag-1"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-3-7-sonnet",
		},
	}
	subAgAuth, errSubAg := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", subAgOpts, mixedCandidates)
	if errSubAg != nil {
		t.Fatalf("subagent Ag Pick() error = %v", errSubAg)
	}
	if subAgAuth.ID != parentAgAuth.ID {
		t.Fatalf("subagent with Antigravity parent did not inherit parent auth: got %q, want %q", subAgAuth.ID, parentAgAuth.ID)
	}

	// 5. Verify parent binding in cache remains intact
	if bound, ok := selector.cache.Get("mixed::claude:mixed-sess-ag::claude-3-7-sonnet"); !ok || bound != "auth-ag-primary" {
		t.Fatalf("parent cache binding was lost: got (%q, %v), want (auth-ag-primary, true)", bound, ok)
	}

	// 6. Subagent turn 2 must stay sticky to auth-ag-primary
	subAgTurn2Auth, errSubAg2 := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", subAgOpts, mixedCandidates)
	if errSubAg2 != nil {
		t.Fatalf("subagent turn 2 Pick() error = %v", errSubAg2)
	}
	if subAgTurn2Auth.ID != "auth-ag-primary" {
		t.Fatalf("subagent turn 2 auth = %q, want auth-ag-primary", subAgTurn2Auth.ID)
	}

	// 7. Parent turn 2 must stay sticky to auth-ag-primary
	parentAgTurn2Auth, errParentAg2 := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", parentAgOpts, mixedCandidates)
	if errParentAg2 != nil {
		t.Fatalf("parent turn 2 Pick() error = %v", errParentAg2)
	}
	if parentAgTurn2Auth.ID != "auth-ag-primary" {
		t.Fatalf("parent turn 2 auth = %q, want auth-ag-primary", parentAgTurn2Auth.ID)
	}
}

func TestSessionAffinityAntigravitySubagentOnResultFailureDoesNotUnbindParent(t *testing.T) {
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

	// 1. Parent request in mixed pool binds to auth-ag-1 with model claude-3-7-sonnet
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-500"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-3-7-sonnet",
		},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "mixed", "claude-3-7-sonnet", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-ag-1" {
		t.Fatalf("parent auth = %q, want auth-ag-1", parentAuth.ID)
	}

	// Verify parent key is bound in cache
	if bound, ok := selector.cache.Get("mixed::claude:claude-root-500::claude-3-7-sonnet"); !ok || bound != "auth-ag-1" {
		t.Fatalf("parent cache binding = (%q, %v), want (auth-ag-1, true)", bound, ok)
	}

	// 2. Subagent in mixed pool with exact same namespace and model
	subOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-500"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-err-1"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-3-7-sonnet",
		},
	}
	// Simulate subagent executing on auth-ag-1 (same auth ID as parent) and hitting 429
	selector.OnResult(Result{
		AuthID:   "auth-ag-1",
		Provider: "antigravity",
		Model:    "claude-3-7-sonnet",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "429 rate limited"},
		Options:  subOpts,
	})

	// 3. Parent binding in cache must remain untouched
	if bound, ok := selector.cache.Get("mixed::claude:claude-root-500::claude-3-7-sonnet"); !ok || bound != "auth-ag-1" {
		t.Fatalf("parent cache binding was destroyed by subagent OnResult failure: got (%q, %v)", bound, ok)
	}

	// 4. Simulate subagent success on auth-ag-2: must not touch or overwrite parent key
	selector.OnResult(Result{
		AuthID:   "auth-ag-2",
		Provider: "antigravity",
		Model:    "claude-3-7-sonnet",
		Success:  true,
		Options:  subOpts,
	})
	if bound, ok := selector.cache.Get("mixed::claude:claude-root-500::claude-3-7-sonnet"); !ok || bound != "auth-ag-1" {
		t.Fatalf("parent cache binding was altered by subagent OnResult success: got (%q, %v)", bound, ok)
	}
}

func TestSessionAffinityOtherGoogleProvidersSubagentIsolation(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"gemini", "vertex", "aistudio", "gemini-interactions"} {
		t.Run(provider, func(t *testing.T) {
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback:         &RoundRobinSelector{},
				TTL:              time.Minute,
				SubagentAffinity: boolPointer(false),
			})
			defer selector.Stop()

			auths := []*Auth{
				{ID: "auth-1", Provider: provider},
				{ID: "auth-2", Provider: provider},
			}

			parentOpts := cliproxyexecutor.Options{
				Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"root-google-sess"}},
				OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent"}]}`),
				Metadata:        map[string]any{},
			}
			parentAuth, errParent := selector.Pick(context.Background(), provider, "custom-model", parentOpts, auths)
			if errParent != nil {
				t.Fatalf("provider %s parent Pick() error = %v", provider, errParent)
			}
			if parentAuth.ID != "auth-1" {
				t.Fatalf("provider %s parent auth = %q, want auth-1", provider, parentAuth.ID)
			}

			subOpts := cliproxyexecutor.Options{
				Headers: http.Header{
					"X-Claude-Code-Session-Id": []string{"root-google-sess"},
					"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
				},
				OriginalRequest: []byte(`{"messages":[{"role":"user","content":"sub"}]}`),
				Metadata:        map[string]any{},
			}
			subAuth, errSub := selector.Pick(context.Background(), provider, "custom-model", subOpts, auths)
			if errSub != nil {
				t.Fatalf("provider %s subagent Pick() error = %v", provider, errSub)
			}
			if subAuth.ID == parentAuth.ID {
				t.Fatalf("provider %s subagent incorrectly inherited parent auth %q", provider, parentAuth.ID)
			}
			if subAuth.ID != "auth-2" {
				t.Fatalf("provider %s subagent auth = %q, want auth-2", provider, subAuth.ID)
			}
		})
	}
}

func TestSessionAffinityNestedAntigravitySubagentIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: boolPointer(false),
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-ag-1", Provider: "antigravity"},
		{ID: "auth-ag-2", Provider: "antigravity"},
	}

	// 1. Parent request with nested request payload binds to auth-ag-1
	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root-task"
			}
		}`),
		Metadata: map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-ag-1" {
		t.Fatalf("parent auth = %q, want auth-ag-1", parentAuth.ID)
	}
	if got := parentOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "session:root-task" {
		t.Fatalf("parent canonical session ID = %v, want session:root-task", got)
	}

	// 2. Subagent 1 request with nested metadata.agent_id must NOT inherit parent's auth-ag-1
	subagent1Opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root-task",
				"metadata": {
					"agent_id": "worker-1"
				}
			}
		}`),
		Metadata: map[string]any{},
	}
	subagent1Auth, errSub1 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagent1Opts, auths)
	if errSub1 != nil {
		t.Fatalf("subagent 1 Pick() error = %v", errSub1)
	}
	if subagent1Auth.ID == parentAuth.ID {
		t.Fatalf("subagent 1 incorrectly inherited parent auth %q; should be isolated to prevent 429 rate limit", parentAuth.ID)
	}
	if subagent1Auth.ID != "auth-ag-2" {
		t.Fatalf("subagent 1 auth = %q, want auth-ag-2", subagent1Auth.ID)
	}
	if got := subagent1Opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "session:root-task:agent:worker-1" {
		t.Fatalf("subagent 1 canonical session ID = %v, want session:root-task:agent:worker-1", got)
	}

	// 3. Subagent 1 turn 2 must stay sticky to auth-ag-2
	subagent1Turn2Opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root-task",
				"metadata": {
					"agent_id": "worker-1"
				}
			}
		}`),
		Metadata: map[string]any{},
	}
	subagent1Turn2Auth, errSub1Turn2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagent1Turn2Opts, auths)
	if errSub1Turn2 != nil {
		t.Fatalf("subagent 1 turn 2 Pick() error = %v", errSub1Turn2)
	}
	if subagent1Turn2Auth.ID != "auth-ag-2" {
		t.Fatalf("subagent 1 turn 2 did not stay sticky: got %q, want auth-ag-2", subagent1Turn2Auth.ID)
	}

	// 4. Subagent 2 request with metadata.subagent_id must select next available credential (auth-ag-1)
	subagent2Opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root-task",
				"metadata": {
					"subagent_id": "worker-2"
				}
			}
		}`),
		Metadata: map[string]any{},
	}
	subagent2Auth, errSub2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", subagent2Opts, auths)
	if errSub2 != nil {
		t.Fatalf("subagent 2 Pick() error = %v", errSub2)
	}
	if subagent2Auth.ID != "auth-ag-1" {
		t.Fatalf("subagent 2 auth = %q, want auth-ag-1", subagent2Auth.ID)
	}

	// 5. Parent second turn must stay sticky to auth-ag-1
	parentTurn2Opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{
			"request": {
				"sessionId": "root-task"
			}
		}`),
		Metadata: map[string]any{},
	}
	parentTurn2Auth, errParent2 := selector.Pick(context.Background(), "antigravity", "gemini-3.7-flash-high", parentTurn2Opts, auths)
	if errParent2 != nil {
		t.Fatalf("parent turn 2 Pick() error = %v", errParent2)
	}
	if parentTurn2Auth.ID != "auth-ag-1" {
		t.Fatalf("parent turn 2 auth = %q, want auth-ag-1", parentTurn2Auth.ID)
	}
}

func TestSessionAffinityNestedGeminiProviderSubagentIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: boolPointer(false),
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "gemini-auth-1", Provider: "gemini"},
		{ID: "gemini-auth-2", Provider: "gemini"},
	}

	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"request":{"sessionId":"gemini-root"}}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "gemini-auth-1" {
		t.Fatalf("parent auth = %q, want gemini-auth-1", parentAuth.ID)
	}

	subOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"request":{"sessionId":"gemini-root","metadata":{"agent_id":"gemini-worker"}}}`),
		Metadata:        map[string]any{},
	}
	subAuth, errSub := selector.Pick(context.Background(), "gemini", "gemini-2.5-pro", subOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subAuth.ID != "gemini-auth-2" {
		t.Fatalf("subagent incorrectly picked parent gemini auth: got %q, want gemini-auth-2", subAuth.ID)
	}
}
