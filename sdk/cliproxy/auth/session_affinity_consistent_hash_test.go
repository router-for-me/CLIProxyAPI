package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const consistentHashTestModel = "claude-haiku-5-5"

func newConsistentHashReplica(t *testing.T, fallback Selector, enabled bool) *SessionAffinitySelector {
	t.Helper()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:       fallback,
		TTL:            time.Hour,
		ConsistentHash: enabled,
	})
	t.Cleanup(selector.Stop)
	return selector
}

func consistentHashAuths(ids ...string) []*Auth {
	auths := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		auths = append(auths, &Auth{ID: id, Provider: "claude", Status: StatusActive})
	}
	return auths
}

func claudeSessionOpts(sessionID string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{sessionID}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{},
	}
}

func pickConsistentHashAuth(t *testing.T, selector *SessionAffinitySelector, opts cliproxyexecutor.Options, auths []*Auth) string {
	t.Helper()
	auth, err := selector.Pick(context.Background(), "claude", consistentHashTestModel, opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth == nil {
		t.Fatal("Pick() returned nil auth")
	}
	return auth.ID
}

// splitSessions sends each session's requests to replicas in turn, the way a load balancer
// without stickiness spreads them, and counts the sessions that reached more than one credential.
func splitSessions(t *testing.T, replicas []*SessionAffinitySelector, auths []*Auth, sessions int) int {
	t.Helper()
	split := 0
	for i := 0; i < sessions; i++ {
		seen := map[string]struct{}{}
		for request := 0; request < 2*len(replicas); request++ {
			replica := replicas[(i+request)%len(replicas)]
			opts := claudeSessionOpts(fmt.Sprintf("session-%d", i))
			seen[pickConsistentHashAuth(t, replica, opts, auths)] = struct{}{}
		}
		if len(seen) > 1 {
			split++
		}
	}
	return split
}

func TestSessionAffinityPerReplicaCacheSplitsSessionsAcrossReplicas(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
	replicas := make([]*SessionAffinitySelector, 3)
	for i := range replicas {
		replicas[i] = newConsistentHashReplica(t, &RoundRobinSelector{}, false)
		// Replicas have served different traffic, so their round-robin positions differ.
		for warm := 0; warm < i; warm++ {
			pickConsistentHashAuth(t, replicas[i], claudeSessionOpts(fmt.Sprintf("warm-%d-%d", i, warm)), auths)
		}
	}
	if split := splitSessions(t, replicas, auths, 30); split == 0 {
		t.Fatal("expected per-replica bindings to split some sessions across credentials")
	}
}

func TestSessionAffinityConsistentHashKeepsSessionOnOneCredentialAcrossReplicas(t *testing.T) {
	for _, fallback := range []struct {
		name string
		new  func() Selector
	}{
		{name: "round-robin", new: func() Selector { return &RoundRobinSelector{} }},
		{name: "weighted-round-robin", new: func() Selector { return &WeightedRoundRobinSelector{} }},
	} {
		t.Run(fallback.name, func(t *testing.T) {
			auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
			replicas := make([]*SessionAffinitySelector, 4)
			for i := range replicas {
				replicas[i] = newConsistentHashReplica(t, fallback.new(), true)
				for warm := 0; warm < i; warm++ {
					pickConsistentHashAuth(t, replicas[i], claudeSessionOpts(fmt.Sprintf("warm-%d-%d", i, warm)), auths)
				}
			}
			if split := splitSessions(t, replicas, auths, 200); split != 0 {
				t.Fatalf("%d of 200 sessions reached more than one credential", split)
			}
		})
	}
}

func TestSessionAffinityConsistentHashUsesEverySessionSignal(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
	signals := map[string]func(id string) cliproxyexecutor.Options{
		"x-session-affinity": func(id string) cliproxyexecutor.Options {
			return cliproxyexecutor.Options{
				Headers:         http.Header{"X-Session-Affinity": []string{id}},
				OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
				Metadata:        map[string]any{},
			}
		},
		"codex session_id": func(id string) cliproxyexecutor.Options {
			return cliproxyexecutor.Options{
				Headers:         http.Header{"Session_id": []string{id}},
				OriginalRequest: []byte(`{"input":[{"role":"user","content":"hello"}]}`),
				Metadata:        map[string]any{},
			}
		},
		"prompt_cache_key": func(id string) cliproxyexecutor.Options {
			return cliproxyexecutor.Options{
				OriginalRequest: []byte(`{"prompt_cache_key":"` + id + `","input":[{"role":"user","content":"hello"}]}`),
				Metadata:        map[string]any{},
			}
		},
	}
	for name, build := range signals {
		t.Run(name, func(t *testing.T) {
			first := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
			second := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
			pickConsistentHashAuth(t, second, claudeSessionOpts("warm"), auths)
			for i := 0; i < 40; i++ {
				id := fmt.Sprintf("signal-session-%d", i)
				a := pickConsistentHashAuth(t, first, build(id), auths)
				b := pickConsistentHashAuth(t, second, build(id), auths)
				if a != b {
					t.Fatalf("session %s: replicas picked %s and %s", id, a, b)
				}
			}
		})
	}
}

func TestSessionAffinityConsistentHashSpreadsSessionsEvenly(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
	selector := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	counts := map[string]int{}
	const sessions = 3000
	for i := 0; i < sessions; i++ {
		counts[pickConsistentHashAuth(t, selector, claudeSessionOpts(fmt.Sprintf("spread-%d", i)), auths)]++
	}
	for _, auth := range auths {
		share := float64(counts[auth.ID]) / sessions
		if share < 0.28 || share > 0.39 {
			t.Fatalf("credential %s took %.3f of sessions, want about a third (counts %v)", auth.ID, share, counts)
		}
	}
}

func TestSessionAffinityConsistentHashFollowsWeights(t *testing.T) {
	auths := consistentHashAuths("claude-light", "claude-heavy", "claude-off")
	auths[0].Attributes = map[string]string{AttributeWeight: "1"}
	auths[1].Attributes = map[string]string{AttributeWeight: "3"}
	auths[2].Attributes = map[string]string{AttributeWeight: "0"}
	selector := newConsistentHashReplica(t, &WeightedRoundRobinSelector{}, true)
	counts := map[string]int{}
	const sessions = 4000
	for i := 0; i < sessions; i++ {
		counts[pickConsistentHashAuth(t, selector, claudeSessionOpts(fmt.Sprintf("weighted-%d", i)), auths)]++
	}
	if counts["claude-off"] != 0 {
		t.Fatalf("zero-weight credential took %d sessions", counts["claude-off"])
	}
	heavy := float64(counts["claude-heavy"]) / sessions
	if heavy < 0.70 || heavy > 0.80 {
		t.Fatalf("weight-3 credential took %.3f of sessions, want about 0.75 (counts %v)", heavy, counts)
	}
}

func TestSessionAffinityConsistentHashFailsOverAndReturns(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
	first := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	second := newConsistentHashReplica(t, &RoundRobinSelector{}, true)

	before := map[string]string{}
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("failover-%d", i)
		before[id] = pickConsistentHashAuth(t, first, claudeSessionOpts(id), auths)
	}

	// claude-a enters a cooldown on both replicas.
	auths[0].Unavailable = true
	auths[0].NextRetryAfter = time.Now().Add(time.Minute)
	moved := 0
	for id, was := range before {
		a := pickConsistentHashAuth(t, first, claudeSessionOpts(id), auths)
		b := pickConsistentHashAuth(t, second, claudeSessionOpts(id), auths)
		if a == "claude-a" {
			t.Fatalf("session %s still routed to the unavailable credential", id)
		}
		if a != b {
			t.Fatalf("session %s: replicas failed over to %s and %s", id, a, b)
		}
		if was == "claude-a" {
			moved++
		} else if a != was {
			t.Fatalf("session %s moved from %s to %s although its credential stayed available", id, was, a)
		}
	}
	if moved == 0 {
		t.Fatal("no session was bound to claude-a; the test does not exercise failover")
	}

	// claude-a recovers: its sessions return to it on every replica.
	auths[0].Unavailable = false
	auths[0].NextRetryAfter = time.Time{}
	for id, was := range before {
		if got := pickConsistentHashAuth(t, first, claudeSessionOpts(id), auths); got != was {
			t.Fatalf("session %s after recovery = %s, want %s", id, got, was)
		}
		if got := pickConsistentHashAuth(t, second, claudeSessionOpts(id), auths); got != was {
			t.Fatalf("session %s after recovery on second replica = %s, want %s", id, got, was)
		}
	}
}

func TestSessionAffinityConsistentHashMovesOnlyRemovedCredentialSessions(t *testing.T) {
	all := consistentHashAuths("claude-a", "claude-b", "claude-c", "claude-d")
	selector := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	before := map[string]string{}
	for i := 0; i < 400; i++ {
		id := fmt.Sprintf("removal-%d", i)
		before[id] = pickConsistentHashAuth(t, selector, claudeSessionOpts(id), all)
	}
	remaining := all[:3]
	for id, was := range before {
		got := pickConsistentHashAuth(t, selector, claudeSessionOpts(id), remaining)
		if was != "claude-d" && got != was {
			t.Fatalf("session %s moved from %s to %s when claude-d was removed", id, was, got)
		}
	}
}

func TestSessionAffinityConsistentHashSubagentFollowsParentOnAnotherReplica(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
	parentReplica := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	childReplica := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	for i := 0; i < 40; i++ {
		root := fmt.Sprintf("claude-root-%d", i)
		parent := pickConsistentHashAuth(t, parentReplica, claudeSessionOpts(root), auths)
		childOpts := cliproxyexecutor.Options{
			Headers: http.Header{
				"X-Claude-Code-Session-Id": []string{root},
				"X-Claude-Code-Agent-Id":   []string{"subagent-1"},
			},
			OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
			Metadata:        map[string]any{},
		}
		if child := pickConsistentHashAuth(t, childReplica, childOpts, auths); child != parent {
			t.Fatalf("subagent of %s picked %s on another replica, parent uses %s", root, child, parent)
		}
	}
}

func TestSessionAffinityConsistentHashLeavesFillFirstAlone(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c")
	selector := newConsistentHashReplica(t, &FillFirstSelector{}, true)
	for i := 0; i < 30; i++ {
		if got := pickConsistentHashAuth(t, selector, claudeSessionOpts(fmt.Sprintf("fill-%d", i)), auths); got != "claude-a" {
			t.Fatalf("fill-first with consistent hash picked %s, want claude-a", got)
		}
	}
}

func TestSessionAffinityConsistentHashReportsNoAvailableCredential(t *testing.T) {
	auths := consistentHashAuths("claude-a")
	auths[0].Attributes = map[string]string{AttributeWeight: "0"}
	selector := newConsistentHashReplica(t, &WeightedRoundRobinSelector{}, true)
	auth, err := selector.Pick(context.Background(), "claude", consistentHashTestModel, claudeSessionOpts("none"), auths)
	if err == nil || auth != nil {
		t.Fatalf("Pick() = %v, %v; want an error and no auth", auth, err)
	}
}

func TestManagerSessionAffinityConsistentHashAgreesAcrossManagers(t *testing.T) {
	ctx := context.Background()
	provider := "affinity-consistent-hash"
	model := "affinity-consistent-hash-model"
	ids := []string{provider + "-a", provider + "-b", provider + "-c"}
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		id := id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	newManager := func() *Manager {
		manager := NewManager(nil, nil, nil)
		selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
			Fallback:       &RoundRobinSelector{},
			TTL:            time.Hour,
			ConsistentHash: true,
		})
		t.Cleanup(selector.Stop)
		manager.SetSelector(selector)
		manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
		for _, id := range ids {
			if _, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{ID: id, Provider: provider, Status: StatusActive}); errRegister != nil {
				t.Fatalf("Register(%s): %v", id, errRegister)
			}
		}
		return manager
	}
	replicas := []*Manager{newManager(), newManager()}
	pick := func(manager *Manager, session string) string {
		t.Helper()
		opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: session}}
		auth, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
		if errPick != nil {
			t.Fatalf("pickNext: %v", errPick)
		}
		return auth.ID
	}
	// The second replica has served another session first, so its round-robin position differs.
	pick(replicas[1], "warm-up")

	var session, bound string
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("manager-session-%d", i)
		a, b := pick(replicas[0], id), pick(replicas[1], id)
		if a != b {
			t.Fatalf("session %s: managers picked %s and %s", id, a, b)
		}
		session, bound = id, a
	}

	// A 429 seen by each replica in turn moves the session to the same next credential.
	quota := Result{AuthID: bound, Provider: provider, Model: model, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}}
	replicas[0].MarkResult(ctx, quota)
	moved := pick(replicas[0], session)
	if moved == bound {
		t.Fatalf("session stayed on %s after a 429", bound)
	}
	replicas[1].MarkResult(ctx, quota)
	if got := pick(replicas[1], session); got != moved {
		t.Fatalf("second replica failed over to %s, first to %s", got, moved)
	}
}
