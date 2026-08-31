package auth

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestRoundRobinCursorMapEvictsBoundedKeys pins audit finding B3: when the
// cursor map hit its key limit it was cleared wholesale, restarting every
// provider×model rotation simultaneously and spiking traffic onto one
// credential per shard. Crossing the limit must evict only a bounded batch of
// other keys, never the live rotations wholesale.
func TestRoundRobinCursorMapEvictsBoundedKeys(t *testing.T) {
	provider := "cursor-bound-provider"
	auths := []*Auth{
		{ID: provider + "-a", Provider: provider, Status: StatusActive},
		{ID: provider + "-b", Provider: provider, Status: StatusActive},
	}
	model := "cursor-bound-model"
	registerCursorTestModels(t, provider, auths, model, "m1", "m2", "m3", "m4", "m5")

	selector := &RoundRobinSelector{maxKeys: 4}
	pick := func(m string) *Auth {
		t.Helper()
		auth, errPick := selector.Pick(context.Background(), provider, m, cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick(%s): %v", m, errPick)
		}
		if auth == nil {
			t.Fatalf("Pick(%s) returned nil auth", m)
		}
		return auth
	}

	// Fill the map to exactly the limit with distinct model keys.
	for _, m := range []string{"m1", "m2", "m3", "m4"} {
		pick(m)
	}

	// One more key crosses the limit: a bounded eviction must drop only the
	// oldest keys, keeping the recent rotations' cursors alive. The old
	// behavior cleared the entire map, leaving only the just-inserted key.
	pick("m5")

	selector.mu.Lock()
	survivors := len(selector.cursors)
	_, m4Survived := selector.cursors[provider+":m4"]
	_, m5Survived := selector.cursors[provider+":m5"]
	selector.mu.Unlock()
	if survivors < 3 {
		t.Fatalf("bounded eviction kept only %d keys; the wholesale clear restarting every rotation is the exact bug (want >= 3, m4 and m5 present)", survivors)
	}
	if !m4Survived || !m5Survived {
		t.Fatalf("recent rotations were evicted (m4=%v m5=%v, survivors=%d); FIFO eviction must drop the oldest keys first", m4Survived, m5Survived, survivors)
	}
	// The map must never exceed its cap.
	if survivors > 4 {
		t.Fatalf("cursor map grew past the cap: %d keys, want <= 4", survivors)
	}
}

// TestWeightedSelectorStateMapEvictsBoundedKeys mirrors B3 for the weighted
// selector's smooth-WRR state map, which had the same wholesale clear.
func TestWeightedSelectorStateMapEvictsBoundedKeys(t *testing.T) {
	provider := "weighted-bound-provider"
	auths := []*Auth{
		{ID: provider + "-a", Provider: provider, Status: StatusActive, Attributes: map[string]string{"weight": "3"}},
		{ID: provider + "-b", Provider: provider, Status: StatusActive, Attributes: map[string]string{"weight": "1"}},
	}
	model := "weighted-bound-model"
	registerCursorTestModels(t, provider, auths, model, "m1", "m2", "m3", "m4", "m5")

	selector := &WeightedRoundRobinSelector{maxKeys: 4}
	pick := func(m string) *Auth {
		t.Helper()
		auth, errPick := selector.Pick(context.Background(), provider, m, cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick(%s): %v", m, errPick)
		}
		if auth == nil {
			t.Fatalf("Pick(%s) returned nil auth", m)
		}
		return auth
	}

	for _, m := range []string{"m1", "m2", "m3", "m4"} {
		pick(m)
	}

	// Cross the limit: bounded eviction keeps the recent smooth-WRR states
	// alive; the old wholesale clear is the exact bug.
	pick("m5")

	selector.mu.Lock()
	survivors := len(selector.states)
	_, m4Survived := selector.states[provider+":m4"]
	_, m5Survived := selector.states[provider+":m5"]
	selector.mu.Unlock()
	if survivors < 3 {
		t.Fatalf("bounded eviction kept only %d states; the wholesale clear restarting every rotation is the exact bug (want >= 3, m4 and m5 present)", survivors)
	}
	if !m4Survived || !m5Survived {
		t.Fatalf("recent states were evicted (m4=%v m5=%v, survivors=%d); FIFO eviction must drop the oldest keys first", m4Survived, m5Survived, survivors)
	}
	if survivors > 4 {
		t.Fatalf("weighted state map grew past the cap: %d keys, want <= 4", survivors)
	}
}

// TestRoundRobinCursorRotationSurvivesBoundedEviction pins the user-visible
// consequence of B3: after other keys are evicted under pressure, an
// unaffected rotation continues from where it left off instead of restarting
// at zero and hammering one credential.
func TestRoundRobinCursorRotationSurvivesBoundedEviction(t *testing.T) {
	provider := "cursor-rotation-provider"
	auths := []*Auth{
		{ID: provider + "-a", Provider: provider, Status: StatusActive},
		{ID: provider + "-b", Provider: provider, Status: StatusActive},
	}
	models := []string{"target-model"}
	for i := 0; i < 40; i++ {
		models = append(models, fmt.Sprintf("filler-%d", i))
	}
	registerCursorTestModels(t, provider, auths, models...)

	selector := &RoundRobinSelector{maxKeys: 4096}
	pick := func(m string) *Auth {
		t.Helper()
		auth, errPick := selector.Pick(context.Background(), provider, m, cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick(%s): %v", m, errPick)
		}
		return auth
	}

	// Advance the target model's rotation: two picks with two auths must
	// rotate to the second credential.
	target := "target-model"
	first := pick(target)
	second := pick(target)
	if first.ID == second.ID {
		t.Fatalf("round-robin did not rotate on repeat picks: %s twice", first.ID)
	}

	// Fill the map with many other keys to cross the limit, then pick the
	// target model again: its cursor must have survived, so the rotation
	// continues rather than restarting at the first credential.
	for i := 0; i < 40; i++ {
		pick(fmt.Sprintf("filler-%d", i))
	}
	selector.mu.Lock()
	targetCursor, ok := selector.cursors[provider+":"+target]
	selector.mu.Unlock()
	if !ok {
		t.Fatal("target model's cursor was evicted; the most recently used rotation must survive a bounded eviction")
	}
	if targetCursor < 2 {
		t.Fatalf("target cursor = %d, want >= 2 (rotation restarted instead of surviving)", targetCursor)
	}
}

// registerCursorTestModels registers the model on both auths of a cursor-map
// test so the registry availability check accepts picks of that model.
// RegisterClient replaces the client's whole model list, so every model must
// be supplied in one call.
func registerCursorTestModels(t *testing.T, provider string, auths []*Auth, models ...string) {
	t.Helper()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	for _, auth := range auths {
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, infos)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
}
