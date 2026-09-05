package auth

import (
	"testing"
)

func TestMetaAuthenticator(t *testing.T) {
	authenticator := NewMetaAuthenticator()
	if authenticator.Provider() != "meta" {
		t.Errorf("expected provider 'meta', got '%s'", authenticator.Provider())
	}
	if lead := authenticator.RefreshLead(); lead != nil {
		t.Errorf("expected nil refresh lead, got %v", lead)
	}
}
