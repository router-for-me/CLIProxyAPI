package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A single upstream 404 is frequently transient on Codex routes (streamed
// response.failed events map to 404), so it must cool the credential for
// minutes instead of the 12h fixed window (#5476). Structured model-support
// errors keep their own long cooldown path.
func TestManager_MarkResult_SingleNotFoundCooldownsMinutes(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	notFoundErr := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"response_failed","message":"upstream stream failed"}}`,
	}

	tests := []struct {
		name  string
		model string
	}{
		{name: "credential level", model: ""},
		{name: "model level", model: "gpt-5.6-sol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-not-found-" + tc.name, Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			before := time.Now()
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    tc.model,
				Success:  false,
				Error:    notFoundErr,
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth to be present")
			}
			cooldown := updated.NextRetryAfter.Sub(before)
			if cooldown <= 0 || cooldown > 15*time.Minute {
				t.Fatalf("single 404 cooldown = %v, want a short window (minutes)", cooldown)
			}
		})
	}
}
