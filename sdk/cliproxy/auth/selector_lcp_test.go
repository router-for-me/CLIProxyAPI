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
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
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
	// For Gemini/Antigravity, subagents must NOT inherit the parent's auth; they should balance to auth-b
	if subagent1Auth.ID != "auth-b" {
		t.Fatalf("subagent 1 should not inherit parent auth for Gemini, got %q, want auth-b", subagent1Auth.ID)
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
