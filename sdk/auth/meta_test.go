package auth

import (
	"testing"
)

func TestMetaAuthenticator(t *testing.T) {
	authenticator := NewMetaAuthenticator()
	if authenticator.Provider() != "meta" {
		t.Errorf("expected provider 'meta', got '%s'", authenticator.Provider())
	}
	if authenticator.RefreshLead() == nil {
		t.Errorf("expected non-nil refresh lead")
	}
}
