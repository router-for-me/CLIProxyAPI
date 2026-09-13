package auth

import "testing"

func TestCodeBuddyCNAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewCodeBuddyCNAuthenticator()
	if authenticator.Provider() != "codebuddy-cn" {
		t.Fatalf("Provider() = %q", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead == nil || *lead <= 0 {
		t.Fatalf("RefreshLead() = %v", lead)
	}
}
