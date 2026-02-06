package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestFillFirstSelectorPick_Deterministic(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "a")
	}
}

func TestRoundRobinSelectorPick_CyclesDeterministic(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	want := []string{"a", "b", "c", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelectorPick_PriorityBuckets(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "c", Attributes: map[string]string{"priority": "0"}},
		{ID: "a", Attributes: map[string]string{"priority": "10"}},
		{ID: "b", Attributes: map[string]string{"priority": "10"}},
	}

	want := []string{"a", "b", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "mixed", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
		if got.ID == "c" {
			t.Fatalf("Pick() #%d unexpectedly selected lower priority auth", i)
		}
	}
}

func TestFillFirstSelectorPick_PriorityFallbackCooldown(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	now := time.Now()
	model := "test-model"

	high := &Auth{
		ID:         "high",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}
	low := &Auth{ID: "low", Attributes: map[string]string{"priority": "0"}}

	got, err := selector.Pick(context.Background(), "mixed", model, cliproxyexecutor.Options{}, []*Auth{high, low})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "low" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "low")
	}
}

func TestRoundRobinSelectorPick_Concurrent(t *testing.T) {
	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got == nil {
					select {
					case errCh <- errors.New("Pick() returned nil auth"):
					default:
					}
					return
				}
				if got.ID == "" {
					select {
					case errCh <- errors.New("Pick() returned auth with empty ID"):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

func TestMaxQuotaSelectorPick_SelectsHighestQuota(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	auths := []*Auth{
		{ID: "low", Quota: QuotaState{Used: 80, Limit: 100, Remaining: 20}},   // 20%
		{ID: "mid", Quota: QuotaState{Used: 50, Limit: 100, Remaining: 50}},   // 50%
		{ID: "high", Quota: QuotaState{Used: 20, Limit: 100, Remaining: 80}},  // 80%
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "high" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should select highest quota)", got.ID, "high")
	}
}

func TestMaxQuotaSelectorPick_DeterministicTieBreaking(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	auths := []*Auth{
		{ID: "c", Quota: QuotaState{Used: 50, Limit: 100, Remaining: 50}}, // 50%
		{ID: "a", Quota: QuotaState{Used: 50, Limit: 100, Remaining: 50}}, // 50%
		{ID: "b", Quota: QuotaState{Used: 50, Limit: 100, Remaining: 50}}, // 50%
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should tie-break alphabetically)", got.ID, "a")
	}
}

func TestMaxQuotaSelectorPick_SkipsCooldownAccounts(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	now := time.Now()
	model := "test-model"

	auths := []*Auth{
		{
			ID:    "blocked",
			Quota: QuotaState{Used: 20, Limit: 100, Remaining: 80}, // 80% quota
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: now.Add(30 * time.Minute),
					Quota: QuotaState{
						Exceeded: true,
					},
				},
			},
		},
		{
			ID:    "available",
			Quota: QuotaState{Used: 50, Limit: 100, Remaining: 50}, // 50% quota
		},
	}

	got, err := selector.Pick(context.Background(), "gemini", model, cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "available" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should skip cooldown)", got.ID, "available")
	}
}

func TestMaxQuotaSelectorPick_AllZeroQuota(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	auths := []*Auth{
		{ID: "c", Quota: QuotaState{Limit: 0}}, // No quota data
		{ID: "a", Quota: QuotaState{Limit: 0}}, // No quota data
		{ID: "b", Quota: QuotaState{Limit: 0}}, // No quota data
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should return first by ID)", got.ID, "a")
	}
}

func TestMaxQuotaSelectorPick_MixedQuotaAvailability(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	auths := []*Auth{
		{ID: "no-data-1", Quota: QuotaState{Limit: 0}},                       // 0% (no data)
		{ID: "with-data", Quota: QuotaState{Used: 40, Limit: 100, Remaining: 60}}, // 60%
		{ID: "no-data-2", Quota: QuotaState{Limit: 0}},                       // 0% (no data)
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "with-data" {
		t.Fatalf("Pick() auth.ID = %q, want %q (should prefer account with quota data)", got.ID, "with-data")
	}
}

func TestMaxQuotaSelectorPick_PriorityBuckets(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	auths := []*Auth{
		{
			ID:         "high-prio",
			Attributes: map[string]string{"priority": "10"},
			Quota:      QuotaState{Used: 50, Limit: 100, Remaining: 50}, // 50%
		},
		{
			ID:         "low-prio",
			Attributes: map[string]string{"priority": "0"},
			Quota:      QuotaState{Used: 10, Limit: 100, Remaining: 90}, // 90%
		},
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "high-prio" {
		t.Fatalf("Pick() auth.ID = %q, want %q (priority should override quota)", got.ID, "high-prio")
	}
}

func TestMaxQuotaSelectorPick_AllCooldown(t *testing.T) {
	t.Parallel()

	selector := &MaxQuotaSelector{}
	now := time.Now()
	model := "test-model"

	auths := []*Auth{
		{
			ID: "auth1",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: now.Add(10 * time.Minute),
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: now.Add(10 * time.Minute),
					},
				},
			},
		},
		{
			ID: "auth2",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: now.Add(5 * time.Minute),
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: now.Add(5 * time.Minute),
					},
				},
			},
		},
	}

	_, err := selector.Pick(context.Background(), "gemini", model, cliproxyexecutor.Options{}, auths)
	if err == nil {
		t.Fatalf("Pick() error = nil, want modelCooldownError")
	}

	// Verify it's a cooldown error
	cooldownErr, ok := err.(*modelCooldownError)
	if !ok {
		t.Fatalf("Pick() error type = %T, want *modelCooldownError", err)
	}
	if cooldownErr.model != model {
		t.Fatalf("cooldownError.model = %q, want %q", cooldownErr.model, model)
	}
}
