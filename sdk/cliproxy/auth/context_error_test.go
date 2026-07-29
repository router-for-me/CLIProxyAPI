package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestResultErrorFromErrorClassifiesContextErrorsAsRequestScoped(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "canceled", err: fmt.Errorf("Post upstream: %w", context.Canceled), wantStatus: 499},
		{name: "deadline exceeded", err: fmt.Errorf("Post upstream: %w", context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resultErrorFromError(test.err)
			if got.HTTPStatus != test.wantStatus {
				t.Fatalf("HTTPStatus = %d, want %d", got.HTTPStatus, test.wantStatus)
			}
			if !got.IsRequestScoped() {
				t.Fatalf("IsRequestScoped() = false, want true: %+v", got)
			}
		})
	}
}

func TestMarkResultDoesNotCoolDownContextErrors(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		manager := NewManager(nil, nil, nil)
		auth := &Auth{ID: "context-error-auth", Provider: "openai-compat"}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}

		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    "test-model",
			Success:  false,
			Error:    resultErrorFromError(err),
		})

		updated, ok := manager.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatal("GetByID() did not return registered auth")
		}
		if state := updated.ModelStates["test-model"]; state != nil {
			t.Fatalf("context error created cooldown model state: %+v", state)
		}
		if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
			t.Fatalf("context error cooled auth: unavailable=%v nextRetryAfter=%v", updated.Unavailable, updated.NextRetryAfter)
		}
	}
}
