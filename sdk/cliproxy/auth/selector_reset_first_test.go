package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthResetting(id string, reset time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(reset.Unix(), 10),
			"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4",
		}},
	}
}

func TestResetFirstSelectorPicksSoonestWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	selector := &ResetFirstSelector{now: func() time.Time { return now }}
	auths := []*Auth{
		claudeAuthResetting("a", now.Add(96*time.Hour)),
		claudeAuthResetting("b", now.Add(20*time.Hour)),
		claudeAuthResetting("c", now.Add(48*time.Hour)),
	}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() = %q, want %q (resets soonest)", got.ID, "b")
	}
}

func TestResetFirstSelectorTriesUnobservedCredentialFirst(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	selector := &ResetFirstSelector{now: func() time.Time { return now }}
	auths := []*Auth{
		claudeAuthResetting("a", now.Add(2*time.Hour)),
		{ID: "z", Provider: "claude"},
	}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "z" {
		t.Fatalf("Pick() = %q, want unobserved %q", got.ID, "z")
	}
}

func TestResetFirstSelectorSkipsCredentialsInCooldown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	selector := &ResetFirstSelector{}
	model := "claude-test"
	exhausted := claudeAuthResetting("soon", now.Add(time.Hour))
	exhausted.ModelStates = map[string]*ModelState{
		model: {
			Status:         StatusActive,
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Hour),
			Quota:          QuotaState{Exceeded: true},
		},
	}
	later := claudeAuthResetting("later", now.Add(72*time.Hour))

	got, err := selector.Pick(context.Background(), "claude", model, cliproxyexecutor.Options{}, []*Auth{exhausted, later})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "later" {
		t.Fatalf("Pick() = %q, want %q", got.ID, "later")
	}
}

func TestWeeklyResetAt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	observed := now.Add(-time.Hour)
	tests := []struct {
		name    string
		auth    *Auth
		want    time.Time
		wantErr bool
	}{
		{
			name: "claude shared weekly header",
			auth: claudeAuthResetting("a", now.Add(30*time.Hour)),
			want: now.Add(30 * time.Hour),
		},
		{
			name: "claude model-scoped weekly header only",
			auth: &Auth{Provider: "claude", Quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d_sonnet-Reset": strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10),
			}}},
			want: now.Add(5 * time.Hour),
		},
		{
			name:    "claude reset in the past is stale",
			auth:    claudeAuthResetting("a", now.Add(-time.Minute)),
			wantErr: true,
		},
		{
			name: "codex secondary reset-after relative to observation",
			auth: &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":        "300",
				"X-Codex-Primary-Reset-After-Seconds":   "600",
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "86400",
			}}},
			want: observed.Add(24 * time.Hour),
		},
		{
			name: "codex absolute reset-at",
			auth: &Auth{Provider: "codex", Quota: QuotaState{Signals: map[string]string{
				"X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(50*time.Hour).Unix(), 10),
			}}},
			want: now.Add(50 * time.Hour),
		},
		{
			name: "codex five-hour window alone is not weekly",
			auth: &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Primary-Reset-After-Seconds": "7200",
			}}},
			wantErr: true,
		},
		{
			name:    "other providers are unknown",
			auth:    &Auth{Provider: "gemini", Quota: QuotaState{Signals: map[string]string{"X": "1"}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := WeeklyResetAt(tt.auth, now)
			if tt.wantErr {
				if ok {
					t.Fatalf("WeeklyResetAt() = %v, want unknown", got)
				}
				return
			}
			if !ok || !got.Equal(tt.want) {
				t.Fatalf("WeeklyResetAt() = %v, %v; want %v", got, ok, tt.want)
			}
		})
	}
}

// The manager hands reset-first candidates it already validated against each
// credential's resolved model. A cooldown recorded under the public alias must
// not reject them again.
func TestResetFirstTrustsManagerValidatedCandidates(t *testing.T) {
	const routeModel = "client-alias"
	next := time.Now().Add(time.Hour)
	auth := &Auth{
		ID:       "a",
		Provider: "claude",
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: next},
			},
		},
	}
	selector := &ResetFirstSelector{}
	ctx := selectorContextForAvailableAuths(context.Background(), selector, routeModel)
	got, err := selector.Pick(ctx, "claude", selectionArgForSelector(selector, routeModel), cliproxyexecutor.Options{}, []*Auth{auth})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got != auth {
		t.Fatalf("Pick() = %v, want a", got)
	}
}
