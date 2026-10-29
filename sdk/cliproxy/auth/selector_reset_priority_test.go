package auth

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type recordingFallbackSelector struct {
	mu       sync.Mutex
	calls    int
	lastSize int
	pickID   string
}

func (s *recordingFallbackSelector) Pick(_ context.Context, _ string, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastSize = len(auths)
	if s.pickID != "" {
		for _, auth := range auths {
			if auth.ID == s.pickID {
				return auth, nil
			}
		}
	}
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	return auths[0], nil
}

func (s *recordingFallbackSelector) snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.lastSize
}

func claudeResetAuth(id string, resetIn time.Duration, now time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(now.Add(resetIn).Unix(), 10),
		}},
	}
}

func TestResetPrioritySelectorPicksNearestResetDeterministically(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	fallback := &recordingFallbackSelector{}
	selector := NewResetPrioritySelector(fallback, 15*time.Minute, 60*time.Second)
	selector.nowFunc = func() time.Time { return now }

	auths := []*Auth{
		claudeResetAuth("a-far", 20*time.Minute, now),
		claudeResetAuth("b-nearest", 5*time.Minute, now),
		claudeResetAuth("c-middle", 10*time.Minute, now),
	}
	for index := 0; index < 10; index++ {
		picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() #%d error = %v", index, errPick)
		}
		if picked == nil || picked.ID != "b-nearest" {
			t.Fatalf("Pick() #%d = %#v, want b-nearest", index, picked)
		}
	}
	if calls, _ := fallback.snapshot(); calls != 0 {
		t.Fatalf("fallback calls = %d, want 0 while an urgent credential exists", calls)
	}
}

func TestResetPrioritySelectorRotatesJitterGroup(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	fallback := &recordingFallbackSelector{}
	selector := NewResetPrioritySelector(fallback, 15*time.Minute, 60*time.Second)
	selector.nowFunc = func() time.Time { return now }

	auths := []*Auth{
		claudeResetAuth("a", 5*time.Minute, now),
		claudeResetAuth("b", 5*time.Minute+30*time.Second, now),
	}
	want := []string{"a", "b", "a", "b"}
	for index, wantID := range want {
		picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, auths)
		if errPick != nil {
			t.Fatalf("Pick() #%d error = %v", index, errPick)
		}
		if picked == nil || picked.ID != wantID {
			t.Fatalf("Pick() #%d = %#v, want %s", index, picked, wantID)
		}
	}
	if calls, _ := fallback.snapshot(); calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", calls)
	}
}

func TestResetPrioritySelectorFailsOverWhenNearestIsCooling(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	fallback := &recordingFallbackSelector{}
	selector := NewResetPrioritySelector(fallback, 15*time.Minute, 60*time.Second)
	selector.nowFunc = func() time.Time { return now }

	cooling := claudeResetAuth("a-nearest", 5*time.Minute, now)
	cooling.Quota.Exceeded = true
	cooling.Quota.Reason = "quota"
	cooling.Quota.NextRecoverAt = now.Add(2 * time.Minute)
	next := claudeResetAuth("b-second", 6*time.Minute, now)
	far := claudeResetAuth("c-far", 30*time.Minute, now)
	auths := []*Auth{cooling, next, far}

	picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if picked == nil || picked.ID != "b-second" {
		t.Fatalf("Pick() = %#v, want b-second when the nearest is cooling", picked)
	}

	// A conductor retry excludes the credential that just produced the 429.
	retry, errRetry := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, []*Auth{next, far})
	if errRetry != nil {
		t.Fatalf("retry Pick() error = %v", errRetry)
	}
	if retry == nil || retry.ID != "b-second" {
		t.Fatalf("retry Pick() = %#v, want b-second", retry)
	}
	if calls, _ := fallback.snapshot(); calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", calls)
	}
}

func TestResetPrioritySelectorFallsBackWithoutResetData(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	fallback := &recordingFallbackSelector{pickID: "b"}
	selector := NewResetPrioritySelector(fallback, 15*time.Minute, 60*time.Second)
	selector.nowFunc = func() time.Time { return now }

	auths := []*Auth{
		{ID: "a", Provider: "claude"},
		{ID: "b", Provider: "claude"},
		{ID: "c", Provider: "claude"},
	}
	picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if picked == nil || picked.ID != "b" {
		t.Fatalf("Pick() = %#v, want fallback b", picked)
	}
	calls, lastSize := fallback.snapshot()
	if calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", calls)
	}
	if lastSize != len(auths) {
		t.Fatalf("fallback candidate count = %d, want %d", lastSize, len(auths))
	}
}

func TestResetPrioritySelectorDerivesUrgencyFromWindowAnchor(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	fallback := &recordingFallbackSelector{}
	selector := NewResetPrioritySelector(fallback, 15*time.Minute, 60*time.Second)
	selector.nowFunc = func() time.Time { return now }

	windowed := func(id string, anchorAge time.Duration) *Auth {
		return &Auth{
			ID:       id,
			Provider: "claude",
			Attributes: map[string]string{
				AttributeQuotaWindowSeconds: strconv.Itoa(int(5 * time.Hour / time.Second)),
			},
			Metadata: map[string]any{
				MetadataQuotaWindowAnchor: now.Add(-anchorAge).Format(time.RFC3339),
			},
		}
	}
	auths := []*Auth{
		windowed("a-soon", 4*time.Hour+50*time.Minute),
		windowed("b-later", 4*time.Hour),
		{ID: "c-unknown", Provider: "claude"},
	}
	picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if picked == nil || picked.ID != "a-soon" {
		t.Fatalf("Pick() = %#v, want a-soon", picked)
	}
	if calls, _ := fallback.snapshot(); calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", calls)
	}
}

func TestResetPrioritySelectorFallsBackWhenResetsAreNotUrgent(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	fallback := &recordingFallbackSelector{pickID: "c"}
	selector := NewResetPrioritySelector(fallback, 15*time.Minute, 60*time.Second)
	selector.nowFunc = func() time.Time { return now }

	auths := []*Auth{
		claudeResetAuth("a", 40*time.Minute, now),
		claudeResetAuth("b", 30*time.Minute, now),
		claudeResetAuth("c", 20*time.Minute, now),
	}
	picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if picked == nil || picked.ID != "c" {
		t.Fatalf("Pick() = %#v, want fallback c", picked)
	}
	if calls, lastSize := fallback.snapshot(); calls != 1 || lastSize != len(auths) {
		t.Fatalf("fallback calls=%d size=%d, want 1/%d", calls, lastSize, len(auths))
	}
}

func TestResetPrioritySelectorUsesDefaultJitterAndUrgentWindows(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	selector := &ResetPrioritySelector{}
	selector.nowFunc = func() time.Time { return now }
	if got := selector.urgent(); got != defaultResetPriorityUrgentThreshold {
		t.Fatalf("urgent() = %v, want %v", got, defaultResetPriorityUrgentThreshold)
	}
	if got := selector.jitter(); got != defaultResetPriorityJitterWindow {
		t.Fatalf("jitter() = %v, want %v", got, defaultResetPriorityJitterWindow)
	}

	auths := []*Auth{
		claudeResetAuth("a", 10*time.Minute, now),
		claudeResetAuth("b", 5*time.Minute, now),
	}
	picked, errPick := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if picked == nil || picked.ID != "b" {
		t.Fatalf("Pick() = %#v, want b", picked)
	}
}
