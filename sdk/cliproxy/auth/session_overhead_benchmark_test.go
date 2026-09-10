package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	log "github.com/sirupsen/logrus"
)

// 1. Explicit Header Fast Path (Pi, Claude Code, OpenCode)
func BenchmarkSessionExplicitHeaderFastPath(b *testing.B) {
	log.SetLevel(log.WarnLevel)
	defer log.SetLevel(log.InfoLevel)

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		TTL: time.Hour,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "openai", Status: StatusActive},
		{ID: "auth-2", Provider: "openai", Status: StatusActive},
	}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"pi-interactive-session-abc-123"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{},
	}

	// Warmup and bind
	ctx := context.Background()
	_, _ = selector.Pick(ctx, "openai", "gpt-5", opts, auths)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		// Clear transient metadata
		delete(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
		delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)

		_, _ = selector.Pick(ctx, "openai", "gpt-5", opts, auths)
	}
}

// 2. Codex Multi-Agent v2 Path (Header + JSON Turn Metadata + Subagent Isolation)
func BenchmarkSessionCodexMultiAgentV2Path(b *testing.B) {
	log.SetLevel(log.WarnLevel)
	defer log.SetLevel(log.InfoLevel)

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		TTL: time.Hour,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-codex-1", Provider: "openai", Status: StatusActive},
		{ID: "auth-codex-2", Provider: "openai", Status: StatusActive},
	}

	// Parent binding
	parentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"parent-session-100"},
		},
		Metadata: map[string]any{},
	}
	ctx := context.Background()
	_, _ = selector.Pick(ctx, "openai", "codex-5", parentOpts, auths)

	// Child subagent request
	childOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id":               []string{"parent-session-100"},
			"Thread-Id":                []string{"subagent-thread-200"},
			"X-Codex-Parent-Thread-Id": []string{"parent-session-100"},
			"X-Openai-Subagent":        []string{"collab_spawn"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"parent-session-100","thread_id":"subagent-thread-200","agent_name":"/root/check_readme","parent_thread_id":"parent-session-100","subagent_kind":"thread_spawn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"inspect"}]}`),
		Metadata:        map[string]any{},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		delete(childOpts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
		delete(childOpts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		delete(childOpts.Metadata, cliproxyexecutor.IsForkMetadataKey)

		_, _ = selector.Pick(ctx, "openai", "codex-5", childOpts, auths)
	}
}

// 3. Codex Fork Path (Header + Turn Metadata Fork Derivation)
func BenchmarkSessionCodexForkPath(b *testing.B) {
	log.SetLevel(log.WarnLevel)
	defer log.SetLevel(log.InfoLevel)

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		TTL: time.Hour,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-codex-1", Provider: "openai", Status: StatusActive},
		{ID: "auth-codex-2", Provider: "openai", Status: StatusActive},
	}

	// Parent
	parentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"parent-thread-alpha"},
		},
		Metadata: map[string]any{},
	}
	ctx := context.Background()
	_, _ = selector.Pick(ctx, "openai", "codex-5", parentOpts, auths)

	forkOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id": []string{"fork-thread-beta"},
			"X-Codex-Turn-Metadata": []string{
				`{"session_id":"fork-thread-beta","forked_from_thread_id":"parent-thread-alpha","request_kind":"turn"}`,
			},
		},
		OriginalRequest: []byte(`{"input":[{"role":"user","content":"fork turn"}]}`),
		Metadata:        map[string]any{},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		delete(forkOpts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
		delete(forkOpts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		delete(forkOpts.Metadata, cliproxyexecutor.IsForkMetadataKey)

		_, _ = selector.Pick(ctx, "openai", "codex-5", forkOpts, auths)
	}
}

// 4. Body-Only Fallback Path (GJSON payload parsing for thread_id & forked_from_thread_id)
func BenchmarkSessionBodyOnlyForkPath(b *testing.B) {
	log.SetLevel(log.WarnLevel)
	defer log.SetLevel(log.InfoLevel)

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		TTL: time.Hour,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "openai", Status: StatusActive},
	}

	// Parent in body
	parentOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"thread_id":"body-parent-100","messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{},
	}
	ctx := context.Background()
	_, _ = selector.Pick(ctx, "openai", "gpt-5", parentOpts, auths)

	forkOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"thread_id":"body-fork-200","forked_from_thread_id":"body-parent-100","messages":[{"role":"user","content":"fork"}]}`),
		Metadata:        map[string]any{},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		delete(forkOpts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
		delete(forkOpts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		delete(forkOpts.Metadata, cliproxyexecutor.IsForkMetadataKey)

		_, _ = selector.Pick(ctx, "openai", "gpt-5", forkOpts, auths)
	}
}

// 5. SessionCache Concurrent High QPS Stress (simulating multi-threaded worker pool)
func BenchmarkSessionCacheConcurrentHighQPS(b *testing.B) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()

	// Pre-populate 1000 active sessions
	for i := 0; i < 1000; i++ {
		cache.Set(fmt.Sprintf("session-%04d", i), fmt.Sprintf("auth-%d", i%10))
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		idx := 0
		for pb.Next() {
			key := fmt.Sprintf("session-%04d", idx%1000)
			// 90% read/touch, 10% write
			if idx%10 == 0 {
				cache.Set(key, fmt.Sprintf("auth-%d", idx%10))
			} else {
				_, _ = cache.Get(key)
				cache.Touch(key, fmt.Sprintf("auth-%d", idx%10))
			}
			idx++
		}
	})
}

// 6. MerklePrefixMatcher Concurrent High QPS (simulating parallel LCP lookups)
func BenchmarkMerklePrefixMatcherConcurrentHighQPS(b *testing.B) {
	matcher := cliproxysession.NewMerklePrefixMatcher(time.Hour)

	// Pre-populate trunk conversation
	texts := make([]string, 16)
	for i := 0; i < 16; i++ {
		texts[i] = fmt.Sprintf("system instruction turn %d", i)
	}
	turns := make([]cliproxysession.CanonicalTurn, 16)
	for i, t := range texts {
		turns[i] = cliproxysession.CanonicalTurn{
			Role: "user",
			Parts: []cliproxysession.CanonicalPart{
				{Kind: "text", Value: t},
			},
		}
	}
	matcher.Bind("lcp:v1:bench:group-0", turns, "auth-1")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = matcher.Match("lcp:v1:bench:group-0", turns)
		}
	})
}

// 7. Large Scale Memory & LRU Eviction Benchmark (Simulates continuous session churn)
func BenchmarkSessionCacheScale100kEviction(b *testing.B) {
	cache := NewSessionCacheWithCapacity(time.Hour, 10000)
	defer cache.Stop()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("session-scale-%d", i)
		cache.Set(key, "auth-target")
	}
}

// 8. MerklePrefixMatcher Large Scale LRU Eviction (Simulates continuous conversation churn with maxGroups=1000)
func BenchmarkMerkleMatcherScaleLRUEviction(b *testing.B) {
	matcher := cliproxysession.NewMerklePrefixMatcherWithConfig(cliproxysession.MerklePrefixMatcherConfig{
		TTL:       time.Hour,
		MaxTurns:  32,
		MaxGroups: 1000,
	})

	turns := []cliproxysession.CanonicalTurn{
		{Role: "user", Parts: []cliproxysession.CanonicalPart{{Kind: "text", Value: "hello world"}}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ns := fmt.Sprintf("lcp:v1:group-%d", i)
		matcher.Bind(ns, turns, "auth-target")
	}
}
