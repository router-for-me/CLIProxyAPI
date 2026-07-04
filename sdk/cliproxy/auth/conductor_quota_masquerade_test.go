package auth

import (
	"context"
	"testing"
	"time"
)

// TestMarkResult_QuotaMasquerade400AppliesQuotaCooldown verifies that an upstream
// response which masquerades a quota/capacity condition as HTTP 400 "Param
// Incorrect" (as observed from MiMo under load) is treated like a 429: the
// model state receives a quota cooldown so the credential is skipped while
// others are tried, rather than staying immediately available.
func TestMarkResult_QuotaMasquerade400AppliesQuotaCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "mimo-quota-masquerade"
	model := "mimo-v2.5-pro"

	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	before := time.Now()
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error: &Error{
			Message:    `{"error":{"code":"400","message":"Param Incorrect"}}`,
			HTTPStatus: 400,
		},
	})

	auth, ok := manager.GetByID(authID)
	if !ok {
		t.Fatalf("auth not found after MarkResult")
	}
	state, ok := auth.ModelStates[model]
	if !ok {
		t.Fatalf("model state not created for %s", model)
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected quota cooldown for masqueraded 400, got zero NextRetryAfter")
	}
	if !state.NextRetryAfter.After(before) {
		t.Fatalf("NextRetryAfter = %v, want a future time after %v", state.NextRetryAfter, before)
	}
	if !state.Quota.Exceeded {
		t.Fatalf("expected Quota.Exceeded = true, got false")
	}
	if state.Quota.Reason != "quota" {
		t.Fatalf("expected Quota.Reason = %q, got %q", "quota", state.Quota.Reason)
	}
}

// TestMarkResult_Plain400DoesNotApplyQuotaCooldown verifies that an ordinary
// HTTP 400 (without the quota-masquerade signature) is not accidentally cooled
// down as a quota error — it should fall through to the default (no cooldown).
func TestMarkResult_Plain400DoesNotApplyQuotaCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "plain-400-auth"
	model := "some-model"

	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error: &Error{
			Message:    `{"error":{"type":"invalid_request_error","message":"max_tokens is required"}}`,
			HTTPStatus: 400,
		},
	})

	auth, ok := manager.GetByID(authID)
	if !ok {
		t.Fatalf("auth not found after MarkResult")
	}
	state, ok := auth.ModelStates[model]
	if !ok {
		t.Fatalf("model state not created for %s", model)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("plain 400 should not set a cooldown, got NextRetryAfter = %v", state.NextRetryAfter)
	}
	if state.Quota.Exceeded {
		t.Fatalf("plain 400 should not set Quota.Exceeded, got true")
	}
}

// TestIsUpstreamQuotaMasqueradeResultError covers the classifier directly.
func TestIsUpstreamQuotaMasqueradeResultError(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		want bool
	}{
		{
			name: "mimo param incorrect 400",
			err:  &Error{Message: `{"error":{"code":"400","message":"Param Incorrect"}}`, HTTPStatus: 400},
			want: true,
		},
		{
			name: "plain 429",
			err:  &Error{Message: "Too many requests", HTTPStatus: 429},
			want: true,
		},
		{
			name: "plain invalid request 400",
			err:  &Error{Message: `{"error":{"type":"invalid_request_error"}}`, HTTPStatus: 400},
			want: false,
		},
		{
			name: "500 server error",
			err:  &Error{Message: "internal error", HTTPStatus: 500},
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "param incorrect mixed case",
			err:  &Error{Message: "PARAM INCORRECT", HTTPStatus: 400},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUpstreamQuotaMasqueradeResultError(tc.err); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
