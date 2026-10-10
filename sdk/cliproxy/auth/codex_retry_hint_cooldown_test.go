package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Preserve the earlier PR's model/auth-scope and opt-out guarantees while using
// upstream's current scheduler implementation unchanged.
func TestCodexRetryHintTransientCoolingPolicies(t *testing.T) {
	original := transientErrorCooldownSeconds.Load()
	quotaOriginal := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(original); quotaCooldownDisabled.Store(quotaOriginal) })
	for _, seconds := range []int{0, -1} {
		SetTransientErrorCooldownSeconds(seconds)
		for _, disabled := range []bool{false, true} {
			for _, model := range []string{"gpt-5.4", ""} {
				t.Run(fmt.Sprintf("seconds=%d/disabled=%v/model=%s", seconds, disabled, model), func(t *testing.T) {
					manager := NewManager(nil, nil, nil)
					id := "retry-hint-policy"
					if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Metadata: map[string]any{"disable_cooling": disabled}}); err != nil {
						t.Fatal(err)
					}
					before := time.Now()
					hint := 120 * time.Second
					manager.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: model, Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "overloaded"}, RetryAfter: &hint})
					auth, _ := manager.GetByID(id)
					next := auth.NextRetryAfter
					if model != "" {
						next = auth.ModelStates[model].NextRetryAfter
					}
					if disabled || seconds < 0 {
						if !next.IsZero() {
							t.Fatalf("opt-out ignored: %v", next)
						}
					} else if next.Before(before.Add(hint)) || next.After(time.Now().Add(hint)) {
						t.Fatalf("hint not used: %v", next)
					}
				})
			}
		}
	}
}
