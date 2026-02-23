package auth

import (
	"strings"
	"testing"
)

func TestAuthLogRef(t *testing.T) {
	auth := &Auth{
		ID:       "sensitive-auth-id-12345",
		Provider: "claude",
	}
	got := authLogRef(auth)
	if !strings.Contains(got, "provider=claude") {
		t.Fatalf("expected provider in log ref, got %q", got)
	}
	if strings.Contains(got, auth.ID) {
		t.Fatalf("log ref leaked raw auth id: %q", got)
	}
	if !strings.Contains(got, "auth_id_hash=") {
		t.Fatalf("expected auth hash marker in log ref, got %q", got)
	}
}
