package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestSessionAffinitySelectorLCPPreservesBindingAcrossConversationGrowth(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: lastAuthSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	first := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"system","content":"stable"},{"role":"user","content":"first"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey:      "caller-a",
			cliproxyexecutor.DerivedSessionIDMetadataKey: "legacy-derived-first",
		},
	}
	firstAuth, errFirst := selector.Pick(context.Background(), "openai", "model", first, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	if firstAuth.ID != "auth-b" {
		t.Fatalf("first Pick() = %q, want auth-b", firstAuth.ID)
	}
	if got := first.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; got == nil || got == "" {
		t.Fatalf("first Pick() did not publish an LCP affinity session identity: %#v", got)
	}
	if gotDerived := first.Metadata[cliproxyexecutor.DerivedSessionIDMetadataKey]; gotDerived != "legacy-derived-first" {
		t.Fatalf("first Pick() unexpectedly overwritten DerivedSessionIDMetadataKey: %#v", gotDerived)
	}
	if fingerprints, ok := first.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey].([]string); !ok || len(fingerprints) != 2 {
		t.Fatalf("first Pick() did not precompute request fingerprints: %#v", first.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey])
	}

	grown := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"system","content":"stable"},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey:      "caller-a",
			cliproxyexecutor.DerivedSessionIDMetadataKey: "legacy-derived-after-growth",
		},
	}
	grownAuth, errGrown := selector.Pick(context.Background(), "openai", "model", grown, auths)
	if errGrown != nil {
		t.Fatalf("grown Pick() error = %v", errGrown)
	}
	if grownAuth.ID != firstAuth.ID {
		t.Fatalf("conversation growth changed auth from %q to %q", firstAuth.ID, grownAuth.ID)
	}
	if first.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] != grown.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] {
		t.Fatalf("LCP session identity changed across growth: first=%v grown=%v", first.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey], grown.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey])
	}
}

func TestSessionAffinitySelectorLCPSkipsWhenCallerScopeMissing(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	first := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"first request without caller scope"}]}`),
	}
	second := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"second request without caller scope"}]}`),
	}
	firstAuth, errFirst := selector.Pick(context.Background(), "openai", "model", first, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	secondAuth, errSecond := selector.Pick(context.Background(), "openai", "model", second, auths)
	if errSecond != nil {
		t.Fatalf("second Pick() error = %v", errSecond)
	}
	if got := first.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; got != nil {
		t.Fatalf("first request without caller scope unexpectedly assigned LCP affinity ID: %#v", got)
	}
	if got := second.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; got != nil {
		t.Fatalf("second request without caller scope unexpectedly assigned LCP affinity ID: %#v", got)
	}
	if firstAuth.ID == secondAuth.ID {
		t.Fatalf("requests without caller scope unexpectedly matched the same auth under round-robin fallback")
	}
}

func TestSessionAffinitySelectorLCPCallerScopeIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	payload := []byte(`{"messages":[{"role":"user","content":"shared common prompt"}]}`)

	callerA := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-a",
		},
	}
	callerB := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-b",
		},
	}

	authA, errA := selector.Pick(context.Background(), "openai", "model", callerA, auths)
	if errA != nil {
		t.Fatalf("callerA Pick() error = %v", errA)
	}
	authB, errB := selector.Pick(context.Background(), "openai", "model", callerB, auths)
	if errB != nil {
		t.Fatalf("callerB Pick() error = %v", errB)
	}
	if authA.ID == authB.ID {
		t.Fatalf("different callers unexpectedly matched the same LCP binding: %q", authA.ID)
	}
}

func TestSessionAffinitySelectorLCPFailureRemovesExactSequence(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"failure"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-a",
		},
	}
	first, errFirst := selector.Pick(context.Background(), "openai", "model", opts, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	if first.ID != "auth-a" {
		t.Fatalf("first Pick() = %q, want auth-a", first.ID)
	}

	selector.OnResult(Result{
		AuthID:   first.ID,
		Provider: "openai",
		Model:    "model",
		Error:    &Error{Code: "rate_limited", Message: "rate limited"},
		Options:  opts,
	})

	next, errNext := selector.Pick(context.Background(), "openai", "model", opts, auths)
	if errNext != nil {
		t.Fatalf("next Pick() error = %v", errNext)
	}
	if next.ID != "auth-b" {
		t.Fatalf("next Pick() = %q, want auth-b after exact LCP removal", next.ID)
	}
}

func TestSessionAffinitySelectorLCPOnResultReusesPrecomputedFingerprints(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"precomputed metadata test"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-a",
		},
	}
	first, errFirst := selector.Pick(context.Background(), "openai", "model", opts, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}

	fingerprints, ok := opts.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey].([]string)
	if !ok || len(fingerprints) != 1 {
		t.Fatalf("opts.Metadata did not store precomputed fingerprints: %#v", opts.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey])
	}

	// Clear OriginalRequest to ensure OnResult relies exclusively on precomputed metadata.
	optsWithoutPayload := cliproxyexecutor.Options{
		SourceFormat:    opts.SourceFormat,
		OriginalRequest: nil,
		Metadata:        opts.Metadata,
	}

	selector.OnResult(Result{
		AuthID:   first.ID,
		Provider: "openai",
		Model:    "model",
		Success:  true,
		Options:  optsWithoutPayload,
	})

	second, errSecond := selector.Pick(context.Background(), "openai", "model", opts, auths)
	if errSecond != nil {
		t.Fatalf("second Pick() error = %v", errSecond)
	}
	if second.ID != first.ID {
		t.Fatalf("OnResult failed to touch LCP binding with precomputed fingerprints: got %q want %q", second.ID, first.ID)
	}
}

func TestSessionAffinitySelectorExplicitHarnessSessionOverridesLCP(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	payload := []byte(`{"messages":[{"role":"user","content":"same prompt"}]}`)
	lcpAuth, errLCP := selector.Pick(context.Background(), "openai", "model", cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	}, auths)
	if errLCP != nil {
		t.Fatalf("LCP Pick() error = %v", errLCP)
	}
	if lcpAuth.ID != "auth-a" {
		t.Fatalf("LCP Pick() = %q, want auth-a", lcpAuth.ID)
	}

	explicitHeaders := http.Header{"X-Session-ID": []string{"harness-session"}}
	explicitAuth, errExplicit := selector.Pick(context.Background(), "openai", "model", cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
		Headers:         explicitHeaders,
	}, auths)
	if errExplicit != nil {
		t.Fatalf("explicit Pick() error = %v", errExplicit)
	}
	if explicitAuth.ID != "auth-b" {
		t.Fatalf("explicit Pick() = %q, want fallback auth-b rather than an LCP binding", explicitAuth.ID)
	}
}

func TestCanonicalSessionIDUnifiedResolution(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// Case 1: Explicit Header
	explicitOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-abc"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{},
	}
	_, errExplicit := selector.Pick(context.Background(), "claude", "model", explicitOpts, auths)
	if errExplicit != nil {
		t.Fatalf("explicit Pick() error = %v", errExplicit)
	}
	if got := explicitOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "claude:claude-abc" {
		t.Fatalf("canonical session ID for explicit header = %v, want claude:claude-abc", got)
	}
	if resolved := CanonicalSessionID(explicitOpts.Headers, explicitOpts.OriginalRequest, explicitOpts.Metadata); resolved != "claude:claude-abc" {
		t.Fatalf("CanonicalSessionID() = %q, want claude:claude-abc", resolved)
	}

	// Case 2: LCP Inferred Session
	lcpOpts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"lcp unified session test"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-unified",
		},
	}
	_, errLCP := selector.Pick(context.Background(), "openai", "model", lcpOpts, auths)
	if errLCP != nil {
		t.Fatalf("LCP Pick() error = %v", errLCP)
	}
	lcpSessionID, ok := lcpOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok || !strings.HasPrefix(lcpSessionID, "lcp:v1:") {
		t.Fatalf("canonical session ID for LCP = %v, want prefix lcp:v1:", lcpSessionID)
	}
	if lcpOpts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] != lcpSessionID {
		t.Fatalf("LCPAffinitySessionIDMetadataKey = %v does not match CanonicalSessionIDMetadataKey = %v", lcpOpts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey], lcpSessionID)
	}
	if resolved := CanonicalSessionID(lcpOpts.Headers, lcpOpts.OriginalRequest, lcpOpts.Metadata); resolved != lcpSessionID {
		t.Fatalf("CanonicalSessionID() for LCP = %q, want %q", resolved, lcpSessionID)
	}

	// Case 3: A current explicit identity overrides stale inferred metadata.
	staleMetadata := map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey:   lcpSessionID,
		cliproxyexecutor.LCPAffinitySessionIDMetadataKey: lcpSessionID,
	}
	if resolved := CanonicalSessionID(http.Header{"X-Session-ID": []string{"current-explicit"}}, nil, staleMetadata); resolved != "header:current-explicit" {
		t.Fatalf("CanonicalSessionID() with stale LCP metadata = %q, want header:current-explicit", resolved)
	}
}

func TestSessionAffinitySelectorLCPUsesCanonicalFormatWhenSourceFormatMissing(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	first := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-gemini",
		},
	}
	second := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-gemini",
		},
	}
	firstAuth, errFirst := selector.Pick(context.Background(), "gemini", "model", first, auths)
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	secondAuth, errSecond := selector.Pick(context.Background(), "gemini", "model", second, auths)
	if errSecond != nil {
		t.Fatalf("second Pick() error = %v", errSecond)
	}
	if secondAuth.ID != firstAuth.ID {
		t.Fatalf("inferred Gemini format changed auth from %q to %q", firstAuth.ID, secondAuth.ID)
	}
}

func TestSessionAffinityClaudeSubagentInheritsParentBindingAndSeparatesAgentID(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Parent request binds to an auth
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-100"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "claude", "model", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if got := parentOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "claude:claude-root-100" {
		t.Fatalf("parent canonical session ID = %v, want claude:claude-root-100", got)
	}

	// 2. Subagent-1 request carries subagent ID
	subagent1Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent 1 task"}]}`),
		Metadata:        map[string]any{},
	}
	subagent1Auth, errSub1 := selector.Pick(context.Background(), "claude", "model", subagent1Opts, auths)
	if errSub1 != nil {
		t.Fatalf("subagent 1 Pick() error = %v", errSub1)
	}
	// Subagent must inherit the exact auth bound by the parent for KV cache reuse
	if subagent1Auth.ID != parentAuth.ID {
		t.Fatalf("subagent 1 did not inherit parent auth: got %q, want parent auth %q", subagent1Auth.ID, parentAuth.ID)
	}
	// Subagent must have its own isolated canonical session ID
	if got := subagent1Opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "claude:claude-root-100:agent:subagent-001" {
		t.Fatalf("subagent 1 canonical session ID = %v, want claude:claude-root-100:agent:subagent-001", got)
	}

	// 3. Subagent-2 request also inherits parent auth
	subagent2Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-002"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent 2 task"}]}`),
		Metadata:        map[string]any{},
	}
	subagent2Auth, errSub2 := selector.Pick(context.Background(), "claude", "model", subagent2Opts, auths)
	if errSub2 != nil {
		t.Fatalf("subagent 2 Pick() error = %v", errSub2)
	}
	if subagent2Auth.ID != parentAuth.ID {
		t.Fatalf("subagent 2 did not inherit parent auth: got %q, want parent auth %q", subagent2Auth.ID, parentAuth.ID)
	}
	if got := subagent2Opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "claude:claude-root-100:agent:subagent-002" {
		t.Fatalf("subagent 2 canonical session ID = %v, want claude:claude-root-100:agent:subagent-002", got)
	}
}

func TestSessionAffinityCodexSubagentInheritsParentThread(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Parent thread binds to an auth
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session-Id": []string{"thread-parent-999"}},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. Child thread carries parent thread header
	childOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id":               []string{"thread-child-888"},
			"x-codex-parent-thread-id": []string{"thread-parent-999"},
			"x-openai-subagent":        []string{"true"},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"child subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	childAuth, errChild := selector.Pick(context.Background(), "openai", "model", childOpts, auths)
	if errChild != nil {
		t.Fatalf("child Pick() error = %v", errChild)
	}
	if childAuth.ID != parentAuth.ID {
		t.Fatalf("child thread did not inherit parent auth: got %q, want %q", childAuth.ID, parentAuth.ID)
	}
	if got := childOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:thread-child-888" {
		t.Fatalf("child canonical session ID = %v, want codex:thread-child-888", got)
	}

	// 3. Forked thread carries X-Codex-Turn-Metadata with forked_from_thread_id
	forkOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"thread-fork-777"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"thread-fork-777","forked_from_thread_id":"thread-parent-999","request_kind":"turn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"fork turn"}]}`),
		Metadata:        map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "openai", "model", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("fork Pick() error = %v", errFork)
	}
	if forkAuth.ID != parentAuth.ID {
		t.Fatalf("fork thread did not inherit parent auth: got %q, want %q", forkAuth.ID, parentAuth.ID)
	}
	if got := forkOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:thread-fork-777" {
		t.Fatalf("fork canonical session ID = %v, want codex:thread-fork-777", got)
	}
}

func TestSessionAffinityCodexForkInheritsParentOnGeminiModel(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-1", Provider: "antigravity"}, {ID: "auth-2", Provider: "antigravity"}}

	// 1. Parent thread binds to an antigravity auth
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session-Id": []string{"parent-gemini-thread"}},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent query"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "mixed", "gemini-3.8-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. Forked thread carries X-Codex-Turn-Metadata with forked_from_thread_id
	// Even on Gemini/Antigravity where subagents are non-inheriting, conversational FORKS must inherit parent auth!
	forkOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"fork-gemini-thread"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"fork-gemini-thread","forked_from_thread_id":"parent-gemini-thread","request_kind":"turn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"fork query"}]}`),
		Metadata:        map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "mixed", "gemini-3.8-flash-high", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("fork Pick() error = %v", errFork)
	}
	if forkAuth.ID != parentAuth.ID {
		t.Fatalf("conversational fork did not inherit parent auth on Gemini: got %q, want %q", forkAuth.ID, parentAuth.ID)
	}
	if got := forkOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:fork-gemini-thread" {
		t.Fatalf("fork canonical session ID = %v, want codex:fork-gemini-thread", got)
	}
	if got := forkOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "codex:parent-gemini-thread" {
		t.Fatalf("fork parent session ID = %v, want codex:parent-gemini-thread", got)
	}
	if isFork, ok := forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); !ok || !isFork {
		t.Fatalf("expected is_fork=true in metadata, got %v", forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey])
	}
}

func TestSessionAffinityCodexMultiAgentV2CollabSpawn(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Parent session in Codex CLI
	parentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"parent-root-100"},
			"Thread-Id":  []string{"parent-root-100"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"parent-root-100","thread_id":"parent-root-100","agent_name":"/root","request_kind":"turn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. Multi-Agent v2 child subagent (collab_spawn)
	childOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id":               []string{"parent-root-100"},
			"Thread-Id":                []string{"subagent-thread-200"},
			"X-Codex-Parent-Thread-Id": []string{"parent-root-100"},
			"X-Openai-Subagent":        []string{"collab_spawn"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"parent-root-100","thread_id":"subagent-thread-200","agent_name":"/root/check_readme","parent_thread_id":"parent-root-100","subagent_kind":"thread_spawn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"child check readme"}]}`),
		Metadata:        map[string]any{},
	}
	childAuth, errChild := selector.Pick(context.Background(), "openai", "model", childOpts, auths)
	if errChild != nil {
		t.Fatalf("child Pick() error = %v", errChild)
	}
	if childAuth.ID != parentAuth.ID {
		t.Fatalf("child subagent did not inherit parent auth: got %q, want %q", childAuth.ID, parentAuth.ID)
	}
	if got := childOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:parent-root-100:agent:check_readme" {
		t.Fatalf("child canonical session ID = %v, want codex:parent-root-100:agent:check_readme", got)
	}
	if got := childOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "codex:parent-root-100" {
		t.Fatalf("child parent session ID = %v, want codex:parent-root-100", got)
	}

	// 3. Failure on child subagent does not evict parent binding
	selector.OnResult(Result{
		Provider: "openai",
		Model:    "model",
		AuthID:   childAuth.ID,
		Success:  false,
		Options:  childOpts,
	})

	// Parent session should still be bound to parentAuth
	parentOpts2 := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"parent-root-100"},
			"Thread-Id":  []string{"parent-root-100"},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent task 2"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth2, errParent2 := selector.Pick(context.Background(), "openai", "model", parentOpts2, auths)
	if errParent2 != nil {
		t.Fatalf("parent Pick() 2 error = %v", errParent2)
	}
	if parentAuth2.ID != parentAuth.ID {
		t.Fatalf("parent auth binding was evicted by subagent failure: got %q, want %q", parentAuth2.ID, parentAuth.ID)
	}
}

func TestSessionAffinityBodyOnlyCodexFork(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-1", Provider: "antigravity"}, {ID: "auth-2", Provider: "antigravity"}}

	// 1. Parent thread in body
	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"thread_id":"body-parent-100","messages":[{"role":"user","content":"parent"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "mixed", "gemini-3.8-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. Forked thread in body without headers (e.g. {"thread_id":"child","forked_from_thread_id":"parent"})
	forkOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"thread_id":"body-fork-200","forked_from_thread_id":"body-parent-100","messages":[{"role":"user","content":"fork"}]}`),
		Metadata:        map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "mixed", "gemini-3.8-flash-high", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("fork Pick() error = %v", errFork)
	}
	if forkAuth.ID != parentAuth.ID {
		t.Fatalf("body-only fork did not inherit parent auth on Gemini: got %q, want %q", forkAuth.ID, parentAuth.ID)
	}
	if isFork, ok := forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); !ok || !isFork {
		t.Fatalf("expected is_fork=true for body-only fork, got %v", forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey])
	}
}

func TestSessionAffinityForkRebindDoesNotMutateParentBinding(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Parent thread binds to auth-a
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session-Id": []string{"parent-thread-alpha"}},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)
	if errParent != nil || parentAuth.ID != "auth-a" {
		t.Fatalf("parent Pick() error = %v, auth = %v", errParent, parentAuth)
	}

	// 2. Fork thread inherits parent auth-a
	forkOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"fork-thread-beta"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"fork-thread-beta","forked_from_thread_id":"parent-thread-alpha","request_kind":"turn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"fork"}]}`),
		Metadata:        map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "openai", "model", forkOpts, auths)
	if errFork != nil || forkAuth.ID != "auth-a" {
		t.Fatalf("fork Pick() error = %v, auth = %v", errFork, forkAuth)
	}

	// 3. Fork thread fails and rebinds to auth-b (failover rebind)
	selector.OnResult(Result{
		Provider: "openai",
		Model:    "model",
		AuthID:   "auth-a",
		Success:  false,
		Options:  forkOpts,
	})

	// Re-pick fork with auth-b
	forkOpts2 := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"fork-thread-beta"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"fork-thread-beta","forked_from_thread_id":"parent-thread-alpha","request_kind":"turn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"fork retry"}]}`),
		Metadata:        map[string]any{},
	}
	forkAuth2, errFork2 := selector.Pick(context.Background(), "openai", "model", forkOpts2, []*Auth{{ID: "auth-b"}})
	if errFork2 != nil || forkAuth2.ID != "auth-b" {
		t.Fatalf("fork retry Pick() error = %v, auth = %v", errFork2, forkAuth2)
	}

	// 4. Parent session MUST STILL BE BOUND to auth-a, not mutated to auth-b!
	parentOpts2 := cliproxyexecutor.Options{
		Headers:         http.Header{"Session-Id": []string{"parent-thread-alpha"}},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent turn 2"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth2, errParent2 := selector.Pick(context.Background(), "openai", "model", parentOpts2, auths)
	if errParent2 != nil || parentAuth2.ID != "auth-a" {
		t.Fatalf("parent auth binding was mutated by fork failover: got %q, want auth-a", parentAuth2.ID)
	}
}

func TestSessionAffinityCodexSubagentWithOmittedThreadIdRetainsParent(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// Parent binds
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session-Id": []string{"sess-main-999"}},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"parent"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, _ := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)

	// Subagent sends Session-Id, but Thread-Id is omitted, while agent_name is set in turn metadata
	subOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id":        []string{"sess-main-999"},
			"X-Openai-Subagent": []string{"collab_spawn"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"sess-main-999","agent_name":"/root/worker","subagent_kind":"thread_spawn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"subagent"}]}`),
		Metadata:        map[string]any{},
	}
	subAuth, errSub := selector.Pick(context.Background(), "openai", "model", subOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subAuth.ID != parentAuth.ID {
		t.Fatalf("subagent did not inherit parent auth: got %q, want %q", subAuth.ID, parentAuth.ID)
	}
	if got := subOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "codex:sess-main-999" {
		t.Fatalf("expected ParentSessionID=codex:sess-main-999, got %v", got)
	}
	if got := subOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:sess-main-999:agent:worker" {
		t.Fatalf("expected CanonicalSessionID=codex:sess-main-999:agent:worker, got %v", got)
	}
}

func TestSessionAffinityPayloadParentSessionInheritance(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Parent session
	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"session_id":"parent-sess-001","messages":[{"role":"user","content":"parent"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. Child session with parent_session_id in payload
	childOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"session_id":"child-sess-002","parent_session_id":"parent-sess-001","messages":[{"role":"user","content":"child"}]}`),
		Metadata:        map[string]any{},
	}
	childAuth, errChild := selector.Pick(context.Background(), "openai", "model", childOpts, auths)
	if errChild != nil {
		t.Fatalf("child Pick() error = %v", errChild)
	}
	if childAuth.ID != parentAuth.ID {
		t.Fatalf("child session did not inherit parent auth: got %q, want %q", childAuth.ID, parentAuth.ID)
	}
	if got := childOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "session:child-sess-002" {
		t.Fatalf("child canonical session ID = %v, want session:child-sess-002", got)
	}
}

func TestSessionAffinityExtendedHeadersAndPayloadIdentities(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Antigravity X-Http-Session-Id
	agyOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Http-Session-Id": []string{"agy-sess-456"}},
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		Metadata:        map[string]any{},
	}
	_, errAgy := selector.Pick(context.Background(), "gemini", "model", agyOpts, auths)
	if errAgy != nil {
		t.Fatalf("agy Pick() error = %v", errAgy)
	}
	if got := agyOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "agy:agy-sess-456" {
		t.Fatalf("agy canonical session ID = %v, want agy:agy-sess-456", got)
	}

	// 2. Pi Slot Session
	slotOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Slot-Session-Id": []string{"pi-slot-789"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"pi slot task"}]}`),
		Metadata:        map[string]any{},
	}
	_, errSlot := selector.Pick(context.Background(), "openai", "model", slotOpts, auths)
	if errSlot != nil {
		t.Fatalf("slot Pick() error = %v", errSlot)
	}
	if got := slotOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "slot:pi-slot-789" {
		t.Fatalf("slot canonical session ID = %v, want slot:pi-slot-789", got)
	}

	// 3. Google Gemini Context Caching
	geminiCacheOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"cachedContent":"projects/123/locations/us-central1/cachedContents/456","contents":[{"role":"user","parts":[{"text":"query"}]}]}`),
		Metadata:        map[string]any{},
	}
	_, errGemini := selector.Pick(context.Background(), "gemini", "model", geminiCacheOpts, auths)
	if errGemini != nil {
		t.Fatalf("gemini cache Pick() error = %v", errGemini)
	}
	if got := geminiCacheOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "geminicache:projects/123/locations/us-central1/cachedContents/456" {
		t.Fatalf("gemini cache canonical session ID = %v, want geminicache:...", got)
	}

	// 4. OpenAI Assistants Thread ID Header & Body
	threadOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Thread-Id": []string{"thread_abc123"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"run thread"}]}`),
		Metadata:        map[string]any{},
	}
	_, errThread := selector.Pick(context.Background(), "openai", "model", threadOpts, auths)
	if errThread != nil {
		t.Fatalf("thread Pick() error = %v", errThread)
	}
	if got := threadOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "thread:thread_abc123" {
		t.Fatalf("thread canonical session ID = %v, want thread:thread_abc123", got)
	}

	// 5. Payload metadata.agent_id with parent session inheritance
	parentAgentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"session_id":"sess-main-555","messages":[{"role":"user","content":"main"}]}`),
		Metadata:        map[string]any{},
	}
	parentAgentAuth, errP := selector.Pick(context.Background(), "openai", "model", parentAgentOpts, auths)
	if errP != nil {
		t.Fatalf("parentAgent Pick() error = %v", errP)
	}

	subAgentPayloadOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"session_id":"sess-main-555","metadata":{"agent_id":"worker-agent-1"},"messages":[{"role":"user","content":"worker"}]}`),
		Metadata:        map[string]any{},
	}
	subAgentAuth, errSub := selector.Pick(context.Background(), "openai", "model", subAgentPayloadOpts, auths)
	if errSub != nil {
		t.Fatalf("subAgent Pick() error = %v", errSub)
	}
	if subAgentAuth.ID != parentAgentAuth.ID {
		t.Fatalf("subAgent did not inherit parent agent auth: got %q, want %q", subAgentAuth.ID, parentAgentAuth.ID)
	}
	if got := subAgentPayloadOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "session:sess-main-555:agent:worker-agent-1" {
		t.Fatalf("subAgent canonical session ID = %v, want session:sess-main-555:agent:worker-agent-1", got)
	}
}

func TestSessionAffinitySelectorSubagentInheritance(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	// 1. Root Task Request (Claude Code Main)
	rootOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"tree-root-sess"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start root task"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-corp",
		},
	}
	rootAuth, errRoot := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", rootOpts, auths)
	if errRoot != nil {
		t.Fatalf("root Pick() error = %v", errRoot)
	}

	// 2. Subagent 1 Request (Claude Code Subagent) inherits Root Auth
	sub1Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"tree-root-sess"},
			"X-Claude-Code-Agent-Id":   []string{"checker-agent"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"run checker"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-corp",
		},
	}
	sub1Auth, errSub1 := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", sub1Opts, auths)
	if errSub1 != nil {
		t.Fatalf("sub1 Pick() error = %v", errSub1)
	}
	if sub1Auth.ID != rootAuth.ID {
		t.Fatalf("sub1 did not inherit root auth: got %s, want %s", sub1Auth.ID, rootAuth.ID)
	}

	// 3. Subagent 2 Request with explicit parent agent inherits same auth
	sub2Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id":      []string{"tree-root-sess"},
			"X-Claude-Code-Agent-Id":        []string{"leaf-agent"},
			"X-Claude-Code-Parent-Agent-Id": []string{"checker-agent"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"run leaf checker"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-corp",
		},
	}
	sub2Auth, errSub2 := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", sub2Opts, auths)
	if errSub2 != nil {
		t.Fatalf("sub2 Pick() error = %v", errSub2)
	}
	if sub2Auth.ID != rootAuth.ID {
		t.Fatalf("sub2 did not inherit root auth: got %s, want %s", sub2Auth.ID, rootAuth.ID)
	}
}

func TestSessionCacheCompareAndDeleteMultipleAliases(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Hour)
	defer cache.Stop()

	// Set 3 aliases in the same group
	cache.SetAliases("auth-1", "s1", "s2", "s3")

	if authID, ok := cache.Get("s1"); !ok || authID != "auth-1" {
		t.Fatalf("s1 = %q, %v", authID, ok)
	}
	if authID, ok := cache.Get("s2"); !ok || authID != "auth-1" {
		t.Fatalf("s2 = %q, %v", authID, ok)
	}
	if authID, ok := cache.Get("s3"); !ok || authID != "auth-1" {
		t.Fatalf("s3 = %q, %v", authID, ok)
	}

	// Compare and delete s1
	if !cache.CompareAndDelete("s1", "auth-1") {
		t.Fatal("CompareAndDelete s1 failed")
	}

	// s1 should be gone
	if _, ok := cache.Get("s1"); ok {
		t.Fatal("s1 still exists after CompareAndDelete")
	}

	// s2 and s3 should still exist and point to auth-1
	if authID, ok := cache.Get("s2"); !ok || authID != "auth-1" {
		t.Fatalf("s2 = %q, %v", authID, ok)
	}
	if authID, ok := cache.Get("s3"); !ok || authID != "auth-1" {
		t.Fatalf("s3 = %q, %v", authID, ok)
	}
}

type nilSelector struct{}

func (nilSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, candidates []*Auth) (*Auth, error) {
	return nil, nil
}

func TestSessionAffinitySelectorNilFallbackNoPanic(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: nilSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()

	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-ID": []string{"sess-nil-test"},
		},
	}
	candidates := []*Auth{{ID: "auth-1", Status: StatusActive}}
	auth, err := selector.Pick(context.Background(), "openai", "gpt-4o", opts, candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth != nil {
		t.Fatalf("expected nil auth, got %+v", auth)
	}
}

func TestSessionAffinitySelectorPromptCacheKeyCamelCase(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"promptCacheKey":"camel-pck-123","input":"hello"}`)
	primary, fallback := extractExplicitSessionIDs(nil, payload, nil)
	if primary != "pck:camel-pck-123" {
		t.Fatalf("primary = %q, want pck:camel-pck-123", primary)
	}
	if fallback != "" {
		t.Fatalf("fallback = %q, want empty", fallback)
	}
}

func TestSessionAffinitySelectorNestedAntigravityPayload(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"project_id": "proj-123",
		"request": {
			"parentSessionId": "parent-456",
			"sessionId": "child-789"
		}
	}`)
	primary, fallback := extractExplicitSessionIDs(nil, payload, nil)
	if primary != "session:child-789" {
		t.Fatalf("primary = %q, want session:child-789", primary)
	}
	if fallback != "session:parent-456" {
		t.Fatalf("fallback = %q, want session:parent-456", fallback)
	}
}

func TestSessionCacheTinyTTLNoPanic(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(1 * time.Nanosecond)
	if cache == nil {
		t.Fatal("NewSessionCache returned nil")
	}
	cache.Set("test-key", "auth-1")
	cache.Touch("test-key", "auth-1")
	cache.Stop()
}

func TestSessionAffinityClaudeMetadataSubagentNonInheritingGeminiModel(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: boolPointer(false),
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a", Provider: "antigravity", Status: StatusActive},
		{ID: "auth-b", Provider: "antigravity", Status: StatusActive},
	}

	// 1. Parent request binds to auth-a
	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"model":"gemini-3.7-flash-high","metadata":{"user_id":"{\"device_id\":\"dev-1\",\"session_id\":\"sess-main-1\"}"},"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "mixed", "gemini-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-a" {
		t.Fatalf("parent auth = %q, want auth-a", parentAuth.ID)
	}
	if got := parentOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "claude:sess-main-1" {
		t.Fatalf("parent canonical session ID = %v, want claude:sess-main-1", got)
	}

	// 2. Subagent 1 request with X-Claude-Code-Agent-Id header and metadata.user_id in payload
	subagent1Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Agent-Id": []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"model":"gemini-3.7-flash-high","metadata":{"user_id":"{\"device_id\":\"dev-1\",\"session_id\":\"sess-main-1\"}"},"messages":[{"role":"user","content":"subagent 1 task"}]}`),
		Metadata:        map[string]any{},
	}
	subagent1Auth, errSub1 := selector.Pick(context.Background(), "mixed", "gemini-3.7-flash-high", subagent1Opts, auths)
	if errSub1 != nil {
		t.Fatalf("subagent 1 Pick() error = %v", errSub1)
	}
	// When SubagentAffinity is false, subagents must NOT inherit the parent's auth; they should balance to auth-b
	if subagent1Auth.ID != "auth-b" {
		t.Fatalf("subagent 1 should not inherit parent auth when subagent affinity is disabled, got %q, want auth-b", subagent1Auth.ID)
	}
	if got := subagent1Opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "claude:sess-main-1:agent:subagent-001" {
		t.Fatalf("subagent 1 canonical session ID = %v, want claude:sess-main-1:agent:subagent-001", got)
	}

	// 3. Subsequent turn for subagent 1 must retain auth-b
	subagent1Turn2Opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Agent-Id": []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"model":"gemini-3.7-flash-high","metadata":{"user_id":"{\"device_id\":\"dev-1\",\"session_id\":\"sess-main-1\"}"},"messages":[{"role":"user","content":"subagent 1 turn 2"}]}`),
		Metadata:        map[string]any{},
	}
	subagent1Turn2Auth, errSub1Turn2 := selector.Pick(context.Background(), "mixed", "gemini-3.7-flash-high", subagent1Turn2Opts, auths)
	if errSub1Turn2 != nil {
		t.Fatalf("subagent 1 turn 2 Pick() error = %v", errSub1Turn2)
	}
	if subagent1Turn2Auth.ID != "auth-b" {
		t.Fatalf("subagent 1 turn 2 affinity broken: got %q, want auth-b", subagent1Turn2Auth.ID)
	}
}

func TestSessionAffinitySelectorLCPForkDerivesDistinctSessionIDAndParentLineage(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-1"}, {ID: "auth-2"}}

	// 1. Root conversation: 3 user turns + 2 assistant answers
	rootOpts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[` +
			`{"role":"user","content":"turn 1"},` +
			`{"role":"assistant","content":"ans 1"},` +
			`{"role":"user","content":"turn 2 trunk"},` +
			`{"role":"assistant","content":"ans 2 trunk"},` +
			`{"role":"user","content":"turn 3 trunk"}` +
			`]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-user-1",
		},
	}
	rootAuth, errRoot := selector.Pick(context.Background(), "openai", "model", rootOpts, auths)
	if errRoot != nil {
		t.Fatalf("root Pick() error = %v", errRoot)
	}
	rootSessionID, ok := rootOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok || rootSessionID == "" {
		t.Fatalf("expected non-empty canonical root session ID, got %#v", rootOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey])
	}
	if parentID := rootOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; parentID != nil {
		t.Fatalf("expected nil parent ID on root session, got %#v", parentID)
	}

	// 2. Fork request: shares turn 1 & ans 1, but diverges on turn 2
	forkOpts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[` +
			`{"role":"user","content":"turn 1"},` +
			`{"role":"assistant","content":"ans 1"},` +
			`{"role":"user","content":"turn 2 fork branch B"}` +
			`]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-user-1",
		},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "openai", "model", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("fork Pick() error = %v", errFork)
	}
	// Routing MUST keep the exact same auth for hardware KV cache reuse
	if forkAuth.ID != rootAuth.ID {
		t.Fatalf("fork routed to %q, want same auth %q as root session", forkAuth.ID, rootAuth.ID)
	}
	forkSessionID, ok := forkOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok || forkSessionID == "" {
		t.Fatalf("expected non-empty canonical fork session ID, got %#v", forkOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey])
	}
	// Branch identity MUST differ from root
	if forkSessionID == rootSessionID {
		t.Fatalf("fork session ID %q should differ from root session ID %q", forkSessionID, rootSessionID)
	}
	forkParentID, ok := forkOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string)
	if !ok || forkParentID == "" {
		t.Fatalf("expected non-empty parent session ID on fork, got %#v", forkOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey])
	}

	// 3. Linear continuation on the fork branch (turn 3 on branch B)
	forkContOpts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[` +
			`{"role":"user","content":"turn 1"},` +
			`{"role":"assistant","content":"ans 1"},` +
			`{"role":"user","content":"turn 2 fork branch B"},` +
			`{"role":"assistant","content":"ans 2 fork branch B"},` +
			`{"role":"user","content":"turn 3 fork branch B"}` +
			`]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-user-1",
		},
	}
	forkContAuth, errForkCont := selector.Pick(context.Background(), "openai", "model", forkContOpts, auths)
	if errForkCont != nil {
		t.Fatalf("forkCont Pick() error = %v", errForkCont)
	}
	if forkContAuth.ID != forkAuth.ID {
		t.Fatalf("fork continuation routed to %q, want same auth %q", forkContAuth.ID, forkAuth.ID)
	}
	contSessionID := forkContOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]
	if contSessionID != forkSessionID {
		t.Fatalf("fork continuation session ID = %q, want identical to fork session %q", contSessionID, forkSessionID)
	}
	contParentID := forkContOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]
	if contParentID != forkParentID {
		t.Fatalf("fork continuation parent ID = %q, want identical to fork parent %q", contParentID, forkParentID)
	}
}

func TestSessionAffinitySelectorLCPFailureEvictionOnCacheHit(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-1"}, {ID: "auth-2"}}

	opts1 := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"test failure eviction"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "test-caller",
		},
	}

	// 1. Initial request binds to auth-1 and succeeds
	auth1, err1 := selector.Pick(context.Background(), "openai", "model", opts1, auths)
	if err1 != nil {
		t.Fatalf("Pick 1 error: %v", err1)
	}
	selector.OnResult(Result{
		Provider: "openai",
		AuthID:   auth1.ID,
		Options:  opts1,
		Success:  true,
	})

	// 2. Second request hits LCP cache for auth-1
	opts2 := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"test failure eviction"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "test-caller",
		},
	}
	auth2, err2 := selector.Pick(context.Background(), "openai", "model", opts2, auths)
	if err2 != nil {
		t.Fatalf("Pick 2 error: %v", err2)
	}
	if auth2.ID != auth1.ID {
		t.Fatalf("Pick 2 did not hit LCP cache: got %q, want %q", auth2.ID, auth1.ID)
	}

	// Second request fails upstream (e.g. 500 error)
	selector.OnResult(Result{
		Provider: "openai",
		AuthID:   auth2.ID,
		Options:  opts2,
		Success:  false,
		Error:    &Error{Code: "upstream_500", Message: "500 internal server error"},
	})

	// 3. Third request should see the failed binding evicted, and fall back to round-robin
	opts3 := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"test failure eviction"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "test-caller",
		},
	}
	auth3, err3 := selector.Pick(context.Background(), "openai", "model", opts3, auths)
	if err3 != nil {
		t.Fatalf("Pick 3 error: %v", err3)
	}
	// Since auth-1 was evicted from LCP on failure, round-robin picks auth-2
	if auth3.ID != "auth-2" {
		t.Fatalf("Pick 3 was not evicted on failure: got %q, want round-robin fallback to auth-2", auth3.ID)
	}
}

func TestSessionAffinityCodexForkWithBothSessionAndThreadIDs(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-codex-1"}, {ID: "auth-codex-2"}}

	// Parent session
	parentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"parent-thread-100"},
			"Thread-Id":  []string{"parent-thread-100"},
		},
		Metadata: map[string]any{},
	}
	parentAuth, _ := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)

	// Child fork has Session-Id: parent-thread-100, Thread-Id: child-thread-200, and forked_from_thread_id: parent-thread-100
	forkOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"parent-thread-100"},
			"Thread-Id":  []string{"child-thread-200"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"parent-thread-100","thread_id":"child-thread-200","forked_from_thread_id":"parent-thread-100"}`,
			},
		},
		Metadata: map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "openai", "model", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("fork Pick() error = %v", errFork)
	}
	if forkAuth.ID != parentAuth.ID {
		t.Fatalf("fork did not inherit parent auth: got %q, want %q", forkAuth.ID, parentAuth.ID)
	}
	// Child must NOT collapse onto parent!
	if got := forkOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:child-thread-200" {
		t.Fatalf("child fork session ID collapsed onto parent: got %v, want codex:child-thread-200", got)
	}
	if got := forkOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "codex:parent-thread-100" {
		t.Fatalf("child fork parent ID = %v, want codex:parent-thread-100", got)
	}
}

func TestSessionAffinityNestedMetadataForkedFromThreadID(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-1"}, {ID: "auth-2"}}

	// Parent
	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"thread_id":"parent-t-1"}`),
		Metadata:        map[string]any{},
	}
	parentAuth, _ := selector.Pick(context.Background(), "openai", "model", parentOpts, auths)

	// Child fork with nested metadata.forked_from_thread_id
	forkOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"thread_id":"child-t-2","metadata":{"forked_from_thread_id":"parent-t-1"}}`),
		Metadata:        map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "openai", "model", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("nested fork Pick() error = %v", errFork)
	}
	if forkAuth.ID != parentAuth.ID {
		t.Fatalf("nested fork did not inherit parent auth: got %q, want %q", forkAuth.ID, parentAuth.ID)
	}
	if isFork, ok := forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); !ok || !isFork {
		t.Fatalf("expected is_fork=true for nested metadata fork, got %v", forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey])
	}
	if got := forkOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "thread:parent-t-1" {
		t.Fatalf("expected ParentSessionID=thread:parent-t-1, got %v", got)
	}
}

func TestSessionAffinityCodexForkWithSessionIdHeaderAndBodyThreadId(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-1", Provider: "antigravity"}, {ID: "auth-2", Provider: "antigravity"}}

	// Parent binds with Session-Id header
	parentOpts := cliproxyexecutor.Options{
		Headers:  http.Header{"Session-Id": []string{"parent-sess-uuid"}},
		Metadata: map[string]any{},
	}
	parentAuth, _ := selector.Pick(context.Background(), "mixed", "gemini-3.8-flash-high", parentOpts, auths)

	// Fork carries Session-Id header (parent-sess-uuid), but body contains thread_id (child-thread-uuid) and metadata.forked_from_thread_id
	forkOpts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"parent-sess-uuid"}},
		OriginalRequest: []byte(`{
			"thread_id": "child-thread-uuid",
			"metadata": {
				"forked_from_thread_id": "parent-sess-uuid"
			}
		}`),
		Metadata: map[string]any{},
	}
	forkAuth, errFork := selector.Pick(context.Background(), "mixed", "gemini-3.8-flash-high", forkOpts, auths)
	if errFork != nil {
		t.Fatalf("fork Pick() error = %v", errFork)
	}
	if forkAuth.ID != parentAuth.ID {
		t.Fatalf("fork did not inherit parent auth on Gemini: got %q, want %q", forkAuth.ID, parentAuth.ID)
	}
	if got := forkOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "codex:child-thread-uuid" {
		t.Fatalf("child fork collapsed onto parent: got %v, want codex:child-thread-uuid", got)
	}
	if got := forkOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; got != "codex:parent-sess-uuid" {
		t.Fatalf("child fork parent ID = %v, want codex:parent-sess-uuid", got)
	}
	if isFork, ok := forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); !ok || !isFork {
		t.Fatalf("expected is_fork=true, got %v", forkOpts.Metadata[cliproxyexecutor.IsForkMetadataKey])
	}
}

func BenchmarkSessionAffinitySelectorPickLCP(b *testing.B) {
	log.SetLevel(log.WarnLevel)
	defer log.SetLevel(log.InfoLevel)

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		TTL: time.Hour,
	})
	auths := []*Auth{
		{ID: "auth-1", Status: StatusActive},
		{ID: "auth-2", Status: StatusActive},
	}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"benchmark message for LCP selector"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "openai",
			cliproxyexecutor.CallerScopeMetadataKey:             "bench-caller",
		},
	}
	// Warm up binding
	_, _ = selector.Pick(context.Background(), "openai", "gpt-4o", opts, auths)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = selector.Pick(context.Background(), "openai", "gpt-4o", opts, auths)
	}
}
