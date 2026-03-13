package auth

import (
	"testing"
	"time"
)

func TestQwenRefreshLead(t *testing.T) {
	authenticator := NewQwenAuthenticator()
	lead := authenticator.RefreshLead()
	if lead == nil {
		t.Fatal("expected non-nil refresh lead")
	}
	if *lead != 3*time.Hour {
		t.Fatalf("expected refresh lead 3h, got %v", *lead)
	}
}
