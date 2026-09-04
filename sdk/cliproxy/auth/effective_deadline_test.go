package auth

import (
	"testing"
	"time"
)

// TestEffectiveDeadline is a table test with one control per field: each
// field alone contributing the max, credential-vs-model precedence when
// both are live, the all-zero case, and the all-past case. effectiveDeadline
// must ignore modelKey entirely when it does not resolve to a ModelState.
func TestEffectiveDeadline(t *testing.T) {
	now := time.Now()
	future := func(d time.Duration) time.Time { return now.Add(d) }
	past := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name     string
		auth     *Auth
		modelKey string
		want     time.Time
	}{
		{
			name: "nil auth",
			auth: nil,
			want: time.Time{},
		},
		{
			name: "all zero",
			auth: &Auth{},
			want: time.Time{},
		},
		{
			name: "all past ignored",
			auth: &Auth{
				NextRetryAfter: past(time.Minute),
				Quota:          QuotaState{NextRecoverAt: past(2 * time.Minute)},
			},
			want: time.Time{},
		},
		{
			name: "auth.NextRetryAfter alone is the max",
			auth: &Auth{
				NextRetryAfter: future(10 * time.Minute),
			},
			want: future(10 * time.Minute),
		},
		{
			name: "auth.Quota.NextRecoverAt alone is the max",
			auth: &Auth{
				Quota: QuotaState{NextRecoverAt: future(15 * time.Minute)},
			},
			want: future(15 * time.Minute),
		},
		{
			name: "credential NextRetryAfter beats credential NextRecoverAt",
			auth: &Auth{
				NextRetryAfter: future(30 * time.Minute),
				Quota:          QuotaState{NextRecoverAt: future(5 * time.Minute)},
			},
			want: future(30 * time.Minute),
		},
		{
			name: "credential NextRecoverAt beats credential NextRetryAfter",
			auth: &Auth{
				NextRetryAfter: future(2 * time.Minute),
				Quota:          QuotaState{NextRecoverAt: future(45 * time.Minute)},
			},
			want: future(45 * time.Minute),
		},
		{
			name: "modelKey empty ignores a live ModelState",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(time.Hour)},
				},
			},
			modelKey: "",
			want:     time.Time{},
		},
		{
			name: "modelKey with no matching state ignored",
			auth: &Auth{
				CredentialCooldown: true,
				NextRetryAfter:     future(time.Minute),
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(time.Hour)},
				},
			},
			modelKey: "claude-4",
			want:     future(time.Minute),
		},
		{
			name: "modelKey with no matching state and no genuine credential scope ignores the aggregate too",
			auth: &Auth{
				NextRetryAfter: future(time.Minute),
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(time.Hour)},
				},
			},
			modelKey: "claude-4",
			want:     time.Time{},
		},
		{
			name: "model NextRetryAfter alone is the max",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(20 * time.Minute)},
				},
			},
			modelKey: "gpt-5",
			want:     future(20 * time.Minute),
		},
		{
			name: "model Quota.NextRecoverAt alone is the max",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"gpt-5": {Quota: QuotaState{NextRecoverAt: future(25 * time.Minute)}},
				},
			},
			modelKey: "gpt-5",
			want:     future(25 * time.Minute),
		},
		{
			name: "genuine credential deadline beats a shorter model deadline",
			auth: &Auth{
				CredentialCooldown: true,
				NextRetryAfter:     future(time.Hour),
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(5 * time.Minute)},
				},
			},
			modelKey: "gpt-5",
			want:     future(time.Hour),
		},
		{
			name: "aggregate-only (non-genuine) credential deadline does not beat a shorter model deadline",
			auth: &Auth{
				NextRetryAfter: future(time.Hour),
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(5 * time.Minute)},
				},
			},
			modelKey: "gpt-5",
			want:     future(5 * time.Minute),
		},
		{
			name: "model deadline beats a shorter credential deadline",
			auth: &Auth{
				NextRetryAfter: future(5 * time.Minute),
				ModelStates: map[string]*ModelState{
					"gpt-5": {NextRetryAfter: future(time.Hour)},
				},
			},
			modelKey: "gpt-5",
			want:     future(time.Hour),
		},
		{
			name: "canonicalModelKey does not fold case, so a differently-cased map key does not match",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"GPT-5": {NextRetryAfter: future(12 * time.Minute)},
				},
			},
			modelKey: "gpt-5",
			want:     time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveDeadline(tt.auth, tt.modelKey, now)
			if !got.Equal(tt.want) {
				t.Fatalf("effectiveDeadline() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEffectiveBlock covers effectiveBlock's reason precedence: disabled
// beats everything, credential-level cooldown/quota blocks every model,
// a matched model-level block reports blockReasonCooldown/blockReasonOther
// per availabilityBlock's existing rule, and a clean auth is never blocked.
func TestEffectiveBlock(t *testing.T) {
	now := time.Now()
	future := func(d time.Duration) time.Time { return now.Add(d) }

	t.Run("nil auth is blocked with blockReasonOther", func(t *testing.T) {
		blocked, reason, until := effectiveBlock(nil, "gpt-5", now)
		if !blocked || reason != blockReasonOther || !until.IsZero() {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("disabled auth beats a live credential cooldown", func(t *testing.T) {
		a := &Auth{
			Disabled:           true,
			CredentialCooldown: true,
			Unavailable:        true,
			NextRetryAfter:     future(time.Hour),
		}
		blocked, reason, until := effectiveBlock(a, "", now)
		if !blocked || reason != blockReasonDisabled || !until.IsZero() {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("credential-wide cooldown blocks every model regardless of clean model state", func(t *testing.T) {
		a := &Auth{
			CredentialCooldown: true,
			Unavailable:        true,
			NextRetryAfter:     future(30 * time.Minute),
			ModelStates: map[string]*ModelState{
				"gpt-5": {Status: StatusActive},
			},
		}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if !blocked || reason != blockReasonCooldown || !until.Equal(future(30*time.Minute)) {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("credential_quota blocks every model", func(t *testing.T) {
		a := &Auth{
			Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: future(10 * time.Minute)},
			ModelStates: map[string]*ModelState{
				"gpt-5": {Status: StatusActive},
			},
		}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if !blocked || reason != blockReasonCooldown || !until.Equal(future(10*time.Minute)) {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("matched model-level cooldown reports blockReasonOther when not quota", func(t *testing.T) {
		a := &Auth{
			ModelStates: map[string]*ModelState{
				"gpt-5": {Unavailable: true, NextRetryAfter: future(5 * time.Minute)},
			},
		}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if !blocked || reason != blockReasonOther || !until.Equal(future(5*time.Minute)) {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("matched model-level quota reports blockReasonCooldown", func(t *testing.T) {
		a := &Auth{
			ModelStates: map[string]*ModelState{
				"gpt-5": {Quota: QuotaState{Exceeded: true, NextRecoverAt: future(5 * time.Minute)}},
			},
		}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if !blocked || reason != blockReasonCooldown || !until.Equal(future(5*time.Minute)) {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("model disabled state returns blockReasonDisabled immediately", func(t *testing.T) {
		a := &Auth{
			ModelStates: map[string]*ModelState{
				"gpt-5": {Status: StatusDisabled},
			},
		}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if !blocked || reason != blockReasonDisabled || !until.IsZero() {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("clean auth with no model states is never blocked", func(t *testing.T) {
		a := &Auth{}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if blocked || reason != blockReasonNone || !until.IsZero() {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})

	t.Run("no ModelStates falls back to auth-level availabilityBlock", func(t *testing.T) {
		a := &Auth{
			Unavailable:    true,
			NextRetryAfter: future(5 * time.Minute),
		}
		blocked, reason, until := effectiveBlock(a, "gpt-5", now)
		if !blocked || reason != blockReasonOther || !until.Equal(future(5*time.Minute)) {
			t.Fatalf("got (%v,%v,%v)", blocked, reason, until)
		}
	})
}
