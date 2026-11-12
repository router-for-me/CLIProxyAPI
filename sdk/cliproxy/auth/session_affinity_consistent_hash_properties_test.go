package auth

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Properties of the consistent-hash ranking: order independence, spread, weights, priority
// tiers, concurrency, message-hash identities, and pinned picks.

// Candidate order must not change the pick (300 sessions, 5 shuffles of 6 credentials).
func TestSessionAffinityConsistentHashIgnoresCandidateOrder(t *testing.T) {
	ids := []string{"c-a", "c-b", "c-c", "c-d", "c-e", "c-f"}
	selector := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		session := fmt.Sprintf("order-%d", i)
		want := pickConsistentHashAuth(t, selector, claudeSessionOpts(session), consistentHashAuths(ids...))
		for k := 0; k < 5; k++ {
			shuffled := append([]string(nil), ids...)
			rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			if got := pickConsistentHashAuth(t, selector, claudeSessionOpts(session), consistentHashAuths(shuffled...)); got != want {
				t.Fatalf("session %s: order %v picked %s, want %s", session, shuffled, got, want)
			}
		}
	}
}

// Chi-square over 10 credentials and 50k sessions; weights 1:2:3:4 shares.
func TestSessionAffinityConsistentHashUniformAndWeightedShares(t *testing.T) {
	const n = 50000
	ids := make([]string, 10)
	for i := range ids {
		ids[i] = fmt.Sprintf("uni-%d", i)
	}
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		var best string
		bestScore := math.Inf(-1)
		for _, id := range ids {
			if s := rendezvousScore(fmt.Sprintf("claude:s-%d", i), id, 1); s > bestScore {
				best, bestScore = id, s
			}
		}
		counts[best]++
	}
	expected := float64(n) / float64(len(ids))
	chi := 0.0
	for _, id := range ids {
		d := float64(counts[id]) - expected
		chi += d * d / expected
	}
	// df=9, p=0.001 critical value 27.88
	if chi > 27.88 {
		t.Fatalf("chi-square %.2f too high: %v", chi, counts)
	}
	t.Logf("uniform chi2=%.2f counts=%v", chi, counts)

	weights := map[string]float64{"w1": 1, "w2": 2, "w3": 3, "w4": 4}
	wc := map[string]int{}
	for i := 0; i < n; i++ {
		var best string
		bestScore := math.Inf(-1)
		for id, w := range weights {
			if s := rendezvousScore(fmt.Sprintf("claude:s-%d", i), id, w); s > bestScore {
				best, bestScore = id, s
			}
		}
		wc[best]++
	}
	for id, w := range weights {
		share := float64(wc[id]) / n
		if math.Abs(share-w/10) > 0.01 {
			t.Fatalf("%s share %.3f, want %.3f (%v)", id, share, w/10, wc)
		}
	}
	t.Logf("weighted counts=%v", wc)
}

// Without explicit IDs and without a caller scope the LCP matcher is skipped, so the
// message-hash identity is used: turn 1 has no assistant message, turn 2 does.
func TestSessionAffinityConsistentHashMessageHashAcrossTurns(t *testing.T) {
	auths := consistentHashAuths("claude-a", "claude-b", "claude-c", "claude-d")
	turn := func(i int, second bool) cliproxyexecutor.Options {
		body := fmt.Sprintf(`{"messages":[{"role":"user","content":"question %d"}]}`, i)
		if second {
			body = fmt.Sprintf(`{"messages":[{"role":"user","content":"question %d"},{"role":"assistant","content":"answer %d"},{"role":"user","content":"more"}]}`, i, i)
		}
		return cliproxyexecutor.Options{OriginalRequest: []byte(body), Metadata: map[string]any{}}
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("consistentHash=%v", enabled), func(t *testing.T) {
			selector := newConsistentHashReplica(t, &RoundRobinSelector{}, enabled)
			moved := 0
			for i := 0; i < 40; i++ {
				first := pickConsistentHashAuth(t, selector, turn(i, false), auths)
				second := pickConsistentHashAuth(t, selector, turn(i, true), auths)
				if first != second {
					moved++
				}
			}
			if moved != 0 {
				t.Errorf("consistentHash=%v: %d/40 conversations changed credential between turn 1 and turn 2", enabled, moved)
			}
		})
	}
}

// Priority: with the top tier in cooldown both replicas agree on the lower tier, and
// sessions return to the top tier when it recovers.
func TestSessionAffinityConsistentHashPriorityTiers(t *testing.T) {
	auths := consistentHashAuths("hi-a", "hi-b", "lo-a", "lo-b", "lo-c")
	for _, a := range auths[:2] {
		a.Attributes = map[string]string{"priority": "10"}
	}
	r1 := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	r2 := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	before := map[string]string{}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("prio-%d", i)
		got := pickConsistentHashAuth(t, r1, claudeSessionOpts(id), auths)
		if got != "hi-a" && got != "hi-b" {
			t.Fatalf("session %s picked lower tier %s while top tier available", id, got)
		}
		before[id] = got
	}
	for _, a := range auths[:2] {
		a.Unavailable = true
		a.NextRetryAfter = time.Now().Add(time.Minute)
	}
	for id := range before {
		a := pickConsistentHashAuth(t, r1, claudeSessionOpts(id), auths)
		b := pickConsistentHashAuth(t, r2, claudeSessionOpts(id), auths)
		if a != b || a[:3] != "lo-" {
			t.Fatalf("session %s: lower tier picks %s / %s", id, a, b)
		}
	}
	for _, a := range auths[:2] {
		a.Unavailable = false
		a.NextRetryAfter = time.Time{}
	}
	for id, was := range before {
		if got := pickConsistentHashAuth(t, r2, claudeSessionOpts(id), auths); got != was {
			t.Fatalf("session %s after recovery %s, want %s", id, got, was)
		}
	}
}

// Many goroutines picking on one selector (run with -race).
func TestSessionAffinityConsistentHashConcurrentPicks(t *testing.T) {
	selector := newConsistentHashReplica(t, &WeightedRoundRobinSelector{}, true)
	auths := consistentHashAuths("cc-a", "cc-b", "cc-c")
	want := map[string]string{}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("conc-%d", i)
		want[id] = pickConsistentHashAuth(t, selector, claudeSessionOpts(id), auths)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 1000)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := 0; k < 200; k++ {
				id := fmt.Sprintf("conc-%d", (g+k)%20)
				auth, err := selector.Pick(context.Background(), "claude", consistentHashTestModel, claudeSessionOpts(id), auths)
				if err != nil || auth == nil || auth.ID != want[id] {
					errs <- fmt.Sprintf("%s: %v %v", id, auth, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// Pins the hash: if the hash input or the float mapping changes, every session moves and
// replicas on different versions disagree during a rolling update. Change these values only on
// purpose, and say so in the release notes.
func TestSessionAffinityConsistentHashGoldenPicks(t *testing.T) {
	auths := consistentHashAuths("cred-a", "cred-b", "cred-c", "cred-d")
	selector := newConsistentHashReplica(t, &RoundRobinSelector{}, true)
	want := map[string]string{
		"golden-0": "cred-a",
		"golden-1": "cred-a",
		"golden-2": "cred-c",
		"golden-3": "cred-a",
		"golden-4": "cred-d",
		"golden-5": "cred-b",
		"golden-6": "cred-b",
		"golden-7": "cred-b",
	}
	for session, expected := range want {
		if got := pickConsistentHashAuth(t, selector, claudeSessionOpts(session), auths); got != expected {
			t.Errorf("session %s picked %s, want %s", session, got, expected)
		}
	}
	if got := rendezvousScore("claude:golden-0", "cred-a", 1); math.Abs(got-3.4595950251417809) > 1e-9 {
		t.Errorf("rendezvousScore(claude:golden-0, cred-a, 1) = %.17g", got)
	}
}
