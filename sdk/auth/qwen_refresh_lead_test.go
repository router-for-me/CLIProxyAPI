package auth

import (
	"testing"
	"time"
)

func TestQwenAuthenticator_RefreshLeadIsSane(t *testing.T) {
	lead := NewQwenAuthenticator().RefreshLead()
	if lead == nil {
		t.Fatal("RefreshLead() = nil, want non-nil")
	}
	if *lead <= 0 {
		t.Fatalf("RefreshLead() = %s, want > 0", *lead)
	}
	if *lead > 30*time.Minute {
		t.Fatalf("RefreshLead() = %s, want <= %s", *lead, 30*time.Minute)
	}
}
