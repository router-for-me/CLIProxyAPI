package kiro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKiroAuthService_RefreshTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		if req["refreshToken"] != "test-refresh-token" {
			t.Fatalf("expected test-refresh-token, got %s", req["refreshToken"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RefreshResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789:profile/TEST",
			ExpiresIn:    3600,
		})
	}))
	defer server.Close()

	svc := NewKiroAuthService(server.Client())
	svc.refreshURL = server.URL

	bundle, err := svc.RefreshTokens(context.Background(), "test-refresh-token")
	if err != nil {
		t.Fatalf("RefreshTokens failed: %v", err)
	}
	if bundle.AccessToken != "new-access-token" {
		t.Fatalf("expected new-access-token, got %s", bundle.AccessToken)
	}
	if bundle.RefreshToken != "new-refresh-token" {
		t.Fatalf("expected new-refresh-token, got %s", bundle.RefreshToken)
	}
	if bundle.ProfileArn != "arn:aws:codewhisperer:us-east-1:123456789:profile/TEST" {
		t.Fatalf("unexpected profile ARN: %s", bundle.ProfileArn)
	}
	if bundle.ExpiresAt == "" {
		t.Fatalf("expected non-empty ExpiresAt")
	}
}

func TestKiro_CreateAuthRecord(t *testing.T) {
	bundle := &KiroTokenBundle{
		AccessToken:  "test-acc",
		RefreshToken: "test-ref",
		ProfileArn:   "arn:test",
		ExpiresAt:    "2026-11-21T02:00:00Z",
		Email:        "user@example.com",
		Provider:     "Google",
	}

	auth, err := CreateAuthRecord(bundle)
	if err != nil {
		t.Fatalf("CreateAuthRecord failed: %v", err)
	}
	if auth.Provider != "kiro" {
		t.Fatalf("expected provider kiro, got %s", auth.Provider)
	}
	if auth.Attributes["access_token"] != "test-acc" {
		t.Fatalf("attributes access_token mismatch")
	}
	if auth.Metadata["refresh_token"] != "test-ref" {
		t.Fatalf("metadata refresh_token mismatch")
	}
	if auth.Metadata["email"] != "user@example.com" {
		t.Fatalf("metadata email mismatch")
	}
}
