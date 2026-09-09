package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type modelNotFoundStatusError struct {
	status int
	body   string
}

func (e modelNotFoundStatusError) Error() string   { return e.body }
func (e modelNotFoundStatusError) StatusCode() int { return e.status }

func TestModelNotFoundResultCoolsCredentialModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstreamErr := modelNotFoundStatusError{
				status: status,
				body:   `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
			}
			resultErr := resultErrorFromError(upstreamErr)
			if resultErr == nil {
				t.Fatal("resultErrorFromError() returned nil")
			}
			if resultErr.Code != "model_not_found" {
				t.Fatalf("error code = %q, want model_not_found", resultErr.Code)
			}
			if shouldSkipCredentialCooldown(resultErr) {
				t.Fatal("model_not_found was treated as request-scoped")
			}

			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "model-not-found-" + http.StatusText(status), Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    "gpt-5.5",
				Success:  false,
				Error:    resultErr,
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("expected auth to remain registered")
			}
			state := updated.ModelStates["gpt-5.5"]
			if state == nil || !state.Unavailable {
				t.Fatalf("expected model cooldown state, got %#v", state)
			}
			remaining := time.Until(state.NextRetryAfter)
			if remaining < 11*time.Hour || remaining > 12*time.Hour {
				t.Fatalf("model cooldown = %v, want about 12h", remaining)
			}
		})
	}
}
