package auth

import "testing"

func TestQoderEnterprisePersonalTokenUsesOfficialVariable(t *testing.T) {
	t.Setenv("QODERCN_PERSONAL_ACCESS_TOKEN", "pt-official")
	if got := qoderEnterprisePersonalToken(); got != "pt-official" {
		t.Fatalf("qoderEnterprisePersonalToken() = %q, want pt-official", got)
	}
}
