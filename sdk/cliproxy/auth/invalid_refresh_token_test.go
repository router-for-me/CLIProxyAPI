package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestInvalidRefreshTokenFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"generic_401", &Error{HTTPStatus: 401, Code: "unauthorized", Message: "unauthorized"}},
		{"missing_status", &Error{Code: "invalid_refresh_token", Message: `{"error":"invalid_refresh_token"}`}},
		{"wrong_status", &Error{HTTPStatus: 503, Code: "invalid_refresh_token", Message: `{"error":"invalid_refresh_token"}`}},
		{"incidental_text", &Error{HTTPStatus: 401, Message: `{"error":{"code":"server_error","message":"invalid_refresh_token mentioned here"}}`}},
		{"code_suffix", &Error{HTTPStatus: 401, Message: `{"error":{"code":"invalid_refresh_token_other"}}`}},
		{"unstructured_text", errors.New("invalid_refresh_token")},
		{"malformed_payload", errors.New(`token refresh failed with status 401: {"error":"invalid_refresh_token"`)},
		{"statusless_payload", errors.New(`{"error":{"code":"invalid_refresh_token"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if isInvalidRefreshTokenError(tc.err) {
				t.Fatal("unconfirmed rejection classified as invalid refresh token")
			}
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			executor := &mockOAuthErrorExecutor{id: "test-provider", errToReturn: tc.err}
			manager.RegisterExecutor(executor)
			auth := &Auth{ID: tc.name, Provider: executor.id, Disabled: true, Status: StatusDisabled,
				Metadata: map[string]any{"refresh_token": "test-refresh", "refresh_interval_seconds": 60}}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			manager.refreshAuth(context.Background(), auth.ID)
			current, _ := manager.GetByID(auth.ID)
			if current.NextRefreshAfter.IsZero() {
				t.Fatal("ordinary failure lost retry backoff")
			}
			if _, ok := nextRefreshCheckAt(time.Now().Add(24*time.Hour), current, time.Second); !ok {
				t.Fatal("ordinary failure was unscheduled")
			}
			if current.LastError.Code == "invalid_refresh_token" {
				t.Fatal("normalization invented confirmed refresh rejection")
			}
		})
	}
}

func TestInvalidRefreshTokenStructuredState(t *testing.T) {
	err := &Error{HTTPStatus: http.StatusUnauthorized, Code: "invalid_refresh_token"}
	if !isInvalidRefreshTokenError(err) {
		t.Fatal("exact structured rejection not recognized")
	}
	stored := refreshErrorFromError(err)
	if stored.Code != "invalid_refresh_token" || stored.HTTPStatus != 401 {
		t.Fatalf("lost error identity: %+v", stored)
	}
	for _, auth := range []*Auth{
		{Disabled: true, LastError: stored},
		{Status: StatusDisabled, LastError: stored},
	} {
		if !hasDisabledInvalidRefreshTokenFailure(auth) {
			t.Fatal("disabled rejection not recognized")
		}
		if HasDisabledInvalidGrantFailure(auth) {
			t.Fatal("public invalid grant matcher was broadened")
		}
	}
	if hasDisabledInvalidRefreshTokenFailure(&Auth{Status: StatusActive, LastError: stored}) {
		t.Fatal("enabled credential matched disabled failure")
	}
}
